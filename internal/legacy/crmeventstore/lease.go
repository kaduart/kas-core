package crmeventstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"kas-core-pilot/internal/eventreaper"
)

// DefaultLeaseCollection holds the single executor lease document. It is a
// KAS-owned collection; it is created only where the executor is allowed to
// write (in this round, only on a disposable database).
const DefaultLeaseCollection = "kas_event_reaper_lease"

const leaseID = "crm-eventstore-reaper"

// Lease is a fencing lease: at most one live holder, identified by a random
// token that changes on every takeover. All times come from the database
// server ($$NOW), so executors on machines with different clocks agree.
type Lease struct {
	coll   *mongo.Collection
	holder string
	ttl    time.Duration

	mu    sync.Mutex
	token string

	// requireNode: act only while the CRM Node reaper proves, through a fresh
	// participants.crmNode marker in THIS lease document, that it runs with
	// EVENT_REAPER_SHARED_LEASE=on (and so competes for and fences this lease).
	requireNode      bool
	nodeMarkerMaxAge time.Duration
}

// RequireCRMNode makes Acquire refuse (ErrIncompatibleConfig) unless the CRM
// Node reaper refreshed participants.crmNode.at within maxAge, by the server
// clock. A Node with the flag absent/off, or pointed at another database or
// lease collection, never writes that marker here, so the executor stays idle
// instead of becoming a second owner. When it refuses, it also gives up a
// lease it holds, so the Node (off) can resume at once.
func (l *Lease) RequireCRMNode(maxAge time.Duration) {
	l.requireNode, l.nodeMarkerMaxAge = true, maxAge
}

// crmNodeMarkerAge returns the marker age by the server clock; ok=false when absent.
func (l *Lease) crmNodeMarkerAge(ctx context.Context) (time.Duration, bool, error) {
	cur, err := l.coll.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "_id", Value: leaseID}}}},
		{{Key: "$project", Value: bson.D{
			{Key: "at", Value: "$participants.crmNode.at"},
			{Key: "ageMs", Value: bson.D{{Key: "$subtract", Value: bson.A{"$$NOW", "$participants.crmNode.at"}}}},
		}}},
	})
	if err != nil {
		return 0, false, err
	}
	defer cur.Close(ctx)
	var doc struct {
		At    any   `bson:"at"`
		AgeMs int64 `bson:"ageMs"`
	}
	if !cur.Next(ctx) {
		return 0, false, cur.Err()
	}
	if err := cur.Decode(&doc); err != nil {
		return 0, false, err
	}
	if doc.At == nil {
		return 0, false, nil
	}
	return time.Duration(doc.AgeMs) * time.Millisecond, true, nil
}

func (l *Lease) checkCRMNode(ctx context.Context) error {
	if !l.requireNode {
		return nil
	}
	age, ok, err := l.crmNodeMarkerAge(ctx)
	if err != nil {
		return err
	}
	if ok && age <= l.nodeMarkerMaxAge {
		return nil
	}
	_ = l.Release(ctx)
	if !ok {
		return fmt.Errorf("%w: no participants.crmNode marker in the lease document (CRM Node reaper not running with EVENT_REAPER_SHARED_LEASE=on, or using another database/lease collection)", eventreaper.ErrIncompatibleConfig)
	}
	return fmt.Errorf("%w: participants.crmNode marker is %s old (max %s); the CRM Node reaper stopped refreshing it (flag turned off, process down or rolled back)", eventreaper.ErrIncompatibleConfig, age.Truncate(time.Second), l.nodeMarkerMaxAge)
}

// NewLease returns a lease for holder (for logs only; the token is what fences).
func NewLease(client *mongo.Client, db, collection, holder string, ttl time.Duration) *Lease {
	if collection == "" {
		collection = DefaultLeaseCollection
	}
	return &Lease{coll: client.Database(db).Collection(collection), holder: holder, ttl: ttl}
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (l *Lease) currentToken() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.token
}

func live() bson.D {
	return bson.D{{Key: "$expr", Value: bson.D{{Key: "$gt", Value: bson.A{"$expiresAt", "$$NOW"}}}}}
}

func expired() bson.D {
	return bson.D{{Key: "$expr", Value: bson.D{{Key: "$lte", Value: bson.A{"$expiresAt", "$$NOW"}}}}}
}

func (l *Lease) setPipeline(token string) mongo.Pipeline {
	return mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "holder", Value: l.holder},
		{Key: "token", Value: token},
		{Key: "expiresAt", Value: bson.D{{Key: "$add", Value: bson.A{"$$NOW", l.ttl.Milliseconds()}}}},
		{Key: "renewedAt", Value: "$$NOW"},
	}}}}
}

// Acquire implements eventreaper.Lease: renew our live lease, take over an
// expired one with a new token, or create it. It returns false if another
// holder has a live lease.
func (l *Lease) Acquire(ctx context.Context) (bool, error) {
	if err := l.checkCRMNode(ctx); err != nil {
		return false, err
	}
	if tok := l.currentToken(); tok != "" {
		r, err := l.coll.UpdateOne(ctx, append(bson.D{{Key: "_id", Value: leaseID}, {Key: "token", Value: tok}}, live()...), l.setPipeline(tok))
		if err != nil {
			return false, err
		}
		if r.MatchedCount == 1 {
			return true, nil
		}
	}
	// Make sure the document exists, born expired (an upsert cannot carry the
	// $expr below, and an insert cannot use the server clock).
	if _, err := l.coll.InsertOne(ctx, bson.D{{Key: "_id", Value: leaseID}, {Key: "expiresAt", Value: time.Unix(0, 0)}}); err != nil && !mongo.IsDuplicateKeyError(err) {
		return false, err
	}
	// Take over only an expired lease, with a new token.
	tok := newToken()
	r, err := l.coll.UpdateOne(ctx, append(bson.D{{Key: "_id", Value: leaseID}}, expired()...), l.setPipeline(tok))
	if err != nil {
		return false, err
	}
	if r.MatchedCount == 0 {
		l.setToken("")
		return false, nil
	}
	l.setToken(tok)
	return true, nil
}

func (l *Lease) setToken(t string) {
	l.mu.Lock()
	l.token = t
	l.mu.Unlock()
}

// Release gives up a lease we hold, so another executor can take it at once.
func (l *Lease) Release(ctx context.Context) error {
	tok := l.currentToken()
	if tok == "" {
		return nil
	}
	_, err := l.coll.UpdateOne(ctx, bson.D{{Key: "_id", Value: leaseID}, {Key: "token", Value: tok}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "expiresAt", Value: time.Unix(0, 0)}}}})
	l.setToken("")
	return err
}

// fence runs inside the Apply transaction. Writing to the lease document
// makes a concurrent takeover conflict with this transaction, so a write can
// only commit while our token is still the live one.
func (l *Lease) fence(ctx context.Context) error {
	tok := l.currentToken()
	if tok == "" {
		return eventreaper.ErrLeaseLost
	}
	filter := append(bson.D{{Key: "_id", Value: leaseID}, {Key: "token", Value: tok}}, live()...)
	if l.requireNode {
		// Per write, not only per cycle: a Node that stops refreshing its marker
		// (flag turned off, rolled back, down) stops our writes at once, inside
		// the same transaction, even in the middle of a cycle.
		filter = bson.D{{Key: "_id", Value: leaseID}, {Key: "token", Value: tok}, {Key: "$expr", Value: bson.D{{Key: "$and", Value: bson.A{
			bson.D{{Key: "$gt", Value: bson.A{"$expiresAt", "$$NOW"}}},
			bson.D{{Key: "$lte", Value: bson.A{
				bson.D{{Key: "$subtract", Value: bson.A{"$$NOW", bson.D{{Key: "$ifNull", Value: bson.A{"$participants.crmNode.at", time.Unix(0, 0)}}}}}},
				l.nodeMarkerMaxAge.Milliseconds(),
			}}},
		}}}}}
	}
	r, err := l.coll.UpdateOne(ctx, filter,
		mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "fencedWriteAt", Value: "$$NOW"}}}}})
	if err != nil {
		return err
	}
	if r.MatchedCount == 0 {
		l.setToken("")
		return eventreaper.ErrLeaseLost
	}
	return nil
}

var _ eventreaper.Lease = (*Lease)(nil)
var _ eventreaper.Store = (*Store)(nil)
