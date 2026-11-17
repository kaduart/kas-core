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

	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/strictjson"
)

const maxRequestBytes = 4 << 10
const maxAuditRequestBytes = 8 << 10

var (
	errInvalidJSON  = errors.New("invalid JSON request")
	errBodyTooLarge = errors.New("request body exceeds limit")
)

type Handler struct {
	store    *configstore.Store
	audit    *audit.Service
	logger   *slog.Logger
	mux      *http.ServeMux
	active   atomic.Int64
	limit    int64
	stateMu  sync.Mutex
	draining bool
	inFlight sync.WaitGroup
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
	h.mux.HandleFunc("/readyz", h.health)
	h.mux.HandleFunc("/internal/v1/config/evaluate", h.evaluate)
	h.mux.HandleFunc("/internal/v1/audit/events", h.recordAuditEvent)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	event, err := h.audit.Record(r.Context(), serviceID, submission)
	if err != nil {
		switch {
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
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		ContractVersion string      `json:"contract_version"`
		Event           audit.Event `json:"event"`
	}{ContractVersion: audit.ContractVersion, Event: event})
	h.logger.Info("audit_event_recorded", "correlation_id", CorrelationID(r.Context()), "status", http.StatusCreated, "duration_ms", time.Since(started).Milliseconds())
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
	case errors.Is(err, configstore.ErrCapabilityForbidden):
		h.writeError(w, r, http.StatusForbidden, "capability_forbidden", "service is not authorized for this capability")
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
