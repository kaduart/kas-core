// Package process is the KAS Process Studio engine: versioned process
// definitions per tenant and durable executions of them (human tasks with
// deadlines, decisions, commands to an external system confirmed by it, waits
// for external events and process-wide interrupts). It coordinates; the
// external system (CRM/ERP) decides whether an operation is valid and applies
// its effect. No tenant business rule lives here.
package process

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"kas-core-pilot/internal/calendar"
	"kas-core-pilot/internal/strictjson"
)

// SchemaV1 is the only definition schema accepted.
const SchemaV1 = "kas.process/v1"

const (
	StepHumanTask       = "human_task"
	StepDecision        = "decision"
	StepExternalCommand = "external_command"
	StepWaitEvent       = "wait_event"
	StepEnd             = "end"

	LateRecordOnly = "record_only"

	defaultGrace          = 5 * time.Minute
	maxDefinitionBytes    = 64 << 10
	maxSteps              = 100
	maxDeadline           = 366 * 24 * time.Hour
	maxGrace              = 24 * time.Hour
	maxReplyTimeout       = 7 * 24 * time.Hour
	maxStatusQueriesLimit = 10
)

var (
	ErrInvalidDefinition = errors.New("invalid process definition")

	namePattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	keyPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	eventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)*$`)
	codePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
)

// Deadline is either a plain ISO-8601 duration or a number of business days
// ending at a cutoff time in the definition's calendar.
type Deadline struct {
	Duration     string `json:"duration,omitempty"`
	BusinessDays int    `json:"business_days,omitempty"`
	Cutoff       string `json:"cutoff,omitempty"`
}

// Step is one node of a definition. Which fields apply depends on Type.
type Step struct {
	Type string `json:"type"`

	// human_task
	Roles        []string  `json:"roles,omitempty"`
	Outcomes     []string  `json:"outcomes,omitempty"`
	Next         string    `json:"next,omitempty"`
	Deadline     *Deadline `json:"deadline,omitempty"`
	Grace        string    `json:"grace,omitempty"`
	OnDeadline   string    `json:"on_deadline,omitempty"`
	LateResponse string    `json:"late_response,omitempty"`

	// decision: outcome of the last resolved task -> step
	Routes map[string]string `json:"routes,omitempty"`

	// external_command
	CommandType      string `json:"command_type,omitempty"`
	ReplyTimeout     string `json:"reply_timeout,omitempty"`
	MaxStatusQueries int    `json:"max_status_queries,omitempty"`
	OnConfirmed      string `json:"on_confirmed,omitempty"`
	OnRejected       string `json:"on_rejected,omitempty"`
	OnNoReply        string `json:"on_no_reply,omitempty"`
	// RequireResultRef makes a "confirmed" result count only when it carries
	// result_ref, the identifier of what the external system created (e.g. the
	// new package). A confirmation without it is refused and the command stays
	// requested, so the status queries go on.
	RequireResultRef bool `json:"require_result_ref,omitempty"`

	// wait_event: event type -> step (deadline/grace/on_deadline shared with human_task)
	OnEvents map[string]string `json:"on_events,omitempty"`

	// end
	Result string `json:"result,omitempty"`

	// Opaque code the external system interprets (e.g. a contract status).
	BusinessStatus string `json:"business_status,omitempty"`
}

// CalendarSpec declares the tenant calendar used by business-day deadlines.
type CalendarSpec struct {
	Zone     string   `json:"zone,omitempty"`
	Holidays []string `json:"holidays,omitempty"`
}

// Definition is the editor-independent JSON of a process. Layout belongs to the
// editor and is stored but never interpreted.
type Definition struct {
	Schema     string            `json:"schema"`
	ProcessKey string            `json:"process_key"`
	Start      string            `json:"start"`
	Calendar   *CalendarSpec     `json:"calendar,omitempty"`
	Steps      map[string]Step   `json:"steps"`
	Interrupts map[string]string `json:"interrupts,omitempty"`
	Layout     json.RawMessage   `json:"layout,omitempty"`

	cal *calendar.Calendar
}

// ParseDefinition decodes strictly (unknown fields and duplicate keys are
// rejected) and validates a definition. It returns the parsed definition, its
// canonical JSON and checksum.
func ParseDefinition(raw []byte) (*Definition, []byte, string, error) {
	if len(raw) == 0 || len(raw) > maxDefinitionBytes {
		return nil, nil, "", fmt.Errorf("%w: size", ErrInvalidDefinition)
	}
	if err := strictjson.RejectDuplicateKeys(raw); err != nil {
		return nil, nil, "", fmt.Errorf("%w: duplicate key", ErrInvalidDefinition)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Definition
	if err := dec.Decode(&d); err != nil {
		return nil, nil, "", fmt.Errorf("%w: %v", ErrInvalidDefinition, err)
	}
	if dec.More() {
		return nil, nil, "", fmt.Errorf("%w: trailing data", ErrInvalidDefinition)
	}
	if errs := d.Validate(); len(errs) > 0 {
		return nil, nil, "", fmt.Errorf("%w: %s", ErrInvalidDefinition, errs[0])
	}
	canonical, err := json.Marshal(&d)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: encode", ErrInvalidDefinition)
	}
	sum := sha256.Sum256(canonical)
	return &d, canonical, hex.EncodeToString(sum[:]), nil
}

// Validate returns every problem found (empty when valid). It also prepares the
// calendar used for deadlines.
func (d *Definition) Validate() []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if d.Schema != SchemaV1 {
		add("schema must be %q", SchemaV1)
	}
	if !keyPattern.MatchString(d.ProcessKey) {
		add("process_key must match %s", keyPattern)
	}
	if len(d.Steps) == 0 || len(d.Steps) > maxSteps {
		add("steps must hold 1..%d entries", maxSteps)
		return errs
	}
	zone, holidays := "", []string(nil)
	if d.Calendar != nil {
		zone, holidays = d.Calendar.Zone, d.Calendar.Holidays
	}
	cal, err := calendar.New(zone, holidays)
	if err != nil {
		add("calendar: %v", err)
	}
	d.cal = cal
	exists := func(name string) bool { _, ok := d.Steps[name]; return ok }
	ref := func(from, field, to string, required bool) {
		if to == "" {
			if required {
				add("step %s: %s is required", from, field)
			}
			return
		}
		if !exists(to) {
			add("step %s: %s points to unknown step %q", from, field, to)
		}
	}
	if !exists(d.Start) {
		add("start points to unknown step %q", d.Start)
	}
	names := make([]string, 0, len(d.Steps))
	for name := range d.Steps {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := d.Steps[name]
		if !namePattern.MatchString(name) {
			add("step name %q must match %s", name, namePattern)
		}
		if s.BusinessStatus != "" && !codePattern.MatchString(s.BusinessStatus) {
			add("step %s: business_status must match %s", name, codePattern)
		}
		if s.RequireResultRef && s.Type != StepExternalCommand {
			add("step %s: require_result_ref applies only to external_command", name)
		}
		switch s.Type {
		case StepHumanTask:
			if len(s.Roles) == 0 || len(s.Roles) > 10 {
				add("step %s: roles must hold 1..10 entries", name)
			}
			for _, r := range s.Roles {
				if !namePattern.MatchString(r) {
					add("step %s: role %q must match %s", name, r, namePattern)
				}
			}
			if len(s.Outcomes) == 0 || len(s.Outcomes) > 20 {
				add("step %s: outcomes must hold 1..20 entries", name)
			}
			seen := map[string]bool{}
			for _, o := range s.Outcomes {
				if !namePattern.MatchString(o) || seen[o] {
					add("step %s: outcome %q is invalid or repeated", name, o)
				}
				seen[o] = true
			}
			ref(name, "next", s.Next, true)
			if next, ok := d.Steps[s.Next]; ok && next.Type == StepDecision {
				for _, o := range s.Outcomes {
					if _, routed := next.Routes[o]; !routed {
						add("step %s: outcome %q has no route in decision %s", name, o, s.Next)
					}
				}
			}
			d.validateDeadline(name, s, add, ref)
		case StepDecision:
			if len(s.Routes) == 0 {
				add("step %s: routes are required", name)
			}
			for outcome, to := range s.Routes {
				if !namePattern.MatchString(outcome) {
					add("step %s: route key %q must match %s", name, outcome, namePattern)
				}
				ref(name, "routes."+outcome, to, true)
			}
		case StepExternalCommand:
			if !eventTypePattern.MatchString(s.CommandType) || len(s.CommandType) > 80 {
				add("step %s: command_type must match %s", name, eventTypePattern)
			}
			if t, err := calendar.ParseDuration(s.ReplyTimeout); err != nil || t > maxReplyTimeout {
				add("step %s: reply_timeout must be an ISO-8601 duration up to 7 days", name)
			}
			if s.MaxStatusQueries < 0 || s.MaxStatusQueries > maxStatusQueriesLimit {
				add("step %s: max_status_queries must be 0..%d", name, maxStatusQueriesLimit)
			}
			ref(name, "on_confirmed", s.OnConfirmed, true)
			ref(name, "on_rejected", s.OnRejected, true)
			ref(name, "on_no_reply", s.OnNoReply, true)
		case StepWaitEvent:
			if len(s.OnEvents) == 0 || len(s.OnEvents) > 10 {
				add("step %s: on_events must hold 1..10 entries", name)
			}
			for et, to := range s.OnEvents {
				if !eventTypePattern.MatchString(et) {
					add("step %s: event type %q must match %s", name, et, eventTypePattern)
				}
				ref(name, "on_events."+et, to, true)
			}
			d.validateDeadline(name, s, add, ref)
		case StepEnd:
			if !namePattern.MatchString(s.Result) {
				add("step %s: result must match %s", name, namePattern)
			}
		default:
			add("step %s: unknown type %q", name, s.Type)
		}
	}
	for et, to := range d.Interrupts {
		if !eventTypePattern.MatchString(et) {
			add("interrupt %q must match %s", et, eventTypePattern)
		}
		if !exists(to) {
			add("interrupt %s points to unknown step %q", et, to)
		}
	}
	if len(errs) == 0 {
		d.validateReachability(add)
	}
	return errs
}

func (d *Definition) validateDeadline(name string, s Step, add func(string, ...any), ref func(string, string, string, bool)) {
	if s.Deadline == nil {
		if s.OnDeadline != "" || s.Grace != "" {
			add("step %s: on_deadline and grace need a deadline", name)
		}
		return
	}
	dl := s.Deadline
	switch {
	case dl.Duration != "" && dl.BusinessDays == 0 && dl.Cutoff == "":
		if v, err := calendar.ParseDuration(dl.Duration); err != nil || v > maxDeadline {
			add("step %s: deadline.duration must be an ISO-8601 duration up to 366 days", name)
		}
	case dl.Duration == "" && dl.BusinessDays > 0:
		if dl.BusinessDays > 366 {
			add("step %s: deadline.business_days must be 1..366", name)
		}
		if dl.Cutoff == "" {
			add("step %s: deadline.cutoff (HH:MM) is required with business_days", name)
		}
	default:
		add("step %s: deadline needs either duration or business_days+cutoff", name)
	}
	if s.Grace != "" {
		if g, err := calendar.ParseDuration(s.Grace); err != nil || g > maxGrace {
			add("step %s: grace must be an ISO-8601 duration up to 1 day", name)
		}
	}
	if s.LateResponse != "" && s.LateResponse != LateRecordOnly {
		add("step %s: late_response supports only %q in this version", name, LateRecordOnly)
	}
	ref(name, "on_deadline", s.OnDeadline, true)
}

func (d *Definition) successors(s Step) []string {
	var out []string
	add := func(v string) {
		if v != "" {
			out = append(out, v)
		}
	}
	add(s.Next)
	add(s.OnDeadline)
	add(s.OnConfirmed)
	add(s.OnRejected)
	add(s.OnNoReply)
	for _, v := range s.Routes {
		add(v)
	}
	for _, v := range s.OnEvents {
		add(v)
	}
	return out
}

func (d *Definition) validateReachability(add func(string, ...any)) {
	seen := map[string]bool{}
	queue := []string{d.Start}
	for _, to := range d.Interrupts {
		queue = append(queue, to)
	}
	endReachable := false
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		s := d.Steps[name]
		if s.Type == StepEnd {
			endReachable = true
		}
		queue = append(queue, d.successors(s)...)
	}
	if !endReachable {
		add("no end step is reachable")
	}
	for name := range d.Steps {
		if !seen[name] {
			add("step %s is unreachable", name)
		}
	}
}

// deadlineFor computes due_at and cutoff_at for a step entered at now.
func (d *Definition) deadlineFor(s Step, now time.Time) (due, cutoff time.Time, ok bool, err error) {
	if s.Deadline == nil {
		return time.Time{}, time.Time{}, false, nil
	}
	if s.Deadline.Duration != "" {
		v, err := calendar.ParseDuration(s.Deadline.Duration)
		if err != nil {
			return time.Time{}, time.Time{}, false, err
		}
		due = now.Add(v)
	} else {
		cal := d.cal
		if cal == nil {
			if cal, err = calendar.New("", nil); err != nil {
				return time.Time{}, time.Time{}, false, err
			}
		}
		if due, err = cal.AddBusinessDays(now, s.Deadline.BusinessDays, s.Deadline.Cutoff); err != nil {
			return time.Time{}, time.Time{}, false, err
		}
	}
	grace := defaultGrace
	if s.Grace != "" {
		if grace, err = calendar.ParseDuration(s.Grace); err != nil {
			return time.Time{}, time.Time{}, false, err
		}
	}
	return due.UTC(), due.Add(grace).UTC(), true, nil
}
