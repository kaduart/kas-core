package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kas-core-pilot/internal/attribution"
	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/process"
	"kas-core-pilot/internal/strictjson"
)

const maxRequestBytes = 4 << 10
const maxAuditRequestBytes = 8 << 10

// maxEventRequestBytes leaves room for the envelope around a 16 KiB payload.
const maxEventRequestBytes = events.MaxPayloadBytes + 4<<10

var (
	errInvalidJSON  = errors.New("invalid JSON request")
	errBodyTooLarge = errors.New("request body exceeds limit")
)

type Handler struct {
	store   *configstore.Store
	audit   *audit.Service
	events  *events.Service
	puller  *events.Puller
	process *process.Engine
	// attribution is the KAS Attribution store (nil = routes answer 503).
	attribution *attribution.Store
	// readiness holds the integration check of each enabled module.
	readiness map[string]ModuleReadiness
	metrics   *metricsState
	logger    *slog.Logger
	mux       *http.ServeMux
	active    atomic.Int64
	limit     int64
	stateMu   sync.Mutex
	draining  bool
	inFlight  sync.WaitGroup
}

type evaluateRequest struct {
	ContractVersion string `json:"contract_version"`
	TenantID        string `json:"tenant_id"`
	Key             string `json:"key"`
	ExpectedType    string `json:"expected_type"`
}

type successResponse struct {
	ContractVersion string          `json:"contract_version"`
	ConfigVersion   string          `json:"config_version"`
	TenantID        string          `json:"tenant_id"`
	Key             string          `json:"key"`
	Type            string          `json:"type"`
	Value           json.RawMessage `json:"value"`
}

type errorResponse struct {
	ContractVersion string `json:"contract_version"`
	Error           struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func NewHandler(store *configstore.Store, maxConcurrent int, logger *slog.Logger) *Handler {
	return newHandler(store, nil, maxConcurrent, logger)
}

func NewHandlerWithAudit(store *configstore.Store, auditService *audit.Service, maxConcurrent int, logger *slog.Logger) *Handler {
	return newHandler(store, auditService, maxConcurrent, logger)
}

func newHandler(store *configstore.Store, auditService *audit.Service, maxConcurrent int, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{store: store, audit: auditService, logger: logger, limit: int64(maxConcurrent), mux: http.NewServeMux()}
	h.mux.HandleFunc("/healthz", h.health)
	h.mux.HandleFunc("/readyz", h.ready)
	h.mux.HandleFunc("/internal/v1/config/evaluate", h.evaluate)
	h.mux.HandleFunc("/internal/v1/audit/events", h.recordAuditEvent)
	h.mux.HandleFunc("/internal/v1/events", h.publishEvent)
	h.mux.HandleFunc("/internal/v1/audit", h.readAudit)
	h.mux.HandleFunc("/internal/v1/events/deliveries/claim", h.claimDeliveries)
	h.mux.HandleFunc("/internal/v1/events/deliveries/ack", h.ackDelivery)
	h.mux.HandleFunc("/internal/v1/events/deliveries/nack", h.nackDelivery)
	h.mux.HandleFunc("/internal/v1/modules", h.getModules)
	h.registerProcessRoutes()
	h.registerAttributionRoutes()
	h.registerReportsRoutes()
	return h
}

// EnableEvents turns on event publication. Call it before serving requests.
// Without it, POST /internal/v1/events answers 503 events_unavailable.
func (h *Handler) EnableEvents(service *events.Service) { h.events = service }

// EnablePull turns on pull consumption (claim, ack, nack). Call it before
// serving requests. Without it those endpoints answer 503 events_unavailable.
func (h *Handler) EnablePull(puller *events.Puller) { h.puller = puller }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.metrics == nil {
		h.serve(w, r)
		return
	}
	route := routeLabel(r.URL.Path)
	recorder := &statusRecorder{ResponseWriter: w}
	started := time.Now()
	if route == "/metrics" {
		// Served before the concurrency limiter and the drain check so that
		// scraping keeps working exactly when the service is busy.
		h.serveMetrics(recorder, r)
	} else {
		h.serve(recorder, r)
	}
	h.metrics.observe(route, recorder.statusCode(), time.Since(started))
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	values := r.Header.Values(correlationHeader)
	correlationID := newCorrelationID()
	invalidCorrelation := len(values) > 1
	if len(values) == 1 {
		if validCorrelationID(values[0]) {
			correlationID = values[0]
		} else {
			invalidCorrelation = true
		}
	}
	w.Header().Set(correlationHeader, correlationID)
	r = r.WithContext(context.WithValue(r.Context(), correlationContextKey{}, correlationID))
	if invalidCorrelation {
		h.writeError(w, r, http.StatusBadRequest, "invalid_correlation_id", "correlation ID is invalid")
		return
	}
	h.stateMu.Lock()
	if h.draining {
		h.stateMu.Unlock()
		h.writeError(w, r, http.StatusServiceUnavailable, "server_shutting_down", "server is shutting down")
		return
	}
	if h.active.Add(1) > h.limit {
		h.active.Add(-1)
		h.stateMu.Unlock()
		h.writeError(w, r, http.StatusServiceUnavailable, "capacity_exceeded", "service is at capacity")
		return
	}
	h.inFlight.Add(1)
	h.stateMu.Unlock()
	defer func() {
		h.active.Add(-1)
		h.inFlight.Done()
	}()
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) BeginDrain() {
	h.stateMu.Lock()
	h.draining = true
	h.stateMu.Unlock()
}

// WaitForRequests waits until handlers accepted before BeginDrain have exited.
func (h *Handler) WaitForRequests() {
	h.inFlight.Wait()
}

func (h *Handler) recordAuditEvent(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.writeError(w, r, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if h.audit == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "audit_unavailable", "audit recording is unavailable")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	var submission audit.Submission
	if err := decodeStrictBody(r, maxAuditRequestBytes, &submission); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the size limit")
			return
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request JSON is invalid")
		return
	}
	serviceID := r.Header.Get("X-KAS-Service")
	token := bearerToken(r.Header.Get("Authorization"))
	if err := h.store.Authorize(serviceID, token, submission.TenantID, configstore.CapabilityAuditWrite); err != nil {
		h.writeAuthorizationError(w, r, err)
		return
	}
	result, err := h.audit.Submit(r.Context(), serviceID, submission)
	if err != nil {
		switch {
		case errors.Is(err, audit.ErrEventConflict):
			h.writeError(w, r, http.StatusConflict, "event_conflict", "event_id was already used with different content")
		case errors.Is(err, audit.ErrServiceMismatch):
			h.writeError(w, r, http.StatusForbidden, "service_identity_mismatch", "declared service does not match authenticated service")
		case errors.Is(err, audit.ErrInvalidEvent):
			h.writeError(w, r, http.StatusBadRequest, "invalid_event", "audit event fields are invalid")
		default:
			h.writeError(w, r, http.StatusServiceUnavailable, "audit_unavailable", "audit event could not be durably stored")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	status, logMessage := http.StatusCreated, "audit_event_recorded"
	if result.Duplicate {
		// Identical resend: nothing was written; the original event is returned.
		status, logMessage = http.StatusOK, "audit_event_duplicate"
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		ContractVersion string      `json:"contract_version"`
		Event           audit.Event `json:"event"`
	}{ContractVersion: audit.ContractVersion, Event: result.Event})
	h.logger.Info(logMessage, "correlation_id", CorrelationID(r.Context()), "status", status, "duration_ms", time.Since(started).Milliseconds())
}

// ready is liveness plus audit storage readiness. It never exposes storage
// details; a failing check returns 503 audit_unavailable.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && h.audit != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		err := h.audit.Ready(ctx)
		cancel()
		if err != nil {
			h.writeError(w, r, http.StatusServiceUnavailable, "audit_unavailable", "audit storage is not ready")
			return
		}
	}
	if r.Method == http.MethodGet && h.events != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		err := h.events.Ready(ctx)
		cancel()
		if err != nil {
			h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable", "event storage is not ready")
			return
		}
	}
	if r.Method == http.MethodGet && h.processReady(r.Context()) != nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "process_unavailable", "process storage is not ready")
		return
	}
	if r.Method == http.MethodGet && h.attribution != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		err := h.attribution.Ready(ctx)
		cancel()
		if err != nil {
			h.writeError(w, r, http.StatusServiceUnavailable, "attribution_unavailable", "attribution storage is not ready")
			return
		}
	}
	h.health(w, r)
}

func (h *Handler) publishEvent(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.writeError(w, r, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if h.events == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable", "event publication is unavailable")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	var submission events.Submission
	if err := decodeStrictBody(r, maxEventRequestBytes, &submission); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the size limit")
			return
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request JSON is invalid")
		return
	}
	serviceID := r.Header.Get("X-KAS-Service")
	token := bearerToken(r.Header.Get("Authorization"))
	if err := h.store.Authorize(serviceID, token, submission.TenantID, configstore.CapabilityEventsPublish); err != nil {
		h.writeAuthorizationError(w, r, err)
		return
	}
	result, err := h.events.Publish(r.Context(), serviceID, submission)
	if err != nil {
		switch {
		case errors.Is(err, events.ErrEventConflict):
			h.writeError(w, r, http.StatusConflict, "event_conflict", "event_id was already used with different content")
		case errors.Is(err, events.ErrServiceMismatch):
			h.writeError(w, r, http.StatusForbidden, "service_identity_mismatch", "declared service does not match authenticated service")
		case errors.Is(err, events.ErrUnknownEventType):
			h.writeError(w, r, http.StatusUnprocessableEntity, "unknown_event_type", "event type is not declared")
		case errors.Is(err, events.ErrPayloadTooLarge):
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "event payload exceeds the size limit")
		case errors.Is(err, events.ErrInvalidEvent):
			h.writeError(w, r, http.StatusBadRequest, "invalid_event", "event fields or payload are invalid")
		default:
			h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable", "event could not be durably stored")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	status, logMessage := http.StatusCreated, "event_published"
	if result.Duplicate {
		// Identical resend: nothing was written; the original event is returned.
		status, logMessage = http.StatusOK, "event_duplicate"
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		ContractVersion string       `json:"contract_version"`
		Event           events.Event `json:"event"`
	}{ContractVersion: events.ContractVersion, Event: result.Event})
	// Log fixed fields only: never the payload.
	h.logger.Info(logMessage, "correlation_id", CorrelationID(r.Context()), "status", status, "duration_ms", time.Since(started).Milliseconds())
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		h.writeError(w, r, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok","contract_version":"v1"}`)
}

func (h *Handler) evaluate(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.writeError(w, r, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	var req evaluateRequest
	if err := decodeStrictBody(r, maxRequestBytes, &req); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the size limit")
			return
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request JSON is invalid")
		return
	}
	if req.ContractVersion != configstore.ContractVersion || !safeIdentifier(req.TenantID) || strings.TrimSpace(req.Key) == "" || strings.TrimSpace(req.ExpectedType) == "" {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "contract version, tenant, key and type are required")
		return
	}
	serviceID := r.Header.Get("X-KAS-Service")
	token := bearerToken(r.Header.Get("Authorization"))
	if err := h.store.Authorize(serviceID, token, req.TenantID, configstore.CapabilityConfigRead); err != nil {
		h.writeAuthorizationError(w, r, err)
		return
	}
	value, err := h.store.Evaluate(req.TenantID, req.Key, req.ExpectedType)
	if err != nil {
		switch {
		case errors.Is(err, configstore.ErrUnknownKey):
			h.writeError(w, r, http.StatusUnprocessableEntity, "unknown_key", "configuration key is not supported")
		case errors.Is(err, configstore.ErrTypeMismatch):
			h.writeError(w, r, http.StatusUnprocessableEntity, "type_mismatch", "requested type does not match configuration key")
		case errors.Is(err, configstore.ErrConfigNotFound):
			h.writeError(w, r, http.StatusNotFound, "config_not_found", "configuration value was not found")
		default:
			h.writeError(w, r, http.StatusInternalServerError, "internal_error", "configuration evaluation failed")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(successResponse{
		ContractVersion: configstore.ContractVersion,
		ConfigVersion:   h.store.Version(),
		TenantID:        req.TenantID,
		Key:             req.Key,
		Type:            req.ExpectedType,
		Value:           value,
	})
	// Log only fixed event/status/duration fields. Never log request headers or body.
	h.logger.Info("config_evaluated", "correlation_id", CorrelationID(r.Context()), "status", http.StatusOK, "duration_ms", time.Since(started).Milliseconds())
}

func (h *Handler) writeAuthorizationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, configstore.ErrTenantNotFound):
		h.writeError(w, r, http.StatusNotFound, "tenant_not_found", "tenant is not registered")
	case errors.Is(err, configstore.ErrTenantInactive):
		h.writeError(w, r, http.StatusForbidden, "tenant_inactive", "tenant is inactive")
	case errors.Is(err, configstore.ErrTenantForbidden):
		h.writeError(w, r, http.StatusForbidden, "tenant_forbidden", "service is not authorized for this tenant")
	case errors.Is(err, configstore.ErrModuleNotContracted), errors.Is(err, configstore.ErrModulePaused):
		h.writeModuleError(w, r, err)
	case errors.Is(err, configstore.ErrCapabilityForbidden):
		h.writeError(w, r, http.StatusForbidden, "capability_forbidden", "service is not authorized for this capability")
	case errors.Is(err, configstore.ErrTenantRequired):
		h.writeError(w, r, http.StatusBadRequest, "tenant_required", "this service is authorized for several tenants; pass tenant_id")
	case errors.Is(err, configstore.ErrConsumerForbidden):
		h.writeError(w, r, http.StatusForbidden, "consumer_forbidden", "service is not authorized for this consumer")
	default:
		h.writeError(w, r, http.StatusUnauthorized, "unauthorized", "service authentication failed")
	}
}

func decodeStrictBody(r *http.Request, maxBytes int, destination any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBytes+1)))
	if err != nil {
		return errInvalidJSON
	}
	if len(body) > maxBytes {
		return errBodyTooLarge
	}
	if err := strictjson.RejectDuplicateKeys(body); err != nil {
		return errInvalidJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errInvalidJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errInvalidJSON
	}
	return nil
}

func bearerToken(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	h.logger.Warn("request_rejected", "correlation_id", CorrelationID(r.Context()), "status", status, "code", code)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	var response errorResponse
	response.ContractVersion = configstore.ContractVersion
	response.Error.Code = code
	response.Error.Message = message
	_ = json.NewEncoder(w).Encode(response)
}

const maxPullRequestBytes = 4 << 10

type claimRequest struct {
	ContractVersion string `json:"contract_version"`
	ConsumerID      string `json:"consumer_id"`
	MaxDeliveries   int    `json:"max_deliveries"`
	LeaseSeconds    int    `json:"lease_seconds"`
}

type settleRequest struct {
	ContractVersion string `json:"contract_version"`
	ConsumerID      string `json:"consumer_id"`
	DeliveryID      int64  `json:"delivery_id"`
	Attempt         int    `json:"attempt"`
	LeaseToken      string `json:"lease_token"`
}

type nackRequest struct {
	settleRequest
	Permanent bool `json:"permanent"`
}

// pullPrelude runs the checks shared by claim, ack and nack: method, enabled,
// media type and strict body. It returns false after writing the error.
func (h *Handler) pullPrelude(w http.ResponseWriter, r *http.Request, destination any) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.writeError(w, r, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return false
	}
	if h.puller == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable", "event consumption is unavailable")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return false
	}
	if err := decodeStrictBody(r, maxPullRequestBytes, destination); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the size limit")
			return false
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request JSON is invalid")
		return false
	}
	return true
}

func (h *Handler) authorizeConsumer(w http.ResponseWriter, r *http.Request, contractVersion, consumerID string) (configstore.ConsumerScope, bool) {
	if contractVersion != events.ContractVersion || consumerID == "" {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "contract version and consumer are required")
		return configstore.ConsumerScope{}, false
	}
	scope, err := h.store.AuthorizeConsumer(r.Header.Get("X-KAS-Service"), bearerToken(r.Header.Get("Authorization")), consumerID)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return configstore.ConsumerScope{}, false
	}
	return scope, true
}

func (h *Handler) writePullError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, events.ErrInvalidPullInput):
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request values are invalid")
	case errors.Is(err, events.ErrLeaseLost):
		h.writeError(w, r, http.StatusConflict, "lease_lost", "the lease expired or the delivery was already settled")
	default:
		h.writeError(w, r, http.StatusServiceUnavailable, "events_unavailable", "event storage is unavailable")
	}
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) claimDeliveries(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var req claimRequest
	if !h.pullPrelude(w, r, &req) {
		return
	}
	scope, ok := h.authorizeConsumer(w, r, req.ContractVersion, req.ConsumerID)
	if !ok {
		return
	}
	deliveries, err := h.puller.Claim(r.Context(), scope.ConsumerID, scope.Tenants, scope.EventTypes,
		req.MaxDeliveries, time.Duration(req.LeaseSeconds)*time.Second)
	if err != nil {
		h.writePullError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, struct {
		ContractVersion string                   `json:"contract_version"`
		Deliveries      []events.ClaimedDelivery `json:"deliveries"`
	}{events.ContractVersion, deliveries})
	// Fixed fields only: never payloads or lease tokens.
	h.logger.Info("event_deliveries_claimed", "correlation_id", CorrelationID(r.Context()), "count", len(deliveries), "duration_ms", time.Since(started).Milliseconds())
}

func (h *Handler) ackDelivery(w http.ResponseWriter, r *http.Request) {
	var req settleRequest
	if !h.pullPrelude(w, r, &req) {
		return
	}
	scope, ok := h.authorizeConsumer(w, r, req.ContractVersion, req.ConsumerID)
	if !ok {
		return
	}
	if err := h.puller.Ack(r.Context(), scope.ConsumerID, req.DeliveryID, req.Attempt, req.LeaseToken); err != nil {
		h.writePullError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, struct {
		ContractVersion string `json:"contract_version"`
		Status          string `json:"status"`
	}{events.ContractVersion, "delivered"})
	h.logger.Info("event_delivery_acked", "correlation_id", CorrelationID(r.Context()))
}

func (h *Handler) nackDelivery(w http.ResponseWriter, r *http.Request) {
	var req nackRequest
	if !h.pullPrelude(w, r, &req) {
		return
	}
	scope, ok := h.authorizeConsumer(w, r, req.ContractVersion, req.ConsumerID)
	if !ok {
		return
	}
	result, err := h.puller.Nack(r.Context(), scope.ConsumerID, req.DeliveryID, req.Attempt, req.LeaseToken, req.Permanent)
	if err != nil {
		h.writePullError(w, r, err)
		return
	}
	response := struct {
		ContractVersion string `json:"contract_version"`
		Status          string `json:"status"`
		NextAttemptAt   string `json:"next_attempt_at,omitempty"`
	}{ContractVersion: events.ContractVersion, Status: result.Status}
	if result.Status == "pending" {
		response.NextAttemptAt = result.NextAttemptAt.Format(time.RFC3339Nano)
	}
	h.writeJSON(w, http.StatusOK, response)
	h.logger.Info("event_delivery_nacked", "correlation_id", CorrelationID(r.Context()), "status", result.Status)
}
