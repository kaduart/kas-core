package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/httpapi"
)

func TestSIGTERMRequestsGracefulShutdown(t *testing.T) {
	ctx, stop := newShutdownSignalContext()
	defer stop()

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to test process: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown context was not canceled by SIGTERM")
	}
}

const mainTestConfig = `{"contract_version":"v1","config_version":"main-test","tenant_registry":[{"id":"tenant-demo-a","status":"active"}],
"key_schema":{"k.v":{"type":"integer"}},"tenants":{"tenant-demo-a":{"k.v":1}},
"callers":{"node-a":{"token_env":"T1","token_env_next":"T2","tenants":["tenant-demo-a"],"capabilities":["config:read"]}}}`

func metricsSetup(t *testing.T, addr, tokenEnv string) (int, http.Handler, *configstore.Store) {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(mainTestConfig), func(name string) (string, bool) {
		switch name {
		case "T1":
			return strings.Repeat("a", 64), true
		case "T2":
			return strings.Repeat("b", 64), true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := httpapi.NewHandler(store, 4, logger)
	code := setupMetrics(logger, api, store, nil, addr, tokenEnv)
	return code, api, store
}

func scrapeMetrics(h http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMetricsAreServedOnLoopbackWithoutAToken(t *testing.T) {
	code, api, store := metricsSetup(t, "127.0.0.1:8088", "")
	if code != 0 {
		t.Fatalf("setup code = %d", code)
	}
	store.Authorize("node-a", strings.Repeat("b", 64), "tenant-demo-a", configstore.CapabilityConfigRead)
	rec := scrapeMetrics(api, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback scrape = %d", rec.Code)
	}
	for _, want := range []string{
		"kas_process_start_time_seconds",
		`kas_service_token_checks_total{slot="primary"} 0`,
		`kas_service_token_checks_total{slot="next"} 1`,
		"kas_services_rotating_tokens 1",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("missing %q in: %s", want, rec.Body.String())
		}
	}
	for _, secret := range []string{"node-a", "tenant-demo-a", strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("metrics leak %q", secret)
		}
	}
}

func TestMetricsOnANonLoopbackListenerNeedAToken(t *testing.T) {
	code, api, _ := metricsSetup(t, "0.0.0.0:8088", "")
	if code != 0 {
		t.Fatalf("setup code = %d", code)
	}
	if rec := scrapeMetrics(api, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("metrics must stay off for a non-loopback listener without a token, got %d", rec.Code)
	}
	t.Setenv("KAS_TEST_METRICS_TOKEN", strings.Repeat("c", 48))
	code, api, _ = metricsSetup(t, "0.0.0.0:8088", "KAS_TEST_METRICS_TOKEN")
	if code != 0 {
		t.Fatalf("setup code = %d", code)
	}
	if rec := scrapeMetrics(api, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rec.Code)
	}
	if rec := scrapeMetrics(api, strings.Repeat("c", 48)); rec.Code != http.StatusOK {
		t.Fatalf("right token = %d", rec.Code)
	}
}

func TestMetricsTokenVariableIsValidated(t *testing.T) {
	if code, _, _ := metricsSetup(t, "127.0.0.1:8088", "KAS_TEST_MISSING_METRICS_TOKEN"); code != 2 {
		t.Fatalf("a missing token variable must fail startup, got %d", code)
	}
	t.Setenv("KAS_TEST_SHORT_METRICS_TOKEN", "short")
	if code, _, _ := metricsSetup(t, "127.0.0.1:8088", "KAS_TEST_SHORT_METRICS_TOKEN"); code != 2 {
		t.Fatalf("a short token must fail startup, got %d", code)
	}
}
