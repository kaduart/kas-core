package crmagenda

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Phase 3B: before any real import, check that every query the mirror sends
// is served by an index. This asks MongoDB only for its query PLANS (explain
// with verbosity "queryPlanner"): no document is returned to the caller. When
// several indexes compete, the planner may try them briefly inside the server.

// QueryPlan is the planner's verdict for one mirror query.
type QueryPlan struct {
	Name       string   `json:"name"`
	Collection string   `json:"collection"`
	Purpose    string   `json:"purpose"`
	Stages     []string `json:"stages"`
	Index      string   `json:"index,omitempty"`
	// CollScan: the query would read the whole collection (a COLLSCAN, or an
	// index scan with unbounded keys, which reads every entry of the index).
	CollScan bool `json:"collection_scan"`
	// Advice: the index that would serve it, when it is missing.
	Advice string `json:"advice,omitempty"`
}

type planQuery struct {
	name, coll, purpose, advice string
	filter, sort, projection    bson.D
	limit                       int64
}

func mirrorQueries() []planQuery {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	oid := bson.NewObjectIDFromTimestamp(t)
	return []planQuery{
		{name: "full_by_id", coll: Collection, purpose: "carga completa paginada por _id",
			filter: bson.D{{Key: "_id", Value: bson.D{{Key: "$gt", Value: oid}}}}, sort: bson.D{{Key: "_id", Value: 1}}, projection: Projection, limit: 500,
			advice: "o índice padrão _id já atende"},
		{name: "incremental_by_updated_at", coll: Collection, purpose: "carga incremental por updatedAt",
			filter: bson.D{{Key: "$or", Value: bson.A{
				bson.D{{Key: "updatedAt", Value: bson.D{{Key: "$gt", Value: t}}}},
				bson.D{{Key: "updatedAt", Value: t}, {Key: "_id", Value: bson.D{{Key: "$gt", Value: oid}}}},
			}}},
			sort: bson.D{{Key: "updatedAt", Value: 1}, {Key: "_id", Value: 1}}, projection: Projection, limit: 500,
			advice: "{ updatedAt: 1, _id: 1 } em appointments"},
		{name: "window_by_date", coll: Collection, purpose: "reconciliação e comparação por janela de datas",
			filter:     bson.D{{Key: "date", Value: bson.D{{Key: "$gte", Value: t}, {Key: "$lt", Value: t.AddDate(0, 1, 0)}}}},
			projection: Projection,
			advice:     "{ date: 1 } (ou { date: -1 }) em appointments"},
		{name: "doctors_availability", coll: DoctorCollection, purpose: "horários semanais dos profissionais",
			filter: bson.D{}, projection: DoctorProjection,
			advice: "nenhum: coleção pequena, leitura inteira é esperada"},
	}
}

// Plans explains every mirror query against the source database.
func (r *Reader) Plans(ctx context.Context) ([]QueryPlan, error) {
	db := r.coll.Database()
	var out []QueryPlan
	for _, q := range mirrorQueries() {
		find := bson.D{{Key: "find", Value: q.coll}, {Key: "filter", Value: q.filter}, {Key: "projection", Value: q.projection}}
		if len(q.sort) > 0 {
			find = append(find, bson.E{Key: "sort", Value: q.sort})
		}
		if q.limit > 0 {
			find = append(find, bson.E{Key: "limit", Value: q.limit})
		}
		res, err := db.RunCommand(ctx, bson.D{{Key: "explain", Value: find}, {Key: "verbosity", Value: "queryPlanner"}}).Raw()
		if err != nil {
			return nil, fmt.Errorf("explain %s: %w", q.name, err)
		}
		p := QueryPlan{Name: q.name, Collection: q.coll, Purpose: q.purpose}
		if wp, err := res.LookupErr("queryPlanner", "winningPlan"); err == nil {
			if doc, ok := wp.DocumentOK(); ok {
				walk(doc, &p)
			}
		}
		if p.CollScan && q.coll == Collection {
			p.Advice = q.advice
		}
		out = append(out, p)
	}
	return out, nil
}

// walk collects stage names (outermost first) and the index used.
func walk(doc bson.Raw, p *QueryPlan) {
	// MongoDB 7+ may wrap the classic plan in queryPlan (slot-based engine).
	if qp, err := doc.LookupErr("queryPlan"); err == nil {
		if d, ok := qp.DocumentOK(); ok {
			walk(d, p)
			return
		}
	}
	if st, ok := doc.Lookup("stage").StringValueOK(); ok {
		p.Stages = append(p.Stages, st)
		if st == "COLLSCAN" {
			p.CollScan = true
		}
	}
	if ix, ok := doc.Lookup("indexName").StringValueOK(); ok && p.Index == "" {
		p.Index = ix
		if unbounded(doc) {
			p.CollScan = true
		}
	}
	if in, err := doc.LookupErr("inputStage"); err == nil {
		if d, ok := in.DocumentOK(); ok {
			walk(d, p)
		}
	}
	if arr, err := doc.LookupErr("inputStages"); err == nil {
		if a, ok := arr.ArrayOK(); ok {
			vals, _ := a.Values()
			for _, v := range vals {
				if d, ok := v.DocumentOK(); ok {
					walk(d, p)
				}
			}
		}
	}
}

// unbounded reports an IXSCAN whose every key range is [MinKey, MaxKey].
func unbounded(ixscan bson.Raw) bool {
	b, err := ixscan.LookupErr("indexBounds")
	if err != nil {
		return false
	}
	doc, ok := b.DocumentOK()
	if !ok {
		return false
	}
	elems, err := doc.Elements()
	if err != nil || len(elems) == 0 {
		return false
	}
	for _, e := range elems {
		arr, ok := e.Value().ArrayOK()
		if !ok {
			return false
		}
		vals, _ := arr.Values()
		if len(vals) != 1 {
			return false
		}
		s, ok := vals[0].StringValueOK()
		if !ok || (s != "[MinKey, MaxKey]" && s != "[MaxKey, MinKey]") {
			return false
		}
	}
	return true
}
