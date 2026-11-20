package process

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kas-core-pilot/internal/calendar"
	"kas-core-pilot/internal/events"
)

const maxAutomaticSteps = 50

type instance struct {
	ID           int64
	TenantID     string
	ProcessKey   string
	Version      int
	BusinessRef  string
	StartKey     string
	Status       string
	CurrentStep  string
	StepVisit    int
	StateVersion int
	LastOutcome  *string
	Result       *string
	loaded       int
}

const instanceColumns = `id, tenant_id, process_key, version, business_ref, start_key, status, current_step,
	step_visit, state_version, last_outcome, result`

func scanInstance(row pgx.Row) (*instance, error) {
	var in instance
	if err := row.Scan(&in.ID, &in.TenantID, &in.ProcessKey, &in.Version, &in.BusinessRef, &in.StartKey,
		&in.Status, &in.CurrentStep, &in.StepVisit, &in.StateVersion, &in.LastOutcome, &in.Result); err != nil {
		return nil, err
	}
	in.loaded = in.StateVersion
	return &in, nil
}

// lockInstance loads an instance of the tenant with FOR UPDATE. With skip, a
// locked row is reported as (nil, nil) instead of waiting.
func lockInstance(ctx context.Context, tx pgx.Tx, tenant string, id int64, skip bool) (*instance, error) {
	q := `SELECT ` + instanceColumns + ` FROM process_instances WHERE tenant_id = $1 AND id = $2 FOR UPDATE`
	if skip {
		q += ` SKIP LOCKED`
	}
	in, err := scanInstance(tx.QueryRow(ctx, q, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		if skip {
			return nil, nil
		}
		return nil, ErrNotFound
	}
	return in, err
}

func (e *Engine) saveInstance(ctx context.Context, tx pgx.Tx, in *instance, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE process_instances SET status = $3, current_step = $4, step_visit = $5,
		state_version = $6, last_outcome = $7, result = $8, updated_at = $9
		WHERE id = $1 AND state_version = $2`,
		in.ID, in.loaded, in.Status, in.CurrentStep, in.StepVisit, in.StateVersion, in.LastOutcome, in.Result, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("instance state_version changed under lock")
	}
	in.loaded = in.StateVersion
	return nil
}

func (e *Engine) definition(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenant, key string, version int) (*Definition, error) {
	cacheKey := tenant + "\x1f" + key + "\x1f" + strconv.Itoa(version)
	if d, ok := e.defs.Load(cacheKey); ok {
		return d.(*Definition), nil
	}
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT definition FROM process_definitions
		WHERE tenant_id = $1 AND process_key = $2 AND version = $3`, tenant, key, version).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d, _, _, err := ParseDefinition(raw)
	if err != nil {
		return nil, err
	}
	e.defs.Store(cacheKey, d)
	return d, nil
}

func (e *Engine) history(ctx context.Context, tx pgx.Tx, in *instance, kind, step, service, actor, role string, detail map[string]any, now time.Time) error {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO process_history (tenant_id, instance_id, recorded_at, kind, step, service_id, actor_id, role, detail)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9::jsonb)`,
		in.TenantID, in.ID, now, kind, step, service, actor, role, string(raw))
	return err
}

func (e *Engine) publish(ctx context.Context, tx pgx.Tx, in *instance, eventID, eventType string, payload map[string]any, now time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var consumers []string
	if e.cfg.Registry != nil {
		consumers = e.cfg.Registry.ConsumersFor(in.TenantID, eventType)
	}
	ts := now.Format(time.RFC3339Nano)
	_, err = events.PublishTx(ctx, tx, events.Event{
		ContractVersion: events.ContractVersion, EventID: eventID, TenantID: in.TenantID,
		ServiceID: e.cfg.ServiceID, EventType: eventType, CorrelationID: "process-" + strconv.FormatInt(in.ID, 10),
		OccurredAt: ts, ReceivedAt: ts, Payload: raw,
	}, consumers)
	return err
}

// PublishDefinition validates and stores a new version of a process for the
// tenant. Resending the same publish_id with the same definition returns the
// stored version; the same publish_id with other content is a conflict.
func (e *Engine) PublishDefinition(ctx context.Context, tenant, service, publishID string, raw []byte) (version int, duplicate bool, err error) {
	if !identifierPattern.MatchString(tenant) || !identifierPattern.MatchString(publishID) || !identifierPattern.MatchString(service) {
		return 0, false, ErrInvalidInput
	}
	d, canonical, checksum, err := ParseDefinition(raw)
	if err != nil {
		return 0, false, err
	}
	now := e.now()
	err = e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "process-def/"+tenant+"/"+d.ProcessKey); err != nil {
			return err
		}
		var storedKey, storedSum string
		err := tx.QueryRow(ctx, `SELECT process_key, checksum, version FROM process_definitions WHERE tenant_id = $1 AND publish_id = $2`,
			tenant, publishID).Scan(&storedKey, &storedSum, &version)
		if err == nil {
			if storedKey != d.ProcessKey || storedSum != checksum {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM process_definitions WHERE tenant_id = $1 AND process_key = $2`,
			tenant, d.ProcessKey).Scan(&version); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO process_definitions (tenant_id, process_key, version, definition, checksum, publish_id, published_by, published_at)
			VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8)`, tenant, d.ProcessKey, version, string(canonical), checksum, publishID, service, now)
		return err
	})
	return version, duplicate, mapErr(err)
}

// SetRoleMembers replaces the members of a role in a tenant from validFrom on
// (zero means now). Earlier memberships stay recorded with their validity, so a
// task resolution is checked against the membership at its occurred_at.
func (e *Engine) SetRoleMembers(ctx context.Context, tenant, service, role, changeID string, members []string, validFrom time.Time) (int64, bool, error) {
	if !identifierPattern.MatchString(tenant) || !namePattern.MatchString(role) || !identifierPattern.MatchString(changeID) || len(members) > 500 {
		return 0, false, ErrInvalidInput
	}
	sorted := slices.Clone(members)
	sort.Strings(sorted)
	sorted = slices.Compact(sorted)
	for _, m := range sorted {
		if !identifierPattern.MatchString(m) {
			return 0, false, ErrInvalidInput
		}
	}
	now := e.now()
	validFrom = pgTime(validFrom)
	if validFrom.IsZero() {
		validFrom = now
	}
	var version int64
	var duplicate bool
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "process-role/"+tenant+"/"+role); err != nil {
			return err
		}
		var storedRole string
		var storedMembers []string
		err := tx.QueryRow(ctx, `SELECT role, members, assignment_version FROM process_role_changes WHERE tenant_id = $1 AND change_id = $2`,
			tenant, changeID).Scan(&storedRole, &storedMembers, &version)
		if err == nil {
			if storedRole != role || !slices.Equal(storedMembers, sorted) {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(assignment_version), 0) + 1 FROM process_role_changes WHERE tenant_id = $1 AND role = $2`,
			tenant, role).Scan(&version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE process_role_assignments SET valid_to = $4
			WHERE tenant_id = $1 AND role = $2 AND valid_to IS NULL AND NOT (actor_id = ANY($3))`,
			tenant, role, sorted, validFrom); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO process_role_assignments (tenant_id, role, actor_id, valid_from, assignment_version)
			SELECT $1, $2, m, $4, $5 FROM unnest($3::text[]) AS m
			WHERE NOT EXISTS (SELECT 1 FROM process_role_assignments a
				WHERE a.tenant_id = $1 AND a.role = $2 AND a.actor_id = m AND a.valid_to IS NULL)`,
			tenant, role, sorted, validFrom, version); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO process_role_changes (tenant_id, role, change_id, assignment_version, members, valid_from, changed_by, recorded_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, tenant, role, changeID, version, sorted, validFrom, service, now)
		return err
	})
	return version, duplicate, mapErr(err)
}

// StartRequest starts an instance of the latest published version.
type StartRequest struct {
	TenantID    string
	ServiceID   string
	ProcessKey  string
	BusinessRef string
	StartKey    string
	ActorID     string
	OccurredAt  time.Time
}

// StartInstance creates an instance pinned to the latest version and runs it
// up to its first waiting step. A repeated start_key returns the same instance.
func (e *Engine) StartInstance(ctx context.Context, req StartRequest) (int64, bool, error) {
	if !identifierPattern.MatchString(req.TenantID) || !keyPattern.MatchString(req.ProcessKey) ||
		!identifierPattern.MatchString(req.BusinessRef) || !identifierPattern.MatchString(req.StartKey) ||
		!identifierPattern.MatchString(req.ActorID) {
		return 0, false, ErrInvalidInput
	}
	req.OccurredAt = pgTime(req.OccurredAt)
	now := e.now()
	if req.OccurredAt.After(now.Add(e.cfg.MaxSkew)) {
		return 0, false, ErrClockSkew
	}
	var id int64
	var duplicate bool
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var storedKey, storedRef string
		err := tx.QueryRow(ctx, `SELECT id, process_key, business_ref FROM process_instances WHERE tenant_id = $1 AND start_key = $2`,
			req.TenantID, req.StartKey).Scan(&id, &storedKey, &storedRef)
		if err == nil {
			if storedKey != req.ProcessKey || storedRef != req.BusinessRef {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var version int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM process_definitions WHERE tenant_id = $1 AND process_key = $2`,
			req.TenantID, req.ProcessKey).Scan(&version); err != nil {
			return err
		}
		if version == 0 {
			return ErrNotFound
		}
		d, err := e.definition(ctx, tx, req.TenantID, req.ProcessKey, version)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT start_insert`); err != nil {
			return err
		}
		in, err := scanInstance(tx.QueryRow(ctx, `INSERT INTO process_instances
			(tenant_id, process_key, version, business_ref, start_key, status, current_step, started_by, started_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'running',$6,$7,$8,$8) RETURNING `+instanceColumns,
			req.TenantID, req.ProcessKey, version, req.BusinessRef, req.StartKey, d.Start, req.ActorID, now))
		if err != nil {
			if uniqueViolation(err, "process_instances_one_active") {
				return ErrInstanceActive
			}
			return err
		}
		id = in.ID
		if err := e.history(ctx, tx, in, "started", "", req.ServiceID, req.ActorID, "", map[string]any{
			"version": version, "business_ref": req.BusinessRef, "occurred_at": req.OccurredAt.UTC(),
		}, now); err != nil {
			return err
		}
		return e.advance(ctx, tx, in, d, d.Start, "start", now)
	})
	return id, duplicate, mapErr(err)
}

// cancelOpen closes everything still open for the instance before it enters a
// new step: pending timers, open tasks and open waits.
func (e *Engine) cancelOpen(ctx context.Context, tx pgx.Tx, in *instance, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE process_timers SET status = 'cancelled', executed_at = $2
		WHERE instance_id = $1 AND status = 'pending'`, in.ID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE process_tasks SET status = 'cancelled', closed_at = $2
		WHERE instance_id = $1 AND status = 'open'`, in.ID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE process_task_submissions SET status = 'superseded', status_reason = 'task_cancelled'
		WHERE status = 'pending' AND task_id IN (SELECT id FROM process_tasks WHERE instance_id = $1 AND status = 'cancelled')`, in.ID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE process_waits SET status = 'cancelled', closed_at = $2
		WHERE instance_id = $1 AND status = 'open'`, in.ID, now)
	return err
}

// advance enters target and keeps going through automatic steps (decisions)
// until the instance waits for someone or something, or ends.
func (e *Engine) advance(ctx context.Context, tx pgx.Tx, in *instance, d *Definition, target, cause string, now time.Time) error {
	for i := 0; i < maxAutomaticSteps; i++ {
		step, ok := d.Steps[target]
		if !ok {
			return fmt.Errorf("unknown step %q", target)
		}
		if err := e.cancelOpen(ctx, tx, in, now); err != nil {
			return err
		}
		in.CurrentStep = target
		in.StepVisit++
		in.StateVersion++
		detail := map[string]any{"cause": cause, "visit": in.StepVisit, "type": step.Type}
		if step.BusinessStatus != "" {
			detail["business_status"] = step.BusinessStatus
		}
		if err := e.history(ctx, tx, in, "entered", target, "", "", "", detail, now); err != nil {
			return err
		}
		switch step.Type {
		case StepDecision:
			outcome := ""
			if in.LastOutcome != nil {
				outcome = *in.LastOutcome
			}
			next, ok := step.Routes[outcome]
			if !ok {
				return fmt.Errorf("decision %s has no route for outcome %q", target, outcome)
			}
			target, cause = next, "decision:"+outcome
			continue
		case StepHumanTask:
			due, cutoff, has, err := d.deadlineFor(step, now)
			if err != nil {
				return err
			}
			var taskID int64
			if err := tx.QueryRow(ctx, `INSERT INTO process_tasks (tenant_id, instance_id, step, visit, roles, outcomes, due_at, cutoff_at, status, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'open',$9) RETURNING id`,
				in.TenantID, in.ID, target, in.StepVisit, step.Roles, step.Outcomes, nullTime(due, has), nullTime(cutoff, has), now).Scan(&taskID); err != nil {
				return err
			}
			if has {
				if err := e.addTimer(ctx, tx, in, "task_deadline", taskID, due, cutoff, cutoff.Add(e.cfg.Settle), now); err != nil {
					return err
				}
			}
		case StepExternalCommand:
			if err := e.issueCommand(ctx, tx, in, target, step, now); err != nil {
				return err
			}
		case StepWaitEvent:
			if err := e.openWait(ctx, tx, in, d, target, step, now); err != nil {
				return err
			}
		case StepEnd:
			in.Status = "completed"
			result := step.Result
			in.Result = &result
			if err := e.history(ctx, tx, in, "completed", target, "", "", "", map[string]any{"result": result}, now); err != nil {
				return err
			}
		}
		return e.saveInstance(ctx, tx, in, now)
	}
	return errors.New("too many automatic steps")
}

func nullTime(t time.Time, ok bool) any {
	if !ok {
		return nil
	}
	return t
}

func (e *Engine) addTimer(ctx context.Context, tx pgx.Tx, in *instance, kind string, ref int64, due, cutoff, fire, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO process_timers (tenant_id, instance_id, kind, ref_id, due_at, cutoff_at, fire_at, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8)`, in.TenantID, in.ID, kind, ref, due, cutoff, fire, now)
	return err
}

// CommandKey derives the stable key of a command. It changes only with the
// generation, which grows only after a definitive outcome.
func CommandKey(tenant string, instanceID int64, step, businessRef string, generation int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{tenant, strconv.FormatInt(instanceID, 10), step, businessRef, strconv.Itoa(generation)}, "|")))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}

type command struct {
	ID             int64
	InstanceID     int64
	Step           string
	Key            string
	Type           string
	Generation     int
	Status         string
	RequestAttempt int
	StatusQueries  int
}

const commandColumns = `id, instance_id, step, command_key, command_type, generation, status, request_attempt, status_queries`

func scanCommand(row pgx.Row) (*command, error) {
	var c command
	err := row.Scan(&c.ID, &c.InstanceID, &c.Step, &c.Key, &c.Type, &c.Generation, &c.Status, &c.RequestAttempt, &c.StatusQueries)
	return &c, err
}

// issueCommand records the command and publishes the request in the caller's
// transaction. A command whose outcome is still requested or unknown is
// re-requested with the SAME key: the external system may already have applied
// it, and its receipt for that key prevents a second effect. Only a definitive
// outcome (confirmed or rejected) opens a new generation and a new key.
func (e *Engine) issueCommand(ctx context.Context, tx pgx.Tx, in *instance, stepName string, step Step, now time.Time) error {
	prev, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM process_commands
		WHERE instance_id = $1 AND step = $2 ORDER BY generation DESC LIMIT 1 FOR UPDATE`, in.ID, stepName))
	var cmd *command
	reused := false
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		cmd = &command{InstanceID: in.ID, Step: stepName, Type: step.CommandType, Generation: 1, RequestAttempt: 1}
	case err != nil:
		return err
	case prev.Status == "requested" || prev.Status == "unknown":
		cmd = prev
		cmd.RequestAttempt++
		reused = true
	default:
		cmd = &command{InstanceID: in.ID, Step: stepName, Type: step.CommandType, Generation: prev.Generation + 1, RequestAttempt: 1}
	}
	cmd.Status = "requested"
	if reused {
		if _, err := tx.Exec(ctx, `UPDATE process_commands SET status = 'requested', request_attempt = $2, status_queries = 0, updated_at = $3 WHERE id = $1`,
			cmd.ID, cmd.RequestAttempt, now); err != nil {
			return err
		}
	} else {
		cmd.Key = CommandKey(in.TenantID, in.ID, stepName, in.BusinessRef, cmd.Generation)
		if err := tx.QueryRow(ctx, `INSERT INTO process_commands (tenant_id, instance_id, step, command_key, command_type, business_ref, generation, status, request_attempt, requested_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'requested',1,$8,$8) RETURNING id`,
			in.TenantID, in.ID, stepName, cmd.Key, cmd.Type, in.BusinessRef, cmd.Generation, now).Scan(&cmd.ID); err != nil {
			return err
		}
	}
	payload := map[string]any{
		"command_key": cmd.Key, "command_type": cmd.Type, "instance_id": in.ID, "step": stepName,
		"business_ref": in.BusinessRef, "generation": cmd.Generation, "attempt": cmd.RequestAttempt,
	}
	if step.BusinessStatus != "" {
		payload["business_status"] = step.BusinessStatus
	}
	if err := e.publish(ctx, tx, in, "cmd:"+cmd.Key+":r"+strconv.Itoa(cmd.RequestAttempt), EventCommandRequested, payload, now); err != nil {
		return err
	}
	if e.testHookAfterCommand != nil {
		if err := e.testHookAfterCommand(); err != nil {
			return err
		}
	}
	timeout, err := calendar.ParseDuration(step.ReplyTimeout)
	if err != nil {
		return err
	}
	fire := now.Add(timeout)
	if err := e.addTimer(ctx, tx, in, "command_reply", cmd.ID, fire, fire, fire, now); err != nil {
		return err
	}
	return e.history(ctx, tx, in, "command_requested", stepName, "", "", "", map[string]any{
		"command_key": cmd.Key, "generation": cmd.Generation, "attempt": cmd.RequestAttempt, "same_key_reused": reused,
	}, now)
}

func randomKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (e *Engine) openWait(ctx context.Context, tx pgx.Tx, in *instance, d *Definition, stepName string, step Step, now time.Time) error {
	key, err := randomKey()
	if err != nil {
		return err
	}
	types := make([]string, 0, len(step.OnEvents))
	for t := range step.OnEvents {
		types = append(types, t)
	}
	sort.Strings(types)
	due, cutoff, has, err := d.deadlineFor(step, now)
	if err != nil {
		return err
	}
	var waitID int64
	if err := tx.QueryRow(ctx, `INSERT INTO process_waits (tenant_id, instance_id, step, visit, wait_key, event_types, business_ref, due_at, cutoff_at, status, opened_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'open',$10) RETURNING id`,
		in.TenantID, in.ID, stepName, in.StepVisit, key, types, in.BusinessRef, nullTime(due, has), nullTime(cutoff, has), now).Scan(&waitID); err != nil {
		return err
	}
	if has {
		if err := e.addTimer(ctx, tx, in, "wait_deadline", waitID, due, cutoff, cutoff.Add(e.cfg.Settle), now); err != nil {
			return err
		}
	}
	if err := e.publish(ctx, tx, in, "wait:"+key, EventWaitOpened, map[string]any{
		"wait_key": key, "instance_id": in.ID, "step": stepName, "business_ref": in.BusinessRef,
	}, now); err != nil {
		return err
	}
	return e.history(ctx, tx, in, "wait_opened", stepName, "", "", "", map[string]any{"wait_key": key, "event_types": types}, now)
}
