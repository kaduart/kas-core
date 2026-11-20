// Package crmsim is a simulated CRM for the Process Studio pilot. It talks to
// KAS only through the public HTTP contract: it claims deliveries with the
// real pull API, applies the "effect" of each command once (effect and
// receipt saved together), reports the result and only then acks.
//
// It exists for isolated tests and demos with fictitious tenants. It refuses
// any KAS address that is not loopback.
package crmsim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	eventCommandRequested   = "process.command.requested"
	eventCommandStatusQuery = "process.command.status_query"
	eventWaitOpened         = "process.wait.opened"

	// CommandCreateRenewal is the renewal pilot's command: a confirmed one
	// creates exactly one new package per command_key, and the result reports
	// that package id as result_ref.
	CommandCreateRenewal = "crm.package.create_renewal"
)

// ErrCrashAfterEffect is returned by Step when CrashAfterEffect is set and an
// effect was just saved: the caller should exit as a crashed CRM would,
// before reporting the result and before the ack.
var ErrCrashAfterEffect = errors.New("simulated crash after effect, before result and ack")

// Config of the simulator.
type Config struct {
	BaseURL    string // KAS base URL; must be loopback
	ServiceID  string
	Token      string
	ConsumerID string
	StatePath  string // JSON file with receipts and effects; empty keeps them in memory
	RejectRefs map[string]bool
	MuteRefs   map[string]bool // effect applied but no result reported
	// DropFirstResultRefs loses the FIRST result report of each command of
	// these business_refs, after the effect and the receipt were saved (the
	// delivery is acked as if the report had been sent). Deterministic fault
	// injection: KAS gets no answer, its status query arrives with the same
	// command_key, and the CRM then reports from the receipt (same package).
	// An entry ending in "*" matches every business_ref with that prefix.
	DropFirstResultRefs map[string]bool
	// CrashAfterEffect makes Step stop right after saving the first effect.
	CrashAfterEffect bool
	LeaseSeconds     int
	Client           *http.Client
	Logger           *slog.Logger
	Now              func() time.Time
}

// State is what the CRM keeps durably: one receipt per command key, how many
// times each effect ran, and the wait keys it was told about.
type State struct {
	Receipts map[string]string `json:"receipts"`
	Effects  map[string]int    `json:"effects"`
	Waits    map[string]string `json:"waits"`
	// Packages maps a command_key to the package it created (renewal pilot).
	Packages map[string]string `json:"packages,omitempty"`
	// Dropped marks the command keys whose first result report was lost.
	Dropped map[string]bool `json:"dropped,omitempty"`
}

func (st *State) init() {
	if st.Receipts == nil {
		st.Receipts = map[string]string{}
	}
	if st.Effects == nil {
		st.Effects = map[string]int{}
	}
	if st.Waits == nil {
		st.Waits = map[string]string{}
	}
	if st.Packages == nil {
		st.Packages = map[string]string{}
	}
	if st.Dropped == nil {
		st.Dropped = map[string]bool{}
	}
}

// Sim is a simulated CRM.
type Sim struct {
	cfg   Config
	mu    sync.Mutex
	state State
}

// New checks the configuration and loads the saved state, if any.
func New(cfg Config) (*Sim, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("crmsim: invalid KAS base URL")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("crmsim: KAS address must be loopback (simulator is for isolated tests only)")
	}
	if cfg.ServiceID == "" || cfg.Token == "" || cfg.ConsumerID == "" {
		return nil, errors.New("crmsim: service, token and consumer are required")
	}
	if cfg.LeaseSeconds == 0 {
		cfg.LeaseSeconds = 10
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	s := &Sim{cfg: cfg}
	s.state.init()
	if cfg.StatePath != "" {
		raw, err := os.ReadFile(cfg.StatePath)
		switch {
		case err == nil:
			if err := json.Unmarshal(raw, &s.state); err != nil {
				return nil, fmt.Errorf("crmsim: state file: %w", err)
			}
			s.state.init()
		case !errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("crmsim: state file: %w", err)
		}
	}
	return s, nil
}

// Snapshot returns a copy of the state.
func (s *Sim) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out State
	out.init()
	for k, v := range s.state.Packages {
		out.Packages[k] = v
	}
	for k, v := range s.state.Dropped {
		out.Dropped[k] = v
	}
	for k, v := range s.state.Receipts {
		out.Receipts[k] = v
	}
	for k, v := range s.state.Effects {
		out.Effects[k] = v
	}
	for k, v := range s.state.Waits {
		out.Waits[k] = v
	}
	return out
}

// saveLocked writes the state atomically (temp file + rename).
func (s *Sim) saveLocked() error {
	if s.cfg.StatePath == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.cfg.StatePath), ".crmsim-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.cfg.StatePath)
}

type claimed struct {
	DeliveryID int64  `json:"delivery_id"`
	Attempt    int    `json:"attempt"`
	LeaseToken string `json:"lease_token"`
	Event      struct {
		EventID   string          `json:"event_id"`
		TenantID  string          `json:"tenant_id"`
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
	} `json:"event"`
}

// HTTPError is a non-2xx answer from KAS.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("kas answered %d: %s", e.Status, e.Body) }

func (s *Sim) call(ctx context.Context, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KAS-Service", s.cfg.ServiceID)
	req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &HTTPError{Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Step claims one batch and handles it. It returns how many deliveries were
// acked. With CrashAfterEffect it returns ErrCrashAfterEffect right after the
// first effect is saved, leaving the delivery un-acked.
func (s *Sim) Step(ctx context.Context) (int, error) {
	var res struct {
		Deliveries []claimed `json:"deliveries"`
	}
	if err := s.call(ctx, "/internal/v1/events/deliveries/claim", map[string]any{
		"contract_version": "v1", "consumer_id": s.cfg.ConsumerID, "max_deliveries": 10, "lease_seconds": s.cfg.LeaseSeconds,
	}, &res); err != nil {
		return 0, err
	}
	acked := 0
	for _, d := range res.Deliveries {
		if err := s.handle(ctx, d); err != nil {
			return acked, err
		}
		if err := s.call(ctx, "/internal/v1/events/deliveries/ack", map[string]any{
			"contract_version": "v1", "consumer_id": s.cfg.ConsumerID, "delivery_id": d.DeliveryID,
			"attempt": d.Attempt, "lease_token": d.LeaseToken,
		}, nil); err != nil {
			return acked, fmt.Errorf("ack %s: %w", d.Event.EventID, err)
		}
		s.cfg.Logger.Info("delivery_acked", "event_id", d.Event.EventID, "tenant_id", d.Event.TenantID)
		acked++
	}
	return acked, nil
}

// Drain steps until nothing is left to claim.
func (s *Sim) Drain(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := s.Step(ctx)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
}

// Run steps every interval until ctx ends. Transient errors are logged.
func (s *Sim) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.Drain(ctx); err != nil {
			if errors.Is(err, ErrCrashAfterEffect) {
				return err
			}
			s.cfg.Logger.Warn("crm_sim_step_failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (s *Sim) handle(ctx context.Context, d claimed) error {
	var p map[string]any
	if err := json.Unmarshal(d.Event.Payload, &p); err != nil {
		return fmt.Errorf("payload of %s: %w", d.Event.EventID, err)
	}
	ref, _ := p["business_ref"].(string)
	switch d.Event.EventType {
	case eventCommandRequested, eventCommandStatusQuery:
		key, _ := p["command_key"].(string)
		commandType, _ := p["command_type"].(string)
		if key == "" {
			return fmt.Errorf("event %s has no command_key", d.Event.EventID)
		}
		s.mu.Lock()
		result, done := s.state.Receipts[key]
		applied := false
		if !done && d.Event.EventType == eventCommandRequested {
			result = "confirmed"
			if s.cfg.RejectRefs[ref] {
				result = "rejected"
			}
			// Effect and receipt are one transaction: both or neither. The
			// receipt is keyed by command_key, so a command creates at most
			// one package, however often it is requested or queried.
			s.state.Effects[key]++
			s.state.Receipts[key] = result
			if result == "confirmed" && commandType == CommandCreateRenewal {
				s.state.Packages[key] = fmt.Sprintf("pacote-novo-%d", len(s.state.Packages)+1)
			}
			if err := s.saveLocked(); err != nil {
				s.state.Effects[key]--
				delete(s.state.Receipts, key)
				delete(s.state.Packages, key)
				s.mu.Unlock()
				return fmt.Errorf("save state: %w", err)
			}
			done, applied = true, true
		}
		pkg := s.state.Packages[key]
		drop := done && !s.cfg.MuteRefs[ref] && matchRef(s.cfg.DropFirstResultRefs, ref) && !s.state.Dropped[key]
		if drop {
			s.state.Dropped[key] = true
			if err := s.saveLocked(); err != nil {
				delete(s.state.Dropped, key)
				s.mu.Unlock()
				return fmt.Errorf("save state: %w", err)
			}
		}
		s.mu.Unlock()
		if applied {
			s.cfg.Logger.Info("effect_applied", "tenant_id", d.Event.TenantID, "command_key", key, "result", result, "package_id", pkg)
			if s.cfg.CrashAfterEffect {
				return ErrCrashAfterEffect
			}
		}
		if drop {
			s.cfg.Logger.Warn("result_dropped", "tenant_id", d.Event.TenantID, "command_key", key, "result", result, "package_id", pkg)
			return nil // the confirmation is lost on the way: ack only
		}
		if !done || s.cfg.MuteRefs[ref] {
			return nil // nothing to report (never applied, or muted): ack only
		}
		body := map[string]any{"contract_version": "v1", "tenant_id": d.Event.TenantID, "status": result,
			"occurred_at": s.cfg.Now().Format(time.RFC3339Nano)}
		if result == "rejected" {
			body["reason_code"] = "demo_rejected"
		}
		if pkg != "" {
			body["result_ref"] = pkg
		}
		if err := s.call(ctx, "/internal/v1/process/commands/"+url.PathEscape(key)+"/result", body, nil); err != nil {
			return fmt.Errorf("report %s: %w", key, err)
		}
		s.cfg.Logger.Info("result_reported", "tenant_id", d.Event.TenantID, "command_key", key, "result", result, "package_id", pkg,
			"event_type", d.Event.EventType)
	case eventWaitOpened:
		wk, _ := p["wait_key"].(string)
		s.mu.Lock()
		s.state.Waits[ref] = wk
		err := s.saveLocked()
		s.mu.Unlock()
		if err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}
	return nil
}

// matchRef reports whether ref is in set, either exactly or by an entry that
// ends in "*" and is a prefix of ref.
func matchRef(set map[string]bool, ref string) bool {
	if set[ref] {
		return true
	}
	for k := range set {
		if strings.HasSuffix(k, "*") && strings.HasPrefix(ref, strings.TrimSuffix(k, "*")) {
			return true
		}
	}
	return false
}

// Keys lists the command keys with a receipt, sorted.
func (st State) Keys() []string {
	out := make([]string, 0, len(st.Receipts))
	for k := range st.Receipts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
