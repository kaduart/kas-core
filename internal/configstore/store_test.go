package configstore

import (
	"errors"
	"strings"
	"testing"
)

const (
	tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

const fixture = `{
  "contract_version":"v1",
  "config_version":"fixture-1",
  "tenant_registry":[
    {"id":"tenant-demo-a","status":"active"},
    {"id":"tenant-demo-b","status":"active"}
  ],
  "key_schema":{
    "logging.max_meta_keys":{"type":"integer","min":1,"max":10},
    "ui.compact_mode":{"type":"boolean"}
  },
  "tenants":{
    "tenant-demo-a":{"logging.max_meta_keys":10,"ui.compact_mode":true},
    "tenant-demo-b":{"logging.max_meta_keys":4,"ui.compact_mode":false}
  },
  "callers":{
    "node-a":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["config:read","audit:write"]},
    "node-b":{"token_env":"TOKEN_B","tenants":["tenant-demo-b"],"capabilities":["config:read","audit:write"]}
  }
}`

func newTestStore(t *testing.T, raw string) (*Store, error) {
	t.Helper()
	return Load(strings.NewReader(raw), func(name string) (string, bool) {
		switch name {
		case "TOKEN_A":
			return tokenA, true
		case "TOKEN_B":
			return tokenB, true
		default:
			return "", false
		}
	})
}

func TestEvaluateAndTenantIsolation(t *testing.T) {
	store, err := newTestStore(t, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Evaluate("tenant-demo-a", "logging.max_meta_keys", "integer"); err != nil || string(got) != "10" {
		t.Fatalf("tenant A got %s, err %v", got, err)
	}
	if got, err := store.Evaluate("tenant-demo-b", "logging.max_meta_keys", "integer"); err != nil || string(got) != "4" {
		t.Fatalf("tenant B got %s, err %v", got, err)
	}
	if got, err := store.Evaluate("tenant-demo-a", "ui.compact_mode", "boolean"); err != nil || string(got) != "true" {
		t.Fatalf("boolean setting got %s, err %v", got, err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-b", CapabilityConfigRead); !errors.Is(err, ErrTenantForbidden) {
		t.Fatalf("cross-tenant authorization error = %v", err)
	}
}

func TestUnauthorizedCallerAndToken(t *testing.T) {
	store, err := newTestStore(t, fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ service, token string }{{"missing", tokenA}, {"node-a", tokenB}, {"node-a", "bad"}} {
		if err := store.Authorize(tc.service, tc.token, "tenant-demo-a", CapabilityConfigRead); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("Authorize(%q) = %v, want unauthorized", tc.service, err)
		}
	}
}

func TestInvalidConfigurationRejected(t *testing.T) {
	cases := map[string]string{
		"wrong value type":             strings.Replace(fixture, `"logging.max_meta_keys":10`, `"logging.max_meta_keys":"10"`, 1),
		"unknown key":                  strings.Replace(fixture, `"logging.max_meta_keys":10`, `"unknown.key":10`, 1),
		"out of range":                 strings.Replace(fixture, `"logging.max_meta_keys":10`, `"logging.max_meta_keys":11`, 1),
		"bad version":                  strings.Replace(fixture, `"contract_version":"v1"`, `"contract_version":"v2"`, 1),
		"invalid range":                strings.Replace(fixture, `"max":10`, `"max":0`, 1),
		"unsupported type":             strings.Replace(fixture, `"type":"integer"`, `"type":"object"`, 1),
		"duplicate top-level field":    strings.Replace(fixture, `"config_version":"fixture-1",`, `"config_version":"fixture-1","config_version":"fixture-2",`, 1),
		"duplicate schema key":         strings.Replace(fixture, `"key_schema":{`, `"key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10},`, 1),
		"duplicate tenant value":       strings.Replace(fixture, `"tenant-demo-a":{"logging.max_meta_keys":10,`, `"tenant-demo-a":{"logging.max_meta_keys":9,"logging.max_meta_keys":10,`, 1),
		"duplicate tenant registry ID": strings.Replace(fixture, `{"id":"tenant-demo-b","status":"active"}`, `{"id":"tenant-demo-a","status":"active"}`, 1),
		"invalid tenant status":        strings.Replace(fixture, `"status":"active"`, `"status":"paused"`, 1),
		"unregistered config tenant":   strings.Replace(fixture, `"tenant-demo-b":{"logging.max_meta_keys":4`, `"tenant-unknown":{"logging.max_meta_keys":4`, 1),
		"unregistered caller tenant":   strings.Replace(fixture, `"tenants":["tenant-demo-a"]`, `"tenants":["tenant-unknown"]`, 1),
		"unknown caller capability":    strings.Replace(fixture, `"capabilities":["config:read","audit:write"]`, `"capabilities":["admin"]`, 1),
		"legacy config capability":     strings.Replace(fixture, `"capabilities":["config:read","audit:write"]`, `"capabilities":["config","audit:write"]`, 1),
		"legacy audit capability":      strings.Replace(fixture, `"capabilities":["config:read","audit:write"]`, `"capabilities":["config:read","audit"]`, 1),
		"duplicate caller capability":  strings.Replace(fixture, `"capabilities":["config:read","audit:write"]`, `"capabilities":["config:read","config:read"]`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newTestStore(t, raw); err == nil {
				t.Fatal("expected invalid config error")
			}
		})
	}
}

func TestTenantRegistryAuthorizationStates(t *testing.T) {
	activeFixture := fixture
	store, err := newTestStore(t, activeFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityConfigRead); err != nil {
		t.Fatalf("active tenant rejected: %v", err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-b", CapabilityConfigRead); !errors.Is(err, ErrTenantForbidden) {
		t.Fatalf("cross-tenant error = %v", err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-unknown", CapabilityConfigRead); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("unknown tenant error = %v", err)
	}
	inactiveFixture := strings.Replace(fixture, `{"id":"tenant-demo-a","status":"active"}`, `{"id":"tenant-demo-a","status":"inactive"}`, 1)
	inactiveStore, err := newTestStore(t, inactiveFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := inactiveStore.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityConfigRead); !errors.Is(err, ErrTenantInactive) {
		t.Fatalf("inactive tenant error = %v", err)
	}
}

func TestCapabilitiesDefaultDenyAndRequireExplicitGrant(t *testing.T) {
	withoutCapabilities := strings.Replace(fixture, `,"capabilities":["config:read","audit:write"]`, ``, 1)
	store, err := newTestStore(t, withoutCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityConfigRead); !errors.Is(err, ErrCapabilityForbidden) {
		t.Fatalf("missing capability was not denied: %v", err)
	}

	configOnly := strings.Replace(fixture, `"capabilities":["config:read","audit:write"]`, `"capabilities":["config:read"]`, 1)
	store, err = newTestStore(t, configOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityConfigRead); err != nil {
		t.Fatalf("explicit config:read capability rejected: %v", err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityAuditWrite); !errors.Is(err, ErrCapabilityForbidden) {
		t.Fatalf("missing audit:write capability was not denied: %v", err)
	}

	auditOnly := strings.Replace(fixture, `"capabilities":["config:read","audit:write"]`, `"capabilities":["audit:write"]`, 1)
	store, err = newTestStore(t, auditOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityAuditWrite); err != nil {
		t.Fatalf("explicit audit:write capability rejected: %v", err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityConfigRead); !errors.Is(err, ErrCapabilityForbidden) {
		t.Fatalf("missing config:read capability was not denied: %v", err)
	}
}

func TestTypeAndMissingKeyErrors(t *testing.T) {
	store, err := newTestStore(t, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Evaluate("tenant-demo-a", "logging.max_meta_keys", "string"); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("type error = %v", err)
	}
	if _, err := store.Evaluate("unknown-tenant", "logging.max_meta_keys", "integer"); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("missing tenant error = %v", err)
	}
}

func TestLoadRejectsOversizedConfiguration(t *testing.T) {
	if _, err := newTestStore(t, strings.Repeat(" ", (1<<20)+1)); err == nil {
		t.Fatal("expected configuration over 1 MiB to be rejected")
	}
}

const eventsFixture = `{
  "contract_version":"v1",
  "config_version":"fixture-events",
  "tenant_registry":[
    {"id":"tenant-demo-a","status":"active"},
    {"id":"tenant-demo-b","status":"active"}
  ],
  "key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},
  "tenants":{"tenant-demo-a":{"logging.max_meta_keys":10}},
  "event_types":["settings.updated","settings.checked"],
  "event_consumers":{
    "consumer-a":{"event_types":["settings.updated"],"tenants":["tenant-demo-a"]},
    "consumer-ab":{"event_types":["settings.updated","settings.checked"],"tenants":["tenant-demo-a","tenant-demo-b"]}
  },
  "callers":{
    "node-a":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["events:publish"]}
  }
}`

func TestEventTypesConsumersAndTenantMatching(t *testing.T) {
	store, err := newTestStore(t, eventsFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !store.EventTypeDeclared("settings.updated") || store.EventTypeDeclared("settings.deleted") {
		t.Fatal("declared event types are not reported correctly")
	}
	cases := []struct {
		tenant, eventType string
		want              string
	}{
		{"tenant-demo-a", "settings.updated", "consumer-a,consumer-ab"},
		{"tenant-demo-a", "settings.checked", "consumer-ab"},
		{"tenant-demo-b", "settings.updated", "consumer-ab"}, // consumer-a is not listed for tenant B
		{"tenant-demo-b", "settings.checked", "consumer-ab"},
		{"tenant-demo-a", "settings.deleted", ""},
	}
	for _, tc := range cases {
		if got := strings.Join(store.ConsumersFor(tc.tenant, tc.eventType), ","); got != tc.want {
			t.Fatalf("ConsumersFor(%s, %s) = %q, want %q", tc.tenant, tc.eventType, got, tc.want)
		}
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityEventsPublish); err != nil {
		t.Fatalf("events:publish must be accepted: %v", err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityAuditWrite); !errors.Is(err, ErrCapabilityForbidden) {
		t.Fatalf("a service with only events:publish must not write audit: %v", err)
	}
}

func TestConfigurationWithoutEventsStillLoads(t *testing.T) {
	store, err := newTestStore(t, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if store.EventTypeDeclared("settings.updated") || len(store.ConsumersFor("tenant-demo-a", "settings.updated")) != 0 {
		t.Fatal("no event types or consumers should exist when none are declared")
	}
}

func TestInvalidEventConfigurationIsRejected(t *testing.T) {
	mutations := map[string]func(string) string{
		"undeclared type in consumer": func(s string) string {
			return strings.Replace(s, `"event_types":["settings.updated"],"tenants":["tenant-demo-a"]`, `"event_types":["settings.unknown"],"tenants":["tenant-demo-a"]`, 1)
		},
		"unknown tenant in consumer": func(s string) string {
			return strings.Replace(s, `"tenants":["tenant-demo-a"]}`, `"tenants":["tenant-ghost"]}`, 1)
		},
		"consumer without tenants": func(s string) string {
			return strings.Replace(s, `"tenants":["tenant-demo-a"]}`, `"tenants":[]}`, 1)
		},
		"duplicate event type": func(s string) string {
			return strings.Replace(s, `["settings.updated","settings.checked"]`, `["settings.updated","settings.updated"]`, 1)
		},
		"invalid event type": func(s string) string {
			return strings.Replace(s, `"settings.checked"]`, `"Settings Checked"]`, 1)
		},
		"invalid consumer id": func(s string) string {
			return strings.Replace(s, `"consumer-a"`, `"Consumer A!"`, 1)
		},
		"unknown field": func(s string) string {
			return strings.Replace(s, `"event_consumers"`, `"event_consumerz"`, 1)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := newTestStore(t, mutate(eventsFixture)); err == nil {
				t.Fatal("invalid event configuration was accepted")
			}
		})
	}
}

const pullFixture = `{
  "contract_version":"v1","config_version":"fixture-pull",
  "tenant_registry":[
    {"id":"tenant-demo-a","status":"active"},
    {"id":"tenant-demo-b","status":"active"},
    {"id":"tenant-demo-c","status":"inactive"}
  ],
  "key_schema":{"k.v":{"type":"integer"}},
  "tenants":{"tenant-demo-a":{"k.v":1}},
  "event_types":["settings.updated","settings.checked"],
  "event_consumers":{
    "pull-a":{"event_types":["settings.updated"],"tenants":["tenant-demo-a","tenant-demo-b","tenant-demo-c"],"service":"node-a"},
    "pull-b":{"event_types":["settings.checked"],"tenants":["tenant-demo-b"],"service":"node-b"},
    "in-process":{"event_types":["settings.updated"],"tenants":["tenant-demo-a"]}
  },
  "callers":{
    "node-a":{"token_env":"TOKEN_A","tenants":["tenant-demo-a","tenant-demo-c"],"capabilities":["events:consume"]},
    "node-b":{"token_env":"TOKEN_B","tenants":["tenant-demo-b"],"capabilities":["events:consume"]},
    "node-pub":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["events:publish"]}
  }
}`

func TestAuthorizeConsumerScopeIsRestrictedToTenantsAndTypes(t *testing.T) {
	store, err := newTestStore(t, pullFixture)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := store.AuthorizeConsumer("node-a", tokenA, "pull-a")
	if err != nil {
		t.Fatal(err)
	}
	// consumer lists a, b and c; the service only has a and c; c is inactive.
	if strings.Join(scope.Tenants, ",") != "tenant-demo-a" || strings.Join(scope.EventTypes, ",") != "settings.updated" {
		t.Fatalf("scope = %+v", scope)
	}
	if scope, err := store.AuthorizeConsumer("node-b", tokenB, "pull-b"); err != nil || strings.Join(scope.Tenants, ",") != "tenant-demo-b" {
		t.Fatalf("node-b scope = %+v, %v", scope, err)
	}
}

func TestAuthorizeConsumerRejections(t *testing.T) {
	store, err := newTestStore(t, pullFixture)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name                 string
		service, token, cons string
		want                 error
	}{
		{"unknown service", "ghost", tokenA, "pull-a", ErrUnauthorized},
		{"wrong token", "node-a", tokenB, "pull-a", ErrUnauthorized},
		{"malformed token", "node-a", "short", "pull-a", ErrUnauthorized},
		{"no consume capability", "node-pub", tokenA, "pull-a", ErrCapabilityForbidden},
		{"consumer of another service", "node-a", tokenA, "pull-b", ErrConsumerForbidden},
		{"unknown consumer", "node-a", tokenA, "pull-ghost", ErrConsumerForbidden},
		{"in-process consumer cannot be pulled", "node-a", tokenA, "in-process", ErrConsumerForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.AuthorizeConsumer(tc.service, tc.token, tc.cons); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestConsumerBindingMustReferenceAServiceWithTheConsumeCapability(t *testing.T) {
	for name, mutate := range map[string]func(string) string{
		"unknown service": func(s string) string {
			return strings.Replace(s, `"service":"node-b"`, `"service":"ghost"`, 1)
		},
		"service without events:consume": func(s string) string {
			return strings.Replace(s, `"service":"node-b"`, `"service":"node-pub"`, 1)
		},
		"invalid service id": func(s string) string {
			return strings.Replace(s, `"service":"node-b"`, `"service":"Bad Service!"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newTestStore(t, mutate(pullFixture)); err == nil {
				t.Fatal("invalid consumer binding was accepted")
			}
		})
	}
}

const rotationFixture = `{
  "contract_version":"v1","config_version":"fixture-rotation",
  "tenant_registry":[{"id":"tenant-demo-a","status":"active"}],
  "key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},
  "tenants":{"tenant-demo-a":{"logging.max_meta_keys":10}},
  "callers":{
    "node-a":{"token_env":"TOKEN_A","token_env_next":"TOKEN_B","tenants":["tenant-demo-a"],"capabilities":["config:read"]},
    "node-plain":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["config:read"]}
  }
}`

const tokenC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func TestRotationAcceptsBothTokensAndRejectsOthers(t *testing.T) {
	store, err := newTestStore(t, rotationFixture)
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"primary": tokenA, "next": tokenB} {
		if err := store.Authorize("node-a", token, "tenant-demo-a", CapabilityConfigRead); err != nil {
			t.Fatalf("%s token must be accepted during rotation: %v", name, err)
		}
	}
	if err := store.Authorize("node-a", tokenC, "tenant-demo-a", CapabilityConfigRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an unknown token must be rejected: %v", err)
	}
	if err := store.Authorize("node-a", "short", "tenant-demo-a", CapabilityConfigRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a malformed token must be rejected: %v", err)
	}
	// A service without a next token accepts only its own.
	if err := store.Authorize("node-plain", tokenB, "tenant-demo-a", CapabilityConfigRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("node-plain must not accept the other service's next token: %v", err)
	}
	if strings.Join(store.RotatingServices(), ",") != "node-a" {
		t.Fatalf("rotating services = %v", store.RotatingServices())
	}
}

func TestTokenSlotUsageCountsWithoutIdentifyingTheCaller(t *testing.T) {
	store, err := newTestStore(t, rotationFixture)
	if err != nil {
		t.Fatal(err)
	}
	store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityConfigRead)
	store.Authorize("node-a", tokenA, "tenant-demo-a", CapabilityConfigRead)
	store.Authorize("node-a", tokenB, "tenant-demo-a", CapabilityConfigRead)
	store.Authorize("node-a", tokenC, "tenant-demo-a", CapabilityConfigRead) // rejected: not counted
	if primary, next := store.TokenSlotUsage(); primary != 2 || next != 1 {
		t.Fatalf("usage = %d primary, %d next", primary, next)
	}
}

func TestRotationAppliesToConsumerAuthorizationToo(t *testing.T) {
	cfg := strings.Replace(pullFixture, `"token_env":"TOKEN_A","tenants":["tenant-demo-a","tenant-demo-c"]`, `"token_env":"TOKEN_A","token_env_next":"TOKEN_B","tenants":["tenant-demo-a","tenant-demo-c"]`, 1)
	store, err := newTestStore(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{tokenA, tokenB} {
		if _, err := store.AuthorizeConsumer("node-a", token, "pull-a"); err != nil {
			t.Fatalf("consumer authorization must accept both tokens: %v", err)
		}
	}
	if _, err := store.AuthorizeConsumer("node-a", tokenC, "pull-a"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown token: %v", err)
	}
}

func TestInvalidRotationConfigurationIsRejected(t *testing.T) {
	for name, mutate := range map[string]func(string) string{
		"same variable": func(s string) string {
			return strings.Replace(s, `"token_env_next":"TOKEN_B"`, `"token_env_next":"TOKEN_A"`, 1)
		},
		"same token in two variables": func(s string) string {
			return strings.Replace(s, `"token_env_next":"TOKEN_B"`, `"token_env_next":"TOKEN_A2"`, 1)
		},
		"missing variable": func(s string) string {
			return strings.Replace(s, `"token_env_next":"TOKEN_B"`, `"token_env_next":"TOKEN_MISSING"`, 1)
		},
		"invalid variable name": func(s string) string {
			return strings.Replace(s, `"token_env_next":"TOKEN_B"`, `"token_env_next":"bad name"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(strings.NewReader(mutate(rotationFixture)), func(n string) (string, bool) {
				switch n {
				case "TOKEN_A", "TOKEN_A2":
					return tokenA, true
				case "TOKEN_B":
					return tokenB, true
				}
				return "", false
			})
			if err == nil {
				t.Fatal("invalid rotation configuration was accepted")
			}
			if strings.Contains(err.Error(), tokenA) || strings.Contains(err.Error(), tokenB) {
				t.Fatalf("error leaks a token: %v", err)
			}
		})
	}
}

const resolveFixture = `{
  "contract_version":"v1","config_version":"fixture-resolve",
  "tenant_registry":[{"id":"tenant-demo-a","status":"active"},{"id":"tenant-demo-b","status":"active"},{"id":"tenant-demo-c","status":"inactive"}],
  "key_schema":{"k.v":{"type":"integer"}},"tenants":{"tenant-demo-a":{"k.v":1}},
  "callers":{
    "one":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["audit:read"]},
    "many":{"token_env":"TOKEN_B","tenants":["tenant-demo-a","tenant-demo-b","tenant-demo-c"],"capabilities":["audit:read"]},
    "writer":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["audit:write"]}
  }
}`

func TestResolveTenantComesFromTheServiceAuthorization(t *testing.T) {
	store, err := newTestStore(t, resolveFixture)
	if err != nil {
		t.Fatal(err)
	}
	if tenant, err := store.ResolveTenant("one", tokenA, "", CapabilityAuditRead); err != nil || tenant != "tenant-demo-a" {
		t.Fatalf("single-tenant service = %q, %v", tenant, err)
	}
	if tenant, err := store.ResolveTenant("one", tokenA, "tenant-demo-a", CapabilityAuditRead); err != nil || tenant != "tenant-demo-a" {
		t.Fatalf("restating the own tenant = %q, %v", tenant, err)
	}
	cases := []struct {
		name                          string
		service, token, tenant, capab string
		want                          error
	}{
		{"single-tenant asks for another tenant", "one", tokenA, "tenant-demo-b", CapabilityAuditRead, ErrTenantForbidden},
		{"multi-tenant must select", "many", tokenB, "", CapabilityAuditRead, ErrTenantRequired},
		{"multi-tenant picks one it has", "many", tokenB, "tenant-demo-b", CapabilityAuditRead, nil},
		{"multi-tenant picks an unregistered tenant", "many", tokenB, "tenant-demo-z", CapabilityAuditRead, ErrTenantNotFound},
		{"multi-tenant picks an inactive tenant", "many", tokenB, "tenant-demo-c", CapabilityAuditRead, ErrTenantInactive},
		{"missing capability", "writer", tokenA, "", CapabilityAuditRead, ErrCapabilityForbidden},
		{"write does not imply read", "writer", tokenA, "tenant-demo-a", CapabilityAuditRead, ErrCapabilityForbidden},
		{"wrong token", "one", tokenB, "", CapabilityAuditRead, ErrUnauthorized},
		{"unknown service", "ghost", tokenA, "", CapabilityAuditRead, ErrUnauthorized},
		{"malformed token", "one", "short", "", CapabilityAuditRead, ErrUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenant, err := store.ResolveTenant(tc.service, tc.token, tc.tenant, tc.capab)
			if !errors.Is(err, tc.want) || (tc.want == nil && (err != nil || tenant == "")) {
				t.Fatalf("ResolveTenant = %q, %v; want %v", tenant, err, tc.want)
			}
			if tc.want != nil && tenant != "" {
				t.Fatalf("a refused request must return no tenant, got %q", tenant)
			}
		})
	}
}

func TestResolveTenantChecksTheTokenOnce(t *testing.T) {
	store, err := newTestStore(t, resolveFixture)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.ResolveTenant("one", tokenA, "", CapabilityAuditRead); err != nil {
			t.Fatal(err)
		}
	}
	if primary, next := store.TokenSlotUsage(); primary != 3 || next != 0 {
		t.Fatalf("token checks = %d primary, %d next; each request must count once", primary, next)
	}
}

func TestAuditReadIsAKnownCapability(t *testing.T) {
	if _, err := newTestStore(t, resolveFixture); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(resolveFixture, `"audit:read"]`, `"audit:read","audit:read"]`, 1)
	if _, err := newTestStore(t, bad); err == nil {
		t.Fatal("a duplicate audit:read must be rejected")
	}
}
