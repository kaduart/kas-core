package crmagenda

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"kas-core-pilot/internal/scheduling"
)

// Phase 3A: the comparison engine against fixtures (fictitious CRM in a
// disposable MongoDB, mirror in a real PostgreSQL schema).

var standardTimes = []string{"08:00", "08:40", "09:20", "10:00", "13:00", "13:40", "14:20"}

func (e *env) doctors(status ...string) {
	e.t.Helper()
	for i, id := range e.doc {
		st := "ativo"
		if i < len(status) {
			st = status[i]
		}
		var weekly bson.A
		for _, d := range []string{"monday", "tuesday", "wednesday", "thursday", "friday"} {
			weekly = append(weekly, bson.D{{Key: "day", Value: d}, {Key: "times", Value: standardTimes}})
		}
		_, err := e.db.Collection(DoctorCollection).InsertOne(context.Background(), bson.D{{Key: "_id", Value: id},
			{Key: "fullName", Value: "Profissional Fictício"}, {Key: "email", Value: "ficticio@exemplo.invalid"},
			{Key: "weeklyAvailability", Value: weekly}, {Key: "status", Value: st}})
		if err != nil {
			e.t.Fatal(err)
		}
	}
}

// fixture: 3 professionals, a week with sessions, imported and synced, and the
// KAS holiday calendar set to the CRM's full-day holidays of the window.
func compareFixture(t *testing.T, from, to string, status ...string) (*env, *Importer) {
	e := newEnv(t)
	e.doctors(status...)
	e.insert(
		appt{Doctor: 0, Patient: 0, Day: "2027-03-01", Time: "08:00", Duration: int32(40)},
		appt{Doctor: 0, Patient: 1, Day: "2027-03-01", Time: "09:20", Duration: int32(40), Status: "pre_agendado"},
		appt{Doctor: 0, Patient: 2, Day: "2027-03-02", Time: "13:00", Duration: int32(40), Status: "canceled"},
		appt{Doctor: 1, Patient: 0, Day: "2027-03-03", Time: "10:00", Duration: int32(40), Status: "confirmed"},
		appt{Doctor: 1, Patient: 1, Day: "2027-03-03", Time: "13:40", Duration: int32(40), Status: "missed"},
		appt{Doctor: 2, Patient: 2, Day: "2027-03-04", Time: "14:20", Duration: int32(40), Status: "completed"},
		appt{Doctor: 2, Patient: 0, Day: "2027-03-05", Time: "08:00", Duration: int32(60)},
	)
	im := e.importer(tenantA, keyA, 100)
	ctx := context.Background()
	if _, err := im.Full(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := im.SyncAvailability(ctx); err != nil || n != 3 {
		t.Fatalf("sync availability: %d %v", n, err)
	}
	var holidays []string
	f, _ := time.Parse("2006-01-02", from)
	l, _ := time.Parse("2006-01-02", to)
	for d := f; !d.After(l); d = d.AddDate(0, 0, 1) {
		if CRMHoliday(d.Format("2006-01-02")) == "full" {
			holidays = append(holidays, d.Format("2006-01-02"))
		}
	}
	if err := e.store.PutSettings(ctx, tenantA, scheduling.Settings{Zone: zone, Holidays: holidays}); err != nil {
		t.Fatal(err)
	}
	return e, im
}

func TestCompareEqualAfterImport(t *testing.T) {
	_, im := compareFixture(t, "2027-03-01", "2027-03-05")
	rep, err := im.Compare(context.Background(), "2027-03-01", "2027-03-05", 50)
	if err != nil {
		t.Fatal(err)
	}
	s := rep.Summary
	if s.ResourceDays != 15 || s.Equal != 15 || len(rep.Days) != 0 {
		t.Fatalf("want 15 equal resource-days: %+v %+v", s, rep.Days)
	}
	if s.PatternsTotal == 0 || s.PatternsEqual != s.PatternsTotal {
		t.Fatalf("weekly patterns differ: %+v", s)
	}
	if rep.ByStatus["pre_agendado"][scheduling.StatusTentative] != 1 || rep.ByStatus["missed"][scheduling.StatusScheduled] != 1 {
		t.Fatalf("status report: %+v", rep.ByStatus)
	}
}

func TestCompareSeparatesImportErrors(t *testing.T) {
	e, im := compareFixture(t, "2027-03-01", "2027-03-05")
	ctx := context.Background()
	// Changed in the CRM and not imported yet; and a new appointment not imported.
	if _, err := e.coll.UpdateOne(ctx, bson.D{{Key: "doctor", Value: e.doc[0]}, {Key: "time", Value: "08:00"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "time", Value: "10:00"}}}}); err != nil {
		t.Fatal(err)
	}
	e.insert(appt{Doctor: 1, Patient: 2, Day: "2027-03-04", Time: "08:40", Duration: int32(40)})
	rep, err := im.Compare(ctx, "2027-03-01", "2027-03-05", 50)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.ImportErrors != 2 || rep.Summary.RealDivergence != 0 {
		t.Fatalf("want 2 import errors: %+v", rep.Summary)
	}
	reasons := ""
	for _, d := range rep.Days {
		reasons += strings.Join(d.Reasons, ",") + ";"
	}
	if !strings.Contains(reasons, "stale_in_mirror:1") || !strings.Contains(reasons, "missing_in_mirror:1") {
		t.Fatalf("reasons: %s", reasons)
	}
	// After an incremental run with the updated rows the mirror is equal again.
	now := time.Now().UTC()
	if _, err := e.coll.UpdateMany(ctx, bson.D{}, bson.D{{Key: "$set", Value: bson.D{{Key: "updatedAt", Value: now}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Incremental(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := im.Compare(ctx, "2027-03-01", "2027-03-05", 50)
	if err != nil {
		t.Fatal(err)
	}
	if again.Summary.Equal != again.Summary.ResourceDays {
		t.Fatalf("not equal after incremental: %+v", again.Summary)
	}
}

func TestCompareExplainsClinicalRules(t *testing.T) {
	// Professional 2 is on vacation in the CRM (status only) and has a KAS
	// exception for the week; the window has Carnival (CRM: Monday whole day,
	// Tuesday morning only).
	e, im := compareFixture(t, "2027-02-08", "2027-02-12", "ativo", "ativo", "ferias")
	ctx := context.Background()
	res := im.Mapper.Ref("pr", e.doc[2].Hex())
	loc := mustZone(t)
	if err := e.store.AddException(ctx, tenantA, res, scheduling.Exception{Start: time.Date(2027, 2, 8, 0, 0, 0, 0, loc),
		End: time.Date(2027, 2, 13, 0, 0, 0, 0, loc), Reason: "vacation"}); err != nil {
		t.Fatal(err)
	}
	rep, err := im.Compare(ctx, "2027-02-08", "2027-02-12", 50)
	if err != nil {
		t.Fatal(err)
	}
	s := rep.Summary
	// Prof 2: 4 working days closed in the KAS but open in the CRM (the
	// Monday is a holiday on both sides) -> clinical. Profs 0 and 1: Tuesday
	// morning-only holiday -> clinical. Everything else equal.
	if s.ClinicalRules != 6 || s.RealDivergence != 0 || s.ImportErrors != 0 || s.Equal != s.ResourceDays-6 {
		t.Fatalf("summary %+v days %+v", s, rep.Days)
	}
	for _, d := range rep.Days {
		if d.ResourceID == res && (!strings.Contains(strings.Join(d.Reasons, ","), "kas_exception") ||
			!strings.Contains(strings.Join(d.Reasons, ","), "professional_status_ferias")) {
			t.Fatalf("vacation day without its reasons: %+v", d)
		}
		if d.ResourceID != res && (d.Day != "2027-02-09" || d.Reasons[0] != "holiday_calendar") {
			t.Fatalf("unexpected: %+v", d)
		}
	}
}

func TestCompareFlagsRealDivergenceAndUnmirroredProfessional(t *testing.T) {
	e, im := compareFixture(t, "2027-03-01", "2027-03-05")
	ctx := context.Background()
	// Someone changed the KAS hours of professional 1 by hand: no known reason.
	res := im.Mapper.Ref("pr", e.doc[1].Hex())
	if err := e.store.PutWeeklyRules(ctx, tenantA, res, []scheduling.WeeklyRule{{Weekday: 3, StartMin: 8 * 60, EndMin: 8*60 + 40}}); err != nil {
		t.Fatal(err)
	}
	// A professional created in the CRM after the last sync.
	late := bson.NewObjectID()
	if _, err := e.db.Collection(DoctorCollection).InsertOne(ctx, bson.D{{Key: "_id", Value: late}, {Key: "status", Value: "ativo"},
		{Key: "weeklyAvailability", Value: bson.A{bson.D{{Key: "day", Value: "monday"}, {Key: "times", Value: bson.A{"08:00"}}}}}}); err != nil {
		t.Fatal(err)
	}
	rep, err := im.Compare(ctx, "2027-03-01", "2027-03-05", 50)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.RealDivergence != 5 || rep.Summary.ImportErrors != 1 {
		t.Fatalf("summary %+v", rep.Summary)
	}
	hist, err := e.store.ListComparisons(ctx, tenantA, Source, 10)
	if err != nil || len(hist) != 1 || hist[0].RealDivergence != 5 || hist[0].WindowFrom != "2027-03-01" {
		t.Fatalf("history %+v %v", hist, err)
	}
	if _, err := im.Compare(ctx, "2027-03-01", "2027-03-05", 50); err != nil {
		t.Fatal(err)
	}
	if hist, _ = e.store.ListComparisons(ctx, tenantA, Source, 10); len(hist) != 2 || hist[0].ID <= hist[1].ID {
		t.Fatalf("history order %+v", hist)
	}
	// Reports and logs carry no CRM id.
	b, _ := json.Marshal(rep)
	for _, id := range append(append([]bson.ObjectID{late}, e.doc...), e.pat...) {
		if strings.Contains(string(b), id.Hex()) || strings.Contains(e.logs.String(), id.Hex()) {
			t.Fatal("a CRM id leaked into the report or logs")
		}
	}
}

func TestCompareRefusesBadWindows(t *testing.T) {
	_, im := compareFixture(t, "2027-03-01", "2027-03-05")
	for _, w := range [][2]string{{"2027-03-05", "2027-03-01"}, {"2027-01-01", "2027-12-31"}, {"ontem", "hoje"}} {
		if _, err := im.Compare(context.Background(), w[0], w[1], 10); err == nil {
			t.Fatalf("window %v accepted", w)
		}
	}
}

func TestCRMHolidayCalendarAsRead(t *testing.T) {
	cases := map[string]string{
		"2027-01-01": "full", "2027-02-08": "full", "2027-02-09": "morning", "2027-02-10": "",
		"2027-03-26": "full", "2027-05-27": "full", "2027-11-20": "", "2027-11-15": "full", "2027-03-01": "",
	}
	for d, want := range cases {
		if got := CRMHoliday(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}
