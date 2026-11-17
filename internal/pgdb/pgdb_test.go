package pgdb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTransportPolicy(t *testing.T) {
	cases := []struct {
		name, dsn string
		wantErr   bool
	}{
		{"remote disable", "postgres://u:p@db.example.com/x?sslmode=disable", true},
		{"remote prefer (plaintext fallback)", "postgres://u:p@db.example.com/x?sslmode=prefer", true},
		{"remote allow", "postgres://u:p@db.example.com/x?sslmode=allow", true},
		{"remote ip disable", "postgres://u:p@10.0.0.5/x?sslmode=disable", true},
		{"remote require", "postgres://u:p@db.example.com/x?sslmode=require", false},
		{"remote verify-full", "postgres://u:p@db.example.com/x?sslmode=verify-full", false},
		{"one remote host without tls among several", "postgres://u:p@localhost,db.example.com/x?sslmode=disable", true},
		{"loopback disable", "postgres://u:p@127.0.0.1/x?sslmode=disable", false},
		{"localhost disable", "postgres://u:p@localhost/x?sslmode=disable", false},
		{"ipv6 loopback disable", "postgres://u:p@[::1]/x?sslmode=disable", false},
		{"unix socket", "postgres://u:p@/x?host=/var/run/postgresql", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := pgxpool.ParseConfig(tc.dsn)
			if err != nil {
				t.Fatal(err)
			}
			err = RequireSecureTransport(config)
			if (err != nil) != tc.wantErr {
				t.Fatalf("RequireSecureTransport error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestOpenRejectsBadInputWithoutLeakingSecrets(t *testing.T) {
	if _, err := Open(context.Background(), "audit", ""); err == nil {
		t.Fatal("empty connection string accepted")
	}
	_, err := Open(context.Background(), "events", "postgres://kas:segredo-ficticio@db.example.com/x?sslmode=disable")
	if !errors.Is(err, ErrTLSRequired) || strings.Contains(err.Error(), "segredo-ficticio") {
		t.Fatalf("remote plaintext error = %v", err)
	}
	_, err = Open(context.Background(), "events", "postgres://kas:segredo-ficticio@127.0.0.1:1/x?connect_timeout=1")
	if err == nil || strings.Contains(err.Error(), "segredo-ficticio") || !strings.HasPrefix(err.Error(), "events ") {
		t.Fatalf("unreachable database error = %v", err)
	}
}
