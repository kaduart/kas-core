// Package attribution is the KAS Attribution module: received revenue by
// campaign, net of refunds, from four generic inputs any company can supply
// (customers, campaign contacts, receipts, refunds). It knows nothing about
// patients, packages, sessions or insurance (spec "KAS por módulos", 8.2).
//
// Money is accepted only in this module's tables (decision D2). Phone numbers
// and e-mails are never stored: they become a keyed fingerprint (HMAC) used
// only to detect a contact point shared by several customers.
package attribution

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const ContractVersion = "v1"

// Kinds of input.
const (
	KindCustomer = "customers"
	KindContact  = "contacts"
	KindReceipt  = "receipts"
	KindRefund   = "refunds"
)

// MaxBatchItems is the largest batch accepted.
const MaxBatchItems = 500

// Sources accepted for a contact.
var Sources = []string{"google_ads", "meta_ads", "tiktok_ads", "instagram", "organic", "referral", "other"}

// StrongMethods make a contact strong evidence even without a click id.
var StrongMethods = []string{"ctwa", "site_signature", "token"}

var (
	ErrInvalidBatch  = errors.New("invalid_batch")
	ErrBatchConflict = errors.New("batch_conflict")
	ErrUnavailable   = errors.New("attribution store unavailable")

	refPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	actorPattern  = regexp.MustCompile(`^(user|service|system)\.[a-z0-9][a-z0-9._-]{0,95}$`)
	textPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._:/-]{0,127}$`)
	methodPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// Item is one input line as received (JSON or a CSV row). Unused fields stay
// empty. Phone and Email are consumed by Normalize and never stored.
type Item struct {
	ExternalID  string `json:"external_id"`
	CustomerRef string `json:"customer_ref,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Email       string `json:"email,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Source      string `json:"source,omitempty"`
	Campaign    string `json:"campaign,omitempty"`
	AdID        string `json:"ad_id,omitempty"`
	Gclid       string `json:"gclid,omitempty"`
	Fbclid      string `json:"fbclid,omitempty"`
	Ttclid      string `json:"ttclid,omitempty"`
	CtwaClid    string `json:"ctwa_clid,omitempty"`
	Method      string `json:"method,omitempty"`
	Acquisition bool   `json:"acquisition,omitempty"`
	ContactedAt string `json:"contacted_at,omitempty"`
	AmountCents int64  `json:"amount_cents,omitempty"`
	PaymentRef  string `json:"payment_ref,omitempty"`
	ReceivedAt  string `json:"received_at,omitempty"`
	ReceiptRef  string `json:"receipt_ref,omitempty"`
	RefundedAt  string `json:"refunded_at,omitempty"`
	Voided      bool   `json:"voided,omitempty"`
}

// Record is the stored, normalized form of an item: what is hashed and kept.
// It never holds a raw phone or e-mail.
type Record struct {
	ExternalID  string    `json:"external_id"`
	CustomerRef string    `json:"customer_ref,omitempty"`
	ContactKey  string    `json:"contact_key,omitempty"` // fingerprint of phone/e-mail
	Unit        string    `json:"unit,omitempty"`
	Source      string    `json:"source,omitempty"`
	Campaign    string    `json:"campaign,omitempty"`
	AdID        string    `json:"ad_id,omitempty"`
	Gclid       string    `json:"gclid,omitempty"`
	Fbclid      string    `json:"fbclid,omitempty"`
	Ttclid      string    `json:"ttclid,omitempty"`
	CtwaClid    string    `json:"ctwa_clid,omitempty"`
	Method      string    `json:"method,omitempty"`
	Acquisition bool      `json:"acquisition,omitempty"`
	At          time.Time `json:"at"`
	AmountCents int64     `json:"amount_cents,omitempty"`
	PaymentRef  string    `json:"payment_ref,omitempty"`
	ReceiptRef  string    `json:"receipt_ref,omitempty"`
	Voided      bool      `json:"voided,omitempty"`
}

// Fingerprinter turns a phone or e-mail into a tenant-scoped keyed hash. The
// same number written differently (mask, +55, with or without the 9 after the
// area code) gives the same fingerprint; different tenants never share one.
type Fingerprinter struct{ key []byte }

func NewFingerprinter(key []byte) (*Fingerprinter, error) {
	if len(key) < 32 {
		return nil, errors.New("fingerprint key must have at least 32 bytes")
	}
	return &Fingerprinter{key: append([]byte(nil), key...)}, nil
}

func (f *Fingerprinter) of(tenantID, kind, value string) string {
	mac := hmac.New(sha256.New, f.key)
	mac.Write([]byte(tenantID + "\x00" + kind + "\x00" + value))
	return "fp:" + hex.EncodeToString(mac.Sum(nil))[:40]
}

// PhoneKey normalizes a Brazilian phone to 55 + area code + last 8 digits, so
// the variants with and without the 9 meet. Other lengths keep their digits.
func PhoneKey(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := strings.TrimLeft(b.String(), "0")
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "5555") {
		s = s[2:]
	}
	if !strings.HasPrefix(s, "55") || len(s) < 12 {
		s = "55" + s
	}
	switch len(s) {
	case 12:
		return s
	case 13:
		return s[:4] + s[5:]
	}
	return s
}

type rejection struct{ code string }

func (r rejection) Error() string { return r.code }

func reject(code string) error { return rejection{code} }

// RejectionCode returns the code of a per-item validation error.
func RejectionCode(err error) string {
	var r rejection
	if errors.As(err, &r) {
		return r.code
	}
	return "invalid_item"
}

func parseTime(raw string) (time.Time, bool) {
	if raw == "" || len(raw) > 40 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	return t.UTC(), err == nil
}

func optional(pattern *regexp.Regexp, values ...string) bool {
	for _, v := range values {
		if v != "" && !pattern.MatchString(v) {
			return false
		}
	}
	return true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Normalize validates one item of a kind and returns its stored form.
func Normalize(tenantID, kind string, it Item, fp *Fingerprinter) (Record, error) {
	if !refPattern.MatchString(it.ExternalID) {
		return Record{}, reject("external_id_invalid")
	}
	r := Record{ExternalID: it.ExternalID, Voided: it.Voided}
	if !optional(refPattern, it.CustomerRef, it.PaymentRef, it.ReceiptRef, it.AdID, it.Gclid, it.Fbclid, it.Ttclid, it.CtwaClid) {
		return Record{}, reject("reference_invalid")
	}
	if !optional(textPattern, it.Unit, it.Campaign) {
		return Record{}, reject("text_invalid")
	}
	// Fields of other kinds must stay empty, so a misplaced column is caught.
	misplaced := func(fields ...bool) bool {
		for _, f := range fields {
			if f {
				return true
			}
		}
		return false
	}
	switch kind {
	case KindCustomer:
		if misplaced(it.CustomerRef != "", it.Source != "", it.AmountCents != 0, it.ContactedAt != "", it.ReceivedAt != "", it.RefundedAt != "") {
			return Record{}, reject("field_not_allowed")
		}
		switch {
		case it.Phone != "":
			k := PhoneKey(it.Phone)
			if len(k) < 10 || len(k) > 15 {
				return Record{}, reject("phone_invalid")
			}
			r.ContactKey = fp.of(tenantID, "phone", k)
		case it.Email != "":
			e := strings.ToLower(strings.TrimSpace(it.Email))
			if !strings.Contains(e, "@") || len(e) > 254 {
				return Record{}, reject("email_invalid")
			}
			r.ContactKey = fp.of(tenantID, "email", e)
		}
		r.Unit = it.Unit
	case KindContact:
		if misplaced(it.Phone != "", it.Email != "", it.AmountCents != 0, it.ReceivedAt != "", it.RefundedAt != "", it.PaymentRef != "", it.ReceiptRef != "") {
			return Record{}, reject("field_not_allowed")
		}
		if it.CustomerRef == "" {
			return Record{}, reject("customer_ref_required")
		}
		if !contains(Sources, it.Source) {
			return Record{}, reject("source_invalid")
		}
		if it.Method != "" && !methodPattern.MatchString(it.Method) {
			return Record{}, reject("method_invalid")
		}
		at, ok := parseTime(it.ContactedAt)
		if !ok {
			return Record{}, reject("contacted_at_invalid")
		}
		r.CustomerRef, r.Source, r.Campaign, r.AdID, r.Method, r.Acquisition, r.At = it.CustomerRef, it.Source, it.Campaign, it.AdID, it.Method, it.Acquisition, at
		r.Gclid, r.Fbclid, r.Ttclid, r.CtwaClid = it.Gclid, it.Fbclid, it.Ttclid, it.CtwaClid
	case KindReceipt:
		if misplaced(it.Phone != "", it.Email != "", it.Source != "", it.ContactedAt != "", it.RefundedAt != "", it.ReceiptRef != "") {
			return Record{}, reject("field_not_allowed")
		}
		if it.CustomerRef == "" {
			return Record{}, reject("customer_ref_required")
		}
		if it.AmountCents <= 0 || it.AmountCents > 1_000_000_000_00 {
			return Record{}, reject("amount_invalid")
		}
		at, ok := parseTime(it.ReceivedAt)
		if !ok {
			return Record{}, reject("received_at_invalid")
		}
		r.CustomerRef, r.AmountCents, r.PaymentRef, r.Unit, r.At = it.CustomerRef, it.AmountCents, it.PaymentRef, it.Unit, at
	case KindRefund:
		if misplaced(it.Phone != "", it.Email != "", it.Source != "", it.ContactedAt != "", it.ReceivedAt != "") {
			return Record{}, reject("field_not_allowed")
		}
		if it.AmountCents <= 0 || it.AmountCents > 1_000_000_000_00 {
			return Record{}, reject("amount_invalid")
		}
		if it.ReceiptRef == "" && it.PaymentRef == "" {
			return Record{}, reject("receipt_or_payment_ref_required")
		}
		at, ok := parseTime(it.RefundedAt)
		if !ok {
			return Record{}, reject("refunded_at_invalid")
		}
		r.CustomerRef, r.AmountCents, r.PaymentRef, r.ReceiptRef, r.Unit, r.At = it.CustomerRef, it.AmountCents, it.PaymentRef, it.ReceiptRef, it.Unit, at
	default:
		return Record{}, ErrInvalidBatch
	}
	return r, nil
}

// Hash is the content hash of a record: same content, same hash.
func (r Record) Hash() string {
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ValidKind reports whether kind is one of the four inputs.
func ValidKind(kind string) bool {
	return kind == KindCustomer || kind == KindContact || kind == KindReceipt || kind == KindRefund
}

// ValidActor reports whether actor has the audit shape (user./service./system.).
func ValidActor(actor string) bool { return actorPattern.MatchString(actor) }

// ValidRef reports whether s is an identifier usable as batch id or reference.
func ValidRef(s string) bool { return refPattern.MatchString(s) }
