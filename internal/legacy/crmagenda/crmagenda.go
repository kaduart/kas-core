// Package crmagenda reads the CRM's appointments, read-only, and maps them to
// the generic Agenda mirror (scheduling.MirrorItem). It is the only place that
// knows the CRM's fields. Only the fields in Projection are ever read: no name,
// phone, note, value or clinical field leaves MongoDB. Every id is replaced by
// an HMAC with a key the KAS keeps, so the mirror cannot be joined back to the
// CRM without that key.
package crmagenda

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/scheduling"
)

// Source names the CRM in the mirror tables.
const Source = "crm"

// Collection is the CRM's appointments collection.
const Collection = "appointments"

// DefaultDurationMin is the CRM's default (models/Appointment.js: duration 40).
const DefaultDurationMin = 40

// Projection is the complete list of fields read from each appointment.
var Projection = bson.D{
	{Key: "_id", Value: 1}, {Key: "doctor", Value: 1}, {Key: "patient", Value: 1}, {Key: "date", Value: 1},
	{Key: "time", Value: 1}, {Key: "duration", Value: 1}, {Key: "operationalStatus", Value: 1},
	{Key: "updatedAt", Value: 1}, {Key: "isDeleted", Value: 1},
}

// Doc is one appointment as read (only the projected fields).
type Doc struct {
	ID        bson.ObjectID  `bson:"_id"`
	Doctor    *bson.ObjectID `bson:"doctor"`
	Patient   *bson.ObjectID `bson:"patient"`
	Date      time.Time      `bson:"date"`
	Time      string         `bson:"time"`
	Duration  any            `bson:"duration"`
	Status    string         `bson:"operationalStatus"`
	UpdatedAt time.Time      `bson:"updatedAt"`
	IsDeleted bool           `bson:"isDeleted"`
}

// Mapper turns a Doc into a MirrorItem.
type Mapper struct {
	key []byte
	loc *time.Location
}

// NewMapper needs a secret key of at least 32 bytes and the clinic's zone.
func NewMapper(key []byte, zone string) (*Mapper, error) {
	if len(key) < 32 {
		return nil, errors.New("mirror key must have at least 32 bytes")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return nil, errors.New("invalid zone")
	}
	return &Mapper{key: append([]byte(nil), key...), loc: loc}, nil
}

// Ref is the opaque id of a CRM id: prefix + 24 hex chars of HMAC-SHA256.
func (m *Mapper) Ref(kind, raw string) string {
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(kind + "\x00" + raw))
	return kind + "-" + hex.EncodeToString(mac.Sum(nil))[:24]
}

// BookingRef is the mirror id of an appointment id.
func (m *Mapper) BookingRef(id bson.ObjectID) string { return m.Ref("ag", id.Hex()) }

var clock = regexp.MustCompile(`^([01]?[0-9]|2[0-3]):([0-5][0-9])$`)

// Skip reasons (counted, never logged with ids).
const (
	SkipNoProfessional = "no_professional"
	SkipNoClient       = "no_client"
	SkipNoTime         = "no_time"
	SkipBadDuration    = "bad_duration"
)

// KASStatus maps the CRM status so that a mirrored booking holds its slot
// exactly when the CRM's own slot calculation treats it as occupied
// (constants/appointmentStatus.js: only canceled and completed free the slot).
// pre_agendado blocks in the CRM, so it maps to tentative (also blocking).
func KASStatus(crm string, deleted bool) string {
	if deleted {
		return scheduling.StatusCanceled
	}
	switch crm {
	case "canceled", "cancelado", "cancelada":
		return scheduling.StatusCanceled
	case "completed":
		return scheduling.StatusCompleted
	case "pre_agendado":
		return scheduling.StatusTentative
	case "confirmed":
		return scheduling.StatusConfirmed
	default: // scheduled, pending, paid, missed, suspended, processing_*, force_cancelled, empty
		return scheduling.StatusScheduled
	}
}

func durationMin(v any) (int, bool) {
	switch d := v.(type) {
	case nil:
		return DefaultDurationMin, true
	case int32:
		return int(d), d > 0 && d <= 720
	case int64:
		return int(d), d > 0 && d <= 720
	case float64:
		if d != math.Trunc(d) {
			return 0, false
		}
		return int(d), d > 0 && d <= 720
	}
	return 0, false
}

// Map converts one appointment. ok = false gives the skip reason.
// The start is the CRM's calendar day (taken in UTC, as the CRM stores it at
// noon UTC) at the CRM's time, in the clinic's zone. The CRM's own
// startDateTime is not used: it is computed with the server's local zone.
func (m *Mapper) Map(d Doc) (scheduling.MirrorItem, string, bool) {
	if d.Doctor == nil || d.Doctor.IsZero() {
		return scheduling.MirrorItem{}, SkipNoProfessional, false
	}
	if d.Patient == nil || d.Patient.IsZero() {
		return scheduling.MirrorItem{}, SkipNoClient, false
	}
	parts := clock.FindStringSubmatch(d.Time)
	if parts == nil || d.Date.IsZero() {
		return scheduling.MirrorItem{}, SkipNoTime, false
	}
	minutes, ok := durationMin(d.Duration)
	if !ok {
		return scheduling.MirrorItem{}, SkipBadDuration, false
	}
	var h, mi int
	fmt.Sscanf(parts[1]+" "+parts[2], "%d %d", &h, &mi)
	day := d.Date.UTC()
	start := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, m.loc)
	updated := d.UpdatedAt
	if updated.IsZero() {
		updated = d.ID.Timestamp()
	}
	status := d.Status
	if status == "" || !regexp.MustCompile(`^[a-z_]{1,40}$`).MatchString(status) {
		status = "unknown"
	}
	return scheduling.MirrorItem{
		BookingID:       m.BookingRef(d.ID),
		ResourceID:      m.Ref("pr", d.Doctor.Hex()),
		ClientRef:       m.Ref("cl", d.Patient.Hex()),
		Start:           start.UTC(),
		End:             start.Add(time.Duration(minutes) * time.Minute).UTC(),
		Status:          KASStatus(d.Status, d.IsDeleted),
		SourceStatus:    status,
		SourceUpdatedAt: updated.UTC(),
	}, "", true
}

// Reader reads appointments, read-only.
type Reader struct {
	coll *mongo.Collection
}

// NewReader uses db.appointments with a read-only intent (no write is ever issued).
func NewReader(db *mongo.Database) *Reader { return &Reader{coll: db.Collection(Collection)} }

func (r *Reader) find(ctx context.Context, filter bson.D, sortBy bson.D, limit int64) ([]Doc, error) {
	cur, err := r.coll.Find(ctx, filter, options.Find().SetProjection(Projection).SetSort(sortBy).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var out []Doc
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PageByID reads the next page of a full load (ordered by _id).
func (r *Reader) PageByID(ctx context.Context, after bson.ObjectID, limit int64) ([]Doc, error) {
	filter := bson.D{}
	if !after.IsZero() {
		filter = bson.D{{Key: "_id", Value: bson.D{{Key: "$gt", Value: after}}}}
	}
	return r.find(ctx, filter, bson.D{{Key: "_id", Value: 1}}, limit)
}

// PageByUpdate reads rows changed after the cursor (updatedAt, then _id).
func (r *Reader) PageByUpdate(ctx context.Context, after time.Time, afterID string, limit int64) ([]Doc, error) {
	var filter bson.D
	if oid, err := bson.ObjectIDFromHex(afterID); err == nil {
		filter = bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "updatedAt", Value: bson.D{{Key: "$gt", Value: after}}}},
			bson.D{{Key: "updatedAt", Value: after}, {Key: "_id", Value: bson.D{{Key: "$gt", Value: oid}}}},
		}}}
	} else {
		filter = bson.D{{Key: "updatedAt", Value: bson.D{{Key: "$gte", Value: after}}}}
	}
	return r.find(ctx, filter, bson.D{{Key: "updatedAt", Value: 1}, {Key: "_id", Value: 1}}, limit)
}

// InWindow reads every appointment whose CRM day falls in [from, to), widened
// by a day on each side (the CRM day is a UTC noon timestamp).
func (r *Reader) InWindow(ctx context.Context, from, to time.Time) ([]Doc, error) {
	filter := bson.D{{Key: "date", Value: bson.D{
		{Key: "$gte", Value: from.Add(-24 * time.Hour)}, {Key: "$lt", Value: to.Add(24 * time.Hour)}}}}
	return r.find(ctx, filter, bson.D{{Key: "_id", Value: 1}}, 0)
}

// SourceDayLoads computes, from the source rows, the same summary the mirror
// gives (scheduling.Store.MirrorDayLoads), to compare the two.
func SourceDayLoads(items []scheduling.MirrorItem, zone string, from, to time.Time) ([]scheduling.DayLoad, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	type key struct{ resource, day string }
	groups := map[key][]scheduling.MirrorItem{}
	for _, it := range items {
		if !scheduling.ActiveStatus(it.Status) || it.Start.Before(from) || !it.Start.Before(to) {
			continue
		}
		k := key{it.ResourceID, it.Start.In(loc).Format("2006-01-02")}
		groups[k] = append(groups[k], it)
	}
	out := make([]scheduling.DayLoad, 0, len(groups))
	for k, list := range groups {
		d := scheduling.DayLoad{ResourceID: k.resource, Day: k.day, Active: len(list)}
		for i, a := range list {
			d.BusyMin += int(a.End.Sub(a.Start) / time.Minute)
			for _, b := range list[i+1:] {
				if a.Start.Before(b.End) && b.Start.Before(a.End) {
					d.Overlaps++
				}
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ResourceID != out[j].ResourceID {
			return out[i].ResourceID < out[j].ResourceID
		}
		return out[i].Day < out[j].Day
	})
	return out, nil
}
