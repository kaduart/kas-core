// Package identity keeps the people who sign in to a KAS panel: users,
// their memberships (tenant + role) and their sessions.
//
// A panel is a service (it authenticates with its own token and holds the
// identity:sessions capability). It forwards the person's e-mail and password
// once, receives an opaque session token and from then on sends that token.
// A session is bound to the service that created it, and the tenants it shows
// are always cut down to the tenants that service may act for.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kas-core-pilot/internal/pgdb"
	"kas-core-pilot/internal/pgmigrate"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrator = pgmigrate.Migrator{
	FS:     migrationFiles,
	Dir:    "migrations",
	Table:  "identity_schema_migrations",
	LockID: 0x4b41534944454e, // "KASIDEN"
}

const (
	dbTimeout = 15 * time.Second
	// SessionTTL is how long a session lasts after sign-in (no sliding renewal).
	SessionTTL = 12 * time.Hour
	// MaxFailedLogins wrong passwords in a row lock the account for LockDuration.
	MaxFailedLogins = 5
	LockDuration    = 15 * time.Minute
)

// Roles a person can have in a tenant.
var Roles = []string{"admin", "gestor", "atendimento", "leitura"}

var (
	ErrInvalidCredentials = errors.New("invalid_credentials")
	ErrLocked             = errors.New("login_locked")
	ErrNoTenantAccess     = errors.New("no_tenant_access")
	ErrSessionInvalid     = errors.New("session_invalid")
	ErrUserExists         = errors.New("user_exists")
	ErrUserNotFound       = errors.New("user_not_found")
	ErrInvalidInput       = errors.New("invalid_input")
)

// dummyHash is verified when the e-mail is unknown, so that an unknown e-mail
// costs the same time as a wrong password.
var dummyHash = sync.OnceValue(func() string {
	h, err := hashWith("kas-identity-dummy-password", pbkdf2Iterations)
	if err != nil {
		panic(err)
	}
	return h
})

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type Membership struct {
	TenantID string `json:"tenant_id"`
	Role     string `json:"role"`
}

// Session is what a panel learns about a signed-in person. Token is set only
// by Login; it is never stored, only its SHA-256 digest is.
type Session struct {
	Token       string       `json:"-"`
	ExpiresAt   time.Time    `json:"expires_at"`
	User        User         `json:"user"`
	Memberships []Membership `json:"memberships"`
}

type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func Open(ctx context.Context, connString string, applyMigrations bool) (*Store, error) {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "identity", connString)
	if err != nil {
		return nil, err
	}
	if applyMigrations {
		err = migrator.Apply(ctx, pool)
	} else {
		err = migrator.Verify(ctx, pool)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("identity database schema: %w", err)
	}
	return &Store{pool: pool, now: time.Now}, nil
}

func Migrate(ctx context.Context, connString string) error {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "identity", connString)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrator.Apply(ctx, pool)
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ready(ctx context.Context) error { return s.pool.Ping(ctx) }

// NormalizeEmail lower-cases and validates an e-mail address.
func NormalizeEmail(email string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	if len(e) < 3 || len(e) > 254 {
		return "", ErrInvalidInput
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e {
		return "", ErrInvalidInput
	}
	return e, nil
}

func validRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func tokenDigest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// CreateUser adds an active user. The e-mail must be new.
func (s *Store) CreateUser(ctx context.Context, email, name, password string) (User, error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return User{}, ErrInvalidInput
	}
	if err := checkPasswordPolicy(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	u := User{ID: "usr_" + randomHex(12), Email: e, Name: name}
	now := s.now().UTC()
	tag, err := s.pool.Exec(ctx, `INSERT INTO identity_users (id, email, display_name, password_hash, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'active', $5, $5) ON CONFLICT (email) DO NOTHING`, u.ID, e, name, hash, now)
	if err != nil {
		return User{}, err
	}
	if tag.RowsAffected() == 0 {
		return User{}, ErrUserExists
	}
	return u, nil
}

// SetPassword replaces the password, unlocks the account and ends every
// open session of the user.
func (s *Store) SetPassword(ctx context.Context, email, password string) error {
	e, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	if err := checkPasswordPolicy(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	now := s.now().UTC()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `UPDATE identity_users SET password_hash = $2, failed_logins = 0, locked_until = NULL, updated_at = $3
			WHERE email = $1 RETURNING id`, e, hash, now).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE identity_sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, id, now)
		return err
	})
}

// SetMembership gives (or changes) the role of a user in a tenant.
func (s *Store) SetMembership(ctx context.Context, email, tenantID, role string) error {
	e, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	if !validRole(role) || strings.TrimSpace(tenantID) == "" || len(tenantID) > 64 {
		return ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(ctx, `INSERT INTO identity_memberships (user_id, tenant_id, role, created_at)
		SELECT id, $2, $3, $4 FROM identity_users WHERE email = $1
		ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = EXCLUDED.role`, e, tenantID, role, s.now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

func filterMemberships(all []Membership, allowed []string) []Membership {
	ok := make(map[string]bool, len(allowed))
	for _, t := range allowed {
		ok[t] = true
	}
	out := make([]Membership, 0, len(all))
	for _, m := range all {
		if ok[m.TenantID] {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out
}

func (s *Store) memberships(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, userID string) ([]Membership, error) {
	rows, err := q.Query(ctx, `SELECT tenant_id, role FROM identity_memberships WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Membership, error) {
		var m Membership
		err := row.Scan(&m.TenantID, &m.Role)
		return m, err
	})
}

// Login checks e-mail and password. On success it opens a session bound to
// serviceID that shows only the memberships in allowedTenants (the tenants the
// calling service may act for). A person with no membership there gets
// ErrNoTenantAccess and no session.
func (s *Store) Login(ctx context.Context, email, password, serviceID string, allowedTenants []string) (Session, error) {
	e, err := NormalizeEmail(email)
	if err != nil || password == "" || len(password) > 512 {
		VerifyPassword(password, dummyHash())
		return Session{}, ErrInvalidCredentials
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	now := s.now().UTC()

	var (
		u           User
		hash        string
		status      string
		lockedUntil *time.Time
	)
	err = s.pool.QueryRow(ctx, `SELECT id, email, display_name, password_hash, status, locked_until
		FROM identity_users WHERE email = $1`, e).Scan(&u.ID, &u.Email, &u.Name, &hash, &status, &lockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		VerifyPassword(password, dummyHash())
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if lockedUntil != nil && lockedUntil.After(now) {
		return Session{}, ErrLocked
	}
	if !VerifyPassword(password, hash) || status != "active" {
		if status == "active" {
			_, err = s.pool.Exec(ctx, `UPDATE identity_users SET
				failed_logins = CASE WHEN failed_logins + 1 >= $2 THEN 0 ELSE failed_logins + 1 END,
				locked_until  = CASE WHEN failed_logins + 1 >= $2 THEN $3::timestamptz ELSE locked_until END
				WHERE id = $1`, u.ID, MaxFailedLogins, now.Add(LockDuration))
			if err != nil {
				return Session{}, err
			}
		}
		return Session{}, ErrInvalidCredentials
	}

	var out Session
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE identity_users SET failed_logins = 0, locked_until = NULL WHERE id = $1`, u.ID); err != nil {
			return err
		}
		all, err := s.memberships(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		visible := filterMemberships(all, allowedTenants)
		if len(visible) == 0 {
			return ErrNoTenantAccess
		}
		token := "kss_" + randomHex(32)
		expires := now.Add(SessionTTL)
		if _, err := tx.Exec(ctx, `INSERT INTO identity_sessions (token_hash, user_id, service_id, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5)`, tokenDigest(token), u.ID, serviceID, now, expires); err != nil {
			return err
		}
		out = Session{Token: token, ExpiresAt: expires, User: u, Memberships: visible}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

// validTokenShape avoids a database round trip for garbage.
func validTokenShape(token string) bool {
	if len(token) != 4+64 || !strings.HasPrefix(token, "kss_") {
		return false
	}
	_, err := hex.DecodeString(token[4:])
	return err == nil
}

// Lookup returns the session behind a token, as seen by serviceID. A token
// created by another service, expired, revoked, of a disabled user or with no
// visible tenant left is ErrSessionInvalid.
func (s *Store) Lookup(ctx context.Context, token, serviceID string, allowedTenants []string) (Session, error) {
	if !validTokenShape(token) {
		return Session{}, ErrSessionInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var (
		sess   Session
		status string
	)
	err := s.pool.QueryRow(ctx, `SELECT s.expires_at, u.id, u.email, u.display_name, u.status
		FROM identity_sessions s JOIN identity_users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.service_id = $2 AND s.revoked_at IS NULL AND s.expires_at > $3`,
		tokenDigest(token), serviceID, s.now().UTC()).Scan(&sess.ExpiresAt, &sess.User.ID, &sess.User.Email, &sess.User.Name, &status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return Session{}, ErrSessionInvalid
	}
	if err != nil {
		return Session{}, err
	}
	all, err := s.memberships(ctx, s.pool, sess.User.ID)
	if err != nil {
		return Session{}, err
	}
	sess.Memberships = filterMemberships(all, allowedTenants)
	if len(sess.Memberships) == 0 {
		return Session{}, ErrSessionInvalid
	}
	return sess, nil
}

// Revoke ends a session. Revoking an unknown or already ended session is not
// an error (sign-out is idempotent).
func (s *Store) Revoke(ctx context.Context, token, serviceID string) error {
	if !validTokenShape(token) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, `UPDATE identity_sessions SET revoked_at = $3
		WHERE token_hash = $1 AND service_id = $2 AND revoked_at IS NULL`, tokenDigest(token), serviceID, s.now().UTC())
	return err
}
