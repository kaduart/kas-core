package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"time"
)

const (
	// MaxClaimBatch is the largest number of deliveries one claim may lease.
	MaxClaimBatch = 50
	// DefaultClaimBatch applies when the request omits max_deliveries.
	DefaultClaimBatch = 10
	// MinLease, MaxLease and DefaultLease bound the claim lease.
	MinLease     = 5 * time.Second
	MaxLease     = 5 * time.Minute
	DefaultLease = 60 * time.Second
)

var (
	// ErrLeaseLost means the lease token or attempt no longer matches the delivery.
	ErrLeaseLost        = errors.New("lease lost")
	ErrInvalidPullInput = errors.New("invalid pull request")
	leaseTokenPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// PullStore is what the Puller needs from storage.
type PullStore interface {
	ClaimFor(ctx context.Context, leaseToken, consumerID string, tenants, eventTypes []string, now time.Time, lease time.Duration, maxAttempts, limit int) ([]Delivery, error)
	Ack(ctx context.Context, consumerID string, deliveryID int64, attempt int, leaseToken string, now time.Time) (SettleOutcome, error)
	Nack(ctx context.Context, consumerID string, deliveryID int64, attempt int, leaseToken string, now, next time.Time, dead bool, code string) (SettleOutcome, error)
}

// PullConfig holds tunables; zero values select the defaults.
type PullConfig struct {
	MaxAttempts int           // default 5
	BaseBackoff time.Duration // default 2s
	MaxBackoff  time.Duration // default 5m
	Now         func() time.Time
	Jitter      func() float64
}

// Puller serves consumers that fetch deliveries over HTTP instead of being
// called. Authentication and tenant/type scoping happen before it is used; it
// trusts the scope it is given. Delivery stays at-least-once: a consumer that
// does not ack in time sees the delivery again.
type Puller struct {
	store PullStore
	cfg   PullConfig
}

func NewPuller(store PullStore, cfg PullConfig) *Puller {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 5
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 2 * time.Second
	}
	if cfg.MaxBackoff < cfg.BaseBackoff {
		cfg.MaxBackoff = 5 * time.Minute
		if cfg.MaxBackoff < cfg.BaseBackoff {
			cfg.MaxBackoff = cfg.BaseBackoff
		}
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Jitter == nil {
		cfg.Jitter = defaultJitter
	}
	return &Puller{store: store, cfg: cfg}
}

// ClaimedDelivery is one leased delivery returned to a pull consumer.
type ClaimedDelivery struct {
	DeliveryID     int64  `json:"delivery_id"`
	Attempt        int    `json:"attempt"`
	LeaseToken     string `json:"lease_token"`
	LeaseExpiresAt string `json:"lease_expires_at"`
	Event          Event  `json:"event"`
}

// Claim leases up to limit deliveries (1..MaxClaimBatch; 0 selects the default)
// for lease (MinLease..MaxLease; 0 selects the default), within the tenants and
// event types of the scope. It returns an empty slice when nothing is due.
func (p *Puller) Claim(ctx context.Context, consumerID string, tenants, eventTypes []string, limit int, lease time.Duration) ([]ClaimedDelivery, error) {
	if limit == 0 {
		limit = DefaultClaimBatch
	}
	if lease == 0 {
		lease = DefaultLease
	}
	if limit < 1 || limit > MaxClaimBatch || lease < MinLease || lease > MaxLease {
		return nil, ErrInvalidPullInput
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, ErrStoreUnavailable
	}
	token := hex.EncodeToString(raw[:])
	now := p.cfg.Now().UTC()
	deliveries, err := p.store.ClaimFor(ctx, token, consumerID, tenants, eventTypes, now, lease, p.cfg.MaxAttempts, limit)
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	out := make([]ClaimedDelivery, 0, len(deliveries))
	for _, d := range deliveries {
		out = append(out, ClaimedDelivery{
			DeliveryID: d.ID, Attempt: d.Attempt, LeaseToken: token,
			LeaseExpiresAt: now.Add(lease).Format(time.RFC3339Nano), Event: d.Event,
		})
	}
	return out, nil
}

func validSettleInput(deliveryID int64, attempt int, leaseToken string) bool {
	return deliveryID > 0 && attempt >= 1 && leaseTokenPattern.MatchString(leaseToken)
}

// Ack confirms a delivery. A repeated ack of an already acknowledged delivery
// succeeds; a lost lease returns ErrLeaseLost.
func (p *Puller) Ack(ctx context.Context, consumerID string, deliveryID int64, attempt int, leaseToken string) error {
	if !validSettleInput(deliveryID, attempt, leaseToken) {
		return ErrInvalidPullInput
	}
	outcome, err := p.store.Ack(ctx, consumerID, deliveryID, attempt, leaseToken, p.cfg.Now().UTC())
	if err != nil {
		return ErrStoreUnavailable
	}
	if outcome == LeaseLost {
		return ErrLeaseLost
	}
	return nil
}

// NackResult reports what a nack did.
type NackResult struct {
	// Status is "pending" (retry scheduled) or "dead".
	Status        string
	NextAttemptAt time.Time // set when Status is "pending"
}

// Nack rejects a delivery. With permanent, or once the attempts are used up, the
// delivery becomes dead; otherwise it returns to pending after an exponential
// backoff with jitter. A lost lease returns ErrLeaseLost.
func (p *Puller) Nack(ctx context.Context, consumerID string, deliveryID int64, attempt int, leaseToken string, permanent bool) (NackResult, error) {
	if !validSettleInput(deliveryID, attempt, leaseToken) {
		return NackResult{}, ErrInvalidPullInput
	}
	now := p.cfg.Now().UTC()
	dead, code := permanent, "consumer_nack"
	switch {
	case permanent:
		code = "consumer_permanent"
	case attempt >= p.cfg.MaxAttempts:
		dead, code = true, "max_attempts"
	}
	next := now.Add(Backoff(attempt, p.cfg.BaseBackoff, p.cfg.MaxBackoff, p.cfg.Jitter()))
	outcome, err := p.store.Nack(ctx, consumerID, deliveryID, attempt, leaseToken, now, next, dead, code)
	if err != nil {
		return NackResult{}, ErrStoreUnavailable
	}
	if outcome == LeaseLost {
		return NackResult{}, ErrLeaseLost
	}
	if dead {
		return NackResult{Status: "dead"}, nil
	}
	return NackResult{Status: "pending", NextAttemptAt: next}, nil
}

var _ PullStore = (*PostgresStore)(nil)
