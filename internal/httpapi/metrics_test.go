package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/metrics"
)

func metricsTestHandler(t *testing.T, bearer string) (*Handler, *metrics.Registry) {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(eventsHandlerFixture), func(name string) (string, bool) {
		switch name {
		case "TOKEN_A":
			return handlerTokenA, true
		case "TOKEN_B":
			return handlerTokenB, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.EnableEvents(events.NewService(&scriptedEventsRepository{}, store))
	registry := metrics.NewRegistry()
	h.EnableMetrics(registry, bearer)
	return h, registry
}

func scrape(h http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMetricsCountRequestsByRouteAndStatusWithoutTenantOrToken(t *testing.T) {
	h, _ := metricsTestHandler(t, "")
	get := func(path string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	get("/healthz")
	get("/healthz")
	get("/readyz")
	// Unknown paths, including ones that embed tenant and token text, collapse into "other".
	get("/internal/v1/tenants/tenant-demo-a/" + handlerTokenA)
	get("/does-not-exist")
	eventCall(h, "node-a", handlerTokenA, validEventBody)                                                           // 201
	eventCall(h, "node-a", handlerTokenB, validEventBody)                                                           // 401
	eventCall(h, "node-a", handlerTokenA, strings.Replace(validEventBody, "settings.updated", "x.y", 1))            // 422
	eventCall(h, "node-a", handlerTokenA, strings.Replace(validEventBody, `"tenant-demo-a"`, `"tenant-demo-b"`, 1)) // 403

	rec := scrape(h, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != metrics.ContentType {
		t.Fatalf("scrape = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{
		`kas_http_requests_total{route="/healthz",status="200"} 2`,
		`kas_http_requests_total{route="/readyz",status="200"} 1`,
		`kas_http_requests_total{route="other",status="404"} 2`,
		`kas_http_requests_total{route="/internal/v1/events",status="201"} 1`,
		`kas_http_requests_total{route="/internal/v1/events",status="401"} 1`,
		`kas_http_requests_total{route="/internal/v1/events",status="422"} 1`,
		`kas_http_requests_total{route="/internal/v1/events",status="403"} 1`,
		`# TYPE kas_http_request_duration_seconds histogram`,
		`kas_http_request_duration_seconds_count{route="/healthz"} 2`,
		`kas_http_request_duration_seconds_bucket{route="/healthz",le="+Inf"} 2`,
		`kas_http_requests_in_flight 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	// No tenant, service or token text may appear anywhere in the exposition.
	for _, secret := range []string{"tenant-demo", handlerTokenA, handlerTokenB, "node-a", "node-b", "node-audit", "consumer-a", "evt.http"} {
		if strings.Contains(body, secret) {
			t.Fatalf("metrics leak %q:\n%s", secret, body)
		}
	}
	// Label names are limited to the fixed set.
	names := regexp.MustCompile(`[{,]([a-z_]+)="`).FindAllStringSubmatch(body, -1)
	for _, m := range names {
		if m[1] != "route" && m[1] != "status" && m[1] != "le" {
			t.Fatalf("unexpected label name %q", m[1])
		}
	}
}

func TestMetricsAreServedWhenTheServiceIsSaturatedOrDraining(t *testing.T) {
	h, _ := metricsTestHandler(t, "")
	h.active.Store(h.limit + 100) // simulate every slot busy
	busy := httptest.NewRecorder()
	h.ServeHTTP(busy, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if busy.Code != http.StatusServiceUnavailable {
		t.Fatalf("a saturated service must refuse business traffic, got %d", busy.Code)
	}
	h.active.Store(0)
	if rec := scrape(h, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `kas_http_requests_total{route="/healthz",status="503"} 1`) {
		t.Fatalf("scrape under saturation = %d %s", rec.Code, rec.Body.String())
	}
	h.active.Store(h.limit + 100)
	if rec := scrape(h, ""); rec.Code != http.StatusOK {
		t.Fatalf("metrics must bypass the concurrency limit, got %d", rec.Code)
	}
	h.active.Store(0)
	h.BeginDrain()
	if rec := scrape(h, ""); rec.Code != http.StatusOK {
		t.Fatalf("metrics must stay available while draining, got %d", rec.Code)
	}
}

func TestMetricsEndpointAuthenticationAndMethods(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef"
	h, _ := metricsTestHandler(t, token)
	if rec := scrape(h, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rec.Code)
	}
	if rec := scrape(h, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", rec.Code)
	}
	if rec := scrape(h, handlerTokenA); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a service token must not open /metrics = %d", rec.Code)
	}
	if rec := scrape(h, token); rec.Code != http.StatusOK {
		t.Fatalf("right token = %d", rec.Code)
	}
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/metrics", strings.NewReader("{}")))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST /metrics = %d", post.Code)
	}
}

func TestMetricsAreOffUnlessEnabled(t *testing.T) {
	h := eventsTestHandler(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/metrics without EnableMetrics = %d, want 404", rec.Code)
	}
}
