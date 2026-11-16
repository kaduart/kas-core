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
    "node-a":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"]},
    "node-b":{"token_env":"TOKEN_B","tenants":["tenant-demo-b"]}
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
	if err := store.Authorize("node-a", tokenA, "tenant-demo-b"); !errors.Is(err, ErrTenantForbidden) {
		t.Fatalf("cross-tenant authorization error = %v", err)
	}
}

func TestUnauthorizedCallerAndToken(t *testing.T) {
	store, err := newTestStore(t, fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ service, token string }{{"missing", tokenA}, {"node-a", tokenB}, {"node-a", "bad"}} {
		if err := store.Authorize(tc.service, tc.token, "tenant-demo-a"); !errors.Is(err, ErrUnauthorized) {
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
	if err := store.Authorize("node-a", tokenA, "tenant-demo-a"); err != nil {
		t.Fatalf("active tenant rejected: %v", err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-demo-b"); !errors.Is(err, ErrTenantForbidden) {
		t.Fatalf("cross-tenant error = %v", err)
	}
	if err := store.Authorize("node-a", tokenA, "tenant-unknown"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("unknown tenant error = %v", err)
	}
	inactiveFixture := strings.Replace(fixture, `{"id":"tenant-demo-a","status":"active"}`, `{"id":"tenant-demo-a","status":"inactive"}`, 1)
	inactiveStore, err := newTestStore(t, inactiveFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := inactiveStore.Authorize("node-a", tokenA, "tenant-demo-a"); !errors.Is(err, ErrTenantInactive) {
		t.Fatalf("inactive tenant error = %v", err)
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
