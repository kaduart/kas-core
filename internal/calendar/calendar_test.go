package calendar

import (
	"testing"
	"time"
)

func TestAddBusinessDaysSkipsWeekendAndHoliday(t *testing.T) {
	cal, err := New("America/Sao_Paulo", []string{"2026-11-24"})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	// Friday 2026-11-20 10:00 local: Mon 23 counts, Tue 24 is a holiday, Wed 25 is the 2nd.
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, loc)
	got, err := cal.AddBusinessDays(start, 2, "23:59")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 25, 23, 59, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("due = %s, want %s", got.In(loc), want)
	}
	// Late on Friday night local (already Saturday in UTC) still starts from Friday.
	got, _ = cal.AddBusinessDays(time.Date(2026, 11, 20, 23, 30, 0, 0, loc), 1, "23:59")
	if want := time.Date(2026, 11, 23, 23, 59, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("due = %s, want %s", got.In(loc), want)
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{"P2D": 48 * time.Hour, "PT5M": 5 * time.Minute, "P1DT2H": 26 * time.Hour, "PT30S": 30 * time.Second}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Fatalf("%s = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "P", "PT", "P1DT", "2D", "P1W", "PT0M", "P1Y"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New("Mars/Olympus", nil); err == nil {
		t.Fatal("bad zone accepted")
	}
	if _, err := New("", []string{"2026-13-01"}); err == nil {
		t.Fatal("bad holiday accepted")
	}
	if _, err := (&Calendar{loc: time.UTC}).AddBusinessDays(time.Now(), 1, "24:00"); err == nil {
		t.Fatal("bad cutoff accepted")
	}
}
