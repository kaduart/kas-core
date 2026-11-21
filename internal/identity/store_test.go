package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"kas-core-pilot/internal/pgtest"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), pgtest.SchemaDSN(t), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestPasswordHash(t *testing.T) {
	h, err := hashWith("senha-forte-123", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("senha-forte-123", h) || VerifyPassword("senha-forte-124", h) {
		t.Fatal("verify mismatch")
	}
	for _, bad := range []string{"", "x$1$a$b", "pbkdf2-sha256$abc$a$b", h + "x"} {
		if VerifyPassword("senha-forte-123", bad) {
			t.Fatalf("malformed hash %q matched", bad)
		}
	}
	if checkPasswordPolicy("curta") == nil {
		t.Fatal("short password accepted")
	}
}

func TestLoginLookupRevoke(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, " Ana@Escola.Exemplo ", "Ana Souza", "senha-forte-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "ana@escola.exemplo", "Outra", "senha-forte-123"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate e-mail: %v", err)
	}
	if err := s.SetMembership(ctx, "ana@escola.exemplo", "escola-demo", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMembership(ctx, "ana@escola.exemplo", "fono-inova", "leitura"); err != nil {
		t.Fatal(err)
	}

	// The panel may act only for escola-demo: fono-inova is cut out.
	allowed := []string{"escola-demo"}
	sess, err := s.Login(ctx, "ANA@escola.exemplo", "senha-forte-123", "kas-web", allowed)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Token == "" || len(sess.Memberships) != 1 || sess.Memberships[0] != (Membership{"escola-demo", "admin"}) {
		t.Fatalf("session %+v", sess)
	}
	got, err := s.Lookup(ctx, sess.Token, "kas-web", allowed)
	if err != nil || got.User.Email != "ana@escola.exemplo" {
		t.Fatalf("lookup %+v %v", got, err)
	}
	// Bound to the issuing service.
	if _, err := s.Lookup(ctx, sess.Token, "outro-servico", allowed); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("other service: %v", err)
	}
	// No visible tenant: refused.
	if _, err := s.Login(ctx, "ana@escola.exemplo", "senha-forte-123", "kas-web", []string{"outra"}); !errors.Is(err, ErrNoTenantAccess) {
		t.Fatalf("no tenant: %v", err)
	}
	if err := s.Revoke(ctx, sess.Token, "kas-web"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, sess.Token, "kas-web", allowed); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked: %v", err)
	}
	if err := s.Revoke(ctx, sess.Token, "kas-web"); err != nil {
		t.Fatalf("revoke twice: %v", err)
	}
	// Expiry.
	sess2, _ := s.Login(ctx, "ana@escola.exemplo", "senha-forte-123", "kas-web", allowed)
	s.now = func() time.Time { return time.Now().Add(SessionTTL + time.Minute) }
	if _, err := s.Lookup(ctx, sess2.Token, "kas-web", allowed); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expired: %v", err)
	}
}

func TestLockoutAndPasswordReset(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "bia@escola.exemplo", "Bia", "senha-forte-123"); err != nil {
		t.Fatal(err)
	}
	_ = s.SetMembership(ctx, "bia@escola.exemplo", "escola-demo", "gestor")
	allowed := []string{"escola-demo"}
	if _, err := s.Login(ctx, "ninguem@escola.exemplo", "x", "kas-web", allowed); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown e-mail: %v", err)
	}
	for i := 0; i < MaxFailedLogins; i++ {
		if _, err := s.Login(ctx, "bia@escola.exemplo", "errada-123456", "kas-web", allowed); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Login(ctx, "bia@escola.exemplo", "senha-forte-123", "kas-web", allowed); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected lock, got %v", err)
	}
	old, _ := s.Login(ctx, "bia@escola.exemplo", "senha-forte-123", "kas-web", allowed) // still locked: no session
	if old.Token != "" {
		t.Fatal("session while locked")
	}
	if err := s.SetPassword(ctx, "bia@escola.exemplo", "nova-senha-456"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "bia@escola.exemplo", "nova-senha-456", "kas-web", allowed); err != nil {
		t.Fatalf("after reset: %v", err)
	}
}
