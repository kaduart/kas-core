package modules

import (
	"context"
	"strings"
	"testing"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/pgtest"
)

const tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// Fictional tenants only.
const configTemplate = `{"contract_version":"v1","config_version":"%VERSION%",
"tenant_registry":[{"id":"escola-demo","status":"active"},{"id":"fono-demo","status":"active"}],
"key_schema":{"k.v":{"type":"integer"}},
"event_types":["module.activated","module.paused","module.removed"],
"event_consumers":{"painel-escola":{"event_types":["module.activated","module.paused","module.removed"],"tenants":["escola-demo"]}},
"modules":%MODULES%,
"callers":{"sis":{"token_env":"TOKEN_A","tenants":["escola-demo"],"capabilities":["config:read"]}}}`

func config(t *testing.T, version, modules string) *configstore.Store {
	t.Helper()
	raw := strings.NewReplacer("%VERSION%", version, "%MODULES%", modules).Replace(configTemplate)
	s, err := configstore.Load(strings.NewReader(raw), func(name string) (string, bool) { return tokenA, name == "TOKEN_A" })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := pgtest.SchemaDSN(t)
	ctx := context.Background()
	outbox, err := events.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("events store: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	s, err := Open(ctx, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dsn
}

func summary(changes []Change) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, c.TenantID+"/"+c.Module+":"+c.From+">"+c.To+"="+c.EventType)
	}
	return strings.Join(parts, " ")
}

func TestSyncAnnouncesEachChangeOnce(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()

	v1 := config(t, "v1", `{"escola-demo":{"attribution":{"status":"active"},"reports":{"status":"active"}}}`)
	changes, err := s.Sync(ctx, v1)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(changes); got != "escola-demo/attribution:absent>active=module.activated escola-demo/reports:absent>active=module.activated" {
		t.Fatalf("first sync: %s", got)
	}
	// Same configuration again: nothing changes, nothing is published.
	if again, err := s.Sync(ctx, v1); err != nil || len(again) != 0 {
		t.Fatalf("idempotent sync: %v %v", summary(again), err)
	}

	// Pause attribution (reports must then be paused too: dependency rule).
	v2 := config(t, "v2", `{"escola-demo":{"attribution":{"status":"paused"},"reports":{"status":"paused"}}}`)
	changes, err = s.Sync(ctx, v2)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(changes); got != "escola-demo/attribution:active>paused=module.paused escola-demo/reports:active>paused=module.paused" {
		t.Fatalf("pause: %s", got)
	}

	// Drop reports from the block: it becomes absent; attribution reactivated.
	v3 := config(t, "v3", `{"escola-demo":{"attribution":{"status":"active"}}}`)
	changes, err = s.Sync(ctx, v3)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(changes); got != "escola-demo/attribution:paused>active=module.activated escola-demo/reports:paused>absent=module.removed" {
		t.Fatalf("remove: %s", got)
	}

	hist, err := s.History(ctx, "escola-demo")
	if err != nil || len(hist) != 6 {
		t.Fatalf("history: %d %v", len(hist), err)
	}
	if h, _ := s.History(ctx, "fono-demo"); len(h) != 0 {
		t.Fatalf("another tenant's history must be empty, got %d", len(h))
	}

	var eventsCount, deliveries int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE service_id = 'kas-core' AND tenant_id = 'escola-demo'`).Scan(&eventsCount); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_deliveries WHERE consumer_id = 'painel-escola'`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if eventsCount != 6 || deliveries != 6 {
		t.Fatalf("outbox: %d events, %d deliveries", eventsCount, deliveries)
	}
	// The payload carries identifiers only.
	var payload string
	if err := s.pool.QueryRow(ctx, `SELECT payload::text FROM outbox_events WHERE event_id = $1`, changes[1].EventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"module": "reports"`, `"status": "absent"`, `"previous_status": "paused"`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("payload %s lacks %s", payload, want)
		}
	}
}

func TestSyncWithoutModulesBlockDoesNothing(t *testing.T) {
	s, _ := open(t)
	changes, err := s.Sync(context.Background(), config(t, "v1", `{}`))
	if err != nil || len(changes) != 0 {
		t.Fatalf("got %s %v", summary(changes), err)
	}
}

func TestOpenRequiresOutbox(t *testing.T) {
	dsn := pgtest.SchemaDSN(t)
	if _, err := Open(context.Background(), dsn, true); err == nil || !strings.Contains(err.Error(), "outbox") {
		t.Fatalf("expected outbox requirement, got %v", err)
	}
}
