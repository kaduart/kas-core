package crmagenda

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"kas-core-pilot/internal/scheduling"
)

// Window mode: extraction by date window, through the date index, with no
// updatedAt index and no full load. Fictitious data only.

func TestWindowCapturesChangesInsideTheWindowOnly(t *testing.T) {
	e := newEnv(t)
	ids := e.week(6)
	im := e.importer(tenantA, keyA, 4) // small batch: more than one batch per run
	ctx := context.Background()

	first, err := im.Window(ctx, march.from, march.to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Read != 6 || first.Changed != 6 || first.Batches != 2 || len(e.mirrored(tenantA)) != 6 {
		t.Fatalf("first window: %+v", first)
	}
	e.verifyEqual(im)
	cur, err := e.store.GetMirrorCursor(ctx, tenantA, Source)
	if err != nil {
		t.Fatal(err)
	}
	if !cur.LastUpdatedAt.IsZero() || cur.LastSourceID != "" {
		t.Fatalf("window must not move the incremental cursor: %+v", cur)
	}

	// Same source again: nothing changes (idempotent).
	again, err := im.Window(ctx, march.from, march.to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed != 0 || again.Deleted != 0 || again.Pending != 0 {
		t.Fatalf("second window over the same source changed rows: %+v", again)
	}

	// A status change, a new booking in the window and one outside it.
	later := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := e.coll.UpdateOne(ctx, bson.D{{Key: "_id", Value: ids[0]}}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "operationalStatus", Value: "canceled"}, {Key: "updatedAt", Value: later}}}}); err != nil {
		t.Fatal(err)
	}
	e.insert(appt{Doctor: 2, Patient: 1, Day: "2027-03-20", Time: "14:00", Duration: int32(40), Updated: later})
	e.insert(appt{Doctor: 2, Patient: 1, Day: "2027-05-20", Time: "14:00", Duration: int32(40), Updated: later})

	upd, err := im.Window(ctx, march.from, march.to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if upd.Read != 7 || upd.Changed != 2 {
		t.Fatalf("window after changes: %+v, want 7 read and 2 changed", upd)
	}
	e.verifyEqual(im)
	m, _ := NewMapper(keyA, zone)
	may, err := e.store.ListBookings(ctx, tenantA, m.Ref("pr", e.doc[2].Hex()),
		time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil && !errors.Is(err, scheduling.ErrNotFound) {
		t.Fatal(err)
	}
	if len(may) != 0 {
		t.Fatalf("a booking outside the window was mirrored: %+v", may)
	}
	var canceled int
	for _, b := range e.mirrored(tenantA) {
		if b.Status == scheduling.StatusCanceled {
			canceled++
		}
	}
	if canceled != 1 {
		t.Fatalf("%d canceled in the mirror, want 1", canceled)
	}
}

func TestWindowDeleteNeedsTwoChecks(t *testing.T) {
	e := newEnv(t)
	ids := e.week(5)
	im := e.importer(tenantA, keyA, 100)
	ctx := context.Background()
	if _, err := im.Window(ctx, march.from, march.to, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: ids[0]}}); err != nil {
		t.Fatal(err)
	}
	first, err := im.Window(ctx, march.from, march.to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Deleted != 0 || first.Pending != 1 || len(e.mirrored(tenantA)) != 5 {
		t.Fatalf("first check must only mark the candidate: %+v", first)
	}
	second, err := im.Window(ctx, march.from, march.to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Deleted != 1 || len(e.mirrored(tenantA)) != 4 {
		t.Fatalf("second check: %+v, want 1 deleted", second)
	}
	e.verifyEqual(im)
}

func TestWindowRefusesBadWindows(t *testing.T) {
	e := newEnv(t)
	im := e.importer(tenantA, keyA, 100)
	ctx := context.Background()
	if _, err := im.Window(ctx, march.to, march.from, 0); !errors.Is(err, scheduling.ErrInvalid) {
		t.Fatalf("inverted window: %v", err)
	}
	if _, err := im.Window(ctx, march.from, march.from.Add(MaxWindow+time.Hour), 0); !errors.Is(err, scheduling.ErrInvalid) {
		t.Fatalf("window above the maximum: %v", err)
	}
}
