package crmagenda

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"kas-core-pilot/internal/mongotest"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/scheduling"
)

// Integration tests: a disposable MongoDB (KAS_TEST_MONGO_URI) plays the CRM,
// with fictitious appointments only, and a real PostgreSQL schema
// (KAS_TEST_DATABASE_URL) holds the mirror. No mock stands in for either.

const (
	tenantA = "clinica-aurora"
	tenantB = "escola-exemplo"
	zone    = "America/Sao_Paulo"
)

var (
	keyA = []byte("chave-ficticia-de-teste-tenant-aurora-000")
	keyB = []byte("chave-ficticia-de-teste-tenant-escola-111")
)

type env struct {
	t     *testing.T
	coll  *mongo.Collection
	db    *mongo.Database
	store *scheduling.Store
	dsn   string
	doc   []bson.ObjectID // doctors
	pat   []bson.ObjectID // patients
	logs  *lockedBuffer
}

// lockedBuffer collects logs from importers running at the same time.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	c := mongotest.Client(t)
	db := c.Database(mongotest.Database(t, c))
	dsn := pgtest.SchemaDSN(t)
	s, err := scheduling.Open(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)
	e := &env{t: t, coll: db.Collection(Collection), db: db, store: s, dsn: dsn, logs: &lockedBuffer{}}
	for i := 0; i < 3; i++ {
		e.doc = append(e.doc, bson.NewObjectID())
		e.pat = append(e.pat, bson.NewObjectID())
	}
	return e
}

func (e *env) importer(tenant string, key []byte, batch int64) *Importer {
	e.t.Helper()
	m, err := NewMapper(key, zone)
	if err != nil {
		e.t.Fatal(err)
	}
	return &Importer{Reader: NewReader(e.db), Mapper: m, Store: e.store, Tenant: tenant, Batch: batch,
		SafetyLag: time.Minute, Log: slog.New(slog.NewJSONHandler(e.logs, nil))}
}

// appt is one fictitious CRM appointment (only the fields the CRM really has).
type appt struct {
	ID       bson.ObjectID
	Doctor   int
	Patient  int
	Day      string // YYYY-MM-DD
	Time     string
	Duration any
	Status   string
	Updated  time.Time
	Deleted  bool
	NoDoctor bool
}

func (e *env) bsonOf(a appt) bson.D {
	day, err := time.Parse("2006-01-02", a.Day)
	if err != nil {
		e.t.Fatal(err)
	}
	d := bson.D{{Key: "_id", Value: a.ID}}
	if !a.NoDoctor {
		d = append(d, bson.E{Key: "doctor", Value: e.doc[a.Doctor]})
	}
	d = append(d,
		bson.E{Key: "patient", Value: e.pat[a.Patient]},
		bson.E{Key: "date", Value: day.Add(12 * time.Hour)}, // the CRM stores the day at noon UTC
		bson.E{Key: "time", Value: a.Time},
		bson.E{Key: "operationalStatus", Value: a.Status},
		bson.E{Key: "updatedAt", Value: a.Updated},
		bson.E{Key: "isDeleted", Value: a.Deleted},
		// Fields a real appointment carries and the importer must never read.
		bson.E{Key: "notes", Value: "observação fictícia"},
		bson.E{Key: "sessionValue", Value: 220},
	)
	if a.Duration != nil {
		d = append(d, bson.E{Key: "duration", Value: a.Duration})
	}
	return d
}

func (e *env) insert(list ...appt) []bson.ObjectID {
	e.t.Helper()
	docs := make([]any, 0, len(list))
	ids := make([]bson.ObjectID, 0, len(list))
	for _, a := range list {
		if a.ID.IsZero() {
			a.ID = bson.NewObjectID()
		}
		if a.Status == "" {
			a.Status = "scheduled"
		}
		if a.Updated.IsZero() {
			a.Updated = time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
		}
		ids = append(ids, a.ID)
		docs = append(docs, e.bsonOf(a))
	}
	if _, err := e.coll.InsertMany(context.Background(), docs); err != nil {
		e.t.Fatal(err)
	}
	return ids
}

// week inserts n appointments spread over March 2027, 3 doctors, without overlaps.
func (e *env) week(n int) []bson.ObjectID {
	list := make([]appt, 0, n)
	for i := 0; i < n; i++ {
		day := 1 + i/24
		slot := (i % 24) / 3
		list = append(list, appt{Doctor: i % 3, Patient: (i / 3) % 3, Day: fmt.Sprintf("2027-03-%02d", day),
			Time: fmt.Sprintf("%02d:%02d", 8+slot, 0), Duration: int32(40)})
	}
	return e.insert(list...)
}

var march = struct{ from, to time.Time }{
	time.Date(2027, 3, 1, 3, 0, 0, 0, time.UTC), time.Date(2027, 4, 1, 3, 0, 0, 0, time.UTC),
}

func (e *env) mirrored(tenant string) []scheduling.Booking {
	e.t.Helper()
	var out []scheduling.Booking
	for i := range e.doc {
		m, _ := NewMapper(map[string][]byte{tenantA: keyA, tenantB: keyB}[tenant], zone)
		list, err := e.store.ListBookings(context.Background(), tenant, m.Ref("pr", e.doc[i].Hex()), march.from.Add(-48*time.Hour), march.to.Add(48*time.Hour))
		if err != nil && !errors.Is(err, scheduling.ErrNotFound) {
			e.t.Fatal(err)
		}
		out = append(out, list...)
	}
	return out
}

func (e *env) verifyEqual(im *Importer) VerifyReport {
	e.t.Helper()
	rep, err := im.Verify(context.Background(), march.from, march.to, zone, 20)
	if err != nil {
		e.t.Fatal(err)
	}
	if !rep.Equal {
		e.t.Fatalf("mirror differs from source: %+v", rep)
	}
	return rep
}

func TestFullLoadIsIdempotentAndEqualToSource(t *testing.T) {
	e := newEnv(t)
	e.week(50)
	im := e.importer(tenantA, keyA, 7)
	rep, err := im.Full(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Read != 50 || rep.Changed != 50 || rep.Batches != 8 {
		t.Fatalf("first load: %+v", rep)
	}
	again, err := im.Full(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Read != 50 || again.Changed != 0 {
		t.Fatalf("second load must change nothing: %+v", again)
	}
	if n := len(e.mirrored(tenantA)); n != 50 {
		t.Fatalf("%d mirrored bookings, want 50", n)
	}
	v := e.verifyEqual(im)
	if v.SourceRows != 50 || v.Matching == 0 {
		t.Fatalf("verify: %+v", v)
	}
	cur, _ := e.store.GetMirrorCursor(context.Background(), tenantA, Source)
	if cur.FullLoadAt.IsZero() || cur.LastUpdatedAt.IsZero() {
		t.Fatalf("cursor not set after full load: %+v", cur)
	}
}

func TestInvalidRowsAreSkippedAndCounted(t *testing.T) {
	e := newEnv(t)
	e.insert(
		appt{Day: "2027-03-02", Time: "08:00"},
		appt{Day: "2027-03-02", Time: "09:00", NoDoctor: true},
		appt{Day: "2027-03-02", Time: "25:00"},
		appt{Day: "2027-03-02", Time: "10:00", Duration: int32(0)},
		appt{Day: "2027-03-02", Time: "11:00", Duration: 40.5},
	)
	rep, err := e.importer(tenantA, keyA, 100).Full(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Skips{SkipNoProfessional: 1, SkipNoTime: 1, SkipBadDuration: 2}
	if rep.Changed != 1 || fmt.Sprint(rep.Skipped) != fmt.Sprint(want) {
		t.Fatalf("got %+v, want 1 stored and skips %v", rep, want)
	}
	if b := e.mirrored(tenantA); len(b) != 1 || b[0].End.Sub(b[0].Start) != 40*time.Minute {
		t.Fatalf("default duration must be 40 min: %+v", b)
	}
}

func TestIncrementalFollowsChangesAndNewRows(t *testing.T) {
	e := newEnv(t)
	ids := e.week(10)
	im := e.importer(tenantA, keyA, 4)
	ctx := context.Background()
	if _, err := im.Incremental(ctx); err == nil {
		t.Fatal("incremental before a full load must be refused")
	}
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := e.coll.UpdateByID(ctx, ids[0], bson.D{{Key: "$set", Value: bson.D{{Key: "time", Value: "17:00"}, {Key: "updatedAt", Value: now}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.coll.UpdateByID(ctx, ids[1], bson.D{{Key: "$set", Value: bson.D{{Key: "operationalStatus", Value: "canceled"}, {Key: "updatedAt", Value: now}}}}); err != nil {
		t.Fatal(err)
	}
	e.insert(appt{Day: "2027-03-20", Time: "08:00", Updated: now})
	rep, err := im.Incremental(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 3 {
		t.Fatalf("incremental: %+v, want 3 changes", rep)
	}
	again, err := im.Incremental(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed != 0 {
		t.Fatalf("second incremental must change nothing: %+v", again)
	}
	e.verifyEqual(im)
	var moved, canceled bool
	for _, b := range e.mirrored(tenantA) {
		if b.ID == im.Mapper.BookingRef(ids[0]) {
			moved = b.Start.In(mustZone(t)).Format("15:04") == "17:00"
		}
		if b.ID == im.Mapper.BookingRef(ids[1]) {
			canceled = b.Status == scheduling.StatusCanceled
		}
	}
	if !moved || !canceled {
		t.Fatalf("moved=%v canceled=%v", moved, canceled)
	}
}

func mustZone(t *testing.T) *time.Location {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestOutOfOrderUpdateIsIgnored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m, _ := NewMapper(keyA, zone)
	start := time.Date(2027, 3, 3, 12, 0, 0, 0, time.UTC)
	item := scheduling.MirrorItem{BookingID: "ag-x1", ResourceID: "pr-x1", ClientRef: "cl-x1", Start: start, End: start.Add(40 * time.Minute),
		Status: scheduling.StatusScheduled, SourceStatus: "scheduled", SourceUpdatedAt: start}
	_ = m
	if _, err := e.store.MirrorApply(ctx, tenantA, Source, []scheduling.MirrorItem{item}, scheduling.MirrorCursor{}, false); err != nil {
		t.Fatal(err)
	}
	older := item
	older.SourceUpdatedAt = start.Add(-time.Hour)
	older.Status = scheduling.StatusCanceled
	res, err := e.store.MirrorApply(ctx, tenantA, Source, []scheduling.MirrorItem{older}, scheduling.MirrorCursor{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stale != 1 || res.Changed != 0 {
		t.Fatalf("older update must be ignored: %+v", res)
	}
	list, _ := e.store.ListBookings(ctx, tenantA, "pr-x1", start.Add(-time.Hour), start.Add(time.Hour))
	if len(list) != 1 || list[0].Status != scheduling.StatusScheduled {
		t.Fatalf("booking changed by an older update: %+v", list)
	}
}

func TestPhysicalDeleteNeedsTwoChecks(t *testing.T) {
	e := newEnv(t)
	ids := e.week(6)
	im := e.importer(tenantA, keyA, 100)
	ctx := context.Background()
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:2] {
		if _, err := e.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: id}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := im.Reconcile(ctx, march.from, march.to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Deleted != 0 || first.Pending != 2 || len(e.mirrored(tenantA)) != 6 {
		t.Fatalf("first check must only mark candidates: %+v", first)
	}
	// One of them comes back (the source was rewriting it).
	e.insert(appt{ID: ids[1], Doctor: 1, Patient: 0, Day: "2027-03-01", Time: "08:00", Duration: int32(40)})
	second, err := im.Reconcile(ctx, march.from, march.to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Deleted != 1 || second.Cleared != 1 || second.Pending != 0 {
		t.Fatalf("second check: %+v, want 1 deleted and 1 cleared", second)
	}
	if n := len(e.mirrored(tenantA)); n != 5 {
		t.Fatalf("%d mirrored, want 5", n)
	}
	e.verifyEqual(im)
}

func TestDeleteNeedsTheMinimumGap(t *testing.T) {
	e := newEnv(t)
	ids := e.week(3)
	im := e.importer(tenantA, keyA, 100)
	ctx := context.Background()
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: ids[0]}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rep, err := im.Reconcile(ctx, march.from, march.to, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Deleted != 0 {
			t.Fatalf("deleted inside the minimum gap on check %d", i+1)
		}
	}
	if n := len(e.mirrored(tenantA)); n != 3 {
		t.Fatalf("%d mirrored, want 3", n)
	}
}

func TestDeletedThenRecreated(t *testing.T) {
	e := newEnv(t)
	ids := e.week(3)
	im := e.importer(tenantA, keyA, 100)
	ctx := context.Background()
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: ids[0]}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := im.Reconcile(ctx, march.from, march.to, 0); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.mirrored(tenantA)); n != 2 {
		t.Fatalf("%d mirrored after delete, want 2", n)
	}
	e.insert(appt{ID: ids[0], Day: "2027-03-01", Time: "15:00", Duration: int32(60), Updated: time.Now().UTC()})
	rep, err := im.Incremental(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 1 || len(e.mirrored(tenantA)) != 3 {
		t.Fatalf("recreated row not mirrored: %+v", rep)
	}
	e.verifyEqual(im)
}

func TestSoftDeleteBecomesCanceled(t *testing.T) {
	e := newEnv(t)
	e.insert(appt{Day: "2027-03-04", Time: "08:00", Deleted: true, Status: "scheduled"})
	im := e.importer(tenantA, keyA, 100)
	if _, err := im.Full(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := e.mirrored(tenantA)
	if len(b) != 1 || b[0].Status != scheduling.StatusCanceled {
		t.Fatalf("soft-deleted row must be canceled: %+v", b)
	}
	e.verifyEqual(im)
}

var errCrash = errors.New("simulated crash")

func TestRestartAfterInterruptedFullLoad(t *testing.T) {
	e := newEnv(t)
	e.week(30)
	ctx := context.Background()
	im := e.importer(tenantA, keyA, 5)
	im.OnBatch = func(n int) error {
		if n == 3 {
			return errCrash
		}
		return nil
	}
	if _, err := im.Full(ctx); !errors.Is(err, errCrash) {
		t.Fatalf("want the simulated crash, got %v", err)
	}
	if n := len(e.mirrored(tenantA)); n != 15 {
		t.Fatalf("%d stored before the crash, want 15 (3 batches)", n)
	}
	if cur, _ := e.store.GetMirrorCursor(ctx, tenantA, Source); !cur.FullLoadAt.IsZero() {
		t.Fatal("an interrupted full load must not be marked done")
	}
	if _, err := im.Incremental(ctx); err == nil {
		t.Fatal("incremental must wait for a complete full load")
	}
	im.OnBatch = nil
	rep, err := im.Full(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 15 || len(e.mirrored(tenantA)) != 30 {
		t.Fatalf("restart: %+v", rep)
	}
	e.verifyEqual(im)
}

func TestRestartAfterInterruptedIncremental(t *testing.T) {
	e := newEnv(t)
	ids := e.week(12)
	ctx := context.Background()
	im := e.importer(tenantA, keyA, 3)
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i, id := range ids {
		if _, err := e.coll.UpdateByID(ctx, id, bson.D{{Key: "$set", Value: bson.D{{Key: "time", Value: "18:00"},
			{Key: "updatedAt", Value: base.Add(time.Duration(i) * time.Millisecond)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	im.OnBatch = func(n int) error {
		if n == 2 {
			return errCrash
		}
		return nil
	}
	if _, err := im.Incremental(ctx); !errors.Is(err, errCrash) {
		t.Fatalf("want crash, got %v", err)
	}
	im.OnBatch = nil
	rep, err := im.Incremental(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Read != 6 || rep.Changed != 6 {
		t.Fatalf("resume must read only the 6 rows after the stored cursor: %+v", rep)
	}
	e.verifyEqual(im)
}

func TestPostgresFailureMidImport(t *testing.T) {
	e := newEnv(t)
	e.week(20)
	ctx := context.Background()
	im := e.importer(tenantA, keyA, 5)
	im.OnBatch = func(n int) error {
		if n == 2 {
			// The database goes away under the importer. Close waits for the
			// importer's lock connection, so it runs aside; new work fails at once.
			go e.store.Close()
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}
	if _, err := im.Full(ctx); err == nil {
		t.Fatal("full load must fail when PostgreSQL is gone")
	}
	s2, err := scheduling.Open(ctx, e.dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Close)
	e.store = s2
	im2 := e.importer(tenantA, keyA, 5)
	rep, err := im2.Full(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 10 || len(e.mirrored(tenantA)) != 20 {
		t.Fatalf("after recovery: %+v", rep)
	}
	e.verifyEqual(im2)
}

func TestMongoFailureChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.week(5)
	ctx := context.Background()
	im := e.importer(tenantA, keyA, 100)
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := e.store.GetMirrorCursor(ctx, tenantA, Source)
	dead := mongotest.Client(t)
	if err := dead.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	im.Reader = NewReader(dead.Database(e.db.Name()))
	for name, run := range map[string]func() error{
		"full":        func() error { _, err := im.Full(ctx); return err },
		"incremental": func() error { _, err := im.Incremental(ctx); return err },
		"reconcile":   func() error { _, err := im.Reconcile(ctx, march.from, march.to, 0); return err },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "read source") {
			t.Fatalf("%s: want a source read error, got %v", name, err)
		}
	}
	after, _ := e.store.GetMirrorCursor(ctx, tenantA, Source)
	if !after.LastUpdatedAt.Equal(before.LastUpdatedAt) || after.LastSourceID != before.LastSourceID {
		t.Fatal("cursor moved without a successful read")
	}
	if n := len(e.mirrored(tenantA)); n != 5 {
		t.Fatalf("%d mirrored, want 5 (a failed reconcile must not delete)", n)
	}
}

func TestConcurrentImportersAreSerialized(t *testing.T) {
	e := newEnv(t)
	e.week(40)
	ctx := context.Background()
	slow := e.importer(tenantA, keyA, 4)
	started := make(chan struct{})
	var once sync.Once
	slow.OnBatch = func(n int) error {
		once.Do(func() { close(started) })
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	errs := make(chan error, 1)
	go func() { _, err := slow.Full(ctx); errs <- err }()
	<-started
	other := e.importer(tenantA, keyA, 4)
	if _, err := other.Full(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("second importer: want ErrBusy, got %v", err)
	}
	if _, err := other.Reconcile(ctx, march.from, march.to, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("reconcile during an import: want ErrBusy, got %v", err)
	}
	// Another tenant is not blocked by tenant A's importer.
	if _, err := e.importer(tenantB, keyB, 4).Full(ctx); err != nil {
		t.Fatalf("tenant B blocked by tenant A: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if _, err := other.Full(ctx); err != nil {
		t.Fatalf("after the first finished: %v", err)
	}
	e.verifyEqual(other)
}

func TestTenantsAreIsolated(t *testing.T) {
	e := newEnv(t)
	ids := e.week(8)
	ctx := context.Background()
	a, b := e.importer(tenantA, keyA, 100), e.importer(tenantB, keyB, 100)
	for _, im := range []*Importer{a, b} {
		if _, err := im.Full(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a.Mapper.BookingRef(ids[0]) == b.Mapper.BookingRef(ids[0]) {
		t.Fatal("different tenant keys must give different ids")
	}
	if _, err := e.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: ids[0]}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := a.Reconcile(ctx, march.from, march.to, 0); err != nil {
			t.Fatal(err)
		}
	}
	if na, nb := len(e.mirrored(tenantA)), len(e.mirrored(tenantB)); na != 7 || nb != 8 {
		t.Fatalf("tenant A reconcile touched tenant B: A=%d B=%d", na, nb)
	}
	// Tenant B's mirror cannot write over tenant A's ids, nor over a native booking.
	native := scheduling.NewBooking{ID: "bk-nativo", ResourceID: "sala-1", ClientRef: "cli-1", IdempotencyKey: "n1", Actor: "teste",
		Start: march.from.Add(30 * time.Hour), End: march.from.Add(31 * time.Hour)}
	if err := e.store.PutSettings(ctx, tenantB, scheduling.Settings{Zone: zone}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.PutResource(ctx, tenantB, scheduling.Resource{ID: "sala-1", Kind: "room", Label: "Sala", Capacity: 1, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.PutWeeklyRules(ctx, tenantB, "sala-1", []scheduling.WeeklyRule{{Weekday: 1, StartMin: 0, EndMin: 24 * 60}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.CreateBooking(ctx, tenantB, native); err != nil {
		t.Fatal(err)
	}
	clash := scheduling.MirrorItem{BookingID: "bk-nativo", ResourceID: "pr-x", ClientRef: "cl-x", Start: native.Start, End: native.End,
		Status: scheduling.StatusScheduled, SourceStatus: "scheduled", SourceUpdatedAt: time.Now()}
	if _, err := e.store.MirrorApply(ctx, tenantB, Source, []scheduling.MirrorItem{clash}, scheduling.MirrorCursor{}, false); !errors.Is(err, scheduling.ErrInvalid) {
		t.Fatalf("mirror overwrote a native booking: %v", err)
	}
}

func TestSourceOverlapsAreKeptAndReported(t *testing.T) {
	e := newEnv(t)
	e.insert(
		appt{Doctor: 0, Patient: 0, Day: "2027-03-05", Time: "08:00", Duration: int32(40)},
		appt{Doctor: 0, Patient: 1, Day: "2027-03-05", Time: "08:20", Duration: int32(40)},
		appt{Doctor: 0, Patient: 2, Day: "2027-03-05", Time: "08:00", Duration: int32(40), Status: "canceled"},
	)
	im := e.importer(tenantA, keyA, 100)
	if _, err := im.Full(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.mirrored(tenantA)); n != 3 {
		t.Fatalf("%d mirrored, want 3 (overlaps are measured, not refused)", n)
	}
	v := e.verifyEqual(im)
	if v.SourceOverlaps != 1 {
		t.Fatalf("want 1 overlapping pair (canceled does not count), got %d", v.SourceOverlaps)
	}
}

func TestVerifyReportsDivergence(t *testing.T) {
	e := newEnv(t)
	ids := e.week(6)
	im := e.importer(tenantA, keyA, 100)
	ctx := context.Background()
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	// Changed in the source but not imported yet.
	if _, err := e.coll.UpdateByID(ctx, ids[0], bson.D{{Key: "$set", Value: bson.D{{Key: "duration", Value: int32(60)}}}}); err != nil {
		t.Fatal(err)
	}
	e.insert(appt{Doctor: 2, Day: "2027-03-25", Time: "10:00"})
	rep, err := im.Verify(ctx, march.from, march.to, zone, 20)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Equal || len(rep.Divergences) != 2 {
		t.Fatalf("want 2 divergent resource-days, got %+v", rep)
	}
	for _, d := range rep.Divergences {
		if d.Source == nil {
			t.Fatalf("divergence without the source side: %+v", d)
		}
	}
}

func TestLogsAndReportsHoldNoSourceIds(t *testing.T) {
	e := newEnv(t)
	ids := e.week(5)
	im := e.importer(tenantA, keyA, 2)
	ctx := context.Background()
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Reconcile(ctx, march.from, march.to, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Verify(ctx, march.from, march.to, zone, 20); err != nil {
		t.Fatal(err)
	}
	out := e.logs.String()
	if out == "" {
		t.Fatal("no logs captured")
	}
	for _, list := range [][]bson.ObjectID{ids, e.doc, e.pat} {
		for _, id := range list {
			if strings.Contains(out, id.Hex()) {
				t.Fatal("a source id reached the logs")
			}
		}
	}
	for _, needle := range []string{"ag-", "pr-", "cl-", "observação"} {
		if strings.Contains(out, needle) {
			t.Fatalf("logs contain %q", needle)
		}
	}
}

func TestRunLogMatchesWhatHappened(t *testing.T) {
	e := newEnv(t)
	ids := e.week(9)
	ctx := context.Background()
	im := e.importer(tenantA, keyA, 4)
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	im.OnBatch = func(int) error { return errCrash }
	if _, err := im.Full(ctx); err == nil {
		t.Fatal("want crash")
	}
	im.OnBatch = nil
	if _, err := e.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: ids[0]}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := im.Reconcile(ctx, march.from, march.to, 0); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := e.store.MirrorRuns(ctx, tenantA, Source, 50)
	if err != nil {
		t.Fatal(err)
	}
	// The crashed run is not recorded: only runs that finished leave a row.
	if len(runs) != 3 || runs[2].Mode != "full" || runs[2].Read != 9 || runs[2].Upserted != 9 ||
		runs[1].Candidates != 1 || runs[0].Deleted != 1 {
		t.Fatalf("run log: %+v", runs)
	}
	other, _ := e.store.MirrorRuns(ctx, tenantB, Source, 50)
	if len(other) != 0 {
		t.Fatal("tenant B sees tenant A's runs")
	}
}

func TestNoQueryCanMoveARowToAnotherTenant(t *testing.T) {
	for _, f := range []string{"../../scheduling/store.go", "../../scheduling/mirror.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"SET tenant_id", "tenant_id = EXCLUDED", ", tenant_id = "} {
			if strings.Contains(string(src), bad) {
				t.Fatalf("%s rewrites tenant_id (%q)", f, bad)
			}
		}
	}
	e := newEnv(t)
	ctx := context.Background()
	start := time.Date(2027, 3, 3, 12, 0, 0, 0, time.UTC)
	item := scheduling.MirrorItem{BookingID: "ag-t1", ResourceID: "pr-t1", ClientRef: "cl-t1", Start: start, End: start.Add(40 * time.Minute),
		Status: scheduling.StatusScheduled, SourceStatus: "scheduled", SourceUpdatedAt: start}
	for _, tenant := range []string{"", "../x", "Clinica Aurora", strings.Repeat("a", 200)} {
		if _, err := e.store.MirrorApply(ctx, tenant, Source, []scheduling.MirrorItem{item}, scheduling.MirrorCursor{}, false); !errors.Is(err, scheduling.ErrInvalid) {
			t.Fatalf("tenant %q accepted: %v", tenant, err)
		}
	}
	// The same booking id in two tenants stays two rows.
	for _, tenant := range []string{tenantA, tenantB} {
		if _, err := e.store.MirrorApply(ctx, tenant, Source, []scheduling.MirrorItem{item}, scheduling.MirrorCursor{}, false); err != nil {
			t.Fatal(err)
		}
	}
	moved := item
	moved.SourceUpdatedAt = start.Add(time.Hour)
	moved.Status = scheduling.StatusCanceled
	if _, err := e.store.MirrorApply(ctx, tenantB, Source, []scheduling.MirrorItem{moved}, scheduling.MirrorCursor{}, false); err != nil {
		t.Fatal(err)
	}
	a, _ := e.store.ListBookings(ctx, tenantA, "pr-t1", start.Add(-time.Hour), start.Add(time.Hour))
	if len(a) != 1 || a[0].Status != scheduling.StatusScheduled {
		t.Fatalf("tenant B's update reached tenant A: %+v", a)
	}
}
