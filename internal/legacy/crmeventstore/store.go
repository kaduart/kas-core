// Package crmeventstore adapts the CRM's legacy MongoDB collection
// "eventstore" (fono-inova-crm-back models/EventStore.js) to the
// eventreaper.Store interface.
//
// This is the only place that knows legacy field names. It reads and writes
// exactly these fields: eventId, eventType, status, createdAt, attempts,
// error (replaced as the legacy reaper does) and the optional future field
// processingToken. It never reads payload, metadata or aggregate fields and
// has no knowledge of clinical models.
package crmeventstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/eventreaper"
)

// Field names of the legacy schema.
const (
	fEventID         = "eventId"
	fEventType       = "eventType"
	fStatus          = "status"
	fCreatedAt       = "createdAt"
	fAttempts        = "attempts"
	fError           = "error"
	fProcessingToken = "processingToken"
)

// DefaultCollection is the legacy collection name (`collection: 'eventstore'`).
const DefaultCollection = "eventstore"

// candidateProjection is the complete list of fields the KAS ever reads.
var candidateProjection = bson.D{
	{Key: "_id", Value: 0},
	{Key: fEventID, Value: 1},
	{Key: fEventType, Value: 1},
	{Key: fStatus, Value: 1},
	{Key: fCreatedAt, Value: 1},
	{Key: fAttempts, Value: 1},
	{Key: fProcessingToken, Value: 1},
}

// Store implements eventreaper.Store on the legacy collection.
type Store struct {
	client *mongo.Client
	coll   *mongo.Collection
	lease  *Lease // when set, every write is fenced by this lease
}

// New returns a store over db.collection. A nil lease makes Apply unfenced,
// which only tests use; the executor always passes its lease.
func New(client *mongo.Client, db, collection string, lease *Lease) *Store {
	if collection == "" {
		collection = DefaultCollection
	}
	return &Store{client: client, coll: client.Database(db).Collection(collection), lease: lease}
}

// CandidateFilter is the legacy selection: status processing, createdAt
// strictly before the cutoff.
func CandidateFilter(cutoff time.Time) bson.D {
	return bson.D{
		{Key: fStatus, Value: eventreaper.StatusProcessing},
		{Key: fCreatedAt, Value: bson.D{{Key: "$lt", Value: cutoff}}},
	}
}

// Candidates implements eventreaper.Store with the legacy query
// find(filter).sort({createdAt: 1}).limit(limit).
func (s *Store) Candidates(ctx context.Context, cutoff time.Time, limit int) ([]eventreaper.Candidate, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: fCreatedAt, Value: 1}}).
		SetLimit(int64(limit)).
		SetProjection(candidateProjection)
	cur, err := s.coll.Find(ctx, CandidateFilter(cutoff), opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []eventreaper.Candidate
	for cur.Next(ctx) {
		c, err := decodeCandidate(cur.Current)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, cur.Err()
}

func decodeCandidate(raw bson.Raw) (eventreaper.Candidate, error) {
	var c eventreaper.Candidate
	id, ok := raw.Lookup(fEventID).StringValueOK()
	if !ok {
		return c, fmt.Errorf("document without string eventId")
	}
	c.EventID = id
	c.EventType, _ = raw.Lookup(fEventType).StringValueOK()
	c.Status, _ = raw.Lookup(fStatus).StringValueOK()
	if dt, ok := raw.Lookup(fCreatedAt).DateTimeOK(); ok {
		c.CreatedAt = time.UnixMilli(dt).UTC()
	}
	c.ProcessingToken, _ = raw.Lookup(fProcessingToken).StringValueOK()
	c.Attempts = decodeAttempts(raw)
	return c, nil
}

func decodeAttempts(raw bson.Raw) eventreaper.Attempts {
	v, err := raw.LookupErr(fAttempts)
	if err != nil {
		return eventreaper.Attempts{Kind: eventreaper.AttemptsMissing}
	}
	switch v.Type {
	case bson.TypeNull:
		return eventreaper.Attempts{Kind: eventreaper.AttemptsNull}
	case bson.TypeInt32:
		return eventreaper.NumberAttempts(float64(v.Int32()))
	case bson.TypeInt64:
		return eventreaper.NumberAttempts(float64(v.Int64()))
	case bson.TypeDouble:
		return eventreaper.NumberAttempts(v.Double())
	case bson.TypeDecimal128:
		// The legacy reader (lean) yields a Decimal128 object, which JS
		// compares through valueOf/toString; treat it as its numeric value.
		f, err := decimalToFloat(v.Decimal128())
		if err != nil {
			return eventreaper.Attempts{Kind: eventreaper.AttemptsOther, Number: math.NaN()}
		}
		return eventreaper.NumberAttempts(f)
	case bson.TypeString:
		return eventreaper.Attempts{Kind: eventreaper.AttemptsString, Text: v.StringValue()}
	case bson.TypeBoolean:
		return eventreaper.Attempts{Kind: eventreaper.AttemptsBool, Bool: v.Boolean()}
	case bson.TypeDateTime:
		return eventreaper.Attempts{Kind: eventreaper.AttemptsOther, IsDate: true, Number: float64(v.DateTime())}
	}
	return eventreaper.Attempts{Kind: eventreaper.AttemptsOther}
}

func decimalToFloat(d bson.Decimal128) (float64, error) {
	var f float64
	_, err := fmt.Sscan(d.String(), &f)
	return f, err
}

// attemptsGuard matches the attempts value exactly as it was observed, so a
// concurrent change to attempts makes the conditional update miss.
func attemptsGuard(raw bson.RawValue, observed eventreaper.Attempts) bson.E {
	switch observed.Kind {
	case eventreaper.AttemptsMissing:
		return bson.E{Key: fAttempts, Value: bson.D{{Key: "$exists", Value: false}}}
	case eventreaper.AttemptsNull:
		// {attempts: null} would also match a missing field.
		return bson.E{Key: fAttempts, Value: bson.D{{Key: "$type", Value: "null"}}}
	}
	return bson.E{Key: fAttempts, Value: bson.D{{Key: "$eq", Value: raw}}}
}

// Apply implements eventreaper.Store. Inside one transaction it checks the
// fencing lease (when configured) and moves the event with a conditional
// update that re-checks status, age, attempts and processingToken.
func (s *Store) Apply(ctx context.Context, t eventreaper.Transition) (bool, error) {
	sess, err := s.client.StartSession()
	if err != nil {
		return false, err
	}
	defer sess.EndSession(ctx)
	res, err := sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		if s.lease != nil {
			if err := s.lease.fence(sc); err != nil {
				return false, err
			}
		}
		// Re-read the raw attempts value inside the transaction so that the
		// guard compares like for like (int32 vs double, string, ...).
		var doc bson.Raw
		err := s.coll.FindOne(sc, bson.D{{Key: fEventID, Value: t.Candidate.EventID}},
			options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 0}, {Key: fAttempts, Value: 1}})).Decode(&doc)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		rawAttempts, _ := doc.LookupErr(fAttempts)
		if !sameAttempts(decodeAttempts(doc), t.Candidate.Attempts) {
			return false, nil
		}
		filter := bson.D{
			{Key: fEventID, Value: t.Candidate.EventID},
			{Key: fStatus, Value: eventreaper.StatusProcessing},
			{Key: fCreatedAt, Value: bson.D{{Key: "$lt", Value: t.Cutoff}}},
			attemptsGuard(rawAttempts, t.Candidate.Attempts),
		}
		update := bson.D{{Key: "$set", Value: bson.D{
			{Key: fStatus, Value: t.To},
			// The legacy code also sends recoveredAt/recoveredBy, but Mongoose
			// strict mode drops them; the stored result is {message} only.
			{Key: fError, Value: bson.D{{Key: "message", Value: t.Message}}},
		}}}
		if t.Candidate.ProcessingToken != "" {
			filter = append(filter, bson.E{Key: fProcessingToken, Value: t.Candidate.ProcessingToken})
			update = append(update, bson.E{Key: "$unset", Value: bson.D{{Key: fProcessingToken, Value: ""}}})
		} else {
			filter = append(filter, bson.E{Key: fProcessingToken, Value: bson.D{{Key: "$exists", Value: false}}})
		}
		r, err := s.coll.UpdateOne(sc, filter, update)
		if err != nil {
			return false, err
		}
		return r.ModifiedCount == 1, nil
	})
	if err != nil {
		return false, err
	}
	return res.(bool), nil
}

func sameAttempts(a, b eventreaper.Attempts) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case eventreaper.AttemptsNumber:
		return a.Number == b.Number || (math.IsNaN(a.Number) && math.IsNaN(b.Number))
	case eventreaper.AttemptsString:
		return a.Text == b.Text
	case eventreaper.AttemptsBool:
		return a.Bool == b.Bool
	case eventreaper.AttemptsOther:
		return a.IsDate == b.IsDate && (a.Number == b.Number || (math.IsNaN(a.Number) && math.IsNaN(b.Number)))
	}
	return true
}

// Observe implements eventreaper.Store.
func (s *Store) Observe(ctx context.Context, ids []string) ([]eventreaper.Observation, error) {
	cur, err := s.coll.Find(ctx, bson.D{{Key: fEventID, Value: bson.D{{Key: "$in", Value: ids}}}},
		options.Find().SetProjection(bson.D{{Key: "_id", Value: 0}, {Key: fEventID, Value: 1}, {Key: fStatus, Value: 1}, {Key: fError + ".message", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []eventreaper.Observation
	for cur.Next(ctx) {
		raw := cur.Current
		o := eventreaper.Observation{Found: true}
		o.EventID, _ = raw.Lookup(fEventID).StringValueOK()
		o.Status, _ = raw.Lookup(fStatus).StringValueOK()
		o.ErrorMessage, _ = raw.Lookup(fError, "message").StringValueOK()
		out = append(out, o)
	}
	return out, cur.Err()
}
