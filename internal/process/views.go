package process

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// HistoryEntry is one immutable fact of an instance.
type HistoryEntry struct {
	ID         int64           `json:"id"`
	RecordedAt time.Time       `json:"recorded_at"`
	Kind       string          `json:"kind"`
	Step       string          `json:"step,omitempty"`
	ServiceID  string          `json:"service_id,omitempty"`
	ActorID    string          `json:"actor_id,omitempty"`
	Role       string          `json:"role,omitempty"`
	Detail     json.RawMessage `json:"detail"`
}

// TaskView is a task as shown to a person or a service.
type TaskView struct {
	ID         int64      `json:"id"`
	InstanceID int64      `json:"instance_id"`
	Step       string     `json:"step"`
	Roles      []string   `json:"roles"`
	Outcomes   []string   `json:"outcomes"`
	DueAt      *time.Time `json:"due_at,omitempty"`
	CutoffAt   *time.Time `json:"cutoff_at,omitempty"`
	Status     string     `json:"status"`
	Outcome    *string    `json:"outcome,omitempty"`
}

// CommandView is a command and its current knowledge.
type CommandView struct {
	CommandKey    string `json:"command_key"`
	Step          string `json:"step"`
	CommandType   string `json:"command_type"`
	Generation    int    `json:"generation"`
	Status        string `json:"status"`
	Attempt       int    `json:"attempt"`
	StatusQueries int    `json:"status_queries"`
	ResultRef     string `json:"result_ref,omitempty"`
}

// InstanceView is an instance with its tasks, commands and history.
type InstanceView struct {
	ID           int64          `json:"id"`
	ProcessKey   string         `json:"process_key"`
	Version      int            `json:"version"`
	BusinessRef  string         `json:"business_ref"`
	Status       string         `json:"status"`
	CurrentStep  string         `json:"current_step"`
	StateVersion int            `json:"state_version"`
	Result       *string        `json:"result,omitempty"`
	Tasks        []TaskView     `json:"tasks"`
	Commands     []CommandView  `json:"commands"`
	History      []HistoryEntry `json:"history"`
}

// GetInstance reads one instance of the tenant.
func (e *Engine) GetInstance(ctx context.Context, tenant string, id int64) (InstanceView, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	in, err := scanInstance(e.pool.QueryRow(ctx, `SELECT `+instanceColumns+` FROM process_instances WHERE tenant_id = $1 AND id = $2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return InstanceView{}, ErrNotFound
	}
	if err != nil {
		return InstanceView{}, ErrUnavailable
	}
	v := InstanceView{ID: in.ID, ProcessKey: in.ProcessKey, Version: in.Version, BusinessRef: in.BusinessRef, Status: in.Status,
		CurrentStep: in.CurrentStep, StateVersion: in.StateVersion, Result: in.Result}
	if v.Tasks, err = e.tasks(ctx, `tenant_id = $1 AND instance_id = $2`, tenant, id); err != nil {
		return InstanceView{}, ErrUnavailable
	}
	rows, err := e.pool.Query(ctx, `SELECT command_key, step, command_type, generation, status, request_attempt, status_queries, COALESCE(result_ref, '')
		FROM process_commands WHERE tenant_id = $1 AND instance_id = $2 ORDER BY id`, tenant, id)
	if err != nil {
		return InstanceView{}, ErrUnavailable
	}
	for rows.Next() {
		var c CommandView
		if err := rows.Scan(&c.CommandKey, &c.Step, &c.CommandType, &c.Generation, &c.Status, &c.Attempt, &c.StatusQueries, &c.ResultRef); err != nil {
			rows.Close()
			return InstanceView{}, ErrUnavailable
		}
		v.Commands = append(v.Commands, c)
	}
	rows.Close()
	rows, err = e.pool.Query(ctx, `SELECT id, recorded_at, kind, COALESCE(step,''), COALESCE(service_id,''), COALESCE(actor_id,''), COALESCE(role,''), detail
		FROM process_history WHERE tenant_id = $1 AND instance_id = $2 ORDER BY id`, tenant, id)
	if err != nil {
		return InstanceView{}, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.ID, &h.RecordedAt, &h.Kind, &h.Step, &h.ServiceID, &h.ActorID, &h.Role, &h.Detail); err != nil {
			return InstanceView{}, ErrUnavailable
		}
		v.History = append(v.History, h)
	}
	if rows.Err() != nil {
		return InstanceView{}, ErrUnavailable
	}
	return v, nil
}

// ListTasks lists tasks of the tenant, optionally by role and status (at most 200).
func (e *Engine) ListTasks(ctx context.Context, tenant, role, status string) ([]TaskView, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	out, err := e.tasks(ctx, `tenant_id = $1 AND ($2 = '' OR $2 = ANY(roles)) AND ($3 = '' OR status = $3)`, tenant, role, status)
	if err != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

func (e *Engine) tasks(ctx context.Context, where string, args ...any) ([]TaskView, error) {
	rows, err := e.pool.Query(ctx, `SELECT id, instance_id, step, roles, outcomes, due_at, cutoff_at, status, outcome
		FROM process_tasks WHERE `+where+` ORDER BY id LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskView{}
	for rows.Next() {
		var t TaskView
		if err := rows.Scan(&t.ID, &t.InstanceID, &t.Step, &t.Roles, &t.Outcomes, &t.DueAt, &t.CutoffAt, &t.Status, &t.Outcome); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DefinitionSummary is the latest published version of one process.
type DefinitionSummary struct {
	ProcessKey    string    `json:"process_key"`
	LatestVersion int       `json:"latest_version"`
	PublishedAt   time.Time `json:"published_at"`
}

// DefinitionView is one published version, as stored (canonical JSON).
type DefinitionView struct {
	ProcessKey  string          `json:"process_key"`
	Version     int             `json:"version"`
	Checksum    string          `json:"checksum"`
	PublishedBy string          `json:"published_by"`
	PublishedAt time.Time       `json:"published_at"`
	Versions    []int           `json:"versions"`
	Definition  json.RawMessage `json:"definition"`
}

// InstanceSummary is an instance without tasks, commands or history.
type InstanceSummary struct {
	ID          int64     `json:"id"`
	ProcessKey  string    `json:"process_key"`
	Version     int       `json:"version"`
	BusinessRef string    `json:"business_ref"`
	Status      string    `json:"status"`
	CurrentStep string    `json:"current_step"`
	Result      *string   `json:"result,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ListDefinitions lists the processes of the tenant with their latest version.
func (e *Engine) ListDefinitions(ctx context.Context, tenant string) ([]DefinitionSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := e.pool.Query(ctx, `SELECT DISTINCT ON (process_key) process_key, version, published_at
		FROM process_definitions WHERE tenant_id = $1 ORDER BY process_key, version DESC`, tenant)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []DefinitionSummary{}
	for rows.Next() {
		var d DefinitionSummary
		if err := rows.Scan(&d.ProcessKey, &d.LatestVersion, &d.PublishedAt); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, d)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

// GetDefinition reads one version of a process of the tenant (0 = latest),
// with the list of all its versions.
func (e *Engine) GetDefinition(ctx context.Context, tenant, processKey string, version int) (DefinitionView, error) {
	if !keyPattern.MatchString(processKey) || version < 0 {
		return DefinitionView{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var v DefinitionView
	var raw []byte
	err := e.pool.QueryRow(ctx, `SELECT process_key, version, checksum, published_by, published_at, definition::text
		FROM process_definitions WHERE tenant_id = $1 AND process_key = $2 AND ($3 = 0 OR version = $3)
		ORDER BY version DESC LIMIT 1`, tenant, processKey, version).
		Scan(&v.ProcessKey, &v.Version, &v.Checksum, &v.PublishedBy, &v.PublishedAt, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefinitionView{}, ErrNotFound
	}
	if err != nil {
		return DefinitionView{}, ErrUnavailable
	}
	v.Definition = raw
	rows, err := e.pool.Query(ctx, `SELECT version FROM process_definitions WHERE tenant_id = $1 AND process_key = $2 ORDER BY version`, tenant, processKey)
	if err != nil {
		return DefinitionView{}, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return DefinitionView{}, ErrUnavailable
		}
		v.Versions = append(v.Versions, n)
	}
	if rows.Err() != nil {
		return DefinitionView{}, ErrUnavailable
	}
	return v, nil
}

// ListInstances lists the newest instances of the tenant (at most 200),
// optionally by process and status.
func (e *Engine) ListInstances(ctx context.Context, tenant, processKey, status string) ([]InstanceSummary, error) {
	if (processKey != "" && !keyPattern.MatchString(processKey)) || (status != "" && status != "running" && status != "completed") {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := e.pool.Query(ctx, `SELECT id, process_key, version, business_ref, status, current_step, result, started_at, updated_at
		FROM process_instances WHERE tenant_id = $1 AND ($2 = '' OR process_key = $2) AND ($3 = '' OR status = $3)
		ORDER BY id DESC LIMIT 200`, tenant, processKey, status)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []InstanceSummary{}
	for rows.Next() {
		var s InstanceSummary
		if err := rows.Scan(&s.ID, &s.ProcessKey, &s.Version, &s.BusinessRef, &s.Status, &s.CurrentStep, &s.Result, &s.StartedAt, &s.UpdatedAt); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, s)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}
