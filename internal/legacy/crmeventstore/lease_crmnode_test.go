package crmeventstore

// Shared reaper lease between the CRM Node reaper and the KAS executor, with the
// activation guard -require-crm-node-lease. The Node side writes
// participants.crmNode.at ($$NOW) into the same lease document on every cycle
// when EVENT_REAPER_SHARED_LEASE=on (fono-inova-crm-back:
// infrastructure/events/reaperLease.js markCrmNodeParticipant). These tests
// write that marker the same way.

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/eventreaper"
)

// markNode writes the Node marker ageMs in the past (server clock), creating the
// lease document born expired when absent, exactly like the Node does.
func markNode(t *testing.T, e env, collection string, ageMs int64) {
	t.Helper()
	ctx := context.Background()
	if collection == "" {
		collection = DefaultLeaseCollection
	}
	c := e.client.Database(e.db).Collection(collection)
	if _, err := c.InsertOne(ctx, bson.D{{Key: "_id", Value: leaseID}, {Key: "expiresAt", Value: time.Unix(0, 0)}}); err != nil && !mongo.IsDuplicateKeyError(err) {
		t.Fatal(err)
	}
	if _, err := c.UpdateOne(ctx, bson.D{{Key: "_id", Value: leaseID}}, mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "participants.crmNode", Value: bson.D{
			{Key: "at", Value: bson.D{{Key: "$subtract", Value: bson.A{"$$NOW", ageMs}}}},
			{Key: "holder", Value: "crm-node:teste"},
			{Key: "mode", Value: "on"},
		}},
	}}}}); err != nil {
		t.Fatal(err)
	}
}

func leaseLive(t *testing.T, e env) bool {
	t.Helper()
	n, err := e.client.Database(e.db).Collection(DefaultLeaseCollection).CountDocuments(context.Background(),
		append(bson.D{{Key: "_id", Value: leaseID}}, live()...))
	if err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// Node with EVENT_REAPER_SHARED_LEASE absent or off never writes the marker:
// the executor refuses and does not take the lease (no second owner).
func TestRequireCRMNodeRefusesWhenNodeIsNotLeaseAware(t *testing.T) {
	e := setup(t)
	l := NewLease(e.client, e.db, "", "kas-A", time.Minute)
	l.RequireCRMNode(15 * time.Minute)
	ok, err := l.Acquire(context.Background())
	if ok || !errors.Is(err, eventreaper.ErrIncompatibleConfig) {
		t.Fatalf("want refusal with ErrIncompatibleConfig, got ok=%v err=%v", ok, err)
	}
	if leaseLive(t, e) {
		t.Fatal("executor must not hold the lease while the Node is not lease-aware")
	}
}

// Node with EVENT_REAPER_SHARED_LEASE=on refreshes the marker: the executor competes normally.
func TestRequireCRMNodeActsWithFreshMarker(t *testing.T) {
	e := setup(t)
	markNode(t, e, "", 1000)
	l := NewLease(e.client, e.db, "", "kas-A", time.Minute)
	l.RequireCRMNode(15 * time.Minute)
	if ok, err := l.Acquire(context.Background()); err != nil || !ok {
		t.Fatalf("want lease with fresh Node marker, got ok=%v err=%v", ok, err)
	}
}

// Node rolled back to off (or down) stops refreshing: the executor stops acting
// and gives the lease back at once, so the Node (off) is not blocked.
func TestRequireCRMNodeStaleMarkerStopsAndReleases(t *testing.T) {
	e := setup(t)
	markNode(t, e, "", 0)
	l := NewLease(e.client, e.db, "", "kas-A", time.Minute)
	l.RequireCRMNode(5 * time.Second)
	ctx := context.Background()
	if ok, err := l.Acquire(ctx); err != nil || !ok {
		t.Fatalf("first acquire: %v %v", ok, err)
	}
	markNode(t, e, "", 60_000) // last refresh one minute ago
	ok, err := l.Acquire(ctx)
	if ok || !errors.Is(err, eventreaper.ErrIncompatibleConfig) {
		t.Fatalf("want refusal on stale marker, got ok=%v err=%v", ok, err)
	}
	if leaseLive(t, e) {
		t.Fatal("stale marker: executor must release the lease")
	}
}

// Divergent configuration: the executor points at another lease collection
// (or database) than the Node. Without the guard both would own a lease (two
// owners); with it the executor finds no marker and refuses.
func TestRequireCRMNodeDivergentLeaseCollectionRefuses(t *testing.T) {
	e := setup(t)
	markNode(t, e, "", 0) // Node marks the default collection
	ctx := context.Background()

	unguarded := NewLease(e.client, e.db, "outra_colecao_de_posse", "kas-sem-guarda", time.Minute)
	if ok, err := unguarded.Acquire(ctx); err != nil || !ok {
		t.Fatalf("without the guard the divergent executor takes its own lease: %v %v", ok, err)
	}
	node := NewLease(e.client, e.db, "", "crm-node:teste", time.Minute) // same semantics as the Node lease
	if ok, err := node.Acquire(ctx); err != nil || !ok {
		t.Fatalf("Node lease on the default collection: %v %v", ok, err)
	}
	// Two live leases on two documents: two owners at the same time.

	guarded := NewLease(e.client, e.db, "outra_colecao_de_posse", "kas-com-guarda", time.Minute)
	guarded.RequireCRMNode(15 * time.Minute)
	_ = unguarded.Release(ctx)
	if ok, err := guarded.Acquire(ctx); ok || !errors.Is(err, eventreaper.ErrIncompatibleConfig) {
		t.Fatalf("guarded divergent executor must refuse, got ok=%v err=%v", ok, err)
	}
}

// The runner reports the refusal as its own skip reason and writes nothing.
func TestRunnerReportsIncompatibleConfig(t *testing.T) {
	e := setup(t, options.Client())
	insert(t, e, legacyDoc("evt-incompat", "processing", now.Add(-time.Hour), 0))
	l := NewLease(e.client, e.db, "", "kas-A", time.Minute)
	l.RequireCRMNode(15 * time.Minute)
	r := &eventreaper.Runner{
		Config: eventreaper.Config{StuckThreshold: 15 * time.Minute, MaxAttemptsForRetry: 3, BatchSize: 10, Protocol: eventreaper.ProtocolV1, MaxRecoveries: 3},
		Mode:   eventreaper.ModeExecutor, Store: New(e.client, e.db, "", l), Lease: l, Now: fixedNow,
	}
	rep := r.RunCycle(context.Background())
	if rep.Skipped != "incompatible_config" {
		t.Fatalf("want skipped=incompatible_config, got %+v", rep)
	}
	if got := getDoc(t, e, "evt-incompat")["status"]; got != "processing" {
		t.Fatalf("nothing may be written, status=%v", got)
	}
}
