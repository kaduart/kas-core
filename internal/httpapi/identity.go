package httpapi

import (
	"errors"
	"net/http"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/identity"
)

// Identity routes (contract/v1/identity.md): a panel service signs a person in,
// reads the session on every request and signs out. The panel authenticates as
// itself (X-KAS-Service + Bearer) and must hold identity:sessions; the person's
// session token travels in X-KAS-Session. E-mails never reach the logs.

const sessionHeader = "X-KAS-Session"

// EnableIdentity turns the identity routes on. Call it before serving requests.
func (h *Handler) EnableIdentity(store *identity.Store) { h.identity = store }

func (h *Handler) registerIdentityRoutes() {
	h.mux.HandleFunc("POST /internal/v1/identity/sessions", h.identityLogin)
	h.mux.HandleFunc("GET /internal/v1/identity/session", h.identitySession)
	h.mux.HandleFunc("DELETE /internal/v1/identity/session", h.identityLogout)
}

type loginRequest struct {
	ContractVersion string `json:"contract_version"`
	Email           string `json:"email"`
	Password        string `json:"password"`
}

type sessionResponse struct {
	ContractVersion string                `json:"contract_version"`
	SessionToken    string                `json:"session_token,omitempty"`
	ExpiresAt       time.Time             `json:"expires_at"`
	User            identity.User         `json:"user"`
	Memberships     []identity.Membership `json:"memberships"`
}

// identityCaller authenticates the panel service and returns the tenants it
// may act for. ok=false means a response was already written.
func (h *Handler) identityCaller(w http.ResponseWriter, r *http.Request) (string, []string, bool) {
	if h.identity == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity storage is not enabled")
		return "", nil, false
	}
	serviceID := r.Header.Get("X-KAS-Service")
	tenants, err := h.store.TenantsForCapability(serviceID, bearerToken(r.Header.Get("Authorization")), configstore.CapabilityIdentitySessions)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return "", nil, false
	}
	return serviceID, tenants, true
}

func (h *Handler) writeIdentityError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidCredentials):
		h.writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "e-mail or password is wrong")
	case errors.Is(err, identity.ErrLocked):
		w.Header().Set("Retry-After", "900")
		h.writeError(w, r, http.StatusTooManyRequests, "login_locked", "too many wrong passwords; try again later")
	case errors.Is(err, identity.ErrNoTenantAccess):
		h.writeError(w, r, http.StatusForbidden, "no_tenant_access", "this person has no access to the companies of this panel")
	case errors.Is(err, identity.ErrSessionInvalid):
		h.writeError(w, r, http.StatusUnauthorized, "session_invalid", "session is missing, expired or ended")
	default:
		h.logger.Error("identity_failure", "correlation_id", CorrelationID(r.Context()), "reason", err.Error())
		h.writeError(w, r, http.StatusServiceUnavailable, "identity_unavailable", "identity storage is unavailable")
	}
}

func (h *Handler) identityLogin(w http.ResponseWriter, r *http.Request) {
	serviceID, tenants, ok := h.identityCaller(w, r)
	if !ok {
		return
	}
	var req loginRequest
	if err := decodeStrictBody(r, maxRequestBytes, &req); err != nil || req.ContractVersion != configstore.ContractVersion {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	sess, err := h.identity.Login(r.Context(), req.Email, req.Password, serviceID, tenants)
	if err != nil {
		h.logger.Info("identity_login_refused", "correlation_id", CorrelationID(r.Context()), "service", serviceID, "code", err.Error())
		h.writeIdentityError(w, r, err)
		return
	}
	h.logger.Info("identity_login", "correlation_id", CorrelationID(r.Context()), "service", serviceID, "user_id", sess.User.ID)
	h.writeJSON(w, http.StatusCreated, sessionResponse{
		ContractVersion: configstore.ContractVersion, SessionToken: sess.Token,
		ExpiresAt: sess.ExpiresAt, User: sess.User, Memberships: sess.Memberships,
	})
}

func (h *Handler) identitySession(w http.ResponseWriter, r *http.Request) {
	serviceID, tenants, ok := h.identityCaller(w, r)
	if !ok {
		return
	}
	sess, err := h.identity.Lookup(r.Context(), r.Header.Get(sessionHeader), serviceID, tenants)
	if err != nil {
		h.writeIdentityError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, sessionResponse{
		ContractVersion: configstore.ContractVersion, ExpiresAt: sess.ExpiresAt, User: sess.User, Memberships: sess.Memberships,
	})
}

func (h *Handler) identityLogout(w http.ResponseWriter, r *http.Request) {
	serviceID, _, ok := h.identityCaller(w, r)
	if !ok {
		return
	}
	if err := h.identity.Revoke(r.Context(), r.Header.Get(sessionHeader), serviceID); err != nil {
		h.writeIdentityError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
