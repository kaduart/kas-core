package scheduling

import (
	"fmt"
	"testing"
	"time"
)

// Pure calculation cost, without database or HTTP (phase 2 benchmark):
//
//	go test ./internal/scheduling -run '^$' -bench . -benchmem -count 5

func benchBookings(days, perDay int) []Booking {
	var out []Booking
	first := at("2027-03-01 08:00")
	for d := 0; d < days; d++ {
		for k := 0; k < perDay; k++ {
			s := first.AddDate(0, 0, d).Add(time.Duration(k) * 40 * time.Minute)
			out = append(out, Booking{ID: fmt.Sprintf("b%d-%d", d, k), ResourceID: "sala-1", ClientRef: fmt.Sprintf("c%d", k),
				Start: s, End: s.Add(40 * time.Minute), BlockedUntil: s.Add(40 * time.Minute), Status: StatusScheduled})
		}
	}
	return out
}

func BenchmarkAvailabilityOneWeek(b *testing.B) {
	in := AvailabilityInput{Settings: DefaultSettings(), Resource: Resource{ID: "sala-1", Capacity: 1, Active: true},
		From: "2027-03-01", To: "2027-03-07", DurationMin: 40, Bookings: benchBookings(7, 10)}
	for wd := 1; wd <= 5; wd++ {
		in.Rules = append(in.Rules, WeeklyRule{Weekday: wd, StartMin: 8 * 60, EndMin: 18 * 60})
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Availability(in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAvailabilityOneMonthWithBuffer(b *testing.B) {
	in := AvailabilityInput{Settings: DefaultSettings(), Resource: Resource{ID: "sala-1", Capacity: 1, Active: true},
		From: "2027-03-01", To: "2027-03-31", DurationMin: 40, BufferMin: 10, StepMin: 10, Bookings: benchBookings(31, 10)}
	for wd := 1; wd <= 5; wd++ {
		in.Rules = append(in.Rules, WeeklyRule{Weekday: wd, StartMin: 8 * 60, EndMin: 18 * 60})
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Availability(in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFindConflicts20Candidates(b *testing.B) {
	existing := benchBookings(31, 10)
	var cands []Candidate
	for i := 0; i < 20; i++ {
		s := at("2027-03-01 15:00").AddDate(0, 0, i)
		cands = append(cands, Candidate{Key: fmt.Sprint(i), ResourceID: "sala-1", ClientRef: "c-novo", Start: s, End: s.Add(40 * time.Minute)})
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := FindConflicts(cands, existing, nil, true); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExpandSeriesOneYear(b *testing.B) {
	rule := SeriesRule{Frequency: "weekly", Weekdays: []int{1, 3, 5}, Time: "09:00", DurationMin: 40, StartDate: "2027-01-04",
		Count: 150, HolidayPolicy: HolidaySkip}
	st := DefaultSettings()
	st.Holidays = []string{"2027-04-21", "2027-05-01", "2027-09-07", "2027-10-12", "2027-11-02", "2027-11-15", "2027-12-25"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ExpandSeries(rule, st); err != nil {
			b.Fatal(err)
		}
	}
}
