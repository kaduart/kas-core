// Package crmoutbox adapts the CRM's MongoDB transactional outbox
// (fono-inova-crm-back infrastructure/outbox/OutboxModel.js, model "Outbox",
// collection "outboxes") to the outboxrecovery.Store interface.
//
// This is the only place that knows the legacy field names. It reads exactly
// _id, status, attempts, scheduledAt, the first characters of lastError (to
// classify it, never stored or reported) and its own sub-document kasRecovery.
// It never reads payload, correlationId or aggregate fields.
//
// Writes:
//   - recover: status failed -> pending, scheduledAt = now, and kasRecovery
//     bookkeeping. The CRM dispatcher (publishPendingEvents) then publishes
//     the entry exactly as it does any pending entry.
//   - exhaust: status stays failed; only kasRecovery.exhaustedAt/reason are
//     set. Nothing in the CRM reads kasRecovery.
//   - unmark (reversal): removes the exhaust annotation.
//
// Every recover/exhaust is a single conditional updateOne on the values
// observed by Scan, so two recoverers, a recoverer and a dispatcher, or a
// restarted recoverer can never apply the same failure occurrence twice.
package crmoutbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/outboxrecovery"
)

// DefaultCollection is the collection Mongoose derives from model "Outbox".
const DefaultCollection = "outboxes"

// Legacy field names.
const (
	fID          = "_id"
	fStatus      = "status"
	fAttempts    = "attempts"
	fScheduledAt = "scheduledAt"
	fLastError   = "lastError"
	fKas         = "kasRecovery"
	fKasCount    = "kasRecovery.count"
	fKasExh      = "kasRecovery.exhaustedAt"
	fKasReason   = "kasRecovery.reason"
	fKasExhAtt   = "kasRecovery.exhaustedAtAttempts"
	fKasLastAt   = "kasRecovery.lastRecoveredAt"
	fKasLastFrom = "kasRecovery.lastFromAttempts"
	errHeadField = "lastErrorHead"
	errHeadChars = 64
)

// ScanProjection is the complete list of what the KAS ever reads from an
// outbox entry. lastError is cut to its first characters on the server.
func ScanProjection() bson.D {
	return bson.D{
		{Key: fID, Value: 1},
		{Key: fStatus, Value: 1},
		{Key: fAttempts, Value: 1},
		{Key: fScheduledAt, Value: 1},
		{Key: fKasCount, Value: 1},
		{Key: fKasExh, Value: 1},
		{Key: errHeadField, Value: bson.D{{Key: "$cond", Value: bson.A{
			bson.D{{Key: "$eq", Value: bson.A{bson.D{{Key: "$type", Value: "$" + fLastError}}, "string"}}},
			bson.D{{Key: "$substrCP", Value: bson.A{"$" + fLastError, 0, errHeadChars}}},
			"",
		}}}},
	}
}

// Store implements outboxrecovery.Store.
type Store struct {
	coll *mongo.Collection
	cfg  outboxrecovery.Config
}

// New returns a store over db.collection; cfg supplies the permanent-error
// classification.
func New(client *mongo.Client, db, collection string, cfg outboxrecovery.Config) *Store {
	if collection == "" {
		collection = DefaultCollection
	}
	return &Store{coll: client.Database(db).Collection(collection), cfg: cfg}
}

// observed holds the raw values the conditional writes must match.
type observed struct {
	id          bson.ObjectID
	attempts    bson.RawValue // zero Type: field absent
	scheduledAt bson.RawValue
	kasCount    bson.RawValue
}

// Scan implements outboxrecovery.Store.
func (s *Store) Scan(ctx context.Context, after string, limit int) ([]outboxrecovery.Entry, error) {
	filter := bson.D{
		{Key: fStatus, Value: outboxrecovery.StatusFailed},
		{Key: fKasExh, Value: bson.D{{Key: "$exists", Value: false}}},
	}
	if after != "" {
		oid, err := bson.ObjectIDFromHex(after)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor %q", after)
		}
		filter = append(filter, bson.E{Key: fID, Value: bson.D{{Key: "$gt", Value: oid}}})
	}
	opts := options.Find().SetProjection(ScanProjection()).SetSort(bson.D{{Key: fID, Value: 1}}).SetLimit(int64(limit))
	cur, err := s.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []outboxrecovery.Entry
	for cur.Next(ctx) {
		// cur.Current is reused by Next; the observed raw values must survive it.
		e, err := s.decode(bson.Raw(append([]byte(nil), cur.Current...)))
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, cur.Err()
}

func (s *Store) decode(raw bson.Raw) (outboxrecovery.Entry, error) {
	var e outboxrecovery.Entry
	idv := raw.Lookup(fID)
	oid, ok := idv.ObjectIDOK()
	if !ok {
		return e, errors.New("outbox entry with a non-ObjectId _id; refusing to continue")
	}
	ob := observed{id: oid}
	e.ID = oid.Hex()
	e.Status, _ = raw.Lookup(fStatus).StringValueOK()

	if v, err := raw.LookupErr(fAttempts); err == nil {
		ob.attempts = v
		e.Attempts, e.AttemptsValid = wholeNumber(v)
	} else {
		// Absent: the dispatcher reads it as `attempts || 0` and $inc creates it.
		e.Attempts, e.AttemptsValid = 0, true
	}
	if v, err := raw.LookupErr(fScheduledAt); err == nil {
		ob.scheduledAt = v
		if dt, ok := v.DateTimeOK(); ok {
			e.RetryAt, e.RetryAtValid = time.UnixMilli(dt).UTC(), true
		}
	}
	if v, err := raw.LookupErr(fKas, "count"); err == nil {
		ob.kasCount = v
		n, ok := wholeNumber(v)
		if !ok {
			e.AttemptsValid = false // bookkeeping we cannot read: report, do not act
		}
		e.Recoveries = n
	}
	if _, err := raw.LookupErr(fKas, "exhaustedAt"); err == nil {
		e.Exhausted = true
	}
	head, _ := raw.Lookup(errHeadField).StringValueOK()
	e.PermanentError = s.cfg.IsPermanent(head)
	e.Observed = ob
	return e, nil
}

// wholeNumber accepts int32, int64 and integral doubles.
func wholeNumber(v bson.RawValue) (int64, bool) {
	switch v.Type {
	case bson.TypeInt32:
		return int64(v.Int32()), true
	case bson.TypeInt64:
		return v.Int64(), true
	case bson.TypeDouble:
		f := v.Double()
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
			return 0, false
		}
		return int64(f), true
	}
	return 0, false
}

// matchRaw is an equality condition on the observed raw value, or
// {$exists:false} when the field was absent.
func matchRaw(field string, v bson.RawValue) bson.E {
	if v.Type == 0 {
		return bson.E{Key: field, Value: bson.D{{Key: "$exists", Value: false}}}
	}
	return bson.E{Key: field, Value: v}
}

func (s *Store) casFilter(e outboxrecovery.Entry) (bson.D, observed, error) {
	ob, ok := e.Observed.(observed)
	if !ok {
		return nil, ob, errors.New("entry was not produced by this store")
	}
	return bson.D{
		{Key: fID, Value: ob.id},
		{Key: fStatus, Value: outboxrecovery.StatusFailed},
		matchRaw(fAttempts, ob.attempts),
		matchRaw(fScheduledAt, ob.scheduledAt),
		matchRaw(fKasCount, ob.kasCount),
		{Key: fKasExh, Value: bson.D{{Key: "$exists", Value: false}}},
	}, ob, nil
}

// Recover implements outboxrecovery.Store.
func (s *Store) Recover(ctx context.Context, e outboxrecovery.Entry, now time.Time) (bool, error) {
	filter, _, err := s.casFilter(e)
	if err != nil {
		return false, err
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: fStatus, Value: outboxrecovery.StatusPending},
			// The dispatcher selects scheduledAt <= now; an absent or
			// non-date value would otherwise strand the entry in pending.
			{Key: fScheduledAt, Value: now},
			{Key: fKasLastAt, Value: now},
			{Key: fKasLastFrom, Value: e.Attempts},
		}},
		{Key: "$inc", Value: bson.D{{Key: fKasCount, Value: int64(1)}}},
	}
	res, err := s.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// Exhaust implements outboxrecovery.Store.
func (s *Store) Exhaust(ctx context.Context, e outboxrecovery.Entry, reason string, now time.Time) (bool, error) {
	filter, _, err := s.casFilter(e)
	if err != nil {
		return false, err
	}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: fKasExh, Value: now},
		{Key: fKasReason, Value: reason},
		{Key: fKasExhAtt, Value: e.Attempts},
	}}}
	res, err := s.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// Unmark removes the exhaust annotation (reversal). An empty reason removes
// every annotation; otherwise only those with that reason. Recovered entries
// are not touched: once pending, they belong to the dispatcher.
func (s *Store) Unmark(ctx context.Context, reason string) (int64, error) {
	filter := bson.D{{Key: fKasExh, Value: bson.D{{Key: "$exists", Value: true}}}}
	if reason != "" {
		filter = append(filter, bson.E{Key: fKasReason, Value: reason})
	}
	res, err := s.coll.UpdateMany(ctx, filter, bson.D{{Key: "$unset", Value: bson.D{
		{Key: fKasExh, Value: ""}, {Key: fKasReason, Value: ""}, {Key: fKasExhAtt, Value: ""},
	}}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// Census implements outboxrecovery.Store.
func (s *Store) Census(ctx context.Context, now time.Time, stalePendingAfter time.Duration) (outboxrecovery.Census, error) {
	c := outboxrecovery.Census{ByStatus: map[string]int64{}}
	cur, err := s.coll.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$" + fStatus}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}}}}},
	})
	if err != nil {
		return c, err
	}
	var rows []struct {
		ID any   `bson:"_id"`
		N  int64 `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return c, err
	}
	for _, r := range rows {
		k, ok := r.ID.(string)
		if !ok {
			k = "(other)"
		}
		c.ByStatus[k] += r.N
	}
	if c.FailedExhausted, err = s.coll.CountDocuments(ctx, bson.D{
		{Key: fStatus, Value: outboxrecovery.StatusFailed},
		{Key: fKasExh, Value: bson.D{{Key: "$exists", Value: true}}},
	}); err != nil {
		return c, err
	}
	if stalePendingAfter > 0 {
		if c.PendingStale, err = s.coll.CountDocuments(ctx, bson.D{
			{Key: fStatus, Value: outboxrecovery.StatusPending},
			{Key: fScheduledAt, Value: bson.D{{Key: "$lt", Value: now.Add(-stalePendingAfter)}}},
		}); err != nil {
			return c, err
		}
	}
	return c, nil
}

// EnsureIndexes creates the one index the scan needs. The CRM schema already
// declares {status:1, scheduledAt:1}; this adds nothing the CRM relies on.
// Production has autoIndex off, so this runs only when explicitly asked for.
func (s *Store) EnsureIndexes(ctx context.Context) error {
	_, err := s.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: fStatus, Value: 1}, {Key: fID, Value: 1}},
		Options: options.Index().SetName("kas_outbox_recovery_scan"),
	})
	return err
}
