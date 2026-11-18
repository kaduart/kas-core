package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	"kas-core-pilot/internal/metrics"
)

// metricsState holds the HTTP instruments. Labels are the matched route from a
// fixed list and the status code. Tenant identifiers, service identifiers and
// tokens are never used as labels, and unknown paths collapse into "other".
type metricsState struct {
	registry    *metrics.Registry
	requests    *metrics.CounterVec
	latency     *metrics.HistogramVec
	tokenDigest *[32]byte // when set, /metrics requires this bearer token
}

// EnableMetrics registers the HTTP instruments in the registry and serves it at
// GET /metrics. When bearerToken is not empty, the endpoint requires it as a
// bearer token. Call it before serving requests.
func (h *Handler) EnableMetrics(registry *metrics.Registry, bearerToken string) {
	state := &metricsState{
		registry: registry,
		requests: metrics.NewCounterVec("kas_http_requests_total", "HTTP requests by route and status code.", "route", "status"),
		latency:  metrics.NewHistogramVec("kas_http_request_duration_seconds", "HTTP request duration by route.", metrics.DefaultBuckets, "route"),
	}
	if bearerToken != "" {
		digest := sha256.Sum256([]byte(bearerToken))
		state.tokenDigest = &digest
	}
	registry.Register(state.requests)
	registry.Register(state.latency)
	registry.Register(metrics.NewGaugeFunc("kas_http_requests_in_flight", "Requests currently being handled.", nil,
		func() []metrics.Sample { return []metrics.Sample{{Value: float64(h.active.Load())}} }))
	h.metrics = state
}

func (m *metricsState) observe(route string, status int, elapsed time.Duration) {
	m.requests.Inc(route, strconv.Itoa(status))
	m.latency.Observe(elapsed.Seconds(), route)
}

// routeLabel maps a request path to a fixed label value.
func routeLabel(path string) string {
	switch path {
	case "/healthz", "/readyz", "/metrics",
		"/internal/v1/config/evaluate",
		"/internal/v1/audit/events",
		"/v1/audit",
		"/internal/v1/events",
		"/internal/v1/events/deliveries/claim",
		"/internal/v1/events/deliveries/ack",
		"/internal/v1/events/deliveries/nack":
		return path
	}
	return "other"
}

func (h *Handler) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		h.writeError(w, r, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if want := h.metrics.tokenDigest; want != nil {
		got := sha256.Sum256([]byte(bearerToken(r.Header.Get("Authorization"))))
		if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			h.writeError(w, r, http.StatusUnauthorized, "unauthorized", "metrics authentication failed")
			return
		}
	}
	w.Header().Set("Content-Type", metrics.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = h.metrics.registry.WriteTo(w)
}

// statusRecorder remembers the response status for the metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) statusCode() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
