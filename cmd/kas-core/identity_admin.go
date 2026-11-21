package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"kas-core-pilot/internal/identity"
)

type identityAdmin struct {
	addEmail, setEmail, name, memberships, passwordEnv, dsnEnv string
}

// runIdentityAdmin creates a panel user or replaces a password, then exits.
// The password is read from an environment variable so it never shows up in
// the process list or the shell history. Migrations are applied if pending.
func runIdentityAdmin(logger *slog.Logger, a identityAdmin) int {
	if a.addEmail != "" && a.setEmail != "" {
		logger.Error("use -identity-add-user or -identity-set-password, not both")
		return 2
	}
	password, found := os.LookupEnv(a.passwordEnv)
	if !found || password == "" {
		logger.Error("password variable is not set", "env", a.passwordEnv)
		return 2
	}
	dsn, found := os.LookupEnv(a.dsnEnv)
	if !found || dsn == "" {
		logger.Error("database connection variable is not set", "env", a.dsnEnv)
		return 2
	}
	ctx := context.Background()
	store, err := identity.Open(ctx, dsn, true)
	if err != nil {
		logger.Error("identity store unavailable", "reason", err.Error())
		return 1
	}
	defer store.Close()

	if a.setEmail != "" {
		if err := store.SetPassword(ctx, a.setEmail, password); err != nil {
			logger.Error("set password failed", "reason", err.Error())
			return 1
		}
		logger.Info("password replaced; open sessions ended")
		return 0
	}

	type pair struct{ tenant, role string }
	var pairs []pair
	for _, item := range strings.Split(a.memberships, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		tenant, role, ok := strings.Cut(item, ":")
		if !ok {
			logger.Error("invalid -identity-memberships; use tenant:role,tenant:role")
			return 2
		}
		pairs = append(pairs, pair{strings.TrimSpace(tenant), strings.TrimSpace(role)})
	}
	if len(pairs) == 0 {
		logger.Error("-identity-add-user needs at least one tenant in -identity-memberships")
		return 2
	}
	user, err := store.CreateUser(ctx, a.addEmail, a.name, password)
	switch {
	case errors.Is(err, identity.ErrUserExists):
		logger.Info("user already exists; memberships are updated, password is kept")
	case err != nil:
		logger.Error("create user failed", "reason", err.Error())
		return 1
	default:
		logger.Info("user created", "user_id", user.ID)
	}
	for _, p := range pairs {
		if err := store.SetMembership(ctx, a.addEmail, p.tenant, p.role); err != nil {
			logger.Error("membership failed", "tenant_id", p.tenant, "reason", err.Error())
			return 1
		}
		logger.Info("membership set", "tenant_id", p.tenant, "role", p.role)
	}
	return 0
}
