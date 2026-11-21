package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/identity"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/scheduling"
)

// Agenda routes (phase 1). The company comes from the credentials and, for the
// panel, from the person's session: Ana works only at escola-demo, Bruno only
// at fono-demo. loja-demo has the Agenda module paused.
const schedulingConfig = `{"contract_version":"v1","config_version":"agenda-1",
"tenant_registry":[{"id":"escola-demo","status":"active"},{"id":"fono-demo","status":"active"},{"id":"loja-demo","status":"active"}],
"key_schema":{"branding.name":{"type":"string"}},"tenants":{},
"modules":{
 "escola-demo":{"scheduling":{"status":"active"}},
 "fono-demo":{"scheduling":{"status":"active"}},
 "loja-demo":{"scheduling":{"status":"paused"}}},
"callers":{
 "kas-web":{"token_env":"TOKEN_A","tenants":["escola-demo","fono-demo"],"capabilities":["identity:sessions","scheduling:read","scheduling:compute"]},
 "escola-sis":{"token_env":"TOKEN_B","tenants":["escola-demo"],"capabilities":["scheduling:read","scheduling:compute"]},
 "loja-sis":{"token_env":"TOKEN_C","tenants":["loja-demo"],"capabilities":["scheduling:read","scheduling:compute"]}}}`

func schedulingHandler(t *testing.T) *Handler {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(schedulingConfig), func(name string) (string, bool) {
		return map[string]string{"TOKEN_A": handlerTokenA, "TOKEN_B": handlerTokenB, "TOKEN_C": isolationTokenC}[name], true
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	for email, tenant := range map[string]string{"ana@escola.exemplo": "escola-demo", "bruno@fono.exemplo": "fono-demo"} {
		if _, err := ids.CreateUser(ctx, email, email, "senha-forte-123"); err != nil {
			t.Fatal(err)
		}
		if err := ids.SetMembership(ctx, email, tenant, "leitura"); err != nil {
			t.Fatal(err)
		}
	}
	// Both companies have a room with the same id and one booking each.
	day := time.Now().AddDate(0, 0, 7)
	start := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, time.UTC)
	for i, tenant := range []string{"escola-demo", "fono-demo"} {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(ss.PutSettings(ctx, tenant, scheduling.Settings{Zone: "UTC", Holidays: []string{}, ClientExclusive: true}))
		must(ss.PutResource(ctx, tenant, scheduling.Resource{ID: "sala-1", Kind: "room", Label: "Sala 1", Capacity: 1, Active: true}))
		rules := []scheduling.WeeklyRule{}
		for wd := 0; wd <= 6; wd++ {
			rules = append(rules, scheduling.WeeklyRule{Weekday: wd, StartMin: 8 * 60, EndMin: 12 * 60})
		}
		must(ss.PutWeeklyRules(ctx, tenant, "sala-1", rules))
		s := start.Add(time.Duration(i) * time.Hour)
		_, _, err := ss.CreateBooking(ctx, tenant, scheduling.NewBooking{ResourceID: "sala-1", ClientRef: "cli-" + tenant,
			IdempotencyKey: "k1", Actor: "teste", Start: s, End: s.Add(40 * time.Minute)})
		must(err)
	}
	return h
}

func schedCall(h http.Handler, method, path, service, token, session, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		req.Header.Set("X-KAS-Session", session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bookingsWindow() string {
	now := time.Now().UTC()
	return "from=" + now.Format(time.RFC3339) + "&to=" + now.AddDate(0, 0, 30).Format(time.RFC3339)
}

func TestSchedulingTenantComesFromTheSession(t *testing.T) {
	h := schedulingHandler(t)
	ana := signIn(t, h, "kas-web", handlerTokenA, "ana@escola.exemplo")
	bruno := signIn(t, h, "kas-web", handlerTokenA, "bruno@fono.exemplo")
	path := "/internal/v1/scheduling/bookings?" + bookingsWindow()

	// The panel without a session gets nothing, whatever tenant it names.
	if rec := schedCall(h, http.MethodGet, path+"&tenant_id=escola-demo", "kas-web", handlerTokenA, "", ""); rec.Code != http.StatusUnauthorized ||
		errorCode(t, rec.Body.Bytes()) != "session_required" {
		t.Fatalf("no session: %d %s", rec.Code, rec.Body)
	}
	// Ana names Bruno's company: refused, even though the panel serves it.
	if rec := schedCall(h, http.MethodGet, path+"&tenant_id=fono-demo", "kas-web", handlerTokenA, ana, ""); rec.Code != http.StatusForbidden ||
		errorCode(t, rec.Body.Bytes()) != "tenant_forbidden" {
		t.Fatalf("cross-company read: %d %s", rec.Code, rec.Body)
	}
	read := func(session, tenant string) []scheduling.Booking {
		t.Helper()
		rec := schedCall(h, http.MethodGet, path+"&tenant_id="+tenant, "kas-web", handlerTokenA, session, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tenant, rec.Code, rec.Body)
		}
		var body struct {
			TenantID string               `json:"tenant_id"`
			Bookings []scheduling.Booking `json:"bookings"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.TenantID != tenant || len(body.Bookings) != 1 || body.Bookings[0].ClientRef != "cli-"+tenant {
			t.Fatalf("%s sees %+v", tenant, body)
		}
		return body.Bookings
	}
	read(ana, "escola-demo")
	read(bruno, "fono-demo")
	// An ordinary service cannot name a company it does not serve.
	if rec := schedCall(h, http.MethodGet, path+"&tenant_id=fono-demo", "escola-sis", handlerTokenB, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("service cross-company: %d %s", rec.Code, rec.Body)
	}
	// Without tenant_id, a single-company service gets its own company.
	if rec := schedCall(h, http.MethodGet, path, "escola-sis", handlerTokenB, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tenant_id":"escola-demo"`) {
		t.Fatalf("implicit tenant: %d %s", rec.Code, rec.Body)
	}
	// A wrong token is refused before anything else.
	if rec := schedCall(h, http.MethodGet, path, "escola-sis", handlerTokenA, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	// A company whose Agenda module is paused is refused (a company that never
	// contracted it cannot even be given the capability: the configuration is refused).
	if rec := schedCall(h, http.MethodGet, path, "loja-sis", isolationTokenC, "", ""); rec.Code != http.StatusForbidden ||
		errorCode(t, rec.Body.Bytes()) != "module_paused" {
		t.Fatalf("module gate: %d %s", rec.Code, rec.Body)
	}
	// The same checks guard the calculations.
	body := `{"contract_version":"v1","tenant_id":"fono-demo","candidates":[{"key":"a","resource_id":"sala-1","client_ref":"x","starts_at":"2030-01-01T09:00:00Z","ends_at":"2030-01-01T10:00:00Z"}]}`
	if rec := schedCall(h, http.MethodPost, "/internal/v1/scheduling/conflicts:check", "kas-web", handlerTokenA, ana, body); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-company conflicts: %d %s", rec.Code, rec.Body)
	}
}

func TestSchedulingCalculations(t *testing.T) {
	h := schedulingHandler(t)
	day := time.Now().AddDate(0, 0, 7).UTC().Format("2006-01-02")
	rec := schedCall(h, http.MethodGet, "/internal/v1/scheduling/availability?resource_id=sala-1&duration_min=40&from="+day+"&to="+day,
		"escola-sis", handlerTokenB, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("availability: %d %s", rec.Code, rec.Body)
	}
	var slots struct {
		Slots []scheduling.Slot `json:"slots"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &slots)
	// 08:00-12:00 in 40-minute steps is 6 starts; the 09:00-09:40 booking blocks
	// 08:40-09:20 and 09:20-10:00, leaving 4.
	if len(slots.Slots) != 4 {
		t.Fatalf("expected 4 free slots, got %d: %s", len(slots.Slots), rec.Body)
	}

	conflicts := `{"contract_version":"v1","candidates":[
	 {"key":"parcial","resource_id":"sala-1","client_ref":"novo","starts_at":"` + day + `T09:20:00Z","ends_at":"` + day + `T10:00:00Z"},
	 {"key":"encostada","resource_id":"sala-1","client_ref":"novo-2","starts_at":"` + day + `T09:40:00Z","ends_at":"` + day + `T10:20:00Z"}]}`
	rec = schedCall(h, http.MethodPost, "/internal/v1/scheduling/conflicts:check", "escola-sis", handlerTokenB, "", conflicts)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"key":"parcial","kind":"resource"`) || strings.Contains(rec.Body.String(), `"key":"encostada"`) {
		t.Fatalf("conflicts: %d %s", rec.Code, rec.Body)
	}

	series := `{"contract_version":"v1","rule":{"frequency":"biweekly","weekdays":[1],"time":"10:00","duration_min":40,"start_date":"2027-01-04","count":3,"holiday_policy":"skip"}}`
	rec = schedCall(h, http.MethodPost, "/internal/v1/scheduling/series:preview", "escola-sis", handlerTokenB, "", series)
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"starts_at"`) != 3 {
		t.Fatalf("series: %d %s", rec.Code, rec.Body)
	}

	for name, c := range map[string]struct{ method, path, body string }{
		"unknown parameter":    {http.MethodGet, "/internal/v1/scheduling/availability?resource_id=sala-1&duration_min=40&from=" + day + "&to=" + day + "&x=1", ""},
		"duration and service": {http.MethodGet, "/internal/v1/scheduling/availability?resource_id=sala-1&duration_min=40&service_id=s&from=" + day + "&to=" + day, ""},
		"contract version":     {http.MethodPost, "/internal/v1/scheduling/series:preview", strings.Replace(series, `"v1"`, `"v9"`, 1)},
		"unknown field":        {http.MethodPost, "/internal/v1/scheduling/series:preview", strings.Replace(series, `"rule"`, `"nome_paciente":"x","rule"`, 1)},
		"bad rule":             {http.MethodPost, "/internal/v1/scheduling/series:preview", strings.Replace(series, `"biweekly"`, `"daily"`, 1)},
		"name in client":       {http.MethodPost, "/internal/v1/scheduling/conflicts:check", strings.Replace(conflicts, `"novo"`, `"Maria Silva"`, 1)},
	} {
		if rec := schedCall(h, c.method, c.path, "escola-sis", handlerTokenB, "", c.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := schedCall(h, http.MethodGet, "/internal/v1/scheduling/availability?resource_id=nao-existe&duration_min=40&from="+day+"&to="+day,
		"escola-sis", handlerTokenB, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown resource: %d", rec.Code)
	}
}

func TestSchedulingDisabled(t *testing.T) {
	store, err := configstore.Load(strings.NewReader(schedulingConfig), func(name string) (string, bool) {
		return map[string]string{"TOKEN_A": handlerTokenA, "TOKEN_B": handlerTokenB, "TOKEN_C": isolationTokenC}[name], true
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := schedCall(h, http.MethodGet, "/internal/v1/scheduling/bookings?"+bookingsWindow(), "escola-sis", handlerTokenB, "", "")
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec.Body.Bytes()) != "scheduling_unavailable" {
		t.Fatalf("disabled: %d %s", rec.Code, rec.Body)
	}
	// No write route exists in phase 1.
	for _, p := range []string{"/internal/v1/scheduling/bookings", "/internal/v1/scheduling/bookings:create"} {
		if rec := schedCall(h, http.MethodPost, p, "escola-sis", handlerTokenB, "", `{}`); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Fatalf("POST %s must not exist: %d", p, rec.Code)
		}
	}
}
