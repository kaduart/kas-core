package crmagenda

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/scheduling"
)

// MaxBatch bounds how many rows are held in memory at once.
const MaxBatch = 2000

// MaxWindow bounds a reconciliation or verification window.
const MaxWindow = 400 * 24 * time.Hour

// ErrBusy: another importer holds the lock for this tenant and source.
var ErrBusy = errors.New("another importer is running for this tenant")

// Importer copies the CRM agenda into the KAS mirror of one tenant.
type Importer struct {
	Reader *Reader
	Mapper *Mapper
	Store  *scheduling.Store
	Tenant string
	// Batch is the page size (1..MaxBatch).
	Batch int64
	// SafetyLag: after a full load, the incremental cursor starts this much
	// before the load began, so rows changed during the load are read again.
	SafetyLag time.Duration
	Log       *slog.Logger
	// OnBatch, when set, runs after each stored batch (n counts from 1); an
	// error stops the run there. Tests use it to simulate a crash mid-import.
	OnBatch func(n int) error
}

func (im *Importer) check() error {
	if im.Reader == nil || im.Mapper == nil || im.Store == nil || !scheduling.ValidRef(im.Tenant) || im.Batch < 1 || im.Batch > MaxBatch {
		return scheduling.ErrInvalid
	}
	if im.Log == nil {
		im.Log = slog.Default()
	}
	return nil
}

// locked runs fn holding the importer lock of (tenant, source).
func (im *Importer) locked(ctx context.Context, fn func() error) error {
	if err := im.check(); err != nil {
		return err
	}
	release, ok, err := im.Store.MirrorLock(ctx, im.Tenant, Source)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}
	defer release()
	return fn()
}

// Skips counts skipped rows by reason.
type Skips map[string]int

func (s Skips) total() int {
	n := 0
	for _, v := range s {
		n += v
	}
	return n
}

func (im *Importer) mapAll(docs []Doc, skips Skips) []scheduling.MirrorItem {
	items := make([]scheduling.MirrorItem, 0, len(docs))
	for _, d := range docs {
		it, reason, ok := im.Mapper.Map(d)
		if !ok {
			skips[reason]++
			continue
		}
		items = append(items, it)
	}
	return items
}

// Report is what one run did (counts only, no ids).
type Report struct {
	Mode     string `json:"mode"`
	Tenant   string `json:"tenant_id"`
	Read     int    `json:"read"`
	Changed  int    `json:"changed"`
	Stale    int    `json:"stale_ignored"`
	Skipped  Skips  `json:"skipped"`
	Batches  int    `json:"batches"`
	Deleted  int    `json:"deleted"`
	Pending  int    `json:"delete_candidates"`
	Cleared  int    `json:"candidates_back"`
	Duration string `json:"duration"`
}

func (im *Importer) finish(ctx context.Context, rep *Report, started time.Time) {
	rep.Duration = time.Since(started).Round(time.Millisecond).String()
	err := im.Store.RecordMirrorRun(ctx, im.Tenant, Source, scheduling.MirrorRun{
		Mode: rep.Mode, Started: started, Read: rep.Read, Upserted: rep.Changed, Skipped: rep.Skipped.total(),
		Deleted: rep.Deleted, Candidates: rep.Pending, Cleared: rep.Cleared,
	})
	if err != nil {
		im.Log.Warn("mirror_run_not_recorded", "tenant_id", im.Tenant, "mode", rep.Mode, "error", err.Error())
	}
	im.Log.Info("mirror_run", "tenant_id", im.Tenant, "mode", rep.Mode, "read", rep.Read, "changed", rep.Changed,
		"stale_ignored", rep.Stale, "skipped", rep.Skipped.total(), "batches", rep.Batches, "deleted", rep.Deleted,
		"delete_candidates", rep.Pending, "duration", rep.Duration)
}

// Full reads every appointment by _id, in pages. It can be interrupted and run
// again at any time: every write is an idempotent upsert. At the end the
// incremental cursor is set to the load's start minus SafetyLag.
func (im *Importer) Full(ctx context.Context) (Report, error) {
	rep := Report{Mode: "full", Tenant: im.Tenant, Skipped: Skips{}}
	started := time.Now().UTC()
	err := im.locked(ctx, func() error {
		cur, err := im.Store.GetMirrorCursor(ctx, im.Tenant, Source)
		if err != nil {
			return err
		}
		var after bson.ObjectID
		for {
			docs, err := im.Reader.PageByID(ctx, after, im.Batch)
			if err != nil {
				return fmt.Errorf("read source: %w", err)
			}
			last := int64(len(docs)) < im.Batch
			items := im.mapAll(docs, rep.Skipped)
			next := cur
			if last {
				next = scheduling.MirrorCursor{LastUpdatedAt: started.Add(-im.SafetyLag)}
			}
			res, err := im.Store.MirrorApply(ctx, im.Tenant, Source, items, next, last)
			if err != nil {
				return fmt.Errorf("write mirror: %w", err)
			}
			rep.Read += len(docs)
			rep.Changed += res.Changed
			rep.Stale += res.Stale
			rep.Batches++
			if im.OnBatch != nil {
				if err := im.OnBatch(rep.Batches); err != nil {
					return err
				}
			}
			if last {
				return nil
			}
			after = docs[len(docs)-1].ID
		}
	})
	if err == nil {
		im.finish(ctx, &rep, started)
	}
	return rep, err
}

// Incremental reads the rows changed since the saved cursor. The cursor moves
// in the same transaction as each batch, so an interruption resumes exactly
// after the last stored batch.
func (im *Importer) Incremental(ctx context.Context) (Report, error) {
	rep := Report{Mode: "incremental", Tenant: im.Tenant, Skipped: Skips{}}
	started := time.Now().UTC()
	err := im.locked(ctx, func() error {
		cur, err := im.Store.GetMirrorCursor(ctx, im.Tenant, Source)
		if err != nil {
			return err
		}
		if cur.FullLoadAt.IsZero() {
			return errors.New("no full load yet: run the full mode first")
		}
		for {
			docs, err := im.Reader.PageByUpdate(ctx, cur.LastUpdatedAt, cur.LastSourceID, im.Batch)
			if err != nil {
				return fmt.Errorf("read source: %w", err)
			}
			if len(docs) == 0 {
				return nil
			}
			items := im.mapAll(docs, rep.Skipped)
			lastDoc := docs[len(docs)-1]
			if !lastDoc.UpdatedAt.IsZero() {
				cur.LastUpdatedAt, cur.LastSourceID = lastDoc.UpdatedAt.UTC(), lastDoc.ID.Hex()
			}
			res, err := im.Store.MirrorApply(ctx, im.Tenant, Source, items, cur, false)
			if err != nil {
				return fmt.Errorf("write mirror: %w", err)
			}
			rep.Read += len(docs)
			rep.Changed += res.Changed
			rep.Stale += res.Stale
			rep.Batches++
			if im.OnBatch != nil {
				if err := im.OnBatch(rep.Batches); err != nil {
					return err
				}
			}
			if int64(len(docs)) < im.Batch {
				return nil
			}
		}
	})
	if err == nil {
		im.finish(ctx, &rep, started)
	}
	return rep, err
}

// Window re-reads every source row whose day falls in [from, to) through the
// date index, upserts it and then reconciles deletions in the same window. It
// needs no updatedAt index and no full load: it is the extraction strategy for
// a source without that index (docs/agenda/FASE3B_ESTRATEGIA_SEM_INDICE.md).
// A change to a row outside the window is not seen; the window must cover the
// days that matter for availability. The incremental cursor is not moved.
func (im *Importer) Window(ctx context.Context, from, to time.Time, minGap time.Duration) (Report, error) {
	rep := Report{Mode: "window", Tenant: im.Tenant, Skipped: Skips{}}
	if !to.After(from) || to.Sub(from) > MaxWindow {
		return rep, scheduling.ErrInvalid
	}
	started := time.Now().UTC()
	err := im.locked(ctx, func() error {
		cur, err := im.Store.GetMirrorCursor(ctx, im.Tenant, Source)
		if err != nil {
			return err
		}
		docs, err := im.Reader.InWindow(ctx, from, to)
		if err != nil {
			return fmt.Errorf("read source: %w", err)
		}
		ids := make([]string, 0, len(docs))
		for _, d := range docs {
			ids = append(ids, im.Mapper.BookingRef(d.ID))
		}
		for i := 0; i < len(docs); i += int(im.Batch) {
			end := min(i+int(im.Batch), len(docs))
			items := im.mapAll(docs[i:end], rep.Skipped)
			res, err := im.Store.MirrorApply(ctx, im.Tenant, Source, items, cur, false)
			if err != nil {
				return fmt.Errorf("write mirror: %w", err)
			}
			rep.Changed += res.Changed
			rep.Stale += res.Stale
			rep.Batches++
			if im.OnBatch != nil {
				if err := im.OnBatch(rep.Batches); err != nil {
					return err
				}
			}
		}
		rep.Read = len(docs)
		res, err := im.Store.MirrorReconcile(ctx, im.Tenant, Source, from, to, ids, minGap)
		if err != nil {
			return fmt.Errorf("write mirror: %w", err)
		}
		rep.Deleted, rep.Pending, rep.Cleared = res.Deleted, res.Candidates, res.Cleared
		return nil
	})
	if err == nil {
		im.finish(ctx, &rep, started)
	}
	return rep, err
}

// sourceIDs streams the ids of the source rows in the window (only _id is read).
func (im *Importer) sourceIDs(ctx context.Context, from, to time.Time) ([]string, error) {
	filter := bson.D{{Key: "date", Value: bson.D{
		{Key: "$gte", Value: from.Add(-24 * time.Hour)}, {Key: "$lt", Value: to.Add(24 * time.Hour)}}}}
	cur, err := im.Reader.coll.Find(ctx, filter, options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}).SetBatchSize(int32(im.Batch)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	ids := []string{}
	for cur.Next(ctx) {
		var d struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		ids = append(ids, im.Mapper.BookingRef(d.ID))
	}
	return ids, cur.Err()
}

// Reconcile finds the mirrored bookings deleted from the source in [from, to).
// A booking missing for the first time is only a candidate; it is deleted by a
// later reconciliation, at least minGap after, that still misses it.
func (im *Importer) Reconcile(ctx context.Context, from, to time.Time, minGap time.Duration) (Report, error) {
	rep := Report{Mode: "reconcile", Tenant: im.Tenant, Skipped: Skips{}}
	if !to.After(from) || to.Sub(from) > MaxWindow {
		return rep, scheduling.ErrInvalid
	}
	started := time.Now().UTC()
	err := im.locked(ctx, func() error {
		ids, err := im.sourceIDs(ctx, from, to)
		if err != nil {
			return fmt.Errorf("read source: %w", err)
		}
		rep.Read = len(ids)
		res, err := im.Store.MirrorReconcile(ctx, im.Tenant, Source, from, to, ids, minGap)
		if err != nil {
			return fmt.Errorf("write mirror: %w", err)
		}
		rep.Deleted, rep.Pending, rep.Cleared = res.Deleted, res.Candidates, res.Cleared
		return nil
	})
	if err == nil {
		im.finish(ctx, &rep, started)
	}
	return rep, err
}

// Divergence is one resource-day whose summary differs between source and mirror.
type Divergence struct {
	ResourceID string              `json:"resource_id"`
	Day        string              `json:"day"`
	Source     *scheduling.DayLoad `json:"source,omitempty"`
	Mirror     *scheduling.DayLoad `json:"mirror,omitempty"`
}

// VerifyReport compares the source with the mirror, per resource and local day.
type VerifyReport struct {
	Tenant         string       `json:"tenant_id"`
	From           string       `json:"from"`
	To             string       `json:"to"`
	SourceRows     int          `json:"source_rows"`
	Skipped        Skips        `json:"skipped"`
	SourceDays     int          `json:"source_resource_days"`
	MirrorDays     int          `json:"mirror_resource_days"`
	Matching       int          `json:"matching_resource_days"`
	SourceOverlaps int          `json:"source_overlapping_pairs"`
	Divergences    []Divergence `json:"divergences"`
	Equal          bool         `json:"equal"`
}

// Verify reads the source window and compares it with the mirror. Ids in the
// report are the mirror's opaque ids.
func (im *Importer) Verify(ctx context.Context, from, to time.Time, zone string, maxListed int) (VerifyReport, error) {
	rep := VerifyReport{Tenant: im.Tenant, From: from.UTC().Format(time.RFC3339), To: to.UTC().Format(time.RFC3339), Skipped: Skips{}, Divergences: []Divergence{}}
	if err := im.check(); err != nil {
		return rep, err
	}
	if !to.After(from) || to.Sub(from) > MaxWindow {
		return rep, scheduling.ErrInvalid
	}
	docs, err := im.Reader.InWindow(ctx, from, to)
	if err != nil {
		return rep, fmt.Errorf("read source: %w", err)
	}
	rep.SourceRows = len(docs)
	src, err := SourceDayLoads(im.mapAll(docs, rep.Skipped), zone, from, to)
	if err != nil {
		return rep, err
	}
	mir, err := im.Store.MirrorDayLoads(ctx, im.Tenant, Source, zone, from, to)
	if err != nil {
		return rep, err
	}
	rep.SourceDays, rep.MirrorDays = len(src), len(mir)
	type key struct{ r, d string }
	srcBy := map[key]scheduling.DayLoad{}
	for _, d := range src {
		srcBy[key{d.ResourceID, d.Day}] = d
		rep.SourceOverlaps += d.Overlaps
	}
	seen := map[key]bool{}
	add := func(dv Divergence) {
		if len(rep.Divergences) < maxListed {
			rep.Divergences = append(rep.Divergences, dv)
		}
	}
	divergent := 0
	for _, m := range mir {
		k := key{m.ResourceID, m.Day}
		seen[k] = true
		s, ok := srcBy[k]
		mm := m
		switch {
		case !ok:
			divergent++
			add(Divergence{ResourceID: m.ResourceID, Day: m.Day, Mirror: &mm})
		case s != m:
			divergent++
			ss := s
			add(Divergence{ResourceID: m.ResourceID, Day: m.Day, Source: &ss, Mirror: &mm})
		default:
			rep.Matching++
		}
	}
	for k, s := range srcBy {
		if !seen[k] {
			divergent++
			ss := s
			add(Divergence{ResourceID: k.r, Day: k.d, Source: &ss})
		}
	}
	rep.Equal = divergent == 0
	im.Log.Info("mirror_verify", "tenant_id", im.Tenant, "source_rows", rep.SourceRows, "source_days", rep.SourceDays,
		"mirror_days", rep.MirrorDays, "matching", rep.Matching, "divergent", divergent, "source_overlaps", rep.SourceOverlaps)
	return rep, nil
}
