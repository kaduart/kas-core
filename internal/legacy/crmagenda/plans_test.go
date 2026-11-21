package crmagenda

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Phase 3B: the plan check finds the missing updatedAt index and sees it fixed.
func TestPlansFlagMissingIndexes(t *testing.T) {
	e := newEnv(t)
	e.doctors()
	e.insert(appt{Doctor: 0, Patient: 0, Day: "2027-03-01", Time: "08:00", Duration: int32(40)})
	ctx := context.Background()
	r := NewReader(e.db)
	plans, err := r.Plans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]QueryPlan{}
	for _, p := range plans {
		by[p.Name] = p
	}
	if by["full_by_id"].CollScan || by["full_by_id"].Index == "" {
		t.Fatalf("full load must use _id: %+v", by["full_by_id"])
	}
	if !by["incremental_by_updated_at"].CollScan || by["incremental_by_updated_at"].Advice == "" {
		t.Fatalf("without the index the incremental must be flagged: %+v", by["incremental_by_updated_at"])
	}
	if !by["window_by_date"].CollScan {
		t.Fatalf("without a date index the window must be flagged: %+v", by["window_by_date"])
	}
	if _, err := e.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "updatedAt", Value: 1}, {Key: "_id", Value: 1}}, Options: options.Index().SetName("kas_incremental")},
		{Keys: bson.D{{Key: "date", Value: -1}}},
	}); err != nil {
		t.Fatal(err)
	}
	plans, err = r.Plans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.Collection == Collection && p.CollScan {
			t.Fatalf("still a collection scan after the indexes: %+v", p)
		}
	}
}
