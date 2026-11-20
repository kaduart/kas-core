package configstore

import (
	"errors"
	"strings"
	"testing"
)

// Fictional tenants: fono-demo has no "modules" block (legacy), escola-demo
// contracts attribution + reports, loja-demo has attribution paused.
const modulesFixture = `{
  "contract_version":"v1",
  "config_version":"modules-1",
  "tenant_registry":[
    {"id":"fono-demo","status":"active"},
    {"id":"escola-demo","status":"active"},
    {"id":"loja-demo","status":"active"}
  ],
  "key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},
  "modules":{
    "escola-demo":{"attribution":{"status":"active"},"reports":{"status":"active"}},
    "loja-demo":{"attribution":{"status":"paused"},"process":{"status":"active"}}
  },
  "callers":{
    "fono-node":{"token_env":"TOKEN_A","tenants":["fono-demo"],"capabilities":["config:read","process:run"]},
    "escola-sis":{"token_env":"TOKEN_B","tenants":["escola-demo"],"capabilities":["attribution:import","attribution:read","reports:read"]},
    "loja-sis":{"token_env":"TOKEN_C","tenants":["loja-demo"],"capabilities":["attribution:read","process:run"]}
  }
}`

func loadModules(t *testing.T, raw string) (*Store, error) {
	t.Helper()
	return Load(strings.NewReader(raw), func(name string) (string, bool) {
		switch name {
		case "TOKEN_A":
			return tokenA, true
		case "TOKEN_B":
			return tokenB, true
		case "TOKEN_C":
			return tokenC, true
		}
		return "", false
	})
}

func TestModuleGate(t *testing.T) {
	s, err := loadModules(t, modulesFixture)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, service, token, tenant, capability string
		want                                     error
	}{
		{"legacy tenant keeps process", "fono-node", tokenA, "fono-demo", CapabilityProcessRun, nil},
		{"core capability is never gated", "fono-node", tokenA, "fono-demo", CapabilityConfigRead, nil},
		{"active module", "escola-sis", tokenB, "escola-demo", CapabilityAttributionImport, nil},
		{"active reports", "escola-sis", tokenB, "escola-demo", CapabilityReportsRead, nil},
		{"paused module refused", "loja-sis", tokenC, "loja-demo", CapabilityAttributionRead, ErrModulePaused},
		{"process contracted in block", "loja-sis", tokenC, "loja-demo", CapabilityProcessRun, nil},
		{"other tenant still forbidden first", "escola-sis", tokenB, "fono-demo", CapabilityAttributionRead, ErrTenantForbidden},
		{"capability not held comes before module", "escola-sis", tokenB, "escola-demo", CapabilityProcessRun, ErrCapabilityForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Authorize(tc.service, tc.token, tc.tenant, tc.capability)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	var me *ModuleError
	if err := s.Authorize("loja-sis", tokenC, "loja-demo", CapabilityAttributionRead); !errors.As(err, &me) || me.Module != ModuleAttribution || me.TenantID != "loja-demo" {
		t.Fatalf("module error detail: %v", err)
	}
	if _, err := s.ResolveTenant("loja-sis", tokenC, "", CapabilityAttributionRead); !errors.Is(err, ErrModulePaused) {
		t.Fatalf("ResolveTenant must apply the gate, got %v", err)
	}
}

func TestModuleNotContracted(t *testing.T) {
	// A tenant with a block that omits process loses process, even though a
	// legacy tenant keeps it. The caller cannot even be configured with it.
	raw := strings.Replace(modulesFixture, `"loja-demo":{"attribution":{"status":"paused"},"process":{"status":"active"}}`,
		`"loja-demo":{"attribution":{"status":"paused"}}`, 1)
	if _, err := loadModules(t, raw); err == nil || !strings.Contains(err.Error(), `did not contract module "process"`) {
		t.Fatalf("expected refusal of process capability without the module, got %v", err)
	}
}

func TestModulesConfigValidation(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"planned module", `"reports":{"status":"active"}},`, `"reports":{"status":"active"},"crm":{"status":"active"}},`, "planned"},
		{"unknown module", `"reports":{"status":"active"}},`, `"reports":{"status":"active"},"billing":{"status":"active"}},`, "unknown module"},
		{"bad status", `"reports":{"status":"active"}},`, `"reports":{"status":"on"}},`, "active or paused"},
		{"missing dependency", `"escola-demo":{"attribution":{"status":"active"},"reports":{"status":"active"}}`, `"escola-demo":{"reports":{"status":"active"}}`, "requires module"},
		{"dependency paused", `"escola-demo":{"attribution":{"status":"active"},`, `"escola-demo":{"attribution":{"status":"paused"},`, "dependency"},
		{"unregistered tenant", `"loja-demo":{"attribution"`, `"ghost":{"attribution":{"status":"active"}},"loja-demo":{"attribution"`, "unregistered tenant"},
		{"capability of uncontracted module", `"capabilities":["config:read","process:run"]}`, `"capabilities":["config:read","process:run","attribution:read"]}`, `did not contract module "attribution"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(modulesFixture, tc.from, tc.to, 1)
			if raw == modulesFixture {
				t.Fatal("fixture replacement did not apply")
			}
			if _, err := loadModules(t, raw); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
	// Duplicate module keys are refused by the strict JSON reader.
	dup := strings.Replace(modulesFixture, `"reports":{"status":"active"}},`, `"reports":{"status":"active"},"reports":{"status":"paused"}},`, 1)
	if _, err := loadModules(t, dup); err == nil {
		t.Fatal("duplicate module key must be refused")
	}
}

func TestModuleStatesView(t *testing.T) {
	s, err := loadModules(t, modulesFixture)
	if err != nil {
		t.Fatal(err)
	}
	tenant, states, err := s.ResolveTenantForModules("escola-sis", tokenB, "")
	if err != nil || tenant != "escola-demo" {
		t.Fatalf("tenant %q err %v", tenant, err)
	}
	got := map[string]ModuleState{}
	for _, st := range states {
		got[st.Key] = st
	}
	if len(states) != len(Catalog()) {
		t.Fatalf("every catalog module must be listed, got %d", len(states))
	}
	if st := got[ModuleAttribution]; st.Contract != ModuleStatusActive || strings.Join(st.CallerCapabilities, ",") != "attribution:import,attribution:read" {
		t.Fatalf("attribution: %+v", st)
	}
	if got[ModuleProcess].Contract != ModuleStatusAbsent || got[ModuleCRM].Contract != ModuleStatusPlanned {
		t.Fatalf("process %+v crm %+v", got[ModuleProcess], got[ModuleCRM])
	}
	_, legacy, err := s.ResolveTenantForModules("fono-node", tokenA, "fono-demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range legacy {
		if st.Key == ModuleProcess && (st.Contract != ModuleStatusActive || len(st.CallerCapabilities) != 1) {
			t.Fatalf("legacy process view: %+v", st)
		}
		if st.Key == ModuleAttribution && st.Contract != ModuleStatusAbsent {
			t.Fatalf("legacy attribution view: %+v", st)
		}
	}
	if _, _, err := s.ResolveTenantForModules("escola-sis", tokenB, "fono-demo"); !errors.Is(err, ErrTenantForbidden) {
		t.Fatalf("cross-tenant module view must be forbidden, got %v", err)
	}
	if _, _, err := s.ResolveTenantForModules("escola-sis", tokenA, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token: %v", err)
	}
}

func TestCatalogIntegrity(t *testing.T) {
	seen := map[string]string{}
	for _, m := range Catalog() {
		if !m.Activatable && len(m.Capabilities) > 0 {
			t.Fatalf("planned module %q must not guard capabilities", m.Key)
		}
		for _, c := range m.Capabilities {
			if other, dup := seen[c]; dup {
				t.Fatalf("capability %s in %s and %s", c, other, m.Key)
			}
			seen[c] = m.Key
			if ModuleOfCapability(c) != m.Key {
				t.Fatalf("lookup mismatch for %s", c)
			}
		}
		for _, d := range m.DependsOn {
			if _, ok := catalogByKey[d]; !ok {
				t.Fatalf("module %q depends on unknown %q", m.Key, d)
			}
		}
	}
	for _, core := range []string{CapabilityConfigRead, CapabilityAuditWrite, CapabilityAuditRead, CapabilityEventsPublish, CapabilityEventsConsume} {
		if ModuleOfCapability(core) != "" {
			t.Fatalf("core capability %s must not belong to a module", core)
		}
	}
}
