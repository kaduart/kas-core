package audit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultReadLimit and MaxReadLimit bound one page of audit records.
	DefaultReadLimit = 50
	MaxReadLimit     = 200
)

var (
	ErrInvalidQuery  = errors.New("invalid audit query")
	ErrInvalidCursor = errors.New("invalid audit cursor")
	// ErrReadUnsupported is returned when the repository cannot be queried.
	ErrReadUnsupported = errors.New("audit store does not support reading")
)

// Query selects records of ONE tenant. The tenant always comes from the
// authenticated service, never from a free request field: Query has no way to
// ask for another tenant's records because the stores filter on TenantID first.
type Query struct {
	TenantID   string
	EntityType string
	EntityID   string
	// Since keeps records recorded at or after this instant (recorded_at is the
	// server-assigned received_at). Zero means no lower bound.
	Since  time.Time
	Limit  int
	Cursor string
}

// Position is a point in the stable order (recorded_at, id). RecordedMicros is
// the server receipt time truncated to microseconds, the finest resolution both
// stores keep; ID is the store's sequence (PostgreSQL identity, JSON Lines line).
type Position struct {
	RecordedMicros int64
	ID             int64
}

func (p Position) after(o Position) bool {
	return p.RecordedMicros > o.RecordedMicros || (p.RecordedMicros == o.RecordedMicros && p.ID > o.ID)
}

// Page is one page of records in (recorded_at, id) order.
type Page struct {
	Items []Event
	// NextCursor is empty when there are no further records.
	NextCursor string
}

// Reader is implemented by stores that can be queried.
type Reader interface {
	// Query returns at most q.Limit records after q's cursor position. The
	// service has already validated q; q.Cursor is decoded into after.
	Query(ctx context.Context, q Query, after *Position) (Page, error)
}

// ValidateQuery checks the filters and limit and normalizes the limit.
func ValidateQuery(q Query) (Query, error) {
	if q.TenantID == "" {
		return Query{}, ErrInvalidQuery
	}
	if q.Limit == 0 {
		q.Limit = DefaultReadLimit
	}
	if q.Limit < 1 || q.Limit > MaxReadLimit {
		return Query{}, ErrInvalidQuery
	}
	if q.EntityType != "" && !identifierPattern.MatchString(q.EntityType) {
		return Query{}, ErrInvalidQuery
	}
	if q.EntityID != "" && !identifierPattern.MatchString(q.EntityID) {
		return Query{}, ErrInvalidQuery
	}
	if q.EntityID != "" && q.EntityType == "" {
		return Query{}, ErrInvalidQuery // an entity ID is only meaningful within a type
	}
	return q, nil
}

// EncodeCursor makes the opaque cursor for the last record of a page.
func EncodeCursor(p Position) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("v1.%d.%d", p.RecordedMicros, p.ID)))
}

// DecodeCursor parses a cursor. Cursors carry no tenant and no filter: they are
// only a position, and every query is re-filtered by the authenticated tenant.
func DecodeCursor(cursor string) (Position, error) {
	if len(cursor) > 128 {
		return Position{}, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return Position{}, ErrInvalidCursor
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return Position{}, ErrInvalidCursor
	}
	micros, err1 := strconv.ParseInt(parts[1], 10, 64)
	id, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || id < 0 {
		return Position{}, ErrInvalidCursor
	}
	return Position{RecordedMicros: micros, ID: id}, nil
}

// List validates the query and reads one page through the repository.
func (s *Service) List(ctx context.Context, q Query) (Page, error) {
	if s == nil || s.repository == nil {
		return Page{}, ErrStoreUnavailable
	}
	reader, ok := s.repository.(Reader)
	if !ok {
		return Page{}, ErrReadUnsupported
	}
	q, err := ValidateQuery(q)
	if err != nil {
		return Page{}, err
	}
	var after *Position
	if q.Cursor != "" {
		position, err := DecodeCursor(q.Cursor)
		if err != nil {
			return Page{}, err
		}
		after = &position
	}
	page, err := reader.Query(ctx, q, after)
	if err != nil {
		if errors.Is(err, ErrInvalidQuery) {
			return Page{}, err
		}
		return Page{}, ErrStoreUnavailable
	}
	return page, nil
}

// micros truncates an instant to microseconds since the Unix epoch.
func micros(t time.Time) int64 { return t.UnixMicro() }
