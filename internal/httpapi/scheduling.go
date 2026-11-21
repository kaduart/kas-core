package httpapi

import (
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/scheduling"
)

// KAS Agenda routes, phase 1 (spec "Agenda genérica na KAS"): reads and pure
// calculations only. No route writes a booking yet. The company always comes
// from the caller's credentials (and, for the panel, the signed-in person's
// session): a tenant_id in the request only picks among the companies the
// caller may already serve, it never grants access.

const maxSchedulingRequestBytes = 256 << 10

// EnableScheduling turns on the Agenda routes and registers the module
// readiness check. Without it those routes answer 503 scheduling_unavailable.
func (h *Handler) EnableScheduling(store *scheduling.Store) {
	h.scheduling = store
	h.EnableModuleReadiness(configstore.ModuleScheduling, store.Missing)
}

func (h *Handler) registerSchedulingRoutes() {
	h.mux.HandleFunc("GET /internal/v1/scheduling/availability", h.schedulingAvailability)
	h.mux.HandleFunc("GET /internal/v1/scheduling/bookings", h.schedulingBookings)
	h.mux.HandleFunc("POST /internal/v1/scheduling/conflicts:check", h.schedulingConflicts)
	h.mux.HandleFunc("POST /internal/v1/scheduling/series:preview", h.schedulingSeries)
}

// schedulingTenant authorizes the caller for the capability and binds the
// panel to the person's session. It answers the error itself.
func (h *Handler) schedulingTenant(w http.ResponseWriter, r *http.Request, requested, capability string) (string, bool) {
	if h.scheduling == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "scheduling_unavailable", "scheduling is not enabled")
		return "", false
	}
	serviceID := r.Header.Get("X-KAS-Service")
	tenantID, err := h.store.ResolveTenant(serviceID, bearerToken(r.Header.Get("Authorization")), requested, capability)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return "", false
	}
	if !h.userSessionGate(w, r, serviceID, tenantID) {
		return "", false
	}
	return tenantID, true
}

func (h *Handler) writeSchedulingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, scheduling.ErrInvalid):
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request parameters are invalid")
	case errors.Is(err, scheduling.ErrNotFound):
		h.writeError(w, r, http.StatusNotFound, "not_found", "resource or service not found")
	default:
		h.logger.Error("scheduling_failed", "correlation_id", CorrelationID(r.Context()), "error", err)
		h.writeError(w, r, http.StatusServiceUnavailable, "scheduling_unavailable", "scheduling store is unavailable")
	}
}

// strictQuery accepts only the named parameters, each at most once.
func strictQuery(r *http.Request, allowed ...string) bool {
	if len(r.URL.RawQuery) > 512 {
		return false
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for name, values := range r.URL.Query() {
		if !ok[name] || len(values) != 1 {
			return false
		}
	}
	return true
}

func optionalInt(raw string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil
}

type schedulingSlotsResponse struct {
	ContractVersion string            `json:"contract_version"`
	TenantID        string            `json:"tenant_id"`
	ResourceID      string            `json:"resource_id"`
	Slots           []scheduling.Slot `json:"slots"`
}

// schedulingAvailability serves GET /internal/v1/scheduling/availability.
func (h *Handler) schedulingAvailability(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tenantID, ok := h.schedulingTenant(w, r, q.Get("tenant_id"), configstore.CapabilitySchedulingCompute)
	if !ok {
		return
	}
	duration, ok1 := optionalInt(q.Get("duration_min"))
	step, ok2 := optionalInt(q.Get("step_min"))
	if !strictQuery(r, "tenant_id", "resource_id", "service_id", "duration_min", "step_min", "from", "to") || !ok1 || !ok2 ||
		(q.Get("service_id") == "") == (duration == 0) {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "query parameters are invalid")
		return
	}
	slots, err := h.scheduling.Availability(r.Context(), tenantID, scheduling.AvailabilityQuery{
		ResourceID: q.Get("resource_id"), ServiceID: q.Get("service_id"), DurationMin: duration, StepMin: step,
		From: q.Get("from"), To: q.Get("to"), NotBefore: time.Now(),
	})
	if err != nil {
		h.writeSchedulingError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, schedulingSlotsResponse{ContractVersion: scheduling.ContractVersion, TenantID: tenantID,
		ResourceID: q.Get("resource_id"), Slots: slots})
}

type schedulingBookingsResponse struct {
	ContractVersion string               `json:"contract_version"`
	TenantID        string               `json:"tenant_id"`
	Bookings        []scheduling.Booking `json:"bookings"`
}

// schedulingBookings serves GET /internal/v1/scheduling/bookings (RFC 3339 from/to).
func (h *Handler) schedulingBookings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tenantID, ok := h.schedulingTenant(w, r, q.Get("tenant_id"), configstore.CapabilitySchedulingRead)
	if !ok {
		return
	}
	from, err1 := time.Parse(time.RFC3339, q.Get("from"))
	to, err2 := time.Parse(time.RFC3339, q.Get("to"))
	if !strictQuery(r, "tenant_id", "resource_id", "from", "to") || err1 != nil || err2 != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "query parameters are invalid")
		return
	}
	list, err := h.scheduling.ListBookings(r.Context(), tenantID, q.Get("resource_id"), from, to)
	if err != nil {
		h.writeSchedulingError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, schedulingBookingsResponse{ContractVersion: scheduling.ContractVersion, TenantID: tenantID, Bookings: list})
}

// decodeSchedulingBody reads a strict JSON body and checks the contract version.
func (h *Handler) decodeSchedulingBody(w http.ResponseWriter, r *http.Request, dst any, version func() string) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return false
	}
	if err := decodeStrictBody(r, maxSchedulingRequestBytes, dst); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the size limit")
			return false
		}
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request JSON is invalid")
		return false
	}
	if version() != scheduling.ContractVersion {
		h.writeError(w, r, http.StatusBadRequest, "unsupported_contract_version", "contract_version must be v1")
		return false
	}
	return true
}

type schedulingConflictsRequest struct {
	ContractVersion string                 `json:"contract_version"`
	TenantID        string                 `json:"tenant_id"`
	Candidates      []scheduling.Candidate `json:"candidates"`
}

type schedulingConflictsResponse struct {
	ContractVersion string                `json:"contract_version"`
	TenantID        string                `json:"tenant_id"`
	Conflicts       []scheduling.Conflict `json:"conflicts"`
}

// schedulingConflicts serves POST /internal/v1/scheduling/conflicts:check. It
// writes nothing: the answer can be outdated a moment later, which is why the
// database constraints, not this check, are what refuse a double booking.
func (h *Handler) schedulingConflicts(w http.ResponseWriter, r *http.Request) {
	var req schedulingConflictsRequest
	if !h.decodeSchedulingBody(w, r, &req, func() string { return req.ContractVersion }) {
		return
	}
	tenantID, ok := h.schedulingTenant(w, r, req.TenantID, configstore.CapabilitySchedulingCompute)
	if !ok {
		return
	}
	conflicts, err := h.scheduling.CheckConflicts(r.Context(), tenantID, req.Candidates)
	if err != nil {
		h.writeSchedulingError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, schedulingConflictsResponse{ContractVersion: scheduling.ContractVersion, TenantID: tenantID, Conflicts: conflicts})
}

type schedulingSeriesRequest struct {
	ContractVersion string                `json:"contract_version"`
	TenantID        string                `json:"tenant_id"`
	Rule            scheduling.SeriesRule `json:"rule"`
}

type schedulingSeriesResponse struct {
	ContractVersion string `json:"contract_version"`
	TenantID        string `json:"tenant_id"`
	scheduling.SeriesResult
}

// schedulingSeries serves POST /internal/v1/scheduling/series:preview with the
// tenant's zone and holidays.
func (h *Handler) schedulingSeries(w http.ResponseWriter, r *http.Request) {
	var req schedulingSeriesRequest
	if !h.decodeSchedulingBody(w, r, &req, func() string { return req.ContractVersion }) {
		return
	}
	tenantID, ok := h.schedulingTenant(w, r, req.TenantID, configstore.CapabilitySchedulingCompute)
	if !ok {
		return
	}
	settings, err := h.scheduling.GetSettings(r.Context(), tenantID)
	if err != nil {
		h.writeSchedulingError(w, r, err)
		return
	}
	res, err := scheduling.ExpandSeries(req.Rule, settings)
	if err != nil {
		h.writeSchedulingError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, schedulingSeriesResponse{ContractVersion: scheduling.ContractVersion, TenantID: tenantID, SeriesResult: res})
}
