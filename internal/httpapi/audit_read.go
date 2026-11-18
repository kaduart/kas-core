package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
)

// readAuditParams are the only query parameters GET /v1/audit accepts.
var readAuditParams = map[string]bool{
	"entity_type": true, "entity_id": true, "since": true, "limit": true, "cursor": true, "tenant_id": true,
}

type auditPageResponse struct {
	ContractVersion string        `json:"contract_version"`
	Items           []audit.Event `json:"items"`
	NextCursor      string        `json:"next_cursor,omitempty"`
}

// readAudit serves GET /v1/audit. The tenant is derived from the authenticated
// service (see configstore.ResolveTenant); a free request field can never select
// another tenant, only choose among the tenants the service is itself allowed
// for. Records of any other tenant are never read: the store query is filtered by
// the resolved tenant.
func (h *Handler) readAudit(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		h.writeError(w, r, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if h.audit == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "audit_unavailable", "audit reading is unavailable")
		return
	}
	query := r.URL.Query()
	structural := len(r.URL.RawQuery) > 2048
	for name, values := range query {
		if !readAuditParams[name] || len(values) != 1 {
			structural = true // unknown or repeated parameter
		}
	}
	tenantID, err := h.store.ResolveTenant(r.Header.Get("X-KAS-Service"), bearerToken(r.Header.Get("Authorization")),
		query.Get("tenant_id"), configstore.CapabilityAuditRead)
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return
	}
	if structural {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "query parameters are invalid")
		return
	}
	q := audit.Query{
		TenantID:   tenantID,
		EntityType: query.Get("entity_type"),
		EntityID:   query.Get("entity_id"),
		Cursor:     query.Get("cursor"),
	}
	if raw := query.Get("limit"); query.Has("limit") {
		// Only plain decimal digits: no sign, spaces or fractions. 0 is rejected
		// here so that "limit=0" is an error rather than the default.
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || (len(raw) > 0 && (raw[0] < '0' || raw[0] > '9')) {
			h.writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		q.Limit = limit
	}
	if raw := query.Get("since"); query.Has("since") {
		since, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || len(raw) > 40 {
			h.writeError(w, r, http.StatusBadRequest, "invalid_request", "since must be an RFC3339 timestamp")
			return
		}
		q.Since = since
	}
	page, err := h.audit.List(r.Context(), q)
	if err != nil {
		switch {
		case errors.Is(err, audit.ErrInvalidQuery):
			h.writeError(w, r, http.StatusBadRequest, "invalid_request", "query values are invalid")
		case errors.Is(err, audit.ErrInvalidCursor):
			h.writeError(w, r, http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
		default:
			h.writeError(w, r, http.StatusServiceUnavailable, "audit_unavailable", "audit records could not be read")
		}
		return
	}
	items := page.Items
	if items == nil {
		items = []audit.Event{}
	}
	h.writeJSON(w, http.StatusOK, auditPageResponse{ContractVersion: audit.ContractVersion, Items: items, NextCursor: page.NextCursor})
	// Fixed fields only: no filters, cursors or record contents.
	h.logger.Info("audit_read", "correlation_id", CorrelationID(r.Context()), "count", len(items), "duration_ms", time.Since(started).Milliseconds())
}
