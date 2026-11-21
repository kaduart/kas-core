package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
)

// Module catalog routes (spec "KAS por módulos", section 8.1). The gate itself
// lives in configstore (every capability check applies it); this file serves
// the module view and audits module refusals.

// ModuleReadiness answers "is the integration ready?" for one module of one
// tenant: the list of what is still missing (empty = ready). Modules register
// one when they are enabled; without it the view omits the integration field.
type ModuleReadiness func(ctx context.Context, tenantID string) (missing []string, err error)

// EnableModuleReadiness registers the readiness check of a module. Call it
// before serving requests.
func (h *Handler) EnableModuleReadiness(module string, check ModuleReadiness) {
	if h.readiness == nil {
		h.readiness = map[string]ModuleReadiness{}
	}
	h.readiness[module] = check
}

type moduleIntegration struct {
	Ready   bool     `json:"ready"`
	Missing []string `json:"missing"`
}

type moduleView struct {
	Key                string             `json:"key"`
	Contract           string             `json:"contract"`
	DependsOn          []string           `json:"depends_on,omitempty"`
	Integration        *moduleIntegration `json:"integration,omitempty"`
	CallerCapabilities []string           `json:"caller_capabilities,omitempty"`
}

type modulesResponse struct {
	ContractVersion string       `json:"contract_version"`
	TenantID        string       `json:"tenant_id"`
	ConfigVersion   string       `json:"config_version"`
	Modules         []moduleView `json:"modules"`
}

// getModules serves GET /internal/v1/modules. Any authenticated service of an
// active tenant it is authorized for may read that tenant's view; the tenant is
// resolved exactly like the other routes (a free parameter only chooses among
// the service's own tenants).
func (h *Handler) getModules(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		h.writeError(w, r, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	query := r.URL.Query()
	structural := len(r.URL.RawQuery) > 256
	for name, values := range query {
		if name != "tenant_id" || len(values) != 1 {
			structural = true
		}
	}
	tenantID, states, err := h.store.ResolveTenantForModules(r.Header.Get("X-KAS-Service"),
		bearerToken(r.Header.Get("Authorization")), query.Get("tenant_id"))
	if err != nil {
		h.writeAuthorizationError(w, r, err)
		return
	}
	if !h.userSessionGate(w, r, r.Header.Get("X-KAS-Service"), tenantID) {
		return
	}
	if structural {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "query parameters are invalid")
		return
	}
	deps := map[string][]string{}
	for _, m := range configstore.Catalog() {
		deps[m.Key] = m.DependsOn
	}
	views := make([]moduleView, 0, len(states))
	for _, st := range states {
		v := moduleView{Key: st.Key, Contract: st.Contract, DependsOn: deps[st.Key], CallerCapabilities: st.CallerCapabilities}
		if check, ok := h.readiness[st.Key]; ok && (st.Contract == configstore.ModuleStatusActive || st.Contract == configstore.ModuleStatusPaused) {
			missing, err := check(r.Context(), tenantID)
			if err != nil {
				h.writeError(w, r, http.StatusServiceUnavailable, "module_readiness_unavailable", "module readiness could not be read")
				return
			}
			sort.Strings(missing)
			if missing == nil {
				missing = []string{}
			}
			v.Integration = &moduleIntegration{Ready: len(missing) == 0, Missing: missing}
		}
		views = append(views, v)
	}
	h.writeJSON(w, http.StatusOK, modulesResponse{
		ContractVersion: configstore.ContractVersion, TenantID: tenantID, ConfigVersion: h.store.Version(), Modules: views,
	})
	h.logger.Info("modules_read", "correlation_id", CorrelationID(r.Context()), "duration_ms", time.Since(started).Milliseconds())
}

// writeModuleError answers a module refusal and records it in the audit trail
// when audit is enabled. The service was already authenticated when the gate
// ran, so the X-KAS-Service header names a verified caller. The record holds
// identifiers only.
func (h *Handler) writeModuleError(w http.ResponseWriter, r *http.Request, err error) {
	var me *configstore.ModuleError
	if errors.As(err, &me) && h.audit != nil {
		serviceID := r.Header.Get("X-KAS-Service")
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, auditErr := h.audit.Submit(r.Context(), serviceID, audit.Submission{
			ContractVersion: audit.ContractVersion,
			EventID:         "module-denied-" + newCorrelationID(),
			TenantID:        me.TenantID,
			ServiceID:       serviceID,
			Actor:           "system.kas-core",
			Action:          "module.access_denied",
			Resource:        "module:" + me.Module,
			Result:          "denied",
			CorrelationID:   CorrelationID(r.Context()),
			OccurredAt:      now,
			EntityType:      "capability",
			EntityID:        me.Capability,
		})
		if auditErr != nil {
			h.logger.Warn("module_denial_audit_failed", "correlation_id", CorrelationID(r.Context()))
		}
	}
	if errors.Is(err, configstore.ErrModulePaused) {
		h.writeError(w, r, http.StatusForbidden, "module_paused", "the module is paused for this tenant")
		return
	}
	h.writeError(w, r, http.StatusForbidden, "module_not_contracted", "the tenant did not contract this module")
}
