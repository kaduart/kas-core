package attribution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	Table:  "attribution_schema_migrations",
	LockID: 0x4b415341545452, // "KASATTR"
}

const dbTimeout = 30 * time.Second

// Store keeps the inputs of every tenant, versioned.
type Store struct {
	pool *pgxpool.Pool
	fp   *Fingerprinter
	now  func() time.Time
}

// Open connects and applies (or verifies) the migrations.
func Open(ctx context.Context, connString string, applyMigrations bool, fp *Fingerprinter) (*Store, error) {
	if fp == nil {
		return nil, errors.New("attribution store needs a fingerprint key")
	}
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "attribution", connString)
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
		return nil, fmt.Errorf("attribution database schema: %w", err)
	}
	return &Store{pool: pool, fp: fp, now: time.Now}, nil
}

// Migrate applies the migrations with a migration-capable role.
func Migrate(ctx context.Context, connString string) error {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "attribution", connString)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrator.Apply(ctx, pool)
}

func (s *Store) Close() { s.pool.Close() }

// Ready pings the database.
func (s *Store) Ready(ctx context.Context) error { return s.pool.Ping(ctx) }

// Batch is one import request.
type Batch struct {
	TenantID  string
	ServiceID string
	Kind      string
	BatchID   string
	Actor     string
	Items     []Item
}

// Rejected is one refused line of a batch.
type Rejected struct {
	Index      int    `json:"index"`
	ExternalID string `json:"external_id,omitempty"`
	Code       string `json:"code"`
}

// BatchResult is the stored answer of a batch.
type BatchResult struct {
	BatchID   string     `json:"batch_id"`
	Kind      string     `json:"kind"`
	Created   int        `json:"created"`
	Updated   int        `json:"updated"`
	Unchanged int        `json:"unchanged"`
	Rejected  []Rejected `json:"rejected"`
}

// requestHash identifies a batch's content so a resent batch_id can be told
// apart from a different batch reusing it. It is keyed (HMAC) because the raw
// items may hold phone numbers or e-mails.
func (s *Store) requestHash(b Batch) string {
	raw, _ := json.Marshal(struct {
		Kind  string `json:"kind"`
		Items []Item `json:"items"`
	}{b.Kind, b.Items})
	mac := hmac.New(sha256.New, s.fp.key)
	mac.Write([]byte(b.TenantID + "\x00"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

// Import stores a batch. Idempotent twice over:
//   - by batch: the same batch_id with the same content returns the original
//     answer and writes nothing; with other content it is ErrBatchConflict;
//   - by item: the key is tenant + kind + external_id; the same content is
//     "unchanged", other content is a new version ("updated"), the old version
//     is kept. A voided item is a new version with voided=true.
//
// An invalid line is reported in Rejected and does not stop the batch.
func (s *Store) Import(ctx context.Context, b Batch) (BatchResult, bool, error) {
	if !ValidKind(b.Kind) || !ValidRef(b.BatchID) || !ValidActor(b.Actor) || len(b.Items) == 0 || len(b.Items) > MaxBatchItems {
		return BatchResult{}, false, ErrInvalidBatch
	}
	hash := s.requestHash(b)
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var result BatchResult
	duplicate := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One importer at a time per tenant keeps versions gap-free.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "attribution/"+b.TenantID); err != nil {
			return err
		}
		var storedHash string
		var stored []byte
		err := tx.QueryRow(ctx, `SELECT request_hash, response FROM attribution_batches WHERE tenant_id = $1 AND batch_id = $2`,
			b.TenantID, b.BatchID).Scan(&storedHash, &stored)
		if err == nil {
			if storedHash != hash {
				return ErrBatchConflict
			}
			duplicate = true
			return json.Unmarshal(stored, &result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result = BatchResult{BatchID: b.BatchID, Kind: b.Kind, Rejected: []Rejected{}}
		seen := map[string]bool{}
		now := s.now().UTC()
		for i, it := range b.Items {
			rec, err := Normalize(b.TenantID, b.Kind, it, s.fp)
			if err != nil {
				result.Rejected = append(result.Rejected, Rejected{Index: i, ExternalID: safeRef(it.ExternalID), Code: RejectionCode(err)})
				continue
			}
			if seen[rec.ExternalID] {
				result.Rejected = append(result.Rejected, Rejected{Index: i, ExternalID: rec.ExternalID, Code: "duplicate_in_batch"})
				continue
			}
			seen[rec.ExternalID] = true
			contentHash := rec.Hash()
			var version int
			var currentHash string
			err = tx.QueryRow(ctx, `SELECT version, content_hash FROM attribution_items
				WHERE tenant_id = $1 AND kind = $2 AND external_id = $3 ORDER BY version DESC LIMIT 1`,
				b.TenantID, b.Kind, rec.ExternalID).Scan(&version, &currentHash)
			switch {
			case err == nil && currentHash == contentHash:
				result.Unchanged++
				continue
			case err == nil:
				result.Updated++
			case errors.Is(err, pgx.ErrNoRows):
				result.Created++
			default:
				return err
			}
			raw, _ := json.Marshal(rec)
			if _, err := tx.Exec(ctx, `INSERT INTO attribution_items
				(tenant_id, kind, external_id, version, content_hash, record, voided, batch_id, recorded_at)
				VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9)`,
				b.TenantID, b.Kind, rec.ExternalID, version+1, contentHash, string(raw), rec.Voided, b.BatchID, now); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(result)
		_, err = tx.Exec(ctx, `INSERT INTO attribution_batches (tenant_id, batch_id, kind, request_hash, response, service_id, actor, recorded_at)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8)`, b.TenantID, b.BatchID, b.Kind, hash, string(raw), b.ServiceID, b.Actor, now)
		return err
	})
	if errors.Is(err, ErrBatchConflict) {
		return BatchResult{}, false, ErrBatchConflict
	}
	if err != nil {
		return BatchResult{}, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return result, duplicate, nil
}

func safeRef(s string) string {
	if ValidRef(s) {
		return s
	}
	return ""
}

// Current loads the latest, not voided version of every input of a tenant.
func (s *Store) Current(ctx context.Context, tenantID string) (Dataset, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (kind, external_id) kind, record, voided
		FROM attribution_items WHERE tenant_id = $1
		ORDER BY kind, external_id, version DESC`, tenantID)
	if err != nil {
		return Dataset{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer rows.Close()
	var d Dataset
	for rows.Next() {
		var kind string
		var raw []byte
		var voided bool
		if err := rows.Scan(&kind, &raw, &voided); err != nil {
			return Dataset{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		if voided {
			continue
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return Dataset{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		switch kind {
		case KindCustomer:
			d.Customers = append(d.Customers, r)
		case KindContact:
			d.Contacts = append(d.Contacts, r)
		case KindReceipt:
			d.Receipts = append(d.Receipts, r)
		case KindRefund:
			d.Refunds = append(d.Refunds, r)
		}
	}
	if err := rows.Err(); err != nil {
		return Dataset{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return d, nil
}

// Missing answers "is the integration ready?" for the module view: Attribution
// is ready once there is at least one contact and one receipt.
func (s *Store) Missing(ctx context.Context, tenantID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var contacts, receipts bool
	if err := s.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM attribution_items WHERE tenant_id = $1 AND kind = 'contacts' AND NOT voided),
		EXISTS (SELECT 1 FROM attribution_items WHERE tenant_id = $1 AND kind = 'receipts' AND NOT voided)`,
		tenantID).Scan(&contacts, &receipts); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var missing []string
	if !contacts {
		missing = append(missing, KindContact)
	}
	if !receipts {
		missing = append(missing, KindReceipt)
	}
	return missing, nil
}

// VersionsOf returns how many versions an input has (for audits and tests).
func (s *Store) VersionsOf(ctx context.Context, tenantID, kind, externalID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM attribution_items WHERE tenant_id = $1 AND kind = $2 AND external_id = $3`,
		tenantID, kind, externalID).Scan(&n)
	return n, err
}
