package configstore

import (
	"bytes"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kas-core-pilot/internal/strictjson"
	"regexp"
	"strings"
)

const ContractVersion = "v1"

var (
	ErrUnauthorized    = errors.New("unauthorized")
	ErrTenantForbidden = errors.New("tenant_forbidden")
	ErrUnknownKey      = errors.New("unknown_key")
	ErrTypeMismatch    = errors.New("type_mismatch")
	ErrConfigNotFound  = errors.New("config_not_found")
	ErrTenantNotFound  = errors.New("tenant_not_found")
	ErrTenantInactive  = errors.New("tenant_inactive")
	tenantIDPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	serviceIDPattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	envNamePattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	keyPattern         = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)*$`)
)

type rawConfig struct {
	ContractVersion string                                `json:"contract_version"`
	ConfigVersion   string                                `json:"config_version"`
	TenantRegistry  []tenantRegistration                  `json:"tenant_registry"`
	KeySchema       map[string]KeyRule                    `json:"key_schema"`
	Tenants         map[string]map[string]json.RawMessage `json:"tenants"`
	Callers         map[string]rawCaller                  `json:"callers"`
}

type tenantRegistration struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// KeyRule describes the type and optional numeric bounds of one allowed key.
// Rules are data-driven so the core does not contain application-specific keys.
type KeyRule struct {
	Type string `json:"type"`
	Min  *int64 `json:"min,omitempty"`
	Max  *int64 `json:"max,omitempty"`
}

type rawCaller struct {
	TokenEnv string   `json:"token_env"`
	Tenants  []string `json:"tenants"`
}

type caller struct {
	token   []byte
	tenants map[string]struct{}
}

type Store struct {
	version string
	tenants map[string]bool
	rules   map[string]KeyRule
	values  map[string]map[string]json.RawMessage
	callers map[string]caller
}

// Load parses and validates all non-secret data, then loads only the referenced
// caller tokens from the provided environment lookup. Token values are never
// retained in config JSON or included in errors.
func Load(r io.Reader, lookupEnv func(string) (string, bool)) (*Store, error) {
	if lookupEnv == nil {
		return nil, errors.New("environment lookup is required")
	}
	const maxConfigurationBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(r, maxConfigurationBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	if len(data) > maxConfigurationBytes {
		return nil, errors.New("configuration exceeds 1 MiB")
	}
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return nil, fmt.Errorf("invalid JSON configuration: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw rawConfig
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("configuration must contain one JSON value")
	}
	if raw.ContractVersion != ContractVersion {
		return nil, errors.New("unsupported configuration contract_version")
	}
	if strings.TrimSpace(raw.ConfigVersion) == "" || len(raw.ConfigVersion) > 80 {
		return nil, errors.New("config_version is required and must be at most 80 characters")
	}
	if len(raw.KeySchema) == 0 || len(raw.TenantRegistry) == 0 || len(raw.Callers) == 0 {
		return nil, errors.New("at least one key rule, registered tenant and caller are required")
	}

	store := &Store{
		version: raw.ConfigVersion,
		tenants: make(map[string]bool, len(raw.TenantRegistry)),
		rules:   raw.KeySchema,
		values:  make(map[string]map[string]json.RawMessage, len(raw.Tenants)),
		callers: make(map[string]caller, len(raw.Callers)),
	}
	for _, tenant := range raw.TenantRegistry {
		if !tenantIDPattern.MatchString(tenant.ID) {
			return nil, errors.New("invalid tenant identifier in tenant_registry")
		}
		if _, duplicate := store.tenants[tenant.ID]; duplicate {
			return nil, errors.New("duplicate tenant identifier in tenant_registry")
		}
		active := false
		switch tenant.Status {
		case "active":
			active = true
		case "inactive":
		default:
			return nil, fmt.Errorf("invalid status for tenant %q", tenant.ID)
		}
		store.tenants[tenant.ID] = active
	}
	for key, rule := range store.rules {
		if !keyPattern.MatchString(key) {
			return nil, errors.New("invalid key in key_schema")
		}
		if err := validateRule(rule); err != nil {
			return nil, fmt.Errorf("invalid rule for key %q: %w", key, err)
		}
	}
	for tenantID, tenantValues := range raw.Tenants {
		if !tenantIDPattern.MatchString(tenantID) {
			return nil, errors.New("invalid tenant ID in configuration")
		}
		if _, registered := store.tenants[tenantID]; !registered {
			return nil, fmt.Errorf("configuration values reference unregistered tenant %q", tenantID)
		}
		if len(tenantValues) == 0 {
			return nil, errors.New("tenant configuration must contain at least one key")
		}
		values := make(map[string]json.RawMessage, len(tenantValues))
		for key, rawValue := range tenantValues {
			rule, supported := store.rules[key]
			if !supported {
				return nil, fmt.Errorf("tenant %q has a value for an undeclared key %q", tenantID, key)
			}
			if err := validateValue(rule, rawValue); err != nil {
				return nil, fmt.Errorf("invalid value for key %q in tenant %q: %w", key, tenantID, err)
			}
			values[key] = append(json.RawMessage(nil), rawValue...)
		}
		store.values[tenantID] = values
	}

	for serviceID, rawCaller := range raw.Callers {
		if !serviceIDPattern.MatchString(serviceID) || !envNamePattern.MatchString(rawCaller.TokenEnv) {
			return nil, errors.New("invalid service caller configuration")
		}
		if len(rawCaller.Tenants) == 0 {
			return nil, errors.New("service caller must be authorized for at least one tenant")
		}
		token, found := lookupEnv(rawCaller.TokenEnv)
		if !found || !validToken(token) {
			return nil, fmt.Errorf("service token environment variable %s must contain a 32-byte hex token", rawCaller.TokenEnv)
		}
		authorizedTenants := make(map[string]struct{}, len(rawCaller.Tenants))
		for _, tenantID := range rawCaller.Tenants {
			if _, exists := store.tenants[tenantID]; !exists {
				return nil, errors.New("service caller references an unknown tenant")
			}
			if _, duplicate := authorizedTenants[tenantID]; duplicate {
				return nil, errors.New("duplicate tenant in service caller authorization")
			}
			authorizedTenants[tenantID] = struct{}{}
		}
		tokenBytes, _ := hex.DecodeString(token)
		store.callers[serviceID] = caller{token: tokenBytes, tenants: authorizedTenants}
	}
	return store, nil
}

func validToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func (s *Store) Version() string { return s.version }

func validateRule(rule KeyRule) error {
	switch rule.Type {
	case "integer":
		if rule.Min != nil && rule.Max != nil && *rule.Min > *rule.Max {
			return errors.New("min must not exceed max")
		}
	case "boolean", "string":
		if rule.Min != nil || rule.Max != nil {
			return errors.New("min/max are only supported for integer rules")
		}
	default:
		return errors.New("type must be integer, boolean or string")
	}
	return nil
}

func validateValue(rule KeyRule, rawValue json.RawMessage) error {
	switch rule.Type {
	case "integer":
		var value int64
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return errors.New("value must be an integer")
		}
		if rule.Min != nil && value < *rule.Min || rule.Max != nil && value > *rule.Max {
			return errors.New("integer value is outside the configured range")
		}
	case "boolean":
		var value bool
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return errors.New("value must be a boolean")
		}
	case "string":
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return errors.New("value must be a string")
		}
	}
	return nil
}

func (s *Store) Authorize(serviceID, token, tenantID string) error {
	c, exists := s.callers[serviceID]
	if !exists || !validToken(token) {
		return ErrUnauthorized
	}
	provided, _ := hex.DecodeString(token)
	if subtle.ConstantTimeCompare(c.token, provided) != 1 {
		return ErrUnauthorized
	}
	active, registered := s.tenants[tenantID]
	if !registered {
		return ErrTenantNotFound
	}
	if !active {
		return ErrTenantInactive
	}
	if _, allowed := c.tenants[tenantID]; !allowed {
		return ErrTenantForbidden
	}
	return nil
}

func (s *Store) Evaluate(tenantID, key, expectedType string) (json.RawMessage, error) {
	rule, exists := s.rules[key]
	if !exists {
		return nil, ErrUnknownKey
	}
	if expectedType != rule.Type {
		return nil, ErrTypeMismatch
	}
	tenantValues, exists := s.values[tenantID]
	if !exists {
		return nil, ErrConfigNotFound
	}
	value, exists := tenantValues[key]
	if !exists {
		return nil, ErrConfigNotFound
	}
	return append(json.RawMessage(nil), value...), nil
}
