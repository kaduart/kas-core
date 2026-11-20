// Package calendar computes business deadlines per tenant: a number of business
// days (weekends and listed holidays excluded) ending at a cutoff time of day in
// the tenant's time zone, or a plain duration. It holds no business rule of any
// tenant: the holidays and the zone are data supplied by the caller.
package calendar

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var (
	ErrInvalidCalendar = errors.New("invalid calendar")
	ErrInvalidDuration = errors.New("invalid duration")

	durationPattern = regexp.MustCompile(`^P(?:(\d{1,3})D)?(?:T(?:(\d{1,4})H)?(?:(\d{1,5})M)?(?:(\d{1,6})S)?)?$`)
	datePattern     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	cutoffPattern   = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)
)

// Calendar is a tenant's working calendar. Saturdays and Sundays are never
// business days; Holidays lists further non-business dates (YYYY-MM-DD) in the
// calendar's zone.
type Calendar struct {
	loc      *time.Location
	zone     string
	holidays map[string]struct{}
}

// New builds a calendar for an IANA zone (empty means America/Sao_Paulo).
func New(zone string, holidays []string) (*Calendar, error) {
	if zone == "" {
		zone = "America/Sao_Paulo"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("%w: zone", ErrInvalidCalendar)
	}
	set := make(map[string]struct{}, len(holidays))
	for _, h := range holidays {
		if !datePattern.MatchString(h) {
			return nil, fmt.Errorf("%w: holiday %q", ErrInvalidCalendar, h)
		}
		if _, err := time.ParseInLocation("2006-01-02", h, loc); err != nil {
			return nil, fmt.Errorf("%w: holiday %q", ErrInvalidCalendar, h)
		}
		set[h] = struct{}{}
	}
	return &Calendar{loc: loc, zone: zone, holidays: set}, nil
}

// Zone returns the IANA zone name.
func (c *Calendar) Zone() string { return c.zone }

// IsBusinessDay reports whether the calendar date of t (in the calendar's zone)
// is a business day.
func (c *Calendar) IsBusinessDay(t time.Time) bool {
	local := t.In(c.loc)
	switch local.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	_, holiday := c.holidays[local.Format("2006-01-02")]
	return !holiday
}

// AddBusinessDays returns the instant at cutoff ("HH:MM") on the n-th business
// day after the calendar date of start. n must be 1..366. The start date itself
// never counts, whatever the hour.
func (c *Calendar) AddBusinessDays(start time.Time, n int, cutoff string) (time.Time, error) {
	if n < 1 || n > 366 {
		return time.Time{}, fmt.Errorf("%w: business days", ErrInvalidDuration)
	}
	m := cutoffPattern.FindStringSubmatch(cutoff)
	if m == nil {
		return time.Time{}, fmt.Errorf("%w: cutoff", ErrInvalidDuration)
	}
	hour, _ := strconv.Atoi(m[1])
	minute, _ := strconv.Atoi(m[2])
	local := start.In(c.loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, c.loc)
	for counted := 0; counted < n; {
		day = day.AddDate(0, 0, 1)
		if c.IsBusinessDay(day) {
			counted++
		}
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, c.loc).UTC(), nil
}

// ParseDuration parses the ISO-8601 subset PnDTnHnMnS (no years, months or
// weeks; days are 24 hours). The zero duration is rejected.
func ParseDuration(s string) (time.Duration, error) {
	m := durationPattern.FindStringSubmatch(s)
	if m == nil || s == "P" || s == "PT" || (len(s) > 0 && s[len(s)-1] == 'T') {
		return 0, ErrInvalidDuration
	}
	units := []time.Duration{24 * time.Hour, time.Hour, time.Minute, time.Second}
	var total time.Duration
	for i, unit := range units {
		if m[i+1] == "" {
			continue
		}
		v, err := strconv.Atoi(m[i+1])
		if err != nil {
			return 0, ErrInvalidDuration
		}
		total += time.Duration(v) * unit
	}
	if total <= 0 {
		return 0, ErrInvalidDuration
	}
	return total, nil
}
