package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/pgtest"
)

// Fictitious tenants, actors and contracts only.
const (
	tenantA  = "demo-empresa-a"
	tenantB  = "demo-empresa-b"
	crmA     = "demo-crm-a"
	consumer = "crm-simulado"
)

var loc, _ = time.LoadLocation("America/Sao_Paulo")

// base is Friday 2026-11-13 10:00 in São Paulo; every test clock starts here,
// in the past, so the real-time outbox claim always sees the deliveries.
var base = time.Date(2026, 11, 13, 10, 0, 0, 0, loc)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t.UTC() }
func (c *fakeClock) Set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type allConsumers struct{}

func (allConsumers) ConsumersFor(string, string) []string { return []string{consumer} }

type env struct {
	t      *testing.T
	dsn    string
	clock  *fakeClock
	eng    *Engine
	outbox *events.PostgresStore
	crm    *crmSim
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := pgtest.SchemaDSN(t)
	ctx := context.Background()
	outbox, err := events.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("events store: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	e := &env{t: t, dsn: dsn, clock: &fakeClock{t: base}, outbox: outbox}
	e.eng = e.open()
	e.crm = &crmSim{env: e, receipts: map[string]string{}, effects: map[string]int{}, decide: map[string]string{}, waits: map[string]string{}, mute: map[string]bool{}}
	return e
}

// open starts an engine on the env's database, as a restarted process would.
func (e *env) open() *Engine {
	e.t.Helper()
	eng, err := Open(context.Background(), e.dsn, true, Config{Registry: allConsumers{}, Now: e.clock.Now})
	if err != nil {
		e.t.Fatalf("open engine: %v", err)
	}
	e.t.Cleanup(eng.Close)
	return eng
}

func (e *env) restart() {
	e.eng.Close()
	e.eng = e.open()
}

func retentionDef(deadline string) string {
	return retentionDefFor(deadline, "comercial", "supervisor", "crm.contract.cancel")
}

func retentionDefFor(deadline, taskRole, reviewRole, commandType string) string {
	return `{
  "schema": "kas.process/v1",
  "process_key": "retencao-cancelamento",
  "start": "negociar",
  "calendar": {"zone": "America/Sao_Paulo", "holidays": ["2026-11-17"]},
  "steps": {
    "negociar": {"type": "human_task", "roles": ["` + taskRole + `"], "outcomes": ["cliente_permanece", "cliente_cancela"],
      "deadline": ` + deadline + `, "grace": "PT5M", "on_deadline": "revisar", "late_response": "record_only", "next": "resultado"},
    "resultado": {"type": "decision", "routes": {"cliente_permanece": "fim_retido", "cliente_cancela": "pedir_cancelamento"}},
    "pedir_cancelamento": {"type": "external_command", "command_type": "` + commandType + `", "reply_timeout": "PT1H",
      "max_status_queries": 2, "on_confirmed": "fim_cancelado", "on_rejected": "revisar", "on_no_reply": "revisar", "business_status": "910"},
    "revisar": {"type": "human_task", "roles": ["` + reviewRole + `"], "outcomes": ["cliente_permanece", "cliente_cancela"], "next": "resultado"},
    "fim_retido": {"type": "end", "result": "retido", "business_status": "220"},
    "fim_cancelado": {"type": "end", "result": "cancelado"}
  },
  "interrupts": {"regulador.cancelamento": "fim_regulador"},
  "layout": {"nodes": []}
}`
}

// The regulator end must exist for the interrupt; add it to the base definition.
func withRegulator(def string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(def), &m); err != nil {
		panic(err)
	}
	m["steps"].(map[string]any)["fim_regulador"] = map[string]any{"type": "end", "result": "cancelado_regulador", "business_status": "801"}
	out, _ := json.Marshal(m)
	return string(out)
}

const waitDef = `{
  "schema": "kas.process/v1",
  "process_key": "confirmar-desistencia",
  "start": "aguardar_cliente",
  "steps": {
    "aguardar_cliente": {"type": "wait_event", "on_events": {"cliente.desistencia_confirmada": "fim_mantido", "cliente.desistencia_recusada": "fim_cancelado"},
      "deadline": {"duration": "P2D"}, "on_deadline": "fim_expirado"},
    "fim_mantido": {"type": "end", "result": "mantido"},
    "fim_cancelado": {"type": "end", "result": "cancelado"},
    "fim_expirado": {"type": "end", "result": "expirado", "business_status": "305"},
    "fim_regulador": {"type": "end", "result": "cancelado_regulador", "business_status": "801"}
  },
  "interrupts": {"regulador.cancelamento": "fim_regulador"}
}`

func (e *env) publish(tenant, def string) int {
	e.t.Helper()
	v, _, err := e.eng.PublishDefinition(context.Background(), tenant, crmA, fmt.Sprintf("pub-%d", time.Now().UnixNano()), []byte(def))
	if err != nil {
		e.t.Fatalf("publish: %v", err)
	}
	return v
}

func (e *env) roles(tenant, role string, actors ...string) {
	e.t.Helper()
	if _, _, err := e.eng.SetRoleMembers(context.Background(), tenant, crmA, role, fmt.Sprintf("chg-%d", time.Now().UnixNano()), actors, base.Add(-24*time.Hour)); err != nil {
		e.t.Fatalf("roles: %v", err)
	}
}

// setupRetention publishes the pilot process for a tenant with its deadline.
func (e *env) setupRetention(tenant, deadline string) {
	e.publish(tenant, withRegulator(retentionDef(deadline)))
	e.roles(tenant, "comercial", "ator-comercial-1", "ator-comercial-2")
	e.roles(tenant, "supervisor", "ator-supervisor-1")
}

var refSeq int

func nextRef() string { refSeq++; return "contrato-" + strconv.Itoa(refSeq) }

func (e *env) start(tenant, key string) int64 {
	e.t.Helper()
	return e.startRef(tenant, key, nextRef())
}

func (e *env) startRef(tenant, key, ref string) int64 {
	e.t.Helper()
	id, _, err := e.eng.StartInstance(context.Background(), StartRequest{TenantID: tenant, ServiceID: crmA, ProcessKey: key,
		BusinessRef: ref, StartKey: "start-" + ref, ActorID: "ator-cliente", OccurredAt: e.clock.Now()})
	if err != nil {
		e.t.Fatalf("start: %v", err)
	}
	return id
}

func (e *env) inst(tenant string, id int64) InstanceView {
	e.t.Helper()
	v, err := e.eng.GetInstance(context.Background(), tenant, id)
	if err != nil {
		e.t.Fatalf("get instance: %v", err)
	}
	return v
}

func (e *env) openTask(tenant string, id int64) TaskView {
	e.t.Helper()
	for _, t := range e.inst(tenant, id).Tasks {
		if t.Status == "open" {
			return t
		}
	}
	e.t.Fatalf("instance %d has no open task", id)
	return TaskView{}
}

var subSeq int

func (e *env) submit(tenant string, taskID int64, outcome, actor, role string, occurred time.Time) (SubmitResult, error) {
	subSeq++
	return e.eng.SubmitTask(context.Background(), SubmitRequest{TenantID: tenant, ServiceID: crmA, TaskID: taskID, Outcome: outcome,
		ActorID: actor, Role: role, OccurredAt: occurred, SubmissionKey: "sub-" + strconv.Itoa(subSeq)})
}

func (e *env) timers() int {
	e.t.Helper()
	n, err := e.eng.RunDueTimers(context.Background(), 100)
	if err != nil {
		e.t.Fatalf("timers: %v", err)
	}
	return n
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.eng.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *env) historyKinds(tenant string, id int64) map[string]int {
	out := map[string]int{}
	for _, h := range e.inst(tenant, id).History {
		out[h.Kind]++
	}
	return out
}

func expect(t *testing.T, got, want any, what string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func expectErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

// crmSim is the simulated CRM: it pulls the real outbox (claim/ack), applies an
// effect and records a receipt per command_key atomically (a map guarded by a
// mutex stands in for a Mongo transaction), reports the result and only then
// acks. decide sets the answer per business_ref: "confirmed" (default) or
// "rejected"; mute makes the CRM apply the effect but lose every answer.
type crmSim struct {
	env      *env
	mu       sync.Mutex
	receipts map[string]string // command_key -> result
	effects  map[string]int    // command_key -> times the effect was applied
	decide   map[string]string // business_ref -> behaviour
	waits    map[string]string // business_ref -> last wait_key received
	mute     map[string]bool   // business_ref -> answers are lost
	now      time.Time         // CRM clock for leases; zero = KAS clock + 1s
}

type pulled struct {
	d     events.Delivery
	token string
}

func (c *crmSim) clock() time.Time {
	if c.now.IsZero() {
		return c.env.clock.Now().Add(time.Second)
	}
	return c.now
}

func (c *crmSim) claim() []pulled {
	c.env.t.Helper()
	token := fmt.Sprintf("%032x", time.Now().UnixNano())
	ds, err := c.env.outbox.ClaimFor(context.Background(), token, consumer, []string{tenantA, tenantB},
		[]string{EventCommandRequested, EventCommandStatusQuery, EventWaitOpened}, c.clock(), 60*time.Second, 5, 50)
	if err != nil {
		c.env.t.Fatalf("claim: %v", err)
	}
	out := make([]pulled, 0, len(ds))
	for _, d := range ds {
		out = append(out, pulled{d: d, token: token})
	}
	return out
}

type crash int

const (
	noCrash crash = iota
	crashBeforeEffect
	crashAfterEffect
)

// handle processes one delivery; with a crash it stops at that point (no ack).
func (c *crmSim) handle(p pulled, at crash) {
	c.env.t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(p.d.Event.Payload, &payload); err != nil {
		c.env.t.Fatal(err)
	}
	ref, _ := payload["business_ref"].(string)
	switch p.d.Event.EventType {
	case EventCommandRequested, EventCommandStatusQuery:
		key := payload["command_key"].(string)
		if at == crashBeforeEffect {
			return
		}
		c.mu.Lock()
		result, done := c.receipts[key]
		muted := c.mute[ref]
		if !done && p.d.Event.EventType == EventCommandRequested {
			result = c.decide[ref]
			if result == "" {
				result = "confirmed"
			}
			c.effects[key]++ // effect and receipt in one "transaction"
			c.receipts[key] = result
			done = true
		}
		c.mu.Unlock()
		if at == crashAfterEffect {
			return
		}
		if done && !muted {
			if _, err := c.env.eng.RecordCommandResult(context.Background(), CommandResult{TenantID: p.d.Event.TenantID, ServiceID: crmA,
				CommandKey: key, Status: result, OccurredAt: c.env.clock.Now()}); err != nil {
				c.env.t.Fatalf("record result: %v", err)
			}
		}
	case EventWaitOpened:
		c.mu.Lock()
		c.waits[ref] = payload["wait_key"].(string)
		c.mu.Unlock()
	}
	outcome, err := c.env.outbox.Ack(context.Background(), consumer, p.d.ID, p.d.Attempt, p.token, c.clock())
	if err != nil || outcome == events.LeaseLost {
		c.env.t.Fatalf("ack: %v %v", outcome, err)
	}
}

// drain claims and handles everything currently due.
func (c *crmSim) drain() {
	for {
		ps := c.claim()
		if len(ps) == 0 {
			return
		}
		for _, p := range ps {
			c.handle(p, noCrash)
		}
	}
}

func (c *crmSim) effectsFor(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.effects[key]
}
