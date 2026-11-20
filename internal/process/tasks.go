package process

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// SubmitRequest is a person's answer to a task, relayed by the authenticated
// external server. OccurredAt is the instant that server received the action,
// never the browser's clock. The server authenticated the person; the KAS
// checks that ActorID holds Role in the tenant at OccurredAt.
type SubmitRequest struct {
	TenantID      string
	ServiceID     string
	TaskID        int64
	Outcome       string
	ActorID       string
	Role          string
	OccurredAt    time.Time
	SubmissionKey string
}

// SubmitResult reports what happened to a submission: applied, late or
// superseded. NextStep is the instance's step after it.
type SubmitResult struct {
	Status     string
	InstanceID int64
	NextStep   string
	Duplicate  bool
}

type task struct {
	ID         int64
	InstanceID int64
	Step       string
	Roles      []string
	Outcomes   []string
	DueAt      *time.Time
	CutoffAt   *time.Time
	Status     string
}

const taskColumns = `id, instance_id, step, roles, outcomes, due_at, cutoff_at, status`

func scanTask(row pgx.Row) (*task, error) {
	var t task
	err := row.Scan(&t.ID, &t.InstanceID, &t.Step, &t.Roles, &t.Outcomes, &t.DueAt, &t.CutoffAt, &t.Status)
	return &t, err
}

type submission struct {
	ID            int64
	Outcome       string
	ActorID       string
	Role          string
	ServiceID     string
	Assignment    int64
	OccurredAt    time.Time
	ReceivedAt    time.Time
	ReportDelayed bool
	Status        string
}

// SubmitTask records the answer first, in its own short transaction that never
// waits for the instance lock, and only then applies it under the lock. A
// deadline timer reads the recorded answers before deciding, so an answer
// received within the tolerance wins even when the timer takes the lock first.
func (e *Engine) SubmitTask(ctx context.Context, req SubmitRequest) (SubmitResult, error) {
	if !identifierPattern.MatchString(req.TenantID) || req.TaskID <= 0 || !namePattern.MatchString(req.Outcome) ||
		!identifierPattern.MatchString(req.ActorID) || !namePattern.MatchString(req.Role) ||
		!identifierPattern.MatchString(req.SubmissionKey) || req.OccurredAt.IsZero() {
		return SubmitResult{}, ErrInvalidInput
	}
	now := e.now()
	occurred := pgTime(req.OccurredAt)
	if occurred.After(now.Add(e.cfg.MaxSkew)) {
		return SubmitResult{}, ErrClockSkew
	}
	res, subID, alreadyClosed, err := e.recordSubmission(ctx, req, occurred, now)
	if err != nil {
		return SubmitResult{}, err
	}
	return e.applySubmission(ctx, req.TenantID, req.TaskID, subID, res, alreadyClosed)
}

// recordSubmission is the first, lock-free phase of SubmitTask.
func (e *Engine) recordSubmission(ctx context.Context, req SubmitRequest, occurred, now time.Time) (SubmitResult, int64, string, error) {
	var res SubmitResult
	var subID int64
	alreadyClosed := ""
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var s submission
		var taskID int64
		err := tx.QueryRow(ctx, `SELECT s.id, s.task_id, s.outcome, s.actor_id, s.role, s.occurred_at, s.status, t.instance_id
			FROM process_task_submissions s JOIN process_tasks t ON t.id = s.task_id
			WHERE s.tenant_id = $1 AND s.submission_key = $2`, req.TenantID, req.SubmissionKey).
			Scan(&s.ID, &taskID, &s.Outcome, &s.ActorID, &s.Role, &s.OccurredAt, &s.Status, &res.InstanceID)
		if err == nil {
			if taskID != req.TaskID || s.Outcome != req.Outcome || s.ActorID != req.ActorID || s.Role != req.Role || !s.OccurredAt.Equal(occurred) {
				return ErrConflict
			}
			res.Duplicate, subID = true, s.ID
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		t, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM process_tasks WHERE tenant_id = $1 AND id = $2`, req.TenantID, req.TaskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		res.InstanceID = t.InstanceID
		if !slices.Contains(t.Outcomes, req.Outcome) {
			return ErrInvalidInput
		}
		if !slices.Contains(t.Roles, req.Role) {
			return ErrRoleNotAllowed
		}
		var assignment int64
		err = tx.QueryRow(ctx, `SELECT assignment_version FROM process_role_assignments
			WHERE tenant_id = $1 AND role = $2 AND actor_id = $3 AND valid_from <= $4 AND (valid_to IS NULL OR valid_to > $4)
			ORDER BY id DESC LIMIT 1`, req.TenantID, req.Role, req.ActorID, occurred).Scan(&assignment)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrActorNotInRole
		}
		if err != nil {
			return err
		}
		if t.Status != "open" {
			alreadyClosed = t.Status
		}
		return tx.QueryRow(ctx, `INSERT INTO process_task_submissions
			(tenant_id, task_id, submission_key, outcome, actor_id, role, service_id, assignment_version, occurred_at, received_at, report_delayed, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending') RETURNING id`,
			req.TenantID, req.TaskID, req.SubmissionKey, req.Outcome, req.ActorID, req.Role, req.ServiceID, assignment,
			occurred, now, now.Sub(occurred) > e.cfg.MaxReportDelay).Scan(&subID)
	})
	return res, subID, alreadyClosed, mapErr(err)
}

// applySubmission is the second phase: decide the task under the instance lock
// (also for a duplicate whose first apply may have been interrupted).
func (e *Engine) applySubmission(ctx context.Context, tenant string, taskID, subID int64, res SubmitResult, alreadyClosed string) (SubmitResult, error) {
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		in, err := lockInstance(ctx, tx, tenant, res.InstanceID, false)
		if err != nil {
			return err
		}
		t, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM process_tasks WHERE id = $1 FOR UPDATE`, taskID))
		if err != nil {
			return err
		}
		if err := e.settleTask(ctx, tx, in, t, e.now(), false); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status FROM process_task_submissions WHERE id = $1`, subID).Scan(&res.Status); err != nil {
			return err
		}
		res.NextStep = in.CurrentStep
		return nil
	})
	if err != nil {
		return res, mapErr(err)
	}
	if res.Status == "rejected" {
		return res, ErrCommandUnknown
	}
	if alreadyClosed == "resolved" || alreadyClosed == "cancelled" {
		return res, ErrTaskClosed
	}
	return res, nil
}

func (e *Engine) pendingSubmissions(ctx context.Context, tx pgx.Tx, taskID int64) ([]submission, error) {
	rows, err := tx.Query(ctx, `SELECT id, outcome, actor_id, role, service_id, assignment_version, occurred_at, received_at, report_delayed, status
		FROM process_task_submissions WHERE task_id = $1 AND status = 'pending' ORDER BY occurred_at, id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []submission
	for rows.Next() {
		var s submission
		if err := rows.Scan(&s.ID, &s.Outcome, &s.ActorID, &s.Role, &s.ServiceID, &s.Assignment, &s.OccurredAt, &s.ReceivedAt, &s.ReportDelayed, &s.Status); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// onTime is the single precedence rule: an answer counts when it happened by
// due_at and reached the KAS by cutoff_at (due_at + grace), and its report was
// not delayed beyond the limit. Tasks without a deadline accept every answer.
func onTime(t *task, s submission) bool {
	if t.DueAt == nil {
		return true
	}
	return !s.ReportDelayed && !s.OccurredAt.After(*t.DueAt) && !s.ReceivedAt.After(*t.CutoffAt)
}

func (e *Engine) markSubmission(ctx context.Context, tx pgx.Tx, id int64, status, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE process_task_submissions SET status = $2, status_reason = NULLIF($3,'') WHERE id = $1 AND status = 'pending'`, id, status, reason)
	return err
}

// settleTask decides a task from its recorded answers, by timestamps only, no
// matter who holds the lock. With fromTimer, or once the clock passed cutoff_at,
// a task without an on-time answer expires; otherwise it stays open and late
// answers are marked late.
func (e *Engine) settleTask(ctx context.Context, tx pgx.Tx, in *instance, t *task, now time.Time, fromTimer bool) error {
	subs, err := e.pendingSubmissions(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	if t.Status != "open" {
		for _, s := range subs {
			status, reason := "superseded", "task_"+t.Status
			if t.Status == "expired" {
				status, reason = "late", "after_deadline"
			}
			if err := e.markSubmission(ctx, tx, s.ID, status, reason); err != nil {
				return err
			}
			if err := e.history(ctx, tx, in, "response_"+status, t.Step, s.ServiceID, s.ActorID, s.Role, map[string]any{
				"submission_id": s.ID, "outcome": s.Outcome, "occurred_at": s.OccurredAt, "received_at": s.ReceivedAt, "reason": reason,
			}, now); err != nil {
				return err
			}
		}
		return nil
	}
	d, err := e.definition(ctx, tx, in.TenantID, in.ProcessKey, in.Version)
	if err != nil {
		return err
	}
	var winner *submission
	var late []submission
	for i := range subs {
		if onTime(t, subs[i]) {
			if winner == nil {
				winner = &subs[i] // ordered by occurred_at, id
			} else {
				if err := e.markSubmission(ctx, tx, subs[i].ID, "superseded", "earlier_answer_won"); err != nil {
					return err
				}
			}
		} else {
			late = append(late, subs[i])
		}
	}
	for _, s := range late {
		if err := e.markSubmission(ctx, tx, s.ID, "late", "after_deadline"); err != nil {
			return err
		}
		if err := e.history(ctx, tx, in, "late_response", t.Step, s.ServiceID, s.ActorID, s.Role, map[string]any{
			"submission_id": s.ID, "outcome": s.Outcome, "occurred_at": s.OccurredAt, "received_at": s.ReceivedAt,
			"due_at": t.DueAt, "cutoff_at": t.CutoffAt, "report_delayed": s.ReportDelayed, "policy": LateRecordOnly,
		}, now); err != nil {
			return err
		}
	}
	if winner != nil {
		return e.resolveTask(ctx, tx, in, d, t, *winner, now)
	}
	expired := t.CutoffAt != nil && (fromTimer || now.After(*t.CutoffAt))
	if !expired {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE process_tasks SET status = 'expired', closed_at = $2 WHERE id = $1`, t.ID, now); err != nil {
		return err
	}
	t.Status = "expired"
	step := d.Steps[t.Step]
	if err := e.history(ctx, tx, in, "deadline_expired", t.Step, "", "", "", map[string]any{
		"task_id": t.ID, "due_at": t.DueAt, "cutoff_at": t.CutoffAt, "decided_at": now, "by_timer": fromTimer,
	}, now); err != nil {
		return err
	}
	return e.advance(ctx, tx, in, d, step.OnDeadline, "deadline", now)
}

// hasUncertainCommand reports a command of the instance whose effect may have
// happened without confirmation.
func hasUncertainCommand(ctx context.Context, tx pgx.Tx, instanceID int64) (bool, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM process_commands WHERE instance_id = $1 AND status IN ('requested','unknown')`, instanceID).Scan(&n)
	return n > 0, err
}

// leadsToEnd follows decisions from target with outcome and reports whether the
// path reaches an end step.
func leadsToEnd(d *Definition, target, outcome string) bool {
	for i := 0; i < maxAutomaticSteps; i++ {
		s, ok := d.Steps[target]
		if !ok {
			return false
		}
		switch s.Type {
		case StepEnd:
			return true
		case StepDecision:
			target = s.Routes[outcome]
		default:
			return false
		}
	}
	return false
}

func (e *Engine) resolveTask(ctx context.Context, tx pgx.Tx, in *instance, d *Definition, t *task, s submission, now time.Time) error {
	step := d.Steps[t.Step]
	if leadsToEnd(d, step.Next, s.Outcome) {
		uncertain, err := hasUncertainCommand(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		if uncertain {
			// The external system may have applied an effect: closing now could
			// record the opposite of what happened. The task stays open.
			if err := e.markSubmission(ctx, tx, s.ID, "rejected", "command_unknown"); err != nil {
				return err
			}
			return e.history(ctx, tx, in, "response_rejected", t.Step, s.ServiceID, s.ActorID, s.Role, map[string]any{
				"submission_id": s.ID, "outcome": s.Outcome, "reason": "command_unknown",
			}, now)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE process_tasks SET status = 'resolved', outcome = $2, submission_id = $3, closed_at = $4 WHERE id = $1`,
		t.ID, s.Outcome, s.ID, now); err != nil {
		return err
	}
	if err := e.markSubmission(ctx, tx, s.ID, "applied", ""); err != nil {
		return err
	}
	t.Status = "resolved"
	outcome := s.Outcome
	in.LastOutcome = &outcome
	if err := e.history(ctx, tx, in, "task_resolved", t.Step, s.ServiceID, s.ActorID, s.Role, map[string]any{
		"task_id": t.ID, "submission_id": s.ID, "outcome": s.Outcome, "assignment_version": s.Assignment,
		"occurred_at": s.OccurredAt, "received_at": s.ReceivedAt, "due_at": t.DueAt, "cutoff_at": t.CutoffAt,
	}, now); err != nil {
		return err
	}
	return e.advance(ctx, tx, in, d, step.Next, "task:"+s.Outcome, now)
}

// RunDueTimers executes up to limit timers whose fire_at has passed and returns
// how many it executed. Several runners may work at once: an instance busy in
// another transaction is skipped and retried on the next pass. Decisions use the
// timer's due_at and cutoff_at, never the moment it actually runs (executed_at).
func (e *Engine) RunDueTimers(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	now := e.now()
	type due struct {
		id, instance int64
		tenant       string
	}
	var list []due
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	rows, err := e.pool.Query(qctx, `SELECT id, instance_id, tenant_id FROM process_timers
		WHERE status = 'pending' AND fire_at <= $1 ORDER BY fire_at, id LIMIT $2`, now, limit)
	if err != nil {
		cancel()
		return 0, ErrUnavailable
	}
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.instance, &d.tenant); err != nil {
			rows.Close()
			cancel()
			return 0, ErrUnavailable
		}
		list = append(list, d)
	}
	rows.Close()
	cancel()
	executed := 0
	for _, d := range list {
		ran := false
		err := e.inTx(ctx, func(tx pgx.Tx) error {
			in, err := lockInstance(ctx, tx, d.tenant, d.instance, true)
			if err != nil || in == nil {
				return err
			}
			var kind string
			var ref int64
			var dueAt, cutoffAt, fireAt time.Time
			err = tx.QueryRow(ctx, `SELECT kind, ref_id, due_at, cutoff_at, fire_at FROM process_timers WHERE id = $1 AND status = 'pending' FOR UPDATE`, d.id).
				Scan(&kind, &ref, &dueAt, &cutoffAt, &fireAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE process_timers SET status = 'done', executed_at = $2 WHERE id = $1`, d.id, now); err != nil {
				return err
			}
			if err := e.history(ctx, tx, in, "timer_executed", in.CurrentStep, "", "", "", map[string]any{
				"kind": kind, "due_at": dueAt, "cutoff_at": cutoffAt, "fire_at": fireAt, "executed_at": now,
			}, now); err != nil {
				return err
			}
			ran = true
			switch kind {
			case "task_deadline":
				t, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM process_tasks WHERE id = $1 FOR UPDATE`, ref))
				if err != nil {
					return err
				}
				return e.settleTask(ctx, tx, in, t, now, true)
			case "wait_deadline":
				return e.expireWait(ctx, tx, in, ref, now)
			case "command_reply":
				return e.commandReplyTimeout(ctx, tx, in, ref, now)
			}
			return nil
		})
		if err != nil {
			e.cfg.Logger.Error("process timer failed", "timer_id", d.id, "error_code", "timer_failed")
			continue
		}
		if ran {
			executed++
		}
	}
	return executed, nil
}

// Run executes due timers every interval until ctx ends.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := e.RunDueTimers(ctx, 100); err != nil {
			e.cfg.Logger.Warn("process timers unavailable", "error_code", "timers_unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
