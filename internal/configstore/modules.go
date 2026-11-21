package configstore

import (
	"errors"
	"fmt"
	"sort"
)

// Module catalog (spec "KAS por módulos", section 8.1). The catalog is fixed in
// code; which tenant contracted which module is configuration. A module only
// says which capabilities it guards: it carries no business rule.

// Attribution and Reports capabilities. Their routes arrive in later changes;
// declaring them now lets the catalog and the gate be tested on their own.
const (
	CapabilityAttributionImport = "attribution:import"
	CapabilityAttributionRead   = "attribution:read"
	CapabilityReportsRead       = "reports:read"
)

const (
	ModuleAttribution = "attribution"
	ModuleReports     = "reports"
	ModuleProcess     = "process"
	ModuleCRM         = "crm"
	ModuleOmnichannel = "omnichannel"
	ModuleAI          = "ai"
)

// Module contract states for one tenant.
const (
	ModuleStatusActive  = "active"
	ModuleStatusPaused  = "paused"
	ModuleStatusAbsent  = "absent"
	ModuleStatusPlanned = "planned"
)

var (
	// ErrModuleNotContracted: the capability belongs to a module the tenant did
	// not contract.
	ErrModuleNotContracted = errors.New("module_not_contracted")
	// ErrModulePaused: the tenant contracted the module but it is paused. Data is
	// kept; only use is refused.
	ErrModulePaused = errors.New("module_paused")
)

// ModuleError carries which module refused the request, so the HTTP layer can
// audit the refusal. errors.Is matches ErrModuleNotContracted or ErrModulePaused.
type ModuleError struct {
	Module     string
	TenantID   string
	Capability string
	reason     error
}

func (e *ModuleError) Error() string { return e.reason.Error() }
func (e *ModuleError) Unwrap() error { return e.reason }

// CatalogModule describes one entry of the fixed catalog.
type CatalogModule struct {
	Key          string
	Activatable  bool
	Capabilities []string
	DependsOn    []string
	// LegacyOpen marks a module whose capabilities existed before the catalog
	// (process). For a tenant without a "modules" block they keep working as
	// before; a tenant with the block must contract the module.
	LegacyOpen bool
}

var catalog = []CatalogModule{
	{Key: ModuleAttribution, Activatable: true, Capabilities: []string{CapabilityAttributionImport, CapabilityAttributionRead}},
	{Key: ModuleReports, Activatable: true, Capabilities: []string{CapabilityReportsRead}, DependsOn: []string{ModuleAttribution}},
	{Key: ModuleProcess, Activatable: true, LegacyOpen: true, Capabilities: []string{
		CapabilityProcessDefine, CapabilityProcessRun, CapabilityProcessTask, CapabilityProcessCommand, CapabilityProcessEvent}},
	{Key: ModuleCRM},
	{Key: ModuleOmnichannel},
	{Key: ModuleAI},
}

var (
	catalogByKey       = map[string]CatalogModule{}
	moduleOfCapability = map[string]string{}
)

func init() {
	for _, m := range catalog {
		catalogByKey[m.Key] = m
		for _, c := range m.Capabilities {
			moduleOfCapability[c] = m.Key
		}
	}
}

// Catalog returns a copy of the fixed catalog, in display order.
func Catalog() []CatalogModule {
	out := make([]CatalogModule, len(catalog))
	for i, m := range catalog {
		m.Capabilities = append([]string(nil), m.Capabilities...)
		m.DependsOn = append([]string(nil), m.DependsOn...)
		out[i] = m
	}
	return out
}

// ModuleOfCapability returns the module that guards a capability, or "" for a
// core capability (config, audit, events) that no module gates.
func ModuleOfCapability(capability string) string { return moduleOfCapability[capability] }

func knownCapability(capability string) bool {
	switch capability {
	case CapabilityConfigRead, CapabilityAuditWrite, CapabilityAuditRead, CapabilityEventsPublish, CapabilityEventsConsume, CapabilityIdentitySessions:
		return true
	}
	_, ok := moduleOfCapability[capability]
	return ok
}

type rawModule struct {
	Status string `json:"status"`
}

// loadModules validates the "modules" block. It runs after the tenant registry
// and the callers are loaded, because it checks both.
func (s *Store) loadModules(raw map[string]map[string]rawModule) error {
	s.modules = make(map[string]map[string]string, len(raw))
	for tenantID, mods := range raw {
		if _, registered := s.tenants[tenantID]; !registered {
			return fmt.Errorf("modules reference unregistered tenant %q", tenantID)
		}
		states := make(map[string]string, len(mods))
		for key, m := range mods {
			entry, known := catalogByKey[key]
			if !known {
				return fmt.Errorf("tenant %q: unknown module %q", tenantID, key)
			}
			if !entry.Activatable {
				return fmt.Errorf("tenant %q: module %q is planned and cannot be contracted yet", tenantID, key)
			}
			switch m.Status {
			case ModuleStatusActive, ModuleStatusPaused:
			default:
				return fmt.Errorf("tenant %q: module %q status must be active or paused", tenantID, key)
			}
			states[key] = m.Status
		}
		for key, status := range states {
			for _, dep := range catalogByKey[key].DependsOn {
				depStatus, ok := states[dep]
				if !ok {
					return fmt.Errorf("tenant %q: module %q requires module %q", tenantID, key, dep)
				}
				if status == ModuleStatusActive && depStatus != ModuleStatusActive {
					return fmt.Errorf("tenant %q: module %q is active but its dependency %q is not", tenantID, key, dep)
				}
			}
		}
		s.modules[tenantID] = states
	}
	// A caller may only hold a module capability for tenants that contracted
	// that module (active or paused). Legacy-open modules are exempt for
	// tenants without a "modules" block.
	for serviceID, c := range s.callers {
		for capability := range c.capabilities {
			module := moduleOfCapability[capability]
			if module == "" {
				continue
			}
			for tenantID := range c.tenants {
				states, hasBlock := s.modules[tenantID]
				if !hasBlock && catalogByKey[module].LegacyOpen {
					continue
				}
				if _, contracted := states[module]; !contracted {
					return fmt.Errorf("service caller %q holds %s for tenant %q, which did not contract module %q",
						serviceID, capability, tenantID, module)
				}
			}
		}
	}
	return nil
}

// moduleGate applies the contract state of the module that guards capability.
func (s *Store) moduleGate(tenantID, capability string) error {
	module := moduleOfCapability[capability]
	if module == "" {
		return nil
	}
	states, hasBlock := s.modules[tenantID]
	if !hasBlock && catalogByKey[module].LegacyOpen {
		return nil
	}
	switch states[module] {
	case ModuleStatusActive:
		return nil
	case ModuleStatusPaused:
		return &ModuleError{Module: module, TenantID: tenantID, Capability: capability, reason: ErrModulePaused}
	default:
		return &ModuleError{Module: module, TenantID: tenantID, Capability: capability, reason: ErrModuleNotContracted}
	}
}

// ModuleState is the contract view of one catalog module for one tenant.
type ModuleState struct {
	Key      string
	Contract string // active, paused, absent or planned
	// CallerCapabilities lists the capabilities of this module held by the
	// asking service for this tenant (what "this system may use" means).
	CallerCapabilities []string
}

// HasModulesBlock reports whether the tenant declares a "modules" block.
func (s *Store) HasModulesBlock(tenantID string) bool {
	_, ok := s.modules[tenantID]
	return ok
}

// ModuleStatus returns the contract state of one module for one tenant, with
// the same legacy rule the gate applies.
func (s *Store) ModuleStatus(tenantID, module string) string {
	entry, known := catalogByKey[module]
	if !known {
		return ModuleStatusAbsent
	}
	if !entry.Activatable {
		return ModuleStatusPlanned
	}
	states, hasBlock := s.modules[tenantID]
	if !hasBlock {
		if entry.LegacyOpen {
			return ModuleStatusActive
		}
		return ModuleStatusAbsent
	}
	if status, ok := states[module]; ok {
		return status
	}
	return ModuleStatusAbsent
}

// ContractedModules lists, per tenant, the modules declared in configuration
// (sorted), for persisting state changes. Tenants without a block are absent.
func (s *Store) ContractedModules() map[string]map[string]string {
	out := make(map[string]map[string]string, len(s.modules))
	for tenantID, states := range s.modules {
		copyStates := make(map[string]string, len(states))
		for k, v := range states {
			copyStates[k] = v
		}
		out[tenantID] = copyStates
	}
	return out
}

// ResolveTenantForModules authenticates the service and selects the tenant like
// ResolveTenant, but needs no particular capability: any authorized service of
// an active tenant may read that tenant's module states. It returns the view of
// every catalog module.
func (s *Store) ResolveTenantForModules(serviceID, token, requested string) (string, []ModuleState, error) {
	c, exists := s.callers[serviceID]
	if !exists || !validToken(token) || !s.tokenMatches(c, token) {
		return "", nil, ErrUnauthorized
	}
	tenantID := requested
	if tenantID == "" {
		if len(c.tenants) != 1 {
			return "", nil, ErrTenantRequired
		}
		for only := range c.tenants {
			tenantID = only
		}
	}
	active, registered := s.tenants[tenantID]
	if !registered {
		return "", nil, ErrTenantNotFound
	}
	if !active {
		return "", nil, ErrTenantInactive
	}
	if _, allowed := c.tenants[tenantID]; !allowed {
		return "", nil, ErrTenantForbidden
	}
	states := make([]ModuleState, 0, len(catalog))
	for _, m := range catalog {
		st := ModuleState{Key: m.Key, Contract: s.ModuleStatus(tenantID, m.Key)}
		if st.Contract == ModuleStatusActive || st.Contract == ModuleStatusPaused {
			for _, capability := range m.Capabilities {
				if _, held := c.capabilities[capability]; held {
					st.CallerCapabilities = append(st.CallerCapabilities, capability)
				}
			}
			sort.Strings(st.CallerCapabilities)
		}
		states = append(states, st)
	}
	return tenantID, states, nil
}
