package httpapi

import (
	"errors"
	"net/http"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/identity"
)

// userSessionGate binds a data request of a panel service (one that signs
// people in, see configstore.RequiresUserSession) to the signed-in person.
// The service token proves which system is calling; the X-KAS-Session header
// proves which person, and the tenant must be one of that person's
// memberships. A panel service therefore cannot read a tenant just by naming
// it: a missing or ended session answers 401, a tenant outside the person's
// memberships answers 403 tenant_forbidden. Other services are not affected.
// It runs after the service itself was authorized for the tenant.
func (h *Handler) userSessionGate(w http.ResponseWriter, r *http.Request, serviceID, tenantID string) bool {
	_, ok := h.sessionMembership(w, r, serviceID, tenantID)
	return ok
}

// sessionPerson is the signed-in person behind a panel request and their role
// in the tenant. It is nil for a service that does not sign people in.
type sessionPerson struct {
	UserID string
	Role   string
}

// sessionMembership is userSessionGate that also returns the person and role.
func (h *Handler) sessionMembership(w http.ResponseWriter, r *http.Request, serviceID, tenantID string) (*sessionPerson, bool) {
	if !h.store.RequiresUserSession(serviceID) {
		return nil, true
	}
	if h.identity == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity storage is not enabled")
		return nil, false
	}
	token := r.Header.Get(sessionHeader)
	if token == "" {
		h.writeError(w, r, http.StatusUnauthorized, "session_required", "this service must send the person's session")
		return nil, false
	}
	// The person's memberships, limited to the tenants this service may serve.
	served, err := h.store.TenantsForCapability(serviceID, bearerToken(r.Header.Get("Authorization")), configstore.CapabilityIdentitySessions)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return nil, false
	}
	sess, err := h.identity.Lookup(r.Context(), token, serviceID, served)
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		h.writeError(w, r, http.StatusUnauthorized, "session_invalid", "the session is not valid")
		return nil, false
	case err != nil:
		h.writeError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity storage is unavailable")
		return nil, false
	}
	for _, m := range sess.Memberships {
		if m.TenantID == tenantID {
			return &sessionPerson{UserID: sess.User.ID, Role: m.Role}, true
		}
	}
	h.logger.Warn("user_session_tenant_denied", "correlation_id", CorrelationID(r.Context()), "service", serviceID,
		"user_id", sess.User.ID, "tenant_id", tenantID)
	h.writeError(w, r, http.StatusForbidden, "tenant_forbidden", "the signed-in person has no access to this tenant")
	return nil, false
}
