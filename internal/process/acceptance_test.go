package process

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kas-core-pilot/internal/events"
)

// Acceptance tests T1–T23 of the KAS Process Studio spec v2 (section 7.1).
// They need KAS_TEST_DATABASE_URL (a disposable PostgreSQL); otherwise skipped.

const (
	twoBusinessDays  = `{"business_days": 2, "cutoff": "23:59"}`
	fiveBusinessDays = `{"business_days": 5, "cutoff": "23:59"}`
	fiveHours        = `{"duration": "PT5H"}` // due 15:00 when started at 10:00
)

var (
	due15    = base.Add(5 * time.Hour)        // 15:00
	cutoff15 = due15.Add(5 * time.Minute)     // 15:05
	fire15   = cutoff15.Add(30 * time.Second) // 15:05:30
)

func outboxCount(e *env, eventType, key string) int {
	return e.count(`SELECT count(*) FROM outbox_events WHERE event_type = $1 AND payload->>'command_key' = $2`, eventType, key)
}

// cancelPath starts an instance and has the commercial answer "cancel", so the
// instance waits on the CRM command.
func cancelPath(e *env, tenant string) (int64, string) {
	e.t.Helper()
	id := e.start(tenant, "retencao-cancelamento")
	task := e.openTask(tenant, id)
	if _, err := e.submit(tenant, task.ID, "cliente_cancela", "ator-comercial-1", "comercial", e.clock.Now()); err != nil {
		e.t.Fatalf("submit: %v", err)
	}
	v := e.inst(tenant, id)
	expect(e.t, v.CurrentStep, "pedir_cancelamento", "step")
	return id, v.Commands[len(v.Commands)-1].CommandKey
}

func TestT01RestartWhileWaitingForCRM(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	id, key := cancelPath(e, tenantA)
	e.restart()
	v := e.inst(tenantA, id)
	expect(t, v.CurrentStep, "pedir_cancelamento", "step after restart")
	expect(t, len(v.Commands), 1, "commands")
	expect(t, outboxCount(e, EventCommandRequested, key), 1, "requests in outbox")
	e.crm.drain()
	v = e.inst(tenantA, id)
	expect(t, v.Status, "completed", "status")
	expect(t, *v.Result, "cancelado", "result")
	kinds := e.historyKinds(tenantA, id)
	for _, k := range []string{"started", "task_resolved", "command_requested", "command_result", "completed"} {
		if kinds[k] != 1 {
			t.Fatalf("history %s = %d, want 1 (%v)", k, kinds[k], kinds)
		}
	}
}

func TestT02TimerAfterDowntimeRunsOnceAndDecidesByDueAt(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, fiveHours)
	id := e.start(tenantA, "retencao-cancelamento")
	e.eng.Close() // KAS down
	e.clock.Set(base.Add(8 * time.Hour))
	e.eng = e.open()
	expect(t, e.timers(), 1, "timers executed")
	expect(t, e.timers(), 0, "timers executed again")
	v := e.inst(tenantA, id)
	expect(t, v.CurrentStep, "revisar", "step")
	for _, h := range v.History {
		if h.Kind == "timer_executed" {
			s := string(h.Detail)
			if !strings.Contains(s, due15.UTC().Format("2006-01-02T15:04:05")) || !strings.Contains(s, base.Add(8*time.Hour).UTC().Format("2006-01-02T15:04:05")) {
				t.Fatalf("timer history lacks due_at or executed_at: %s", s)
			}
		}
	}
}

func TestT03StepAndOutboxCommitTogether(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	id := e.start(tenantA, "retencao-cancelamento")
	task := e.openTask(tenantA, id)
	req := SubmitRequest{TenantID: tenantA, ServiceID: crmA, TaskID: task.ID, Outcome: "cliente_cancela",
		ActorID: "ator-comercial-1", Role: "comercial", OccurredAt: e.clock.Now(), SubmissionKey: "sub-t03"}
	e.eng.testHookAfterCommand = func() error { return errors.New("falha injetada") }
	if _, err := e.eng.SubmitTask(context.Background(), req); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("injected failure not reported: %v", err)
	}
	v := e.inst(tenantA, id)
	expect(t, v.CurrentStep, "negociar", "step after rollback")
	expect(t, len(v.Commands), 0, "commands after rollback")
	expect(t, e.count(`SELECT count(*) FROM outbox_events WHERE event_type = $1`, EventCommandRequested), 0, "outbox after rollback")
	e.eng.testHookAfterCommand = nil
	if _, err := e.eng.SubmitTask(context.Background(), req); err != nil {
		t.Fatalf("retry: %v", err)
	}
	v = e.inst(tenantA, id)
	expect(t, v.CurrentStep, "pedir_cancelamento", "step after retry")
	expect(t, e.count(`SELECT count(*) FROM outbox_events WHERE event_type = $1`, EventCommandRequested), 1, "outbox after retry")
}

func TestT04CRMCrashBetweenEffectAndAck(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	id, key := cancelPath(e, tenantA)
	for _, p := range e.crm.claim() {
		e.crm.handle(p, crashAfterEffect)
	}
	expect(t, e.crm.effectsFor(key), 1, "effects after crash")
	expect(t, e.inst(tenantA, id).CurrentStep, "pedir_cancelamento", "step before redelivery")
	e.crm.now = e.clock.Now().Add(2 * time.Minute) // lease expired: redelivery
	e.crm.drain()
	expect(t, e.crm.effectsFor(key), 1, "effects after redelivery")
	expect(t, *e.inst(tenantA, id).Result, "cancelado", "result")
}

func TestT05CRMCrashBeforeEffect(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	id, key := cancelPath(e, tenantA)
	for _, p := range e.crm.claim() {
		e.crm.handle(p, crashBeforeEffect)
	}
	expect(t, e.crm.effectsFor(key), 0, "effects after crash")
	e.crm.now = e.clock.Now().Add(2 * time.Minute)
	e.crm.drain()
	expect(t, e.crm.effectsFor(key), 1, "effects after redelivery")
	expect(t, *e.inst(tenantA, id).Result, "cancelado", "result")
}

func TestT06AckWithLostLease(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	_, key := cancelPath(e, tenantA)
	first := e.crm.claim()
	expect(t, len(first), 1, "first claim")
	e.crm.handle(first[0], crashAfterEffect) // slow consumer: no ack yet
	e.crm.now = e.clock.Now().Add(2 * time.Minute)
	e.crm.drain() // another worker got the redelivery and finished it
	outcome, err := e.outbox.Ack(context.Background(), consumer, first[0].d.ID, first[0].d.Attempt, first[0].token, e.crm.now)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != events.LeaseLost {
		t.Fatalf("late ack outcome = %v, want lease lost", outcome)
	}
	expect(t, e.crm.effectsFor(key), 1, "effects")
}

// unknownPath leaves the command unknown after the status queries run out.
func unknownPath(e *env) (int64, string, string) {
	e.t.Helper()
	ref := nextRef()
	e.crm.mute[ref] = true
	id := e.startRef(tenantA, "retencao-cancelamento", ref)
	task := e.openTask(tenantA, id)
	if _, err := e.submit(tenantA, task.ID, "cliente_cancela", "ator-comercial-1", "comercial", e.clock.Now()); err != nil {
		e.t.Fatal(err)
	}
	key := e.inst(tenantA, id).Commands[0].CommandKey
	e.crm.drain()
	for i := 0; i < 3; i++ { // 2 status queries, then unknown
		e.clock.Add(time.Hour + time.Second)
		e.timers()
		e.crm.drain()
	}
	return id, key, ref
}

func TestT07NoReplyQueriesSameKeyThenReview(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	id, key, _ := unknownPath(e)
	v := e.inst(tenantA, id)
	expect(t, v.CurrentStep, "revisar", "step")
	expect(t, v.Commands[0].Status, "unknown", "command status")
	expect(t, v.Commands[0].StatusQueries, 2, "status queries")
	expect(t, outboxCount(e, EventCommandStatusQuery, key), 2, "queries with the same key")
	expect(t, e.historyKinds(tenantA, id)["command_unknown"], 1, "alert in history")
}

func TestT08ReviewRetriesWithSameKeyWithoutSecondEffect(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	id, key, ref := unknownPath(e)
	expect(t, e.crm.effectsFor(key), 1, "effect applied while answers were lost")
	e.crm.mute[ref] = false
	review := e.openTask(tenantA, id)
	if _, err := e.submit(tenantA, review.ID, "cliente_cancela", "ator-supervisor-1", "supervisor", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	v := e.inst(tenantA, id)
	expect(t, len(v.Commands), 1, "commands")
	expect(t, v.Commands[0].CommandKey, key, "same key")
	expect(t, v.Commands[0].Attempt, 2, "request attempt")
	e.crm.drain()
	expect(t, e.crm.effectsFor(key), 1, "effects after retry")
	expect(t, *e.inst(tenantA, id).Result, "cancelado", "result")
}

func TestT09RejectedOpensNewGeneration(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	ref := nextRef()
	e.crm.decide[ref] = "rejected"
	id := e.startRef(tenantA, "retencao-cancelamento", ref)
	if _, err := e.submit(tenantA, e.openTask(tenantA, id).ID, "cliente_cancela", "ator-comercial-1", "comercial", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.crm.drain()
	expect(t, e.inst(tenantA, id).CurrentStep, "revisar", "step after rejection")
	e.crm.decide[ref] = "confirmed"
	if _, err := e.submit(tenantA, e.openTask(tenantA, id).ID, "cliente_cancela", "ator-supervisor-1", "supervisor", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.crm.drain()
	v := e.inst(tenantA, id)
	expect(t, len(v.Commands), 2, "commands")
	expect(t, v.Commands[1].Generation, 2, "generation")
	if v.Commands[0].CommandKey == v.Commands[1].CommandKey {
		t.Fatal("new generation reused the key")
	}
	expect(t, *v.Result, "cancelado", "result")
}

func TestT10ReviewCannotCloseWhileCommandUnknown(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	id, _, _ := unknownPath(e)
	review := e.openTask(tenantA, id)
	_, err := e.submit(tenantA, review.ID, "cliente_permanece", "ator-supervisor-1", "supervisor", e.clock.Now())
	expectErr(t, err, ErrCommandUnknown)
	v := e.inst(tenantA, id)
	expect(t, v.CurrentStep, "revisar", "step")
	expect(t, e.openTask(tenantA, id).ID, review.ID, "review still open")
}

func TestT11AnswerWithinToleranceIsOnTime(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, fiveHours)
	id := e.start(tenantA, "retencao-cancelamento")
	task := e.openTask(tenantA, id)
	e.clock.Set(due15.Add(3 * time.Minute)) // received 15:03
	res, err := e.submit(tenantA, task.ID, "cliente_permanece", "ator-comercial-1", "comercial", due15.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Status, "applied", "status")
	e.clock.Set(fire15.Add(time.Minute))
	expect(t, e.timers(), 0, "timers")
	expect(t, *e.inst(tenantA, id).Result, "retido", "result")
	expect(t, e.historyKinds(tenantA, id)["deadline_expired"], 0, "deadline transitions")
}

func TestT12RecordedAnswerWinsEvenIfTimerLocksFirst(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, fiveHours)
	rounds := 200
	if testing.Short() {
		rounds = 20
	}
	rng := rand.New(rand.NewSource(12))
	for i := 0; i < rounds; i++ {
		e.clock.Set(base)
		id := e.start(tenantA, "retencao-cancelamento")
		task := e.openTask(tenantA, id)
		e.clock.Set(cutoff15.Add(-time.Second)) // received just inside the tolerance
		req := SubmitRequest{TenantID: tenantA, ServiceID: crmA, TaskID: task.ID, Outcome: "cliente_permanece",
			ActorID: "ator-comercial-2", Role: "comercial", OccurredAt: due15.Add(-time.Second), SubmissionKey: "t12-" + task.Step + "-" + itoa(id)}
		res, subID, closed, err := e.eng.recordSubmission(context.Background(), req, req.OccurredAt, e.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		e.clock.Set(fire15)
		var wg sync.WaitGroup
		apply := func() {
			defer wg.Done()
			_, _ = e.eng.applySubmission(context.Background(), tenantA, task.ID, subID, res, closed)
		}
		timer := func() { defer wg.Done(); _, _ = e.eng.RunDueTimers(context.Background(), 10) }
		wg.Add(2)
		if rng.Intn(2) == 0 {
			go timer()
			go apply()
		} else {
			go apply()
			go timer()
		}
		wg.Wait()
		e.timers() // a timer skipped because the instance was busy runs now
		v := e.inst(tenantA, id)
		if v.Status != "completed" || *v.Result != "retido" {
			t.Fatalf("round %d: status %s result %v, want completed retido", i, v.Status, v.Result)
		}
		kinds := e.historyKinds(tenantA, id)
		if kinds["task_resolved"] != 1 || kinds["deadline_expired"] != 0 {
			t.Fatalf("round %d: history %v", i, kinds)
		}
	}
}

func TestT13AnswerAfterToleranceIsLateAndRecorded(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, fiveHours)
	id := e.start(tenantA, "retencao-cancelamento")
	task := e.openTask(tenantA, id)
	e.clock.Set(cutoff15.Add(time.Second)) // received 15:05:01, timer not run yet
	res, err := e.submit(tenantA, task.ID, "cliente_permanece", "ator-comercial-1", "comercial", due15.Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Status, "late", "status")
	v := e.inst(tenantA, id)
	expect(t, v.CurrentStep, "revisar", "step")
	kinds := e.historyKinds(tenantA, id)
	expect(t, kinds["late_response"], 1, "late responses in history")
	expect(t, kinds["deadline_expired"], 1, "deadline transitions")
}

func TestT14DeadlineIsClockNotWorker(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, fiveHours)
	id := e.start(tenantA, "retencao-cancelamento")
	task := e.openTask(tenantA, id)
	e.clock.Set(base.Add(6 * time.Hour)) // 16:00, the timer worker is stopped
	res, err := e.submit(tenantA, task.ID, "cliente_cancela", "ator-comercial-1", "comercial", e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Status, "late", "status")
	expect(t, e.inst(tenantA, id).CurrentStep, "revisar", "step")
	e.clock.Set(base.Add(8 * time.Hour)) // worker back at 18:00
	expect(t, e.timers(), 0, "timers left")
	expect(t, e.historyKinds(tenantA, id)["deadline_expired"], 1, "deadline transitions")
	expect(t, len(e.inst(tenantA, id).Commands), 0, "no command from the late answer")
}

func TestT15ClockSkewAndDelayedReport(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, fiveHours)
	id := e.start(tenantA, "retencao-cancelamento")
	task := e.openTask(tenantA, id)
	e.clock.Set(base.Add(time.Hour))
	_, err := e.submit(tenantA, task.ID, "cliente_permanece", "ator-comercial-1", "comercial", e.clock.Now().Add(31*time.Second))
	expectErr(t, err, ErrClockSkew)
	res, err := e.submit(tenantA, task.ID, "cliente_permanece", "ator-comercial-1", "comercial", e.clock.Now().Add(-11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Status, "late", "status of a delayed report")
	expect(t, e.count(`SELECT count(*) FROM process_task_submissions WHERE report_delayed`), 1, "delayed reports")
	expect(t, e.inst(tenantA, id).CurrentStep, "negociar", "task stays open")
}

func TestT16RoleAndActorChecks(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, `{"duration": "P1D"}`)
	id1 := e.start(tenantA, "retencao-cancelamento")
	id2 := e.start(tenantA, "retencao-cancelamento")
	t1, t2 := e.openTask(tenantA, id1), e.openTask(tenantA, id2)
	_, err := e.submit(tenantA, t1.ID, "cliente_permanece", "ator-supervisor-1", "supervisor", e.clock.Now())
	expectErr(t, err, ErrRoleNotAllowed)
	_, err = e.submit(tenantA, t1.ID, "cliente_permanece", "ator-estranho", "comercial", e.clock.Now())
	expectErr(t, err, ErrActorNotInRole)
	// ator-comercial-1 leaves the role at 12:00.
	if _, _, err := e.eng.SetRoleMembers(context.Background(), tenantA, crmA, "comercial", "chg-t16", []string{"ator-comercial-2"}, base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(base.Add(3 * time.Hour))
	if _, err := e.submit(tenantA, t1.ID, "cliente_permanece", "ator-comercial-1", "comercial", base.Add(time.Hour)); err != nil {
		t.Fatalf("answer given while in the role: %v", err)
	}
	_, err = e.submit(tenantA, t2.ID, "cliente_permanece", "ator-comercial-1", "comercial", base.Add(2*time.Hour+30*time.Minute))
	expectErr(t, err, ErrActorNotInRole)
	for _, h := range e.inst(tenantA, id1).History {
		if h.Kind == "task_resolved" && (!strings.Contains(string(h.Detail), `"assignment_version"`) || h.ActorID != "ator-comercial-1" || h.Role != "comercial" || h.ServiceID != crmA) {
			t.Fatalf("resolution history incomplete: %+v", h)
		}
	}
}

func TestT17TenantIsolation(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	e.setupRetention(tenantB, fiveBusinessDays)
	id, key := cancelPath(e, tenantA)
	before := e.inst(tenantA, id)
	_, err := e.eng.GetInstance(context.Background(), tenantB, id)
	expectErr(t, err, ErrNotFound)
	_, err = e.eng.RecordCommandResult(context.Background(), CommandResult{TenantID: tenantB, ServiceID: crmA, CommandKey: key, Status: "confirmed", OccurredAt: e.clock.Now()})
	expectErr(t, err, ErrNotFound)
	idB := e.start(tenantB, "retencao-cancelamento")
	_, err = e.submit(tenantA, e.openTask(tenantB, idB).ID, "cliente_permanece", "ator-comercial-1", "comercial", e.clock.Now())
	expectErr(t, err, ErrNotFound)
	after := e.inst(tenantA, id)
	expect(t, after.StateVersion, before.StateVersion, "state version of A")
	expect(t, e.inst(tenantB, idB).CurrentStep, "negociar", "B untouched")
}

func TestT18VersionPinning(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	old := e.start(tenantA, "retencao-cancelamento")
	if v := e.publish(tenantA, withRegulator(retentionDef(fiveBusinessDays))); v != 2 {
		t.Fatalf("new version = %d", v)
	}
	fresh := e.start(tenantA, "retencao-cancelamento")
	expect(t, e.inst(tenantA, old).Version, 1, "old instance version")
	expect(t, e.inst(tenantA, fresh).Version, 2, "new instance version")
	expect(t, e.openTask(tenantA, old).DueAt.In(loc).Format("2006-01-02 15:04"), "2026-11-18 23:59", "old due")
	expect(t, e.openTask(tenantA, fresh).DueAt.In(loc).Format("2006-01-02 15:04"), "2026-11-23 23:59", "new due")
}

func event(e *env, ref, eventType, waitKey, id string, occurred time.Time) (EventResult, error) {
	return e.eng.IngestEvent(context.Background(), EventRequest{TenantID: tenantA, ServiceID: crmA, EventID: id,
		EventType: eventType, BusinessRef: ref, WaitKey: waitKey, OccurredAt: occurred})
}

func TestT19EventsWithWaitKey(t *testing.T) {
	e := newEnv(t)
	e.publish(tenantA, waitDef)
	ref := nextRef()
	id := e.startRef(tenantA, "confirmar-desistencia", ref)
	e.crm.drain()
	key := e.crm.waits[ref]
	if key == "" {
		t.Fatal("CRM did not receive the wait key")
	}
	_, err := event(e, ref, "cliente.outro", key, "ev-1", e.clock.Now())
	expectErr(t, err, ErrWaitTypeMismatch)
	res, err := event(e, ref, "cliente.desistencia_confirmada", key, "ev-2", base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Outcome, "stale", "event before the wait opened")
	res, err = event(e, ref, "cliente.desistencia_confirmada", key, "ev-3", e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Outcome, "matched", "outcome")
	expect(t, *e.inst(tenantA, id).Result, "mantido", "result")
	res, err = event(e, ref, "cliente.desistencia_recusada", key, "ev-4", e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Outcome, "wait_closed", "outcome after close")
	again, err := event(e, ref, "cliente.desistencia_confirmada", key, "ev-3", e.clock.Now())
	if err != nil || !again.Duplicate || again.Outcome != "matched" {
		t.Fatalf("resend = %+v, %v", again, err)
	}
}

func TestT20EventsWithoutWaitKey(t *testing.T) {
	e := newEnv(t)
	e.publish(tenantA, waitDef)
	ref := nextRef()
	id := e.startRef(tenantA, "confirmar-desistencia", ref)
	_, _, err := e.eng.StartInstance(context.Background(), StartRequest{TenantID: tenantA, ServiceID: crmA, ProcessKey: "confirmar-desistencia",
		BusinessRef: ref, StartKey: "outra-chave", ActorID: "ator-cliente", OccurredAt: e.clock.Now()})
	expectErr(t, err, ErrInstanceActive)
	res, err := event(e, ref, "cliente.desistencia_recusada", "", "ev-a", e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Outcome, "matched", "outcome by business_ref")
	expect(t, *e.inst(tenantA, id).Result, "cancelado", "result")
	res, err = event(e, "contrato-inexistente", "cliente.desistencia_recusada", "", "ev-b", e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Outcome, "unmatched", "outcome")
	expect(t, e.count(`SELECT count(*) FROM process_unmatched_events WHERE event_id = 'ev-b'`), 1, "unmatched kept")
}

func TestT21BusinessDaysWithHolidayAndCutoff(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	id := e.start(tenantA, "retencao-cancelamento") // Fri 13/11 10:00, Tue 17/11 is a holiday
	task := e.openTask(tenantA, id)
	expect(t, task.DueAt.In(loc).Format("Mon 2006-01-02 15:04"), "Wed 2026-11-18 23:59", "due_at")
	expect(t, task.CutoffAt.Sub(*task.DueAt), 5*time.Minute, "grace")
}

func TestT22RegulatorInterruptCancelsTimers(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, fiveHours)
	ref := nextRef()
	id := e.startRef(tenantA, "retencao-cancelamento", ref)
	res, err := event(e, ref, "regulador.cancelamento", "", "reg-1", e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res.Outcome, "interrupt", "outcome")
	v := e.inst(tenantA, id)
	expect(t, *v.Result, "cancelado_regulador", "result")
	expect(t, e.count(`SELECT count(*) FROM process_timers WHERE instance_id = $1 AND status = 'pending'`, id), 0, "pending timers")
	e.clock.Set(base.Add(10 * time.Hour))
	expect(t, e.timers(), 0, "timers fired after interrupt")
}

func TestT23RuntimeRoleCannotRewriteHistoryOrDefinitions(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	e.start(tenantA, "retencao-cancelamento")
	ctx := context.Background()
	conn, err := e.eng.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var schema string
	if err := conn.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role := "kas_proc_rt_" + itoa(time.Now().UnixNano()%1_000_000)
	if _, err := conn.Exec(ctx, `CREATE ROLE `+role); err != nil {
		t.Skipf("cannot create roles here: %v", err)
	}
	defer func() { _, _ = e.eng.pool.Exec(ctx, `DROP OWNED BY `+role+`; DROP ROLE `+role) }()
	grants, err := os.ReadFile("../../deploy/grants-process.sql")
	if err != nil {
		t.Fatal(err)
	}
	section := string(grants)[strings.Index(string(grants), "-- Process Studio"):]
	section = strings.NewReplacer(`:"schema"`, schema, `:"runtime_role"`, role).Replace(section)
	if _, err := conn.Exec(ctx, `GRANT USAGE ON SCHEMA `+schema+` TO `+role+`;`+section); err != nil {
		t.Fatalf("apply grants: %v", err)
	}
	if _, err := conn.Exec(ctx, `SET ROLE `+role); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, `RESET ROLE`) }()
	denied := regexp.MustCompile(`permission denied`)
	for _, stmt := range []string{
		`UPDATE process_history SET kind = 'x'`,
		`DELETE FROM process_history`,
		`UPDATE process_definitions SET checksum = 'x'`,
		`DELETE FROM process_definitions`,
		`DELETE FROM process_instances`,
	} {
		if _, err := conn.Exec(ctx, stmt); err == nil || !denied.MatchString(err.Error()) {
			t.Fatalf("%s: err = %v, want permission denied", stmt, err)
		}
	}
	if _, err := conn.Exec(ctx, `SELECT count(*) FROM process_history`); err != nil {
		t.Fatalf("runtime role cannot read history: %v", err)
	}
}

// TestPilotTwoCompaniesSameEngine runs company A (2 business days, commercial →
// supervisor, CRM) and company B (5 business days, service desk → retention
// team, ERP) through the same executor without company-specific code.
func TestPilotTwoCompaniesSameEngine(t *testing.T) {
	e := newEnv(t)
	e.setupRetention(tenantA, twoBusinessDays)
	e.publish(tenantB, withRegulator(retentionDefFor(fiveBusinessDays, "atendimento", "retencao", "erp.subscription.cancel")))
	e.roles(tenantB, "atendimento", "ator-b-atendimento")
	e.roles(tenantB, "retencao", "ator-b-retencao")
	a := e.start(tenantA, "retencao-cancelamento")
	b := e.start(tenantB, "retencao-cancelamento")
	expect(t, e.openTask(tenantB, b).DueAt.In(loc).Format("2006-01-02"), "2026-11-23", "B due (holiday is B's too: same definition calendar)")
	if _, err := e.submit(tenantB, e.openTask(tenantB, b).ID, "cliente_cancela", "ator-b-atendimento", "atendimento", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.submit(tenantA, e.openTask(tenantA, a).ID, "cliente_permanece", "ator-comercial-1", "comercial", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.crm.drain()
	expect(t, *e.inst(tenantA, a).Result, "retido", "A result")
	vb := e.inst(tenantB, b)
	expect(t, *vb.Result, "cancelado", "B result")
	expect(t, vb.Commands[0].CommandType, "erp.subscription.cancel", "B command type")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
