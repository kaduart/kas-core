package scheduling

import (
	"errors"
	"testing"
	"time"
)

var sp = mustZone("America/Sao_Paulo")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// at builds a São Paulo local instant: at("2026-12-07 09:00").
func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, sp)
	if err != nil {
		panic(err)
	}
	return t
}

func starts(slots []Slot) []string {
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = s.Start.In(sp).Format("01-02 15:04")
	}
	return out
}

func equalStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// 2026-12-07 is a Monday.
func baseInput() AvailabilityInput {
	return AvailabilityInput{
		Settings: DefaultSettings(),
		Resource: Resource{ID: "sala-1", Capacity: 1, Active: true},
		Rules:    []WeeklyRule{{Weekday: 1, StartMin: 9 * 60, EndMin: 12 * 60}},
		From:     "2026-12-07", To: "2026-12-07", DurationMin: 40,
	}
}

func TestAvailabilityRulesAndZone(t *testing.T) {
	slots, err := Availability(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	equalStrings(t, starts(slots), []string{"12-07 09:00", "12-07 09:40", "12-07 10:20", "12-07 11:00"})
	// 09:00 in São Paulo (UTC-3) is 12:00 UTC.
	if got := slots[0].Start.UTC().Format("15:04"); got != "12:00" {
		t.Fatalf("zone not applied: %s UTC", got)
	}
	in := baseInput()
	in.Settings.Zone = "Europe/Lisbon"
	slots, _ = Availability(in)
	if got := slots[0].Start.UTC().Format("15:04"); got != "09:00" {
		t.Fatalf("Lisbon 09:00 should be 09:00 UTC in December, got %s", got)
	}
}

func TestAvailabilityExceptionsHolidaysBookingsAndBuffer(t *testing.T) {
	in := baseInput()
	in.To = "2026-12-14"
	in.Settings.Holidays = []string{"2026-12-14"}
	in.Exceptions = []Exception{{Start: at("2026-12-07 09:30"), End: at("2026-12-07 09:45"), Reason: "off"}}
	in.Bookings = []Booking{
		{ID: "b1", ResourceID: "sala-1", Start: at("2026-12-07 10:20"), End: at("2026-12-07 11:00"), Status: StatusScheduled},
		{ID: "b2", ResourceID: "sala-1", Start: at("2026-12-07 11:20"), End: at("2026-12-07 12:00"), Status: StatusCanceled},
		{ID: "b3", ResourceID: "outra", Start: at("2026-12-07 09:00"), End: at("2026-12-07 12:00"), Status: StatusScheduled},
	}
	slots, err := Availability(in)
	if err != nil {
		t.Fatal(err)
	}
	// 09:00 and 09:40 touch the exception; 10:20 is booked; the canceled booking
	// and the other resource do not count; the 14th is a holiday.
	equalStrings(t, starts(slots), []string{"12-07 11:00"})

	in.Exceptions = nil
	in.BufferMin = 10
	slots, _ = Availability(in)
	// The new booking needs 40 + 10 minutes: 09:40 would block the room until
	// 10:30, past the 10:20 booking. 11:00 is fine: that booking has no buffer.
	equalStrings(t, starts(slots), []string{"12-07 09:00", "12-07 11:00"})
	// With its own 10-minute buffer, the 10:20 booking holds the room until 11:10.
	in.Bookings[0].BufferMin, in.Bookings[0].BlockedUntil = 10, at("2026-12-07 11:10")
	slots, _ = Availability(in)
	equalStrings(t, starts(slots), []string{"12-07 09:00"})
}

func TestAvailabilityCapacityValidityAndNotBefore(t *testing.T) {
	in := baseInput()
	in.Resource.Capacity = 2
	in.Bookings = []Booking{
		{ID: "b1", ResourceID: "sala-1", Start: at("2026-12-07 09:00"), End: at("2026-12-07 09:40"), Status: StatusConfirmed},
		{ID: "b2", ResourceID: "sala-1", Start: at("2026-12-07 09:00"), End: at("2026-12-07 09:40"), Status: StatusTentative},
		{ID: "b3", ResourceID: "sala-1", Start: at("2026-12-07 09:40"), End: at("2026-12-07 10:20"), Status: StatusScheduled},
	}
	slots, _ := Availability(in)
	equalStrings(t, starts(slots), []string{"12-07 09:40", "12-07 10:20", "12-07 11:00"})
	if slots[0].Remaining != 1 || slots[1].Remaining != 2 {
		t.Fatalf("remaining places: %+v", slots)
	}

	in = baseInput()
	in.Rules[0].ValidFrom = "2026-12-08"
	if slots, _ := Availability(in); len(slots) != 0 {
		t.Fatalf("rule not yet valid should open nothing: %v", starts(slots))
	}
	in = baseInput()
	in.NotBefore = at("2026-12-07 09:30")
	slots, _ = Availability(in)
	equalStrings(t, starts(slots), []string{"12-07 09:40", "12-07 10:20", "12-07 11:00"})

	in = baseInput()
	in.Resource.Active = false
	if slots, _ := Availability(in); len(slots) != 0 {
		t.Fatal("inactive resource must have no slots")
	}
}

func TestAvailabilityDaylightSavingIsWallClock(t *testing.T) {
	in := baseInput()
	in.Settings.Zone = "America/New_York"
	// 2026-03-08: clocks jump from 02:00 to 03:00 in New York. Sunday rule 01:00-04:00.
	in.Rules = []WeeklyRule{{Weekday: 0, StartMin: 60, EndMin: 240}}
	in.From, in.To, in.DurationMin = "2026-03-08", "2026-03-08", 60
	slots, err := Availability(in)
	if err != nil {
		t.Fatal(err)
	}
	// 02:00 does not exist that day: the starts are 01:00 and 03:00 local, once each.
	if len(slots) != 2 {
		t.Fatalf("expected 2 wall-clock starts, got %d", len(slots))
	}
	ny := mustZone("America/New_York")
	if got := slots[1].Start.In(ny).Format("15:04"); got != "03:00" {
		t.Fatalf("last start should be 03:00 local, got %s", got)
	}
}

func TestAvailabilityRejectsBadInput(t *testing.T) {
	cases := map[string]func(*AvailabilityInput){
		"zone":     func(in *AvailabilityInput) { in.Settings.Zone = "Marte/Base" },
		"local":    func(in *AvailabilityInput) { in.Settings.Zone = "Local" },
		"range":    func(in *AvailabilityInput) { in.To = "2027-03-01" },
		"reversed": func(in *AvailabilityInput) { in.To = "2026-12-01" },
		"duration": func(in *AvailabilityInput) { in.DurationMin = 1 },
		"rule":     func(in *AvailabilityInput) { in.Rules[0].EndMin = in.Rules[0].StartMin },
	}
	for name, mutate := range cases {
		in := baseInput()
		mutate(&in)
		if _, err := Availability(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func cand(key, resource, client, from, to string) Candidate {
	return Candidate{Key: key, ResourceID: resource, ClientRef: client, Start: at("2026-12-07 " + from), End: at("2026-12-07 " + to)}
}

func TestFindConflictsOverlapRules(t *testing.T) {
	existing := []Booking{
		{ID: "b1", ResourceID: "r1", ClientRef: "c1", Start: at("2026-12-07 09:00"), End: at("2026-12-07 09:40"), Status: StatusScheduled},
		{ID: "b2", ResourceID: "r2", ClientRef: "c2", Start: at("2026-12-07 09:00"), End: at("2026-12-07 10:00"), Status: StatusScheduled},
		{ID: "b3", ResourceID: "r1", ClientRef: "c3", Start: at("2026-12-07 11:00"), End: at("2026-12-07 12:00"), Status: StatusCanceled},
	}
	got, err := FindConflicts([]Candidate{
		cand("parcial", "r1", "x1", "09:20", "10:00"),   // partial overlap with b1
		cand("contida", "r2", "x2", "09:10", "09:30"),   // contained in b2
		cand("encostada", "r1", "x3", "09:40", "10:20"), // touches b1: allowed
		cand("cancelada", "r1", "x4", "11:00", "12:00"), // b3 is canceled: allowed
		cand("cliente", "r9", "c1", "09:30", "09:50"),   // same client as b1
		cand("depois", "r1", "x5", "10:00", "10:30"),    // overlaps the accepted "encostada"
	}, existing, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Conflict{
		"parcial": {Key: "parcial", Kind: "resource", With: "b1"},
		"contida": {Key: "contida", Kind: "resource", With: "b2"},
		"cliente": {Key: "cliente", Kind: "client", With: "b1"},
		"depois":  {Key: "depois", Kind: "resource", With: "encostada"},
	}
	if len(got) != len(want) {
		t.Fatalf("conflicts %+v", got)
	}
	for _, c := range got {
		if want[c.Key] != c {
			t.Fatalf("conflict %+v, want %+v", c, want[c.Key])
		}
	}
	// Buffers: the 08:20 candidate with 10 minutes holds the room until 09:10.
	got, _ = FindConflicts([]Candidate{
		{Key: "antes", ResourceID: "r1", ClientRef: "y1", Start: at("2026-12-07 08:20"), End: at("2026-12-07 09:00"), BufferMin: 10},
		{Key: "antes-ok", ResourceID: "r2", ClientRef: "y2", Start: at("2026-12-07 08:10"), End: at("2026-12-07 08:50"), BufferMin: 10},
	}, existing, nil, true)
	if len(got) != 1 || got[0].Key != "antes" {
		t.Fatalf("buffer conflicts: %+v", got)
	}
	// Client exclusivity off: the same client may overlap.
	got, _ = FindConflicts([]Candidate{cand("cliente", "r9", "c1", "09:30", "09:50")}, existing, nil, false)
	if len(got) != 0 {
		t.Fatalf("client rule off: %+v", got)
	}
}

func TestFindConflictsCapacity(t *testing.T) {
	cands := []Candidate{}
	for _, k := range []string{"a", "b", "c", "d"} {
		cands = append(cands, cand(k, "grupo", "cli-"+k, "14:00", "15:00"))
	}
	got, err := FindConflicts(cands, nil, map[string]int{"grupo": 3}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "d" || got[0].Kind != "capacity" {
		t.Fatalf("group of 3: %+v", got)
	}
	if _, err := FindConflicts([]Candidate{cand("a", "r", "c", "10:00", "09:00")}, nil, nil, true); !errors.Is(err, ErrInvalid) {
		t.Fatal("reversed interval must be invalid")
	}
	if _, err := FindConflicts([]Candidate{cand("a", "r", "Maria Silva", "09:00", "10:00")}, nil, nil, true); !errors.Is(err, ErrInvalid) {
		t.Fatal("a name in client_ref must be refused")
	}
}

func dates(r SeriesResult) []string {
	out := make([]string, len(r.Occurrences))
	for i, o := range r.Occurrences {
		out[i] = o.Start.In(sp).Format("2006-01-02 15:04")
	}
	return out
}

func TestSeriesWeeklyBiweeklyAndHolidays(t *testing.T) {
	st := DefaultSettings()
	st.Holidays = []string{"2026-12-21", "2026-12-28"}
	rule := SeriesRule{Frequency: "weekly", Weekdays: []int{1, 3}, Time: "14:00", DurationMin: 40,
		StartDate: "2026-12-09", Count: 5, HolidayPolicy: HolidaySkip}
	res, err := ExpandSeries(rule, st)
	if err != nil {
		t.Fatal(err)
	}
	// Starts on Wednesday the 9th; Mondays 21 and 28 are holidays and are skipped,
	// and the series grows to keep 5 occurrences.
	equalStrings(t, dates(res), []string{"2026-12-09 14:00", "2026-12-14 14:00", "2026-12-16 14:00", "2026-12-23 14:00", "2026-12-30 14:00"})
	equalStrings(t, res.SkippedHolidays, []string{"2026-12-21", "2026-12-28"})

	rule.HolidayPolicy = HolidayKeep
	res, _ = ExpandSeries(rule, st)
	equalStrings(t, dates(res), []string{"2026-12-09 14:00", "2026-12-14 14:00", "2026-12-16 14:00", "2026-12-21 14:00", "2026-12-23 14:00"})

	bi := SeriesRule{Frequency: "biweekly", Weekdays: []int{1}, Time: "08:30", DurationMin: 50,
		StartDate: "2026-12-07", Until: "2027-01-31", HolidayPolicy: HolidaySkip}
	res, _ = ExpandSeries(bi, st)
	// 12-21 is a holiday and skipped; an Until series does not grow.
	equalStrings(t, dates(res), []string{"2026-12-07 08:30", "2027-01-04 08:30", "2027-01-18 08:30"})
	if d := res.Occurrences[0].End.Sub(res.Occurrences[0].Start); d != 50*time.Minute {
		t.Fatalf("duration %v", d)
	}
}

func TestSeriesLimits(t *testing.T) {
	st := DefaultSettings()
	ok := SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "09:00", DurationMin: 40, StartDate: "2026-12-07", Count: 4, HolidayPolicy: HolidayKeep}
	bad := map[string]func(*SeriesRule){
		"frequency":      func(r *SeriesRule) { r.Frequency = "daily" },
		"no limit":       func(r *SeriesRule) { r.Count = 0 },
		"too many":       func(r *SeriesRule) { r.Count = MaxSeriesCount + 1 },
		"past 2 years":   func(r *SeriesRule) { r.Count = 0; r.Until = "2029-01-01" },
		"count > 2y":     func(r *SeriesRule) { r.Count = 200 }, // 200 Mondays is almost 4 years
		"weekday":        func(r *SeriesRule) { r.Weekdays = []int{7} },
		"repeated day":   func(r *SeriesRule) { r.Weekdays = []int{1, 1} },
		"no weekday":     func(r *SeriesRule) { r.Weekdays = nil },
		"time":           func(r *SeriesRule) { r.Time = "9h" },
		"policy":         func(r *SeriesRule) { r.HolidayPolicy = "shift" },
		"until < start":  func(r *SeriesRule) { r.Count = 0; r.Until = "2026-12-01" },
		"short duration": func(r *SeriesRule) { r.DurationMin = 1 },
	}
	if _, err := ExpandSeries(ok, st); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range bad {
		r := ok
		r.Weekdays = append([]int(nil), ok.Weekdays...)
		mutate(&r)
		if _, err := ExpandSeries(r, st); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	// The maximum count fits in two years with several weekdays.
	full := ok
	full.Weekdays, full.Count = []int{1, 2, 3, 4, 5}, MaxSeriesCount
	res, err := ExpandSeries(full, st)
	if err != nil || len(res.Occurrences) != MaxSeriesCount {
		t.Fatalf("max series: %d, %v", len(res.Occurrences), err)
	}
}
