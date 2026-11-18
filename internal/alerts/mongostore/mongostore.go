// Package mongostore persists an alerts.Center snapshot in one MongoDB
// document, so a restarted process keeps dedup, cooldowns and alerts whose
// delivery failed. One document per center (Name).
package mongostore

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/alerts"
)

type Store struct {
	Coll *mongo.Collection
	Name string
}

// Keys (tenant, source, kind, subject) may contain "." or "$", so the
// snapshot is stored as arrays, never as field names.
type seenEntry struct {
	Key string    `bson:"k"`
	At  time.Time `bson:"t"`
}

type groupEntry struct {
	Name  string            `bson:"name"`
	State alerts.GroupState `bson:"state"`
}

type doc struct {
	ID        string       `bson:"_id"`
	Seen      []seenEntry  `bson:"seen"`
	Groups    []groupEntry `bson:"groups"`
	UpdatedAt time.Time    `bson:"updatedAt"`
}

func (s Store) Load(ctx context.Context) (*alerts.Snapshot, error) {
	var d doc
	err := s.Coll.FindOne(ctx, bson.D{{Key: "_id", Value: s.Name}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snap := &alerts.Snapshot{Seen: map[string]time.Time{}, Groups: map[string]alerts.GroupState{}}
	for _, e := range d.Seen {
		snap.Seen[e.Key] = e.At
	}
	for _, g := range d.Groups {
		snap.Groups[g.Name] = g.State
	}
	return snap, nil
}

func (s Store) Save(ctx context.Context, snap alerts.Snapshot) error {
	d := doc{ID: s.Name, UpdatedAt: time.Now().UTC(), Seen: []seenEntry{}, Groups: []groupEntry{}}
	for k, t := range snap.Seen {
		d.Seen = append(d.Seen, seenEntry{Key: k, At: t})
	}
	for name, g := range snap.Groups {
		d.Groups = append(d.Groups, groupEntry{Name: name, State: g})
	}
	_, err := s.Coll.ReplaceOne(ctx, bson.D{{Key: "_id", Value: s.Name}}, d, options.Replace().SetUpsert(true))
	return err
}
