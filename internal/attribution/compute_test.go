package attribution

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testKey = []byte("chave-ficticia-de-teste-com-32-bytes-ou-mais")

func fingerprinter(t *testing.T) *Fingerprinter {
	t.Helper()
	fp, err := NewFingerprinter(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// loadSample reads testdata/attribution/<dir> through the same CSV parser and
// normalization the API uses.
func loadSample(t *testing.T, tenant, dir string) Dataset {
	t.Helper()
	fp := fingerprinter(t)
	var d Dataset
	for _, kind := range []string{KindCustomer, KindContact, KindReceipt, KindRefund} {
		f, err := os.Open(filepath.Join("..", "..", "testdata", "attribution", dir, kind+".csv"))
		if err != nil {
			t.Fatal(err)
		}
		items, err := ParseCSV(kind, f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		for _, it := range items {
			r, err := Normalize(tenant, kind, it, fp)
			if err != nil {
				t.Fatalf("%s %s: %v", kind, it.ExternalID, err)
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
	}
	return d
}

func lineOf(res Result, level, source, key string) *Line {
	for i := range res.Lines {
		l := &res.Lines[i]
		if l.Level == level && l.Source == source && (l.Campaign == key || l.AdID == key) {
			return l
		}
	}
	return nil
}

// Conciliation with the CRM (PR #99): the same fictional sample, translated to
// the generic inputs, gives 1.290 = 480 + 570 + 240.
func TestFonoSampleMatchesCRM(t *testing.T) {
	res, err := Compute(loadSample(t, "fono-demo", "amostra-fono"), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if res.NetTotal != 129000 || res.ByLevel[LevelProven] != 48000 || res.ByLevel[LevelProbable] != 57000 || res.ByLevel[LevelNone] != 24000 {
		t.Fatalf("totals %d %v", res.NetTotal, res.ByLevel)
	}
	if !res.Invariant.Closes || res.Invariant.SumLevels != 129000 {
		t.Fatalf("invariant %+v", res.Invariant)
	}
	checks := []struct {
		level, source, key    string
		received, refund, net int64
		customers             int
	}{
		{LevelProven, "meta_ads", "AD-FICTICIO-1", 20000, 0, 20000, 1},
		{LevelProven, "meta_ads", "AD-FICTICIO-2", 16000, 0, 16000, 1},
		{LevelProven, "google_ads", "gg-fono-infantil", 12000, 0, 12000, 1},
		{LevelProven, "tiktok_ads", "tt-avaliacao", 18000, 18000, 0, 1},
		{LevelProbable, "google_ads", "gg-fono-infantil", 47000, 0, 47000, 3},
		{LevelProbable, "instagram", "", 10000, 0, 10000, 1},
	}
	for _, c := range checks {
		l := lineOf(res, c.level, c.source, c.key)
		if l == nil || l.Received != c.received || l.Refunds != c.refund || l.Net != c.net || l.Customers != c.customers {
			t.Fatalf("line %s/%s/%s: %+v", c.level, c.source, c.key, l)
		}
	}
	if l := lineOf(res, LevelProbable, "google_ads", "gg-fono-infantil"); l.ReasonCount["sinal_forte_em_contato_compartilhado"] != 3 {
		t.Fatalf("shared contact reason: %+v", l.ReasonCount)
	}
	if res.Unlinked.Net != 24000 || res.Unlinked.Reasons["sem_origem_capturada"] != 15000 ||
		res.Unlinked.Reasons["contato_da_campanha_depois_do_recebimento"] != 9000 {
		t.Fatalf("unlinked %+v", res.Unlinked)
	}
	if res.Rules["modelo"] != Model || res.Rules["contato"] != TouchRuleVersion || res.Rules["estorno"] != RefundRuleVersion {
		t.Fatalf("rules %v", res.Rules)
	}
	if _, err := Compute(Dataset{}, Query{Model: "ultimo-contato"}); err != ErrModelUnsupported {
		t.Fatalf("unknown model: %v", err)
	}
}

func TestPhoneKeyVariants(t *testing.T) {
	want := PhoneKey("5562999990009")
	for _, v := range []string{"(62) 99999-0009", "62999990009", "+55 62 99999-0009", "556299990009", "(62) 9999-0009", "+5562999990009"} {
		if got := PhoneKey(v); got != want {
			t.Fatalf("%s -> %s, want %s", v, got, want)
		}
	}
	fp := fingerprinter(t)
	a, _ := Normalize("t1", KindCustomer, Item{ExternalID: "c", Phone: "(62) 99999-0009"}, fp)
	b, _ := Normalize("t2", KindCustomer, Item{ExternalID: "c", Phone: "(62) 99999-0009"}, fp)
	if a.ContactKey == "" || a.ContactKey == b.ContactKey {
		t.Fatal("fingerprint must exist and differ between tenants")
	}
	if a.ContactKey == "5562999990009" || len(a.ContactKey) < 20 {
		t.Fatal("raw phone must never be stored")
	}
}

// ── Cases from the CRM review (PR #99), repeated in Go ──

func ts(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func cust(id string) Record { return Record{ExternalID: id, ContactKey: "fp:" + id} }

func TestSameChannelDifferentCampaigns(t *testing.T) {
	// A (Google, campaign A, 01/09) and B (Google, campaign B, contact 05/11,
	// acquisition 10/11). Payment 15/10 between them, refunded 20/11.
	d := Dataset{
		Customers: []Record{cust("px")},
		Contacts: []Record{
			{ExternalID: "b-acq", CustomerRef: "px", Source: "google_ads", Campaign: "B", Gclid: "GCLID-B", Method: "gclid", Acquisition: true, At: ts("2026-11-10T13:00:00Z")},
			{ExternalID: "a", CustomerRef: "px", Source: "google_ads", Campaign: "A", Gclid: "GCLID-A", Method: "gclid", At: ts("2026-09-01T12:00:00Z")},
			{ExternalID: "b", CustomerRef: "px", Source: "google_ads", Campaign: "B", Gclid: "GCLID-B", Method: "gclid", At: ts("2026-11-05T12:00:00Z")},
		},
		Receipts: []Record{{ExternalID: "r", CustomerRef: "px", AmountCents: 30000, PaymentRef: "p", At: ts("2026-10-15T15:00:00Z")}},
		Refunds:  []Record{{ExternalID: "e", CustomerRef: "px", AmountCents: 30000, PaymentRef: "p", At: ts("2026-11-20T15:00:00Z")}},
	}
	res, _ := Compute(d, Query{})
	a := lineOf(res, LevelProven, "google_ads", "A")
	if a == nil || a.Received != 30000 || a.Refunds != 30000 || a.Net != 0 || a.Entries != 2 {
		t.Fatalf("A: %+v", a)
	}
	if lineOf(res, LevelProven, "google_ads", "B") != nil || res.Unlinked.Net != 0 {
		t.Fatalf("B must get nothing: %+v", res)
	}
	// Different gclid with the same campaign name is NOT the same touch.
	if SameTouch(Record{Source: "google_ads", Gclid: "G1", Campaign: "X"}, Record{Source: "google_ads", Gclid: "G2", Campaign: "X"}) {
		t.Fatal("gclid conflict must win over campaign")
	}
	if !SameTouch(Record{Source: "google_ads", Gclid: "G1", Campaign: "X"}, Record{Source: "google_ads", Gclid: "G1", Campaign: "Y"}) {
		t.Fatal("same gclid with renamed campaign is the same touch")
	}
}

func TestRefundRules(t *testing.T) {
	acq := Record{ExternalID: "acq", CustomerRef: "px", Source: "meta_ads", AdID: "AD-NOV", CtwaClid: "C", Method: "ctwa", Acquisition: true, At: ts("2026-11-05T13:00:00Z")}
	base := Dataset{Customers: []Record{cust("px")}, Contacts: []Record{acq}}
	rec := func(id string, cents int64, at, pay string) Record {
		return Record{ExternalID: id, CustomerRef: "px", AmountCents: cents, PaymentRef: pay, At: ts(at)}
	}

	// Two installments (before and after the campaign); refund by payment only
	// is split by amount: 200 x 100/400 = 50 unlinked, 150 campaign.
	d := base
	d.Receipts = []Record{rec("p1", 10000, "2026-10-15T15:00:00Z", "pac"), rec("p2", 30000, "2026-11-10T15:00:00Z", "pac")}
	d.Refunds = []Record{{ExternalID: "e", AmountCents: 20000, PaymentRef: "pac", At: ts("2026-11-20T15:00:00Z")}}
	res, _ := Compute(d, Query{})
	if l := lineOf(res, LevelProven, "meta_ads", "AD-NOV"); l == nil || l.Net != 15000 || l.Refunds != 15000 || res.Unlinked.Net != 5000 || !res.Invariant.Closes {
		t.Fatalf("split: %+v %+v", l, res.Unlinked)
	}
	// With receipt_ref the whole refund goes to that receipt.
	d.Refunds = []Record{{ExternalID: "e", AmountCents: 20000, ReceiptRef: "p2", PaymentRef: "pac", At: ts("2026-11-20T15:00:00Z")}}
	res, _ = Compute(d, Query{})
	if l := lineOf(res, LevelProven, "meta_ads", "AD-NOV"); l.Net != 10000 || res.Unlinked.Net != 10000 {
		t.Fatalf("exact: %+v %+v", l, res.Unlinked)
	}
	// Cents are never lost in a split.
	d.Receipts = []Record{rec("p1", 3333, "2026-10-15T15:00:00Z", "x"), rec("p2", 6667, "2026-11-10T15:00:00Z", "x")}
	d.Refunds = []Record{{ExternalID: "e", AmountCents: 1001, PaymentRef: "x", At: ts("2026-11-20T15:00:00Z")}}
	res, _ = Compute(d, Query{})
	if res.Unlinked.Refunds != 334 || lineOf(res, LevelProven, "meta_ads", "AD-NOV").Refunds != 667 || !res.Invariant.Closes {
		t.Fatalf("cents: %+v", res)
	}
	// Two partial refunds of one receipt.
	d.Receipts = []Record{rec("n", 30000, "2026-11-10T15:00:00Z", "pn")}
	d.Refunds = []Record{
		{ExternalID: "e1", AmountCents: 10000, PaymentRef: "pn", At: ts("2026-11-18T15:00:00Z")},
		{ExternalID: "e2", AmountCents: 5000, PaymentRef: "pn", At: ts("2026-11-19T15:00:00Z")},
	}
	res, _ = Compute(d, Query{})
	if l := lineOf(res, LevelProven, "meta_ads", "AD-NOV"); l.Received != 30000 || l.Refunds != 15000 || l.Net != 15000 {
		t.Fatalf("partials: %+v", l)
	}
	// Different periods: received in October (campaign before), refunded in
	// November; each period shows its side, and together they cancel out.
	early := acq
	early.At = ts("2026-10-05T13:00:00Z")
	d = Dataset{Customers: []Record{cust("px")}, Contacts: []Record{early},
		Receipts: []Record{rec("c", 30000, "2026-10-12T15:00:00Z", "po")},
		Refunds:  []Record{{ExternalID: "e", AmountCents: 30000, PaymentRef: "po", At: ts("2026-11-08T15:00:00Z")}}}
	oct, _ := Compute(d, Query{From: ts("2026-10-01T00:00:00Z"), To: ts("2026-10-31T23:59:59Z")})
	nov, _ := Compute(d, Query{From: ts("2026-11-01T00:00:00Z"), To: ts("2026-11-30T23:59:59Z")})
	lo, ln := lineOf(oct, LevelProven, "meta_ads", "AD-NOV"), lineOf(nov, LevelProven, "meta_ads", "AD-NOV")
	if lo.Net != 30000 || ln.Net != -30000 || ln.Received != 0 || nov.NetTotal != -30000 || !nov.Invariant.Closes {
		t.Fatalf("periods: %+v %+v", lo, ln)
	}
	// A refund whose receipt is unknown stays unlinked with its reason.
	d.Refunds = []Record{{ExternalID: "e", AmountCents: 4000, PaymentRef: "sumiu", At: ts("2026-11-12T15:00:00Z")}}
	res, _ = Compute(d, Query{From: ts("2026-11-01T00:00:00Z")})
	if res.Unlinked.Reasons["estorno_sem_recebimento_original"] != -4000 {
		t.Fatalf("unknown receipt: %+v", res.Unlinked)
	}
}

func TestReceiptBeforeCampaignAndUnitFilter(t *testing.T) {
	acq := Record{ExternalID: "acq", CustomerRef: "px", Source: "meta_ads", AdID: "AD", CtwaClid: "C", Acquisition: true, At: ts("2026-11-05T13:00:00Z")}
	d := Dataset{Customers: []Record{cust("px")}, Contacts: []Record{acq}, Receipts: []Record{
		{ExternalID: "out", CustomerRef: "px", AmountCents: 10000, At: ts("2026-10-15T15:00:00Z"), Unit: "centro"},
		{ExternalID: "nov", CustomerRef: "px", AmountCents: 25000, At: ts("2026-11-10T15:00:00Z"), Unit: "sul"},
	}}
	res, _ := Compute(d, Query{})
	if res.ByLevel[LevelProven] != 25000 || res.Unlinked.Reasons["contato_da_campanha_depois_do_recebimento"] != 10000 {
		t.Fatalf("before campaign: %+v", res)
	}
	sul, _ := Compute(d, Query{Unit: "sul"})
	if sul.NetTotal != 25000 || !sul.Invariant.Closes {
		t.Fatalf("unit filter: %+v", sul)
	}
}

func TestNormalizeRejections(t *testing.T) {
	fp := fingerprinter(t)
	cases := []struct {
		kind string
		it   Item
		code string
	}{
		{KindReceipt, Item{ExternalID: "r", CustomerRef: "c", AmountCents: 0, ReceivedAt: "2026-11-10T15:00:00Z"}, "amount_invalid"},
		{KindReceipt, Item{ExternalID: "r", CustomerRef: "c", AmountCents: 10, ReceivedAt: "10/11/2026"}, "received_at_invalid"},
		{KindReceipt, Item{ExternalID: "r", AmountCents: 10, ReceivedAt: "2026-11-10T15:00:00Z"}, "customer_ref_required"},
		{KindRefund, Item{ExternalID: "e", AmountCents: 10, RefundedAt: "2026-11-10T15:00:00Z"}, "receipt_or_payment_ref_required"},
		{KindContact, Item{ExternalID: "k", CustomerRef: "c", Source: "panfleto", ContactedAt: "2026-11-10T15:00:00Z"}, "source_invalid"},
		{KindContact, Item{ExternalID: "k", CustomerRef: "c", Source: "organic", ContactedAt: "2026-11-10T15:00:00Z", AmountCents: 5}, "field_not_allowed"},
		{KindCustomer, Item{ExternalID: "c", Phone: "12"}, "phone_invalid"},
		{KindCustomer, Item{ExternalID: "nome com espaço"}, "external_id_invalid"},
	}
	for _, c := range cases {
		if _, err := Normalize("t", c.kind, c.it, fp); RejectionCode(err) != c.code {
			t.Fatalf("%s %+v: got %v want %s", c.kind, c.it, err, c.code)
		}
	}
}

// Refunds consume each receipt's balance (estorno-v3-saldo-por-recebimento).
// The sample is the CRM export of the analyst's three cases; the CRM gives the
// same numbers (tests/unit/revenueAttribution.estornos.test.js).
func TestRefundBalanceSampleMatchesCRM(t *testing.T) {
	d := loadSample(t, "fono-demo", "estornos-saldo")
	nov := Query{From: ts("2026-11-01T03:00:00Z"), To: ts("2026-12-01T02:59:59Z")}
	res, err := Compute(d, nov)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"gg-caso-1": 10000, "gg-caso-2": 50000, "gg-caso-3": -10000}
	for camp, cents := range want {
		if l := lineOf(res, LevelProven, "google_ads", camp); l == nil || l.Net != cents {
			t.Fatalf("%s: want %d, got %+v", camp, cents, l)
		}
	}
	if len(res.Lines) != 3 || res.NetTotal != 45000 || res.ByLevel[LevelProven] != 50000 || res.ByLevel[LevelNone] != -5000 || !res.Invariant.Closes {
		t.Fatalf("totals: %+v", res)
	}
	if res.Unlinked.Reasons["estorno_antes_do_recebimento"] != -5000 || res.Unlinked.Reasons["contato_da_campanha_depois_do_recebimento"] != 0 {
		t.Fatalf("unlinked reasons: %+v", res.Unlinked.Reasons)
	}
	if res.Rules["estorno"] != "estorno-v3-saldo-por-recebimento" {
		t.Fatalf("rule: %v", res.Rules)
	}
	// October shows its own side: receipts 400, earlier refund 200, net 200.
	oct, _ := Compute(d, Query{From: ts("2026-10-01T03:00:00Z"), To: ts("2026-11-01T02:59:59Z")})
	if oct.NetTotal != 20000 || !oct.Invariant.Closes || lineOf(oct, LevelProven, "google_ads", "gg-caso-3").Net != 20000 {
		t.Fatalf("october: %+v", oct)
	}
	// Without the October refund, the November one would be split half and half.
	var noPrior Dataset = d
	noPrior.Refunds = nil
	for _, f := range d.Refunds {
		if f.ExternalID != "00000000000000000000e304" {
			noPrior.Refunds = append(noPrior.Refunds, f)
		}
	}
	res2, _ := Compute(noPrior, nov)
	if l := lineOf(res2, LevelProven, "google_ads", "gg-caso-3"); l.Net != -5000 {
		t.Fatalf("no prior refund: %+v", l)
	}
}

func TestRefundBeyondBalanceAndOrder(t *testing.T) {
	acq := Record{ExternalID: "acq", CustomerRef: "px", Source: "google_ads", Gclid: "G", Acquisition: true, At: ts("2026-11-01T13:00:00Z")}
	d := Dataset{Customers: []Record{cust("px")}, Contacts: []Record{acq},
		Receipts: []Record{{ExternalID: "r", CustomerRef: "px", AmountCents: 10000, PaymentRef: "p", At: ts("2026-11-05T15:00:00Z")}},
		// Listed out of order on purpose: they are applied by date.
		Refunds: []Record{
			{ExternalID: "z2", AmountCents: 6000, PaymentRef: "p", At: ts("2026-11-20T15:00:00Z")},
			{ExternalID: "z1", AmountCents: 7000, ReceiptRef: "r", At: ts("2026-11-10T15:00:00Z")},
		}}
	res, _ := Compute(d, Query{})
	l := lineOf(res, LevelProven, "google_ads", "")
	if l == nil || l.Net != 0 || res.Unlinked.Reasons["estorno_acima_do_saldo"] != -3000 || res.NetTotal != -3000 || !res.Invariant.Closes {
		t.Fatalf("beyond balance: %+v %+v", l, res.Unlinked)
	}
	// Remainder cents never go above a receipt's balance.
	d.Receipts = []Record{
		{ExternalID: "a", CustomerRef: "px", AmountCents: 1, PaymentRef: "q", At: ts("2026-10-05T15:00:00Z")},
		{ExternalID: "b", CustomerRef: "px", AmountCents: 1, PaymentRef: "q", At: ts("2026-10-06T15:00:00Z")},
		{ExternalID: "c", CustomerRef: "px", AmountCents: 9998, PaymentRef: "q", At: ts("2026-11-06T15:00:00Z")},
	}
	d.Refunds = []Record{{ExternalID: "w", AmountCents: 10000, PaymentRef: "q", At: ts("2026-11-20T15:00:00Z")}}
	res, _ = Compute(d, Query{})
	if res.NetTotal != 0 || res.ByLevel[LevelNone] != 0 || res.ByLevel[LevelProven] != 0 || res.Unlinked.Reasons["estorno_acima_do_saldo"] != 0 {
		t.Fatalf("cents within balance: %+v", res)
	}
}
