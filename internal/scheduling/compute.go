package scheduling

import (
	"fmt"
	"sort"
	"time"
)

// Pure calculations: no database, no clock unless given. The same inputs always
// give the same answer, which is what the benchmark and the tests rely on.

// AvailabilityInput holds everything the availability calculation needs.
type AvailabilityInput struct {
	Settings   Settings
	Resource   Resource
	Rules      []WeeklyRule
	Exceptions []Exception
	// Bookings of the resource in the period, any status (only active ones count).
	Bookings []Booking
	// From and To are local dates (YYYY-MM-DD), inclusive.
	From, To    string
	DurationMin int
	BufferMin   int
	// StepMin is the distance between candidate starts (0 = DurationMin).
	StepMin int
	// NotBefore drops slots that start earlier (zero = no filter).
	NotBefore time.Time
}

// Slot is a free start time. Remaining is the number of places left (1 for an
// exclusive resource).
type Slot struct {
	Start     time.Time `json:"starts_at"`
	End       time.Time `json:"ends_at"`
	Remaining int       `json:"remaining"`
}

func zoneOf(s Settings) (*time.Location, error) {
	if s.Zone == "" || s.Zone == "Local" {
		return nil, ErrInvalid
	}
	loc, err := time.LoadLocation(s.Zone)
	if err != nil {
		return nil, ErrInvalid
	}
	return loc, nil
}

func holidaySet(s Settings) map[string]bool {
	set := make(map[string]bool, len(s.Holidays))
	for _, h := range s.Holidays {
		set[h] = true
	}
	return set
}

// localAt builds the instant of a local date plus minutes from midnight, in the
// zone. time.Date resolves daylight-saving gaps and folds the same way every time.
func localAt(day time.Time, minutes int, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, loc)
}

func ruleApplies(r WeeklyRule, day time.Time) bool {
	if int(day.Weekday()) != r.Weekday {
		return false
	}
	date := day.Format(dateLayout)
	if r.ValidFrom != "" && date < r.ValidFrom {
		return false
	}
	if r.ValidTo != "" && date > r.ValidTo {
		return false
	}
	return true
}

// ValidateRule checks one weekly rule.
func ValidateRule(r WeeklyRule) error {
	if r.Weekday < 0 || r.Weekday > 6 || r.StartMin < 0 || r.EndMin > 1440 || r.EndMin <= r.StartMin {
		return ErrInvalid
	}
	for _, d := range []string{r.ValidFrom, r.ValidTo} {
		if d != "" {
			if _, ok := parseDate(d); !ok {
				return ErrInvalid
			}
		}
	}
	if r.ValidFrom != "" && r.ValidTo != "" && r.ValidTo < r.ValidFrom {
		return ErrInvalid
	}
	return nil
}

// Availability lists the free slots of one resource. A slot is free when:
// the day is not a tenant holiday; a weekly rule covers the whole slot; no
// exception touches it; and fewer active bookings than the resource capacity
// overlap the time it would block (slot plus the service buffer), each booking
// counted with its own buffer. That is the same rule the database enforces.
func Availability(in AvailabilityInput) ([]Slot, error) {
	loc, err := zoneOf(in.Settings)
	if err != nil {
		return nil, err
	}
	from, ok1 := parseDate(in.From)
	to, ok2 := parseDate(in.To)
	if !ok1 || !ok2 || to.Before(from) || to.Sub(from) > (MaxRangeDays-1)*24*time.Hour {
		return nil, ErrInvalid
	}
	if in.DurationMin < 5 || in.DurationMin > 720 || in.BufferMin < 0 || in.BufferMin > 240 || in.StepMin < 0 || in.StepMin > 720 {
		return nil, ErrInvalid
	}
	for _, r := range in.Rules {
		if err := ValidateRule(r); err != nil {
			return nil, err
		}
	}
	if !in.Resource.Active {
		return []Slot{}, nil
	}
	capacity := in.Resource.Capacity
	if capacity < 1 {
		capacity = 1
	}
	step := in.StepMin
	if step == 0 {
		step = in.DurationMin
	}
	duration := time.Duration(in.DurationMin) * time.Minute
	buffer := time.Duration(in.BufferMin) * time.Minute
	holidays := holidaySet(in.Settings)

	active := make([]Interval, 0, len(in.Bookings))
	for _, b := range in.Bookings {
		if ActiveStatus(b.Status) && b.ResourceID == in.Resource.ID {
			active = append(active, b.held())
		}
	}
	seen := map[int64]bool{}
	slots := []Slot{}
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		if holidays[day.Format(dateLayout)] {
			continue
		}
		for _, rule := range in.Rules {
			if !ruleApplies(rule, day) {
				continue
			}
			for m := rule.StartMin; m+in.DurationMin <= rule.EndMin; m += step {
				start := localAt(day, m, loc)
				slot := Interval{Start: start, End: start.Add(duration)}
				if seen[start.Unix()] || (!in.NotBefore.IsZero() && start.Before(in.NotBefore)) {
					continue
				}
				if closedBy(slot, in.Exceptions) {
					continue
				}
				guarded := Interval{Start: slot.Start, End: slot.End.Add(buffer)}
				taken := 0
				for _, b := range active {
					if guarded.Overlaps(b) {
						taken++
					}
				}
				if taken >= capacity {
					continue
				}
				seen[start.Unix()] = true
				slots = append(slots, Slot{Start: slot.Start, End: slot.End, Remaining: capacity - taken})
			}
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Start.Before(slots[j].Start) })
	return slots, nil
}

func closedBy(slot Interval, exceptions []Exception) bool {
	for _, e := range exceptions {
		if slot.Overlaps(Interval{Start: e.Start, End: e.End}) {
			return true
		}
	}
	return false
}

// Candidate is a booking someone wants to make, checked before writing.
type Candidate struct {
	Key        string    `json:"key"`
	ResourceID string    `json:"resource_id"`
	ClientRef  string    `json:"client_ref"`
	Start      time.Time `json:"starts_at"`
	End        time.Time `json:"ends_at"`
	// BufferMin blocks the resource after End (0 to 240).
	BufferMin int `json:"buffer_min,omitempty"`
}

// Conflict explains why a candidate cannot be booked.
type Conflict struct {
	Key string `json:"key"`
	// Kind is resource (exclusive resource taken), capacity (group full) or
	// client (the same client already has an overlapping booking).
	Kind string `json:"kind"`
	// With is the booking id or the earlier candidate key it clashes with.
	With string `json:"with"`
}

// FindConflicts checks candidates against existing bookings and against each
// other, in order: an earlier candidate wins over a later one. Overlap is
// half-open, so 09:00-09:40 and 09:40-10:20 never conflict, while a partial or
// contained overlap always does. capacity maps resource id to its capacity
// (missing = 1). For a group, every overlapping active booking takes a place.
func FindConflicts(candidates []Candidate, existing []Booking, capacity map[string]int, clientExclusive bool) ([]Conflict, error) {
	if len(candidates) == 0 || len(candidates) > MaxCandidates {
		return nil, ErrInvalid
	}
	// iv is the booking itself (used for the client rule); block adds the
	// buffer (used for the resource rule), as the database constraints do.
	type held struct {
		id, resource, client string
		iv, block            Interval
	}
	holds := make([]held, 0, len(existing)+len(candidates))
	for _, b := range existing {
		if ActiveStatus(b.Status) {
			holds = append(holds, held{b.ID, b.ResourceID, b.ClientRef, b.interval(), b.held()})
		}
	}
	keys := map[string]bool{}
	conflicts := []Conflict{}
	for _, c := range candidates {
		if !ValidRef(c.Key) || keys[c.Key] || !ValidRef(c.ResourceID) || !ValidRef(c.ClientRef) || !c.End.After(c.Start) ||
			c.BufferMin < 0 || c.BufferMin > 240 {
			return nil, ErrInvalid
		}
		keys[c.Key] = true
		iv := Interval{Start: c.Start, End: c.End}
		block := Interval{Start: c.Start, End: c.End.Add(time.Duration(c.BufferMin) * time.Minute)}
		limit := capacity[c.ResourceID]
		if limit < 1 {
			limit = 1
		}
		var found *Conflict
		taken := 0
		for _, h := range holds {
			if clientExclusive && h.client == c.ClientRef && iv.Overlaps(h.iv) {
				found = &Conflict{Key: c.Key, Kind: "client", With: h.id}
				break
			}
			if h.resource == c.ResourceID && block.Overlaps(h.block) {
				taken++
				if taken >= limit {
					kind := "resource"
					if limit > 1 {
						kind = "capacity"
					}
					found = &Conflict{Key: c.Key, Kind: kind, With: h.id}
					break
				}
			}
		}
		if found != nil {
			conflicts = append(conflicts, *found)
			continue
		}
		holds = append(holds, held{c.Key, c.ResourceID, c.ClientRef, iv, block})
	}
	return conflicts, nil
}

// Holiday policies of a recurring series.
const (
	HolidayKeep = "keep" // holidays are booked like any day
	HolidaySkip = "skip" // the holiday date is skipped; a count series grows to keep its size
)

// SeriesRule describes a recurring booking. Count, Until or both are set; with
// both, the series stops at whichever comes first ("N sessions, until the
// contract ends").
type SeriesRule struct {
	Frequency     string `json:"frequency"` // weekly or biweekly
	Weekdays      []int  `json:"weekdays"`  // 0 = Sunday
	Time          string `json:"time"`      // HH:MM local
	DurationMin   int    `json:"duration_min"`
	StartDate     string `json:"start_date"`
	Count         int    `json:"count,omitempty"`
	Until         string `json:"until,omitempty"`
	HolidayPolicy string `json:"holiday_policy"`
	// SkipDates are local dates the series leaves out whatever the policy
	// (chosen by the caller, e.g. a module's own exclusions); they are reported
	// with the holidays and a Count series grows past them.
	SkipDates []string `json:"skip_dates,omitempty"`
	// TimeByWeekday overrides Time for some weekdays (key 0 = Sunday), for a
	// series that meets at different hours on different days.
	TimeByWeekday map[int]string `json:"time_by_weekday,omitempty"`
}

// SeriesResult lists the occurrences and the holiday dates left out.
type SeriesResult struct {
	Occurrences     []Interval `json:"occurrences"`
	SkippedHolidays []string   `json:"skipped_holidays"`
}

func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// ExpandSeries turns a rule into dated occurrences. Rounds are weeks counted
// from the week (Monday to Sunday) that holds StartDate: weekly uses every
// week, biweekly every other one (weeks 0, 2, 4...), so every weekday of a
// biweekly series falls in the same week, as in the CRM's package form. Days of
// week 0 before StartDate are left out. With HolidaySkip, a holiday date (or a
// date in SkipDates) is dropped and a Count series continues until it has
// Count occurrences. The series may not reach past two years from StartDate.
func ExpandSeries(rule SeriesRule, settings Settings) (SeriesResult, error) {
	loc, err := zoneOf(settings)
	if err != nil {
		return SeriesResult{}, err
	}
	stepDays := 0
	switch rule.Frequency {
	case "weekly":
		stepDays = 7
	case "biweekly":
		stepDays = 14
	default:
		return SeriesResult{}, ErrInvalid
	}
	minutes, ok := parseClock(rule.Time)
	start, ok2 := parseDate(rule.StartDate)
	if !ok || !ok2 || rule.DurationMin < 5 || rule.DurationMin > 720 {
		return SeriesResult{}, ErrInvalid
	}
	if rule.HolidayPolicy != HolidayKeep && rule.HolidayPolicy != HolidaySkip {
		return SeriesResult{}, ErrInvalid
	}
	if (rule.Count == 0 && rule.Until == "") || rule.Count < 0 || rule.Count > MaxSeriesCount {
		return SeriesResult{}, ErrInvalid
	}
	horizon := start.Add(MaxSeriesHorizon)
	until := horizon
	if rule.Until != "" {
		u, ok := parseDate(rule.Until)
		if !ok || u.Before(start) || u.After(horizon) {
			return SeriesResult{}, ErrInvalid
		}
		until = u
	}
	if len(rule.Weekdays) == 0 || len(rule.Weekdays) > 7 || len(rule.SkipDates) > MaxHolidays {
		return SeriesResult{}, ErrInvalid
	}
	skip := map[string]bool{}
	for _, d := range rule.SkipDates {
		if _, ok := parseDate(d); !ok {
			return SeriesResult{}, ErrInvalid
		}
		skip[d] = true
	}
	clockOf := map[int]int{}
	for wd, c := range rule.TimeByWeekday {
		m, ok := parseClock(c)
		if !ok || wd < 0 || wd > 6 {
			return SeriesResult{}, ErrInvalid
		}
		clockOf[(wd+6)%7] = m
	}
	// Offsets from the Monday of the start week, in week order (Sunday last).
	used := map[int]bool{}
	offsets := make([]int, 0, len(rule.Weekdays))
	for _, wd := range rule.Weekdays {
		if wd < 0 || wd > 6 || used[wd] {
			return SeriesResult{}, ErrInvalid
		}
		used[wd] = true
		offsets = append(offsets, (wd+6)%7)
	}
	sort.Ints(offsets)
	for wd := range rule.TimeByWeekday {
		if !used[wd] {
			return SeriesResult{}, ErrInvalid
		}
	}
	monday := start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))

	holidays := holidaySet(settings)
	duration := time.Duration(rule.DurationMin) * time.Minute
	res := SeriesResult{Occurrences: []Interval{}, SkippedHolidays: []string{}}
	for k := 0; ; k++ {
		for _, off := range offsets {
			day := monday.AddDate(0, 0, k*stepDays+off)
			if day.Before(start) {
				continue
			}
			if day.After(until) {
				if rule.Count > 0 && len(res.Occurrences) < rule.Count && rule.Until == "" {
					return SeriesResult{}, fmt.Errorf("%w: series would pass two years", ErrInvalid)
				}
				return res, nil
			}
			date := day.Format(dateLayout)
			if skip[date] || (holidays[date] && rule.HolidayPolicy == HolidaySkip) {
				res.SkippedHolidays = append(res.SkippedHolidays, date)
				continue
			}
			m := minutes
			if c, ok := clockOf[off]; ok {
				m = c
			}
			s := localAt(day, m, loc)
			res.Occurrences = append(res.Occurrences, Interval{Start: s, End: s.Add(duration)})
			if rule.Count > 0 && len(res.Occurrences) == rule.Count {
				return res, nil
			}
		}
	}
}

// OpenAt says whether [start, end) lies inside the resource's opening hours:
// one local day that is not a tenant holiday, fully covered by one weekly
// rule of that weekday, and touched by no exception. The buffer after end is
// not required to fit (it is cleanup time, not service time).
func OpenAt(settings Settings, rules []WeeklyRule, exceptions []Exception, start, end time.Time) (bool, error) {
	loc, err := zoneOf(settings)
	if err != nil {
		return false, err
	}
	ls, le := start.In(loc), end.In(loc)
	day := time.Date(ls.Year(), ls.Month(), ls.Day(), 0, 0, 0, 0, loc)
	if !end.After(start) || le.Sub(day) > 24*time.Hour || holidaySet(settings)[day.Format(dateLayout)] {
		return false, nil
	}
	if closedBy(Interval{Start: start, End: end}, exceptions) {
		return false, nil
	}
	for _, r := range rules {
		if !ruleApplies(r, day) {
			continue
		}
		if !localAt(day, r.StartMin, loc).After(start) && !localAt(day, r.EndMin, loc).Before(end) {
			return true, nil
		}
	}
	return false, nil
}
