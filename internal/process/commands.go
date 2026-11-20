package process

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"kas-core-pilot/internal/calendar"
)

// commandReplyTimeout runs when a command got no result in reply_timeout: it
// asks the external system again for the result of the SAME key, and once the
// queries are exhausted marks the command unknown and follows on_no_reply.
func (e *Engine) commandReplyTimeout(ctx context.Context, tx pgx.Tx, in *instance, commandID int64, now time.Time) error {
	cmd, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM process_commands WHERE id = $1 FOR UPDATE`, commandID))
	if err != nil {
		return err
	}
	if cmd.Status != "requested" || in.Status != "running" || in.CurrentStep != cmd.Step {
		return nil
	}
	d, err := e.definition(ctx, tx, in.TenantID, in.ProcessKey, in.Version)
	if err != nil {
		return err
	}
	step := d.Steps[cmd.Step]
	if cmd.StatusQueries < step.MaxStatusQueries {
		if err := e.sendStatusQuery(ctx, tx, in, cmd, now); err != nil {
			return err
		}
		timeout, err := calendar.ParseDuration(step.ReplyTimeout)
		if err != nil {
			return err
		}
		fire := now.Add(timeout)
		return e.addTimer(ctx, tx, in, "command_reply", cmd.ID, fire, fire, fire, now)
	}
	if _, err := tx.Exec(ctx, `UPDATE process_commands SET status = 'unknown', updated_at = $2 WHERE id = $1`, cmd.ID, now); err != nil {
		return err
	}
	e.cfg.Logger.Warn("process command without result", "error_code", "command_unknown", "tenant_id", in.TenantID, "instance_id", in.ID)
	if err := e.history(ctx, tx, in, "command_unknown", cmd.Step, "", "", "", map[string]any{
		"command_key": cmd.Key, "status_queries": cmd.StatusQueries, "alert": true,
	}, now); err != nil {
		return err
	}
	return e.advance(ctx, tx, in, d, step.OnNoReply, "command_no_reply", now)
}

func (e *Engine) sendStatusQuery(ctx context.Context, tx pgx.Tx, in *instance, cmd *command, now time.Time) error {
	cmd.StatusQueries++
	if _, err := tx.Exec(ctx, `UPDATE process_commands SET status_queries = $2, updated_at = $3 WHERE id = $1`, cmd.ID, cmd.StatusQueries, now); err != nil {
		return err
	}
	if err := e.publish(ctx, tx, in, "cmd:"+cmd.Key+":q"+strconv.Itoa(cmd.RequestAttempt)+"."+strconv.Itoa(cmd.StatusQueries),
		EventCommandStatusQuery, map[string]any{
			"command_key": cmd.Key, "command_type": cmd.Type, "instance_id": in.ID, "business_ref": in.BusinessRef,
			"query": cmd.StatusQueries,
		}, now); err != nil {
		return err
	}
	return e.history(ctx, tx, in, "command_status_query", cmd.Step, "", "", "", map[string]any{
		"command_key": cmd.Key, "query": cmd.StatusQueries,
	}, now)
}

// CommandResult is the external system's definitive answer for a command key.
type CommandResult struct {
	TenantID   string
	ServiceID  string
	CommandKey string
	Status     string // confirmed | rejected
	ReasonCode string
	// ResultRef is the optional business identifier of what the external
	// system created for this command (e.g. the id of the new package). It is
	// opaque, at most 128 characters of [A-Za-z0-9._:-], never personal data,
	// and only accepted with "confirmed".
	ResultRef  string
	OccurredAt time.Time
}

// RecordCommandResult stores the result reported for a command key. Resending
// the same result (same status and same result_ref) is harmless; a different
// definitive result or a different result_ref is a conflict. When the step
// requires result_ref, a confirmation without it is refused and nothing
// changes. The instance moves on only if it is still waiting on that command;
// otherwise the result is recorded for the review to see.
func (e *Engine) RecordCommandResult(ctx context.Context, r CommandResult) (bool, error) {
	if !identifierPattern.MatchString(r.TenantID) || !identifierPattern.MatchString(r.CommandKey) ||
		(r.Status != "confirmed" && r.Status != "rejected") || (r.ReasonCode != "" && !codePattern.MatchString(r.ReasonCode)) ||
		(r.ResultRef != "" && (r.Status != "confirmed" || !identifierPattern.MatchString(r.ResultRef))) {
		return false, ErrInvalidInput
	}
	r.OccurredAt = pgTime(r.OccurredAt)
	now := e.now()
	if r.OccurredAt.After(now.Add(e.cfg.MaxSkew)) {
		return false, ErrClockSkew
	}
	duplicate := false
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var instanceID int64
		err := tx.QueryRow(ctx, `SELECT instance_id FROM process_commands WHERE tenant_id = $1 AND command_key = $2`, r.TenantID, r.CommandKey).Scan(&instanceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		in, err := lockInstance(ctx, tx, r.TenantID, instanceID, false)
		if err != nil {
			return err
		}
		cmd, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM process_commands WHERE tenant_id = $1 AND command_key = $2 FOR UPDATE`, r.TenantID, r.CommandKey))
		if err != nil {
			return err
		}
		if cmd.Status == "confirmed" || cmd.Status == "rejected" {
			if cmd.Status != r.Status || cmd.ResultRef != r.ResultRef {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		d, err := e.definition(ctx, tx, in.TenantID, in.ProcessKey, in.Version)
		if err != nil {
			return err
		}
		step := d.Steps[cmd.Step]
		if step.RequireResultRef && r.Status == "confirmed" && r.ResultRef == "" {
			e.cfg.Logger.Warn("process command confirmed without result_ref", "error_code", "result_ref_required", "tenant_id", in.TenantID, "instance_id", in.ID)
			return ErrResultRefRequired
		}
		if _, err := tx.Exec(ctx, `UPDATE process_commands SET status = $2, result_reason = NULLIF($3,''), result_occurred_at = $4, result_ref = NULLIF($5,''), updated_at = $6 WHERE id = $1`,
			cmd.ID, r.Status, r.ReasonCode, r.OccurredAt.UTC(), r.ResultRef, now); err != nil {
			return err
		}
		waiting := in.Status == "running" && in.CurrentStep == cmd.Step
		kind := "command_result"
		if !waiting {
			kind = "late_command_result"
		}
		detail := map[string]any{
			"command_key": cmd.Key, "status": r.Status, "reason_code": r.ReasonCode, "occurred_at": r.OccurredAt.UTC(), "previous_status": cmd.Status,
			"status_queries": cmd.StatusQueries,
		}
		if r.ResultRef != "" {
			detail["result_ref"] = r.ResultRef
		}
		if err := e.history(ctx, tx, in, kind, cmd.Step, r.ServiceID, "", "", detail, now); err != nil {
			return err
		}
		if !waiting {
			return nil
		}
		next := step.OnConfirmed
		if r.Status == "rejected" {
			next = step.OnRejected
		}
		return e.advance(ctx, tx, in, d, next, "command_"+r.Status, now)
	})
	return duplicate, mapErr(err)
}

// QueryCommand asks the external system again, with the same key, for the
// result of a command still requested or unknown (the review's "consult
// again"). It never changes the instance's step.
func (e *Engine) QueryCommand(ctx context.Context, tenant, service, actor, key string) error {
	if !identifierPattern.MatchString(tenant) || !identifierPattern.MatchString(key) || !identifierPattern.MatchString(actor) {
		return ErrInvalidInput
	}
	now := e.now()
	return mapErr(e.inTx(ctx, func(tx pgx.Tx) error {
		var instanceID int64
		err := tx.QueryRow(ctx, `SELECT instance_id FROM process_commands WHERE tenant_id = $1 AND command_key = $2`, tenant, key).Scan(&instanceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		in, err := lockInstance(ctx, tx, tenant, instanceID, false)
		if err != nil {
			return err
		}
		cmd, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM process_commands WHERE tenant_id = $1 AND command_key = $2 FOR UPDATE`, tenant, key))
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"requested", "unknown"}, cmd.Status) {
			return ErrCommandNotPending
		}
		if err := e.history(ctx, tx, in, "command_query_requested", cmd.Step, service, actor, "", map[string]any{"command_key": key}, now); err != nil {
			return err
		}
		return e.sendStatusQuery(ctx, tx, in, cmd, now)
	}))
}
