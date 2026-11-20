package process

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type wait struct {
	ID         int64
	InstanceID int64
	Step       string
	Key        string
	Types      []string
	DueAt      *time.Time
	CutoffAt   *time.Time
	Status     string
	OpenedAt   time.Time
}

const waitColumns = `id, instance_id, step, wait_key, event_types, due_at, cutoff_at, status, opened_at`

func scanWait(row pgx.Row) (*wait, error) {
	var w wait
	err := row.Scan(&w.ID, &w.InstanceID, &w.Step, &w.Key, &w.Types, &w.DueAt, &w.CutoffAt, &w.Status, &w.OpenedAt)
	return &w, err
}

func (e *Engine) expireWait(ctx context.Context, tx pgx.Tx, in *instance, waitID int64, now time.Time) error {
	w, err := scanWait(tx.QueryRow(ctx, `SELECT `+waitColumns+` FROM process_waits WHERE id = $1 FOR UPDATE`, waitID))
	if err != nil {
		return err
	}
	if w.Status != "open" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE process_waits SET status = 'expired', closed_at = $2 WHERE id = $1`, w.ID, now); err != nil {
		return err
	}
	d, err := e.definition(ctx, tx, in.TenantID, in.ProcessKey, in.Version)
	if err != nil {
		return err
	}
	if err := e.history(ctx, tx, in, "deadline_expired", w.Step, "", "", "", map[string]any{
		"wait_key": w.Key, "due_at": w.DueAt, "cutoff_at": w.CutoffAt, "decided_at": now,
	}, now); err != nil {
		return err
	}
	return e.advance(ctx, tx, in, d, d.Steps[w.Step].OnDeadline, "deadline", now)
}

// EventRequest is an external event reported by an authenticated service.
// WaitKey is present when the event answers a wait the process opened (for
// example a confirmation by password or PIN, validated by the external system).
// Spontaneous events (a regulator's cancellation, a contract expiry) come
// without it and match by business_ref and type.
type EventRequest struct {
	TenantID    string
	ServiceID   string
	EventID     string
	EventType   string
	BusinessRef string
	WaitKey     string
	OccurredAt  time.Time
}

// EventResult says how an event was matched.
type EventResult struct {
	Outcome    string
	Matched    bool
	InstanceID int64
	Duplicate  bool
}

// IngestEvent records an event once per (tenant, service, event_id) and routes
// it to the right wait or interrupt. Nothing is discarded: unmatched and
// ambiguous events are kept and alerted.
func (e *Engine) IngestEvent(ctx context.Context, r EventRequest) (EventResult, error) {
	if !identifierPattern.MatchString(r.TenantID) || !identifierPattern.MatchString(r.EventID) ||
		!eventTypePattern.MatchString(r.EventType) || !identifierPattern.MatchString(r.BusinessRef) ||
		(r.WaitKey != "" && !identifierPattern.MatchString(r.WaitKey)) || r.OccurredAt.IsZero() {
		return EventResult{}, ErrInvalidInput
	}
	now := e.now()
	occurred := pgTime(r.OccurredAt)
	if occurred.After(now.Add(e.cfg.MaxSkew)) {
		return EventResult{}, ErrClockSkew
	}
	var res EventResult
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var sType, sRef, sKey, sOutcome string
		var sOccurred time.Time
		var sInstance *int64
		err := tx.QueryRow(ctx, `SELECT event_type, business_ref, COALESCE(wait_key,''), occurred_at, outcome, instance_id
			FROM process_events_received WHERE tenant_id = $1 AND service_id = $2 AND event_id = $3`, r.TenantID, r.ServiceID, r.EventID).
			Scan(&sType, &sRef, &sKey, &sOccurred, &sOutcome, &sInstance)
		if err == nil {
			if sType != r.EventType || sRef != r.BusinessRef || sKey != r.WaitKey || !sOccurred.Equal(occurred) {
				return ErrConflict
			}
			res = EventResult{Outcome: sOutcome, Matched: sOutcome == "matched" || sOutcome == "interrupt", Duplicate: true}
			if sInstance != nil {
				res.InstanceID = *sInstance
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		outcome, instanceID, err := e.routeEvent(ctx, tx, r, occurred, now)
		if err != nil {
			return err
		}
		res = EventResult{Outcome: outcome, Matched: outcome == "matched" || outcome == "interrupt", InstanceID: instanceID}
		var inst any
		if instanceID > 0 {
			inst = instanceID
		}
		if outcome == "unmatched" || outcome == "ambiguous" {
			e.cfg.Logger.Warn("process event without a single wait", "error_code", "event_"+outcome, "tenant_id", r.TenantID, "event_type", r.EventType)
		}
		_, err = tx.Exec(ctx, `INSERT INTO process_events_received (tenant_id, service_id, event_id, event_type, business_ref, wait_key, occurred_at, received_at, outcome, instance_id)
			VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10)`,
			r.TenantID, r.ServiceID, r.EventID, r.EventType, r.BusinessRef, r.WaitKey, occurred, now, outcome, inst)
		return err
	})
	if err != nil {
		return EventResult{}, mapErr(err)
	}
	if res.Outcome == "wait_type_mismatch" {
		return res, ErrWaitTypeMismatch
	}
	return res, nil
}

func (e *Engine) routeEvent(ctx context.Context, tx pgx.Tx, r EventRequest, occurred, now time.Time) (string, int64, error) {
	var target *wait
	if r.WaitKey != "" {
		w, err := scanWait(tx.QueryRow(ctx, `SELECT `+waitColumns+` FROM process_waits WHERE tenant_id = $1 AND wait_key = $2`, r.TenantID, r.WaitKey))
		if errors.Is(err, pgx.ErrNoRows) {
			return "unmatched", 0, nil
		}
		if err != nil {
			return "", 0, err
		}
		if !slices.Contains(w.Types, r.EventType) {
			return "wait_type_mismatch", w.InstanceID, nil
		}
		target = w
	} else {
		rows, err := tx.Query(ctx, `SELECT `+waitColumns+` FROM process_waits
			WHERE tenant_id = $1 AND business_ref = $2 AND status = 'open' AND $3 = ANY(event_types)`, r.TenantID, r.BusinessRef, r.EventType)
		if err != nil {
			return "", 0, err
		}
		var found []*wait
		for rows.Next() {
			w, err := scanWait(rows)
			if err != nil {
				rows.Close()
				return "", 0, err
			}
			found = append(found, w)
		}
		rows.Close()
		if len(found) > 1 {
			return "ambiguous", 0, nil
		}
		if len(found) == 1 {
			target = found[0]
		}
	}
	if target != nil {
		in, err := lockInstance(ctx, tx, r.TenantID, target.InstanceID, false)
		if err != nil {
			return "", 0, err
		}
		w, err := scanWait(tx.QueryRow(ctx, `SELECT `+waitColumns+` FROM process_waits WHERE id = $1 FOR UPDATE`, target.ID))
		if err != nil {
			return "", 0, err
		}
		return e.applyEventToWait(ctx, tx, in, w, r, occurred, now)
	}
	// No wait: try the process-wide interrupts of running instances.
	rows, err := tx.Query(ctx, `SELECT id, process_key, version FROM process_instances
		WHERE tenant_id = $1 AND business_ref = $2 AND status = 'running'`, r.TenantID, r.BusinessRef)
	if err != nil {
		return "", 0, err
	}
	type cand struct {
		id      int64
		key     string
		version int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.key, &c.version); err != nil {
			rows.Close()
			return "", 0, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	var hits []cand
	for _, c := range cands {
		d, err := e.definition(ctx, tx, r.TenantID, c.key, c.version)
		if err != nil {
			return "", 0, err
		}
		if _, ok := d.Interrupts[r.EventType]; ok {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 0:
		return "unmatched", 0, nil
	case 1:
	default:
		return "ambiguous", 0, nil
	}
	in, err := lockInstance(ctx, tx, r.TenantID, hits[0].id, false)
	if err != nil {
		return "", 0, err
	}
	if in.Status != "running" {
		return "unmatched", 0, nil
	}
	d, err := e.definition(ctx, tx, in.TenantID, in.ProcessKey, in.Version)
	if err != nil {
		return "", 0, err
	}
	if err := e.history(ctx, tx, in, "interrupted", in.CurrentStep, r.ServiceID, "", "", map[string]any{
		"event_id": r.EventID, "event_type": r.EventType, "occurred_at": occurred,
	}, now); err != nil {
		return "", 0, err
	}
	return "interrupt", in.ID, e.advance(ctx, tx, in, d, d.Interrupts[r.EventType], "interrupt:"+r.EventType, now)
}

func (e *Engine) applyEventToWait(ctx context.Context, tx pgx.Tx, in *instance, w *wait, r EventRequest, occurred, now time.Time) (string, int64, error) {
	detail := map[string]any{"event_id": r.EventID, "event_type": r.EventType, "wait_key": w.Key, "occurred_at": occurred, "received_at": now}
	if w.Status != "open" {
		return "wait_closed", in.ID, e.history(ctx, tx, in, "event_wait_closed", w.Step, r.ServiceID, "", "", detail, now)
	}
	if occurred.Before(w.OpenedAt) {
		return "stale", in.ID, e.history(ctx, tx, in, "event_stale", w.Step, r.ServiceID, "", "", detail, now)
	}
	if w.DueAt != nil && (occurred.After(*w.DueAt) || now.After(*w.CutoffAt)) {
		if err := e.history(ctx, tx, in, "event_late", w.Step, r.ServiceID, "", "", detail, now); err != nil {
			return "", 0, err
		}
		if now.After(*w.CutoffAt) {
			// The deadline is a fact of the clock, not of the timer worker.
			if err := e.expireWait(ctx, tx, in, w.ID, now); err != nil {
				return "", 0, err
			}
		}
		return "late", in.ID, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE process_waits SET status = 'matched', closed_at = $2 WHERE id = $1`, w.ID, now); err != nil {
		return "", 0, err
	}
	d, err := e.definition(ctx, tx, in.TenantID, in.ProcessKey, in.Version)
	if err != nil {
		return "", 0, err
	}
	if err := e.history(ctx, tx, in, "event_matched", w.Step, r.ServiceID, "", "", detail, now); err != nil {
		return "", 0, err
	}
	return "matched", in.ID, e.advance(ctx, tx, in, d, d.Steps[w.Step].OnEvents[r.EventType], "event:"+r.EventType, now)
}
