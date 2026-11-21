package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/identity"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/scheduling"
)

// Agenda writes (phase 4A), on fictitious tenants only. clinica-aurora is a
// native KAS agenda; fono-espelho stands for a tenant whose agenda is mirrored
// from another system (as the Fono Inova CRM is) and must refuse every write.
// Maria works at clinica-aurora as atendimento, Leo as leitura; Fabio is admin
// of fono-espelho.
const schedulingWriteConfig = `{"contract_version":"v1","config_version":"agenda-4a",
"tenant_registry":[{"id":"clinica-aurora","status":"active"},{"id":"fono-espelho","status":"active"}],
"key_schema":{"branding.name":{"type":"string"}},"tenants":{},
"modules":{"clinica-aurora":{"scheduling":{"status":"active"}},"fono-espelho":{"scheduling":{"status":"active"}}},
"callers":{
 "kas-web":{"token_env":"TOKEN_A","tenants":["clinica-aurora","fono-espelho"],"capabilities":["identity:sessions","scheduling:read","scheduling:compute","scheduling:write"]},
 "aurora-sis":{"token_env":"TOKEN_B","tenants":["clinica-aurora"],"capabilities":["scheduling:read","scheduling:write"]},
 "aurora-leitor":{"token_env":"TOKEN_C","tenants":["clinica-aurora"],"capabilities":["scheduling:read","scheduling:compute"]},
 "espelho-sis":{"token_env":"TOKEN_D","tenants":["fono-espelho"],"capabilities":["scheduling:read","scheduling:write"]}}}`

const writeTokenD = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

type writeEnv struct {
	h     *Handler
	ss    *scheduling.Store
	dsn   string
	day   time.Time // 00:00 UTC of a day a week ahead
	maria string    // session tokens
	leo   string
	fabio string
}

func newWriteEnv(t *testing.T) *writeEnv {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(schedulingWriteConfig), func(name string) (string, bool) {
		return map[string]string{"TOKEN_A": handlerTokenA, "TOKEN_B": handlerTokenB, "TOKEN_C": isolationTokenC, "TOKEN_D": writeTokenD}[name], true
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, 64, slog.New(slog.NewTextHandler(io.Discard, nil)))
	dsn := pgtest.SchemaDSN(t)
	ctx := context.Background()
	ss, err := scheduling.Open(ctx, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ss.Close)
	h.EnableScheduling(ss)
	ids, err := identity.Open(ctx, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ids.Close)
	h.EnableIdentity(ids)
	for _, u := range []struct{ email, tenant, role string }{
		{"maria@aurora.exemplo", "clinica-aurora", "atendimento"},
		{"leo@aurora.exemplo", "clinica-aurora", "leitura"},
		{"fabio@espelho.exemplo", "fono-espelho", "admin"},
	} {
		if _, err := ids.CreateUser(ctx, u.email, u.email, "senha-forte-123"); err != nil {
			t.Fatal(err)
		}
		if err := ids.SetMembership(ctx, u.email, u.tenant, u.role); err != nil {
			t.Fatal(err)
		}
	}
	d := time.Now().UTC().AddDate(0, 0, 7)
	e := &writeEnv{h: h, ss: ss, dsn: dsn, day: time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tenant := range []string{"clinica-aurora", "fono-espelho"} {
		must(ss.PutSettings(ctx, tenant, scheduling.Settings{Zone: "UTC", Holidays: []string{}, ClientExclusive: true}))
		for _, res := range []string{"prof-ana", "prof-bia"} {
			must(ss.PutResource(ctx, tenant, scheduling.Resource{ID: res, Kind: "person", Label: res, Capacity: 1, Active: true}))
			rules := []scheduling.WeeklyRule{}
			for wd := 0; wd <= 6; wd++ {
				rules = append(rules, scheduling.WeeklyRule{Weekday: wd, StartMin: 8 * 60, EndMin: 12 * 60})
			}
			must(ss.PutWeeklyRules(ctx, tenant, res, rules))
		}
	}
	must(ss.PutService(ctx, "clinica-aurora", scheduling.Service{ID: "avaliacao", Label: "Avaliação", DurationMin: 40, BufferMin: 20}))
	// fono-espelho receives one mirrored booking: from then on it is read only.
	s := e.at(9, 0)
	_, err = ss.MirrorApply(ctx, "fono-espelho", "crm", []scheduling.MirrorItem{{BookingID: "ag-espelho-1", ResourceID: "prof-ana",
		ClientRef: "cl-espelho-1", Start: s, End: s.Add(40 * time.Minute), Status: scheduling.StatusScheduled, SourceStatus: "scheduled",
		SourceUpdatedAt: time.Now()}}, scheduling.MirrorCursor{}, false)
	must(err)
	e.maria = signIn(t, h, "kas-web", handlerTokenA, "maria@aurora.exemplo")
	e.leo = signIn(t, h, "kas-web", handlerTokenA, "leo@aurora.exemplo")
	e.fabio = signIn(t, h, "kas-web", handlerTokenA, "fabio@espelho.exemplo")
	return e
}

func (e *writeEnv) at(h, m int) time.Time {
	return e.day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func createBody(tenant, key, res, client string, start time.Time, minutes int, extra string) string {
	return fmt.Sprintf(`{"contract_version":"v1","tenant_id":%q,"idempotency_key":%q,"resource_id":%q,"client_ref":%q,"starts_at":%q,"ends_at":%q%s}`,
		tenant, key, res, client, start.Format(time.RFC3339), start.Add(time.Duration(minutes)*time.Minute).Format(time.RFC3339), extra)
}

func bookingOf(t *testing.T, body []byte) (scheduling.Booking, bool) {
	t.Helper()
	var out schedulingBookingResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return out.Booking, out.Replayed
}

const bookingsPath = "/internal/v1/scheduling/bookings"

func TestSchedulingWriteRoutesAuthorizeTenantAndRole(t *testing.T) {
	e := newWriteEnv(t)
	body := createBody("clinica-aurora", "k-1", "prof-ana", "cli-1", e.at(9, 0), 40, "")
	cases := []struct {
		name, service, token, session, body string
		code                                int
		errCode                             string
	}{
		{"panel without session", "kas-web", handlerTokenA, "", body, 401, "session_required"},
		{"leitura role", "kas-web", handlerTokenA, e.leo, body, 403, "role_forbidden"},
		{"person of another tenant", "kas-web", handlerTokenA, e.fabio, body, 403, "tenant_forbidden"},
		{"service without scheduling:write", "aurora-leitor", isolationTokenC, "", body, 403, ""},
		{"service of another tenant", "espelho-sis", writeTokenD, "", body, 403, ""},
		{"wrong token", "aurora-sis", "errado", "", body, 401, ""},
	}
	for _, c := range cases {
		rec := schedCall(e.h, http.MethodPost, bookingsPath, c.service, c.token, c.session, c.body)
		if rec.Code != c.code || (c.errCode != "" && errorCode(t, rec.Body.Bytes()) != c.errCode) {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	if list, _ := e.ss.ListBookings(context.Background(), "clinica-aurora", "", e.day, e.day.AddDate(0, 0, 1)); len(list) != 0 {
		t.Fatalf("a refused request wrote: %+v", list)
	}
	// Atendimento (panel) and the tenant's own service may write.
	if rec := schedCall(e.h, http.MethodPost, bookingsPath, "kas-web", handlerTokenA, e.maria, body); rec.Code != http.StatusCreated {
		t.Fatalf("maria: %d %s", rec.Code, rec.Body)
	}
	other := createBody("clinica-aurora", "k-2", "prof-bia", "cli-2", e.at(9, 0), 40, "")
	if rec := schedCall(e.h, http.MethodPost, bookingsPath, "aurora-sis", handlerTokenB, "", other); rec.Code != http.StatusCreated {
		t.Fatalf("service: %d %s", rec.Code, rec.Body)
	}
	// The audit log names who wrote: the person for the panel, the service otherwise.
	actors := auditActors(t, e.dsn, "clinica-aurora")
	if len(actors) != 2 || !strings.HasPrefix(actors[0], "user:") || actors[1] != "service:aurora-sis" {
		t.Fatalf("audit actors: %v", actors)
	}
}

func TestSchedulingWriteRefusedOnMirroredAgenda(t *testing.T) {
	e := newWriteEnv(t)
	ctx := context.Background()
	body := createBody("fono-espelho", "k-1", "prof-bia", "cli-1", e.at(10, 0), 40, "")
	for _, c := range []struct{ service, token, session string }{
		{"kas-web", handlerTokenA, e.fabio}, {"espelho-sis", writeTokenD, ""},
	} {
		rec := schedCall(e.h, http.MethodPost, bookingsPath, c.service, c.token, c.session, body)
		if rec.Code != http.StatusConflict || errorCode(t, rec.Body.Bytes()) != "agenda_read_only" {
			t.Fatalf("create on mirrored agenda (%s): %d %s", c.service, rec.Code, rec.Body)
		}
	}
	cancel := `{"contract_version":"v1","tenant_id":"fono-espelho","expected_version":1}`
	if rec := schedCall(e.h, http.MethodPost, bookingsPath+"/ag-espelho-1/cancel", "espelho-sis", writeTokenD, "", cancel); rec.Code != http.StatusConflict ||
		errorCode(t, rec.Body.Bytes()) != "agenda_read_only" {
		t.Fatalf("cancel mirrored: %d %s", rec.Code, rec.Body)
	}
	move := fmt.Sprintf(`{"contract_version":"v1","tenant_id":"fono-espelho","expected_version":1,"starts_at":%q,"ends_at":%q}`,
		e.at(11, 0).Format(time.RFC3339), e.at(11, 40).Format(time.RFC3339))
	if rec := schedCall(e.h, http.MethodPost, bookingsPath+"/ag-espelho-1/reschedule", "espelho-sis", writeTokenD, "", move); rec.Code != http.StatusConflict ||
		errorCode(t, rec.Body.Bytes()) != "agenda_read_only" {
		t.Fatalf("reschedule mirrored: %d %s", rec.Code, rec.Body)
	}
	list, err := e.ss.ListBookings(ctx, "fono-espelho", "", e.day, e.day.AddDate(0, 0, 1))
	if err != nil || len(list) != 1 || list[0].Status != scheduling.StatusScheduled || list[0].Version != 1 || !list[0].Start.Equal(e.at(9, 0)) {
		t.Fatalf("mirrored agenda changed: %+v %v", list, err)
	}
	if n := len(auditActors(t, e.dsn, "fono-espelho")); n != 0 {
		t.Fatalf("%d audit rows on the mirrored agenda", n)
	}
}

func TestSchedulingCreateRulesBufferHoursAndIdempotency(t *testing.T) {
	e := newWriteEnv(t)
	call := func(body string) (int, string, []byte) {
		rec := schedCall(e.h, http.MethodPost, bookingsPath, "aurora-sis", handlerTokenB, "", body)
		code := ""
		if rec.Code >= 400 {
			code = errorCode(t, rec.Body.Bytes())
		}
		return rec.Code, code, rec.Body.Bytes()
	}
	// Service with 20 min buffer: 09:00-09:40 blocks prof-ana until 10:00.
	first := createBody("clinica-aurora", "k-aval", "prof-ana", "cli-1", e.at(9, 0), 40, `,"service_id":"avaliacao"`)
	code, _, raw := call(first)
	b, _ := bookingOf(t, raw)
	if code != http.StatusCreated || b.BufferMin != 20 || !b.BlockedUntil.Equal(e.at(10, 0)) || b.Version != 1 {
		t.Fatalf("create: %d %s", code, raw)
	}
	// Same key, same body: the same booking (200, replayed). Same key, other body: 409.
	code, _, raw = call(first)
	again, replayed := bookingOf(t, raw)
	if code != http.StatusOK || !replayed || again.ID != b.ID {
		t.Fatalf("replay: %d %s", code, raw)
	}
	if code, ec, _ := call(createBody("clinica-aurora", "k-aval", "prof-ana", "cli-1", e.at(11, 0), 40, "")); code != 409 || ec != "idempotency_mismatch" {
		t.Fatalf("key reuse: %d %s", code, ec)
	}
	for _, c := range []struct {
		name, body, errCode string
		code                int
	}{
		{"inside the buffer", createBody("clinica-aurora", "k-b1", "prof-ana", "cli-2", e.at(9, 50), 40, ""), "slot_taken", 409},
		{"client already busy", createBody("clinica-aurora", "k-b2", "prof-bia", "cli-1", e.at(9, 20), 40, ""), "client_busy", 409},
		{"before opening", createBody("clinica-aurora", "k-b3", "prof-bia", "cli-3", e.at(7, 30), 40, ""), "outside_opening_hours", 422},
		{"past closing", createBody("clinica-aurora", "k-b4", "prof-bia", "cli-3", e.at(11, 40), 40, ""), "outside_opening_hours", 422},
		{"unknown service", createBody("clinica-aurora", "k-b5", "prof-bia", "cli-3", e.at(10, 0), 40, `,"service_id":"nao-existe"`), "not_found", 404},
		{"unknown resource", createBody("clinica-aurora", "k-b6", "prof-x", "cli-3", e.at(10, 0), 40, ""), "not_found", 404},
		{"bad status", createBody("clinica-aurora", "k-b7", "prof-bia", "cli-3", e.at(10, 0), 40, `,"status":"completed"`), "invalid_request", 400},
		{"end before start", createBody("clinica-aurora", "k-b8", "prof-bia", "cli-3", e.at(10, 0), -10, ""), "invalid_request", 400},
		{"unknown field", strings.Replace(createBody("clinica-aurora", "k-b9", "prof-bia", "cli-3", e.at(10, 0), 40, ""), `"v1"`, `"v1","x":1`, 1), "invalid_request", 400},
	} {
		if code, ec, raw := call(c.body); code != c.code || ec != c.errCode {
			t.Fatalf("%s: %d %s", c.name, code, raw)
		}
	}
	// Right after the buffer is free; the last 40 minutes before closing too.
	for i, s := range []time.Time{e.at(10, 0), e.at(11, 20)} {
		if code, _, raw := call(createBody("clinica-aurora", fmt.Sprintf("k-ok%d", i), "prof-ana", fmt.Sprintf("cli-ok%d", i), s, 40, "")); code != http.StatusCreated {
			t.Fatalf("free slot %v: %d %s", s, code, raw)
		}
	}
}

func TestSchedulingCancelAndReschedule(t *testing.T) {
	e := newWriteEnv(t)
	rec := schedCall(e.h, http.MethodPost, bookingsPath, "kas-web", handlerTokenA, e.maria, createBody("clinica-aurora", "k-1", "prof-ana", "cli-1", e.at(9, 0), 40, ""))
	b, _ := bookingOf(t, rec.Body.Bytes())
	move := func(version int, start time.Time, session string) *httpRec {
		body := fmt.Sprintf(`{"contract_version":"v1","tenant_id":"clinica-aurora","expected_version":%d,"starts_at":%q,"ends_at":%q}`,
			version, start.Format(time.RFC3339), start.Add(40*time.Minute).Format(time.RFC3339))
		return wrap(schedCall(e.h, http.MethodPost, bookingsPath+"/"+b.ID+"/reschedule", "kas-web", handlerTokenA, session, body))
	}
	cancel := func(version int, session string) *httpRec {
		body := fmt.Sprintf(`{"contract_version":"v1","tenant_id":"clinica-aurora","expected_version":%d}`, version)
		return wrap(schedCall(e.h, http.MethodPost, bookingsPath+"/"+b.ID+"/cancel", "kas-web", handlerTokenA, session, body))
	}
	if r := move(1, e.at(10, 0), e.leo); r.code != 403 {
		t.Fatalf("leitura moved: %+v", r)
	}
	if r := move(1, e.at(7, 0), e.maria); r.code != 422 || errorCode(t, r.body) != "outside_opening_hours" {
		t.Fatalf("move outside hours: %+v", r)
	}
	r := move(1, e.at(10, 0), e.maria)
	moved, _ := bookingOf(t, r.body)
	if r.code != 200 || moved.Version != 2 || !moved.Start.Equal(e.at(10, 0)) {
		t.Fatalf("move: %+v", r)
	}
	if r := move(1, e.at(11, 0), e.maria); r.code != 409 || errorCode(t, r.body) != "version_conflict" {
		t.Fatalf("stale move: %+v", r)
	}
	if r := cancel(1, e.maria); r.code != 409 || errorCode(t, r.body) != "version_conflict" {
		t.Fatalf("stale cancel: %+v", r)
	}
	r = cancel(2, e.maria)
	gone, _ := bookingOf(t, r.body)
	if r.code != 200 || gone.Status != scheduling.StatusCanceled || gone.Version != 3 {
		t.Fatalf("cancel: %+v", r)
	}
	// Repeating the cancel (lost answer) is harmless.
	if r := cancel(2, e.maria); r.code != 200 {
		t.Fatalf("repeated cancel: %+v", r)
	}
	if r := move(3, e.at(11, 0), e.maria); r.code != 400 {
		t.Fatalf("moved a canceled booking: %+v", r)
	}
	if r := wrap(schedCall(e.h, http.MethodPost, bookingsPath+"/bk-nao-existe/cancel", "kas-web", handlerTokenA, e.maria,
		`{"contract_version":"v1","tenant_id":"clinica-aurora","expected_version":1}`)); r.code != 404 {
		t.Fatalf("unknown booking: %+v", r)
	}
	// The freed slot is bookable again; the audit has create, reschedule, cancel, create.
	if rec := schedCall(e.h, http.MethodPost, bookingsPath, "kas-web", handlerTokenA, e.maria,
		createBody("clinica-aurora", "k-2", "prof-ana", "cli-2", e.at(10, 0), 40, "")); rec.Code != 201 {
		t.Fatalf("freed slot: %d %s", rec.Code, rec.Body)
	}
	if got := auditActions(t, e.dsn, "clinica-aurora"); strings.Join(got, ",") != "create,reschedule,cancel,create" {
		t.Fatalf("audit: %v", got)
	}
}

func TestSchedulingConcurrentWrites(t *testing.T) {
	e := newWriteEnv(t)
	const n = 24
	run := func(f func(i int) *httpRec) []*httpRec {
		out := make([]*httpRec, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				out[i] = f(i)
			}(i)
		}
		close(start)
		wg.Wait()
		return out
	}
	count := func(rs []*httpRec) map[string]int {
		m := map[string]int{}
		for _, r := range rs {
			k := fmt.Sprint(r.code)
			if r.code >= 400 {
				k += ":" + errorCode(t, r.body)
			}
			m[k]++
		}
		return m
	}
	// 1) 24 different clients race for the same slot: exactly one wins.
	rs := run(func(i int) *httpRec {
		return wrap(schedCall(e.h, http.MethodPost, bookingsPath, "aurora-sis", handlerTokenB, "",
			createBody("clinica-aurora", fmt.Sprintf("race-%d", i), "prof-ana", fmt.Sprintf("cli-r%d", i), e.at(9, 0), 40, "")))
	})
	if c := count(rs); c["201"] != 1 || c["409:slot_taken"] != n-1 {
		t.Fatalf("same slot race: %v", c)
	}
	// 2) The same request sent 24 times at once (retries): one booking.
	same := createBody("clinica-aurora", "retry-1", "prof-bia", "cli-retry", e.at(9, 0), 40, "")
	rs = run(func(int) *httpRec {
		return wrap(schedCall(e.h, http.MethodPost, bookingsPath, "aurora-sis", handlerTokenB, "", same))
	})
	ids := map[string]bool{}
	for _, r := range rs {
		b, _ := bookingOf(t, r.body)
		ids[b.ID] = true
	}
	if c := count(rs); c["201"] != 1 || c["200"] != n-1 || len(ids) != 1 {
		t.Fatalf("idempotent race: %v ids=%d", c, len(ids))
	}
	// 3) 24 concurrent moves of one booking, all from version 1, to different
	// free times: exactly one applies.
	victim, _ := bookingOf(t, rs[0].body)
	rs = run(func(i int) *httpRec {
		s := e.at(10, 0).Add(time.Duration(i%6) * 20 * time.Minute)
		body := fmt.Sprintf(`{"contract_version":"v1","tenant_id":"clinica-aurora","expected_version":1,"starts_at":%q,"ends_at":%q}`,
			s.Format(time.RFC3339), s.Add(20*time.Minute).Format(time.RFC3339))
		return wrap(schedCall(e.h, http.MethodPost, bookingsPath+"/"+victim.ID+"/reschedule", "aurora-sis", handlerTokenB, "", body))
	})
	if c := count(rs); c["200"] != 1 || c["409:version_conflict"] != n-1 {
		t.Fatalf("reschedule race: %v", c)
	}
	// 4) Cancel and reschedule of version 2 at once: one wins, the other gets
	// version_conflict (or sees the booking already canceled).
	rs = run(func(i int) *httpRec {
		if i%2 == 0 {
			return wrap(schedCall(e.h, http.MethodPost, bookingsPath+"/"+victim.ID+"/cancel", "aurora-sis", handlerTokenB, "",
				`{"contract_version":"v1","tenant_id":"clinica-aurora","expected_version":2}`))
		}
		s := e.at(11, 20)
		return wrap(schedCall(e.h, http.MethodPost, bookingsPath+"/"+victim.ID+"/reschedule", "aurora-sis", handlerTokenB, "",
			fmt.Sprintf(`{"contract_version":"v1","tenant_id":"clinica-aurora","expected_version":2,"starts_at":%q,"ends_at":%q}`,
				s.Format(time.RFC3339), s.Add(20*time.Minute).Format(time.RFC3339))))
	})
	final, err := e.ss.ListBookings(context.Background(), "clinica-aurora", "prof-bia", e.day, e.day.AddDate(0, 0, 1))
	if err != nil || len(final) != 1 || final[0].Version != 3 {
		t.Fatalf("after cancel/reschedule race: %+v %v (%v)", final, err, count(rs))
	}
	// 5) Moves of two different bookings into the same free slot: one wins.
	a, _ := bookingOf(t, schedCall(e.h, http.MethodPost, bookingsPath, "aurora-sis", handlerTokenB, "",
		createBody("clinica-aurora", "m-a", "prof-ana", "cli-ma", e.at(10, 0), 40, "")).Body.Bytes())
	b, _ := bookingOf(t, schedCall(e.h, http.MethodPost, bookingsPath, "aurora-sis", handlerTokenB, "",
		createBody("clinica-aurora", "m-b", "prof-ana", "cli-mb", e.at(10, 40), 40, "")).Body.Bytes())
	if a.ID == "" || b.ID == "" {
		t.Fatal("setup of move race failed")
	}
	rs = run(func(i int) *httpRec {
		id := a.ID
		if i%2 == 1 {
			id = b.ID
		}
		s := e.at(11, 20)
		return wrap(schedCall(e.h, http.MethodPost, bookingsPath+"/"+id+"/reschedule", "aurora-sis", handlerTokenB, "",
			fmt.Sprintf(`{"contract_version":"v1","tenant_id":"clinica-aurora","expected_version":1,"starts_at":%q,"ends_at":%q}`,
				s.Format(time.RFC3339), s.Add(40*time.Minute).Format(time.RFC3339))))
	})
	if c := count(rs); c["200"] != 1 {
		t.Fatalf("two bookings into one slot: %v", c)
	}
	// No overlap survives on any resource, and every write left one audit row.
	for _, res := range []string{"prof-ana", "prof-bia"} {
		list, _ := e.ss.ListBookings(context.Background(), "clinica-aurora", res, e.day, e.day.AddDate(0, 0, 1))
		for i := range list {
			for j := i + 1; j < len(list); j++ {
				x, y := list[i], list[j]
				if scheduling.ActiveStatus(x.Status) && scheduling.ActiveStatus(y.Status) && x.Start.Before(y.BlockedUntil) && y.Start.Before(x.BlockedUntil) {
					t.Fatalf("overlap on %s: %+v %+v", res, x, y)
				}
			}
		}
	}
	var versions, logs int
	conn := pgConn(t, e.dsn)
	if err := conn.QueryRow(context.Background(), `SELECT coalesce(sum(version),0) FROM sched_bookings WHERE tenant_id='clinica-aurora'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM sched_booking_log WHERE tenant_id='clinica-aurora'`).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if versions != logs {
		t.Fatalf("audit rows %d != versions %d", logs, versions)
	}
}

type httpRec struct {
	code int
	body []byte
}

func wrap(rec interface {
	Result() *http.Response
}) *httpRec {
	res := rec.Result()
	b, _ := io.ReadAll(res.Body)
	return &httpRec{code: res.StatusCode, body: b}
}

func pgConn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func auditColumn(t *testing.T, dsn, tenant, col string) []string {
	t.Helper()
	rows, err := pgConn(t, dsn).Query(context.Background(), `SELECT `+col+` FROM sched_booking_log WHERE tenant_id = $1 ORDER BY id`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func auditActors(t *testing.T, dsn, tenant string) []string {
	return auditColumn(t, dsn, tenant, "actor")
}
func auditActions(t *testing.T, dsn, tenant string) []string {
	return auditColumn(t, dsn, tenant, "action")
}

func TestSchedulingCatalogShowsReadOnly(t *testing.T) {
	e := newWriteEnv(t)
	for _, c := range []struct {
		session, tenant string
		readOnly        bool
		services        int
	}{{e.leo, "clinica-aurora", false, 1}, {e.fabio, "fono-espelho", true, 0}} {
		rec := schedCall(e.h, http.MethodGet, "/internal/v1/scheduling/catalog?tenant_id="+c.tenant, "kas-web", handlerTokenA, c.session, "")
		var out schedulingCatalogResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.TenantID != c.tenant || out.ReadOnly != c.readOnly ||
			len(out.Resources) != 2 || len(out.Services) != c.services || out.Zone != "UTC" {
			t.Fatalf("%s: %d %s", c.tenant, rec.Code, rec.Body)
		}
	}
	if rec := schedCall(e.h, http.MethodGet, "/internal/v1/scheduling/catalog?tenant_id=fono-espelho", "kas-web", handlerTokenA, e.leo, ""); rec.Code != 403 {
		t.Fatalf("leo read another tenant's catalog: %d", rec.Code)
	}
}
