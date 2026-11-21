// Package pgdb opens PostgreSQL connection pools for KAS modules with one shared
// transport-security policy and bounded pool settings.
package pgdb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// OpenTimeout bounds connecting, pinging and (in callers) migrating.
	OpenTimeout = 10 * time.Second
	maxConns    = 8
	// MaxConnsExplicit bounds an explicit pool_max_conns in the connection
	// string. Without it the pool stays at maxConns, as before.
	MaxConnsExplicit = 32
)

// ErrTLSRequired is returned for a non-loopback host that could connect without TLS.
var ErrTLSRequired = errors.New("TLS is required for a non-loopback database host; use sslmode=require, verify-ca or verify-full")

// Open parses the connection string, enforces the transport policy, creates a
// pool and verifies connectivity. label (for example "audit") only prefixes
// errors; the connection string is never included in them.
func Open(ctx context.Context, label, connString string) (*pgxpool.Pool, error) {
	if strings.TrimSpace(connString) == "" {
		return nil, fmt.Errorf("%s database connection string is required", label)
	}
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("%s database connection string is invalid", label)
	}
	if err := RequireSecureTransport(config); err != nil {
		return nil, err
	}
	config.MaxConns = boundedMaxConns(connString, config.MaxConns)
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("%s database connection could not be created", label)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("%s database is unreachable", label)
	}
	return pool, nil
}

// RequireSecureTransport rejects any connection attempt to a non-loopback host
// that could run without TLS (sslmode=disable, allow or prefer). Modes require,
// verify-ca and verify-full pass; verify-full is recommended because require
// encrypts without authenticating the server.
func RequireSecureTransport(config *pgxpool.Config) error {
	cc := config.ConnConfig
	if !isLocalHost(cc.Host) && cc.TLSConfig == nil {
		return ErrTLSRequired
	}
	for _, fallback := range cc.Fallbacks {
		if !isLocalHost(fallback.Host) && fallback.TLSConfig == nil {
			return ErrTLSRequired
		}
	}
	return nil
}

func isLocalHost(host string) bool {
	if host == "" || strings.HasPrefix(host, "/") || strings.EqualFold(host, "localhost") {
		return true // empty means the default local socket
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// boundedMaxConns keeps the historical pool of 8 unless the connection string
// sets pool_max_conns explicitly; an explicit value is honoured within
// [2, MaxConnsExplicit].
func boundedMaxConns(connString string, parsed int32) int32 {
	explicit := strings.Contains(connString, "pool_max_conns")
	switch {
	case explicit && parsed >= 2 && parsed <= MaxConnsExplicit:
		return parsed
	case explicit && parsed > MaxConnsExplicit:
		return MaxConnsExplicit
	case !explicit && parsed >= 2 && parsed <= maxConns:
		return parsed
	default:
		return maxConns
	}
}
