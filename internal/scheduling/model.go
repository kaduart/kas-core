// Package scheduling is the KAS generic Agenda (spec "Agenda genérica na KAS",
// decisions D-A1..D-A4). It knows resources, weekly rules, exceptions,
// services and bookings of any business. It knows nothing about patients,
// packages, payments or any tenant's rules: a booking points to its client only
// through client_ref, an opaque id owned by the tenant's own system.
//
// The pure calculations (availability, conflicts, recurring series) live in
// compute.go and never touch the database. The Postgres store (store.go) keeps
// the data and is the only place that writes bookings; double booking is refused
// by the database itself, not by the calculation.
package scheduling

import (
	"errors"
	"regexp"
	"time"
)

// ContractVersion is the version of the HTTP contract.
const ContractVersion = "v1"

// Booking statuses. Active ones hold the slot.
const (
	StatusTentative = "tentative"
	StatusScheduled = "scheduled"
	StatusConfirmed = "confirmed"
	StatusCompleted = "completed"
	StatusCanceled  = "canceled"
	StatusNoShow    = "no_show"
)

// DefaultZone is used when a tenant has no settings yet.
const DefaultZone = "America/Sao_Paulo"

// Limits of the calculations, so one request cannot ask for unbounded work.
const (
	MaxRangeDays     = 62
	MaxSeriesCount   = 520
	MaxSeriesHorizon = 2 * 366 * 24 * time.Hour
	MaxCandidates    = 600
	MaxHolidays      = 400
)

var (
	ErrNotFound        = errors.New("not_found")
	ErrInvalid         = errors.New("invalid_request")
	ErrSlotTaken       = errors.New("slot_taken")
	ErrClientBusy      = errors.New("client_busy")
	ErrVersionConflict = errors.New("version_conflict")
	// ErrIdempotencyMismatch: the key was already used for a different booking.
	ErrIdempotencyMismatch = errors.New("idempotency_mismatch")
	// ErrUnavailable: the database kept failing (serialization, deadlock) after
	// the automatic retries. Nothing partial was written.
	ErrUnavailable = errors.New("scheduling_unavailable")
)

var refPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// ValidRef reports whether s is an acceptable opaque identifier (tenant, resource,
// service, booking, client_ref, idempotency key). It cannot hold spaces or "@",
// which keeps names and e-mails out by construction.
func ValidRef(s string) bool { return refPattern.MatchString(s) }

// ActiveStatus reports whether a booking in this status holds its slot.
func ActiveStatus(status string) bool {
	return status == StatusTentative || status == StatusScheduled || status == StatusConfirmed
}

func validStatus(status string) bool {
	switch status {
	case StatusTentative, StatusScheduled, StatusConfirmed, StatusCompleted, StatusCanceled, StatusNoShow:
		return true
	}
	return false
}

// Settings are the tenant-wide options.
type Settings struct {
	Zone string `json:"zone"`
	// Holidays are local dates (YYYY-MM-DD) with no availability.
	Holidays []string `json:"holidays"`
	// ClientExclusive: the same client cannot hold two overlapping bookings.
	ClientExclusive bool `json:"client_exclusive"`
}

// DefaultSettings applies when a tenant never saved settings.
func DefaultSettings() Settings {
	return Settings{Zone: DefaultZone, Holidays: []string{}, ClientExclusive: true}
}

// Resource is anything that can be booked: a person, a room, an equipment.
// Capacity above 1 means a group (several bookings at the same time).
type Resource struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	Tags     []string `json:"tags"`
	Capacity int      `json:"capacity"`
	Active   bool     `json:"active"`
}

// WeeklyRule opens a window on one weekday (0 = Sunday), in local minutes.
// ValidFrom/ValidTo are optional local dates (YYYY-MM-DD), inclusive.
type WeeklyRule struct {
	Weekday   int    `json:"weekday"`
	StartMin  int    `json:"start_min"`
	EndMin    int    `json:"end_min"`
	ValidFrom string `json:"valid_from,omitempty"`
	ValidTo   string `json:"valid_to,omitempty"`
}

// Exception closes an interval of one resource (vacation, leave, day off).
type Exception struct {
	Start  time.Time `json:"starts_at"`
	End    time.Time `json:"ends_at"`
	Reason string    `json:"reason"`
}

// Service gives a duration and a buffer (minutes kept free around a booking).
type Service struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	DurationMin int    `json:"duration_min"`
	BufferMin   int    `json:"buffer_min"`
}

// Booking is one reservation. It never holds personal data.
type Booking struct {
	ID         string    `json:"id"`
	ResourceID string    `json:"resource_id"`
	ClientRef  string    `json:"client_ref"`
	ServiceID  string    `json:"service_id,omitempty"`
	Start      time.Time `json:"starts_at"`
	End        time.Time `json:"ends_at"`
	Status     string    `json:"status"`
	SeriesID   string    `json:"series_id,omitempty"`
	Version    int       `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Interval is a half-open time interval [Start, End).
type Interval struct {
	Start time.Time `json:"starts_at"`
	End   time.Time `json:"ends_at"`
}

// Overlaps uses '[)': an interval ending at 09:40 does not touch one starting at 09:40.
func (a Interval) Overlaps(b Interval) bool { return a.Start.Before(b.End) && b.Start.Before(a.End) }

func (b Booking) interval() Interval { return Interval{Start: b.Start, End: b.End} }

const dateLayout = "2006-01-02"

func parseDate(s string) (time.Time, bool) {
	t, err := time.Parse(dateLayout, s)
	return t, err == nil
}

// ValidateSettings checks the zone and the holiday list.
func ValidateSettings(s Settings) error {
	if _, err := time.LoadLocation(s.Zone); err != nil || s.Zone == "" || s.Zone == "Local" {
		return ErrInvalid
	}
	if len(s.Holidays) > MaxHolidays {
		return ErrInvalid
	}
	for _, h := range s.Holidays {
		if _, ok := parseDate(h); !ok {
			return ErrInvalid
		}
	}
	return nil
}
