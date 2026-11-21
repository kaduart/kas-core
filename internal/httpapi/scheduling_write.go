package httpapi

import (
	"errors"
	"net/http"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/scheduling"
)

// Agenda writes, phase 4A: create, cancel and reschedule a booking. The store
// is what protects the agenda (database constraints, per-resource lock,
// idempotency key, optimistic version, audit row in the same transaction);
// these handlers only authorize, decode and translate errors.
//
// Who may write:
//   - a service holding scheduling:write for the tenant (actor "service:<id>");
//   - a panel service, only for a signed-in person whose role in that tenant
//     is admin, gestor or atendimento (actor "user:<id>"); "leitura" gets 403.
// A tenant whose Agenda is mirrored from another system (the Fono Inova CRM
// mirror) answers 409 agenda_read_only: its agenda is written only by the
// mirror, never through these routes.

var schedulingWriterRoles = map[string]bool{"admin": true, "gestor": true, "atendimento": true}

func (h *Handler) registerSchedulingWriteRoutes() {
	h.mux.HandleFunc("POST /internal/v1/scheduling/bookings", h.schedulingCreate)
	h.mux.HandleFunc("POST /internal/v1/scheduling/bookings/{id}/cancel", h.schedulingCancel)
	h.mux.HandleFunc("POST /internal/v1/scheduling/bookings/{id}/reschedule", h.schedulingReschedule)
}

// schedulingEnabled answers 503 before the body is read when the Agenda is off.
func (h *Handler) schedulingEnabled(w http.ResponseWriter, r *http.Request) bool {
	if h.scheduling == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "scheduling_unavailable", "scheduling is not enabled")
		return false
	}
	return true
}

// schedulingWriter authorizes a write and returns the tenant and the actor
// recorded in the audit log.
func (h *Handler) schedulingWriter(w http.ResponseWriter, r *http.Request, requested string) (string, string, bool) {
	if h.scheduling == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "scheduling_unavailable", "scheduling is not enabled")
		return "", "", false
	}
	serviceID := r.Header.Get("X-KAS-Service")
	tenantID, err := h.store.ResolveTenant(serviceID, bearerToken(r.Header.Get("Authorization")), requested, configstore.CapabilitySchedulingWrite)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return "", "", false
	}
	person, ok := h.sessionMembership(w, r, serviceID, tenantID)
	if !ok {
		return "", "", false
	}
	if person == nil {
		return tenantID, "service:" + serviceID, true
	}
	if !schedulingWriterRoles[person.Role] {
		h.logger.Warn("scheduling_write_role_denied", "correlation_id", CorrelationID(r.Context()), "service", serviceID,
			"user_id", person.UserID, "tenant_id", tenantID, "role", person.Role)
		h.writeError(w, r, http.StatusForbidden, "role_forbidden", "this role may not change the agenda")
		return "", "", false
	}
	return tenantID, "user:" + person.UserID, true
}

func (h *Handler) writeSchedulingWriteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, scheduling.ErrReadOnly):
		h.writeError(w, r, http.StatusConflict, "agenda_read_only", "this agenda is mirrored from another system and cannot be changed here")
	case errors.Is(err, scheduling.ErrSlotTaken):
		h.writeError(w, r, http.StatusConflict, "slot_taken", "the resource is already booked at this time")
	case errors.Is(err, scheduling.ErrClientBusy):
		h.writeError(w, r, http.StatusConflict, "client_busy", "the client already has a booking at this time")
	case errors.Is(err, scheduling.ErrVersionConflict):
		h.writeError(w, r, http.StatusConflict, "version_conflict", "the booking changed since it was read")
	case errors.Is(err, scheduling.ErrIdempotencyMismatch):
		h.writeError(w, r, http.StatusConflict, "idempotency_mismatch", "this idempotency key was used for a different booking")
	case errors.Is(err, scheduling.ErrClosed):
		h.writeError(w, r, http.StatusUnprocessableEntity, "outside_opening_hours", "the time is outside the resource's opening hours")
	default:
		h.writeSchedulingError(w, r, err)
	}
}

type schedulingCreateRequest struct {
	ContractVersion string    `json:"contract_version"`
	TenantID        string    `json:"tenant_id"`
	IdempotencyKey  string    `json:"idempotency_key"`
	ResourceID      string    `json:"resource_id"`
	ClientRef       string    `json:"client_ref"`
	ServiceID       string    `json:"service_id"`
	Start           time.Time `json:"starts_at"`
	End             time.Time `json:"ends_at"`
	Status          string    `json:"status"`
	BufferMin       int       `json:"buffer_min"`
}

type schedulingBookingResponse struct {
	ContractVersion string             `json:"contract_version"`
	TenantID        string             `json:"tenant_id"`
	Booking         scheduling.Booking `json:"booking"`
	Replayed        bool               `json:"replayed,omitempty"`
}

// schedulingCreate serves POST /internal/v1/scheduling/bookings. The same
// idempotency key with the same body returns the booking already created
// (200, replayed); with a different body it answers 409 idempotency_mismatch.
func (h *Handler) schedulingCreate(w http.ResponseWriter, r *http.Request) {
	var req schedulingCreateRequest
	if !h.schedulingEnabled(w, r) || !h.decodeSchedulingBody(w, r, &req, func() string { return req.ContractVersion }) {
		return
	}
	tenantID, actor, ok := h.schedulingWriter(w, r, req.TenantID)
	if !ok {
		return
	}
	if req.Status != "" && req.Status != scheduling.StatusScheduled && req.Status != scheduling.StatusTentative &&
		req.Status != scheduling.StatusConfirmed {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be scheduled, tentative or confirmed")
		return
	}
	b, created, err := h.scheduling.CreateBooking(r.Context(), tenantID, scheduling.NewBooking{
		ResourceID: req.ResourceID, ClientRef: req.ClientRef, ServiceID: req.ServiceID, Start: req.Start, End: req.End,
		Status: req.Status, BufferMin: req.BufferMin, IdempotencyKey: req.IdempotencyKey, Actor: actor, RequireOpen: true,
	})
	if err != nil {
		h.writeSchedulingWriteError(w, r, err)
		return
	}
	code := http.StatusCreated
	if !created {
		code = http.StatusOK
	}
	h.logger.Info("scheduling_booking_written", "correlation_id", CorrelationID(r.Context()), "tenant_id", tenantID,
		"booking_id", b.ID, "op", "create", "replayed", !created)
	h.writeJSON(w, code, schedulingBookingResponse{ContractVersion: scheduling.ContractVersion, TenantID: tenantID, Booking: b, Replayed: !created})
}

type schedulingCancelRequest struct {
	ContractVersion string `json:"contract_version"`
	TenantID        string `json:"tenant_id"`
	ExpectedVersion int    `json:"expected_version"`
}

// schedulingCancel serves POST /internal/v1/scheduling/bookings/{id}/cancel.
// Cancelling a booking already cancelled returns it unchanged.
func (h *Handler) schedulingCancel(w http.ResponseWriter, r *http.Request) {
	var req schedulingCancelRequest
	if !h.schedulingEnabled(w, r) || !h.decodeSchedulingBody(w, r, &req, func() string { return req.ContractVersion }) {
		return
	}
	tenantID, actor, ok := h.schedulingWriter(w, r, req.TenantID)
	if !ok {
		return
	}
	b, err := h.scheduling.Cancel(r.Context(), tenantID, r.PathValue("id"), req.ExpectedVersion, actor)
	if err != nil {
		h.writeSchedulingWriteError(w, r, err)
		return
	}
	h.logger.Info("scheduling_booking_written", "correlation_id", CorrelationID(r.Context()), "tenant_id", tenantID,
		"booking_id", b.ID, "op", "cancel")
	h.writeJSON(w, http.StatusOK, schedulingBookingResponse{ContractVersion: scheduling.ContractVersion, TenantID: tenantID, Booking: b})
}

type schedulingRescheduleRequest struct {
	ContractVersion string    `json:"contract_version"`
	TenantID        string    `json:"tenant_id"`
	ExpectedVersion int       `json:"expected_version"`
	Start           time.Time `json:"starts_at"`
	End             time.Time `json:"ends_at"`
}

// schedulingReschedule serves POST /internal/v1/scheduling/bookings/{id}/reschedule.
func (h *Handler) schedulingReschedule(w http.ResponseWriter, r *http.Request) {
	var req schedulingRescheduleRequest
	if !h.schedulingEnabled(w, r) || !h.decodeSchedulingBody(w, r, &req, func() string { return req.ContractVersion }) {
		return
	}
	tenantID, actor, ok := h.schedulingWriter(w, r, req.TenantID)
	if !ok {
		return
	}
	b, err := h.scheduling.RescheduleOpen(r.Context(), tenantID, r.PathValue("id"), req.ExpectedVersion, req.Start, req.End, actor)
	if err != nil {
		h.writeSchedulingWriteError(w, r, err)
		return
	}
	h.logger.Info("scheduling_booking_written", "correlation_id", CorrelationID(r.Context()), "tenant_id", tenantID,
		"booking_id", b.ID, "op", "reschedule")
	h.writeJSON(w, http.StatusOK, schedulingBookingResponse{ContractVersion: scheduling.ContractVersion, TenantID: tenantID, Booking: b})
}
