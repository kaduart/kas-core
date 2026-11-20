package attribution

import (
	"encoding/json"
	"testing"
	"time"
)

// The second fictional company (a school) uses e-mails, units, one payment
// with two receipts, a shared household, a contact after the receipt and a
// voided line: none of it needs code of its own.
func TestEscolaSample(t *testing.T) {
	d := loadSample(t, "escola-demo", "escola-demo")
	kept := d.Receipts[:0]
	for _, r := range d.Receipts {
		if !r.Voided {
			kept = append(kept, r)
		}
	}
	d.Receipts = kept
	res, err := Compute(d, Query{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(res, "", " ")
	if res.NetTotal != 647000 || res.ByLevel[LevelProven] != 267000 || res.ByLevel[LevelProbable] != 285000 ||
		res.ByLevel[LevelNone] != 95000 || !res.Invariant.Closes {
		t.Fatalf("escola report:\n%s", raw)
	}
	if res.Unlinked.Reasons["sem_origem_capturada"] != 95000 || res.Unlinked.Reasons["contato_da_campanha_depois_do_recebimento"] != 0 {
		t.Fatalf("reasons: %v", res.Unlinked.Reasons)
	}
	// Unit filter: centro only.
	centro, _ := Compute(d, Query{Unit: "centro"})
	if centro.NetTotal != 172000+95000+95000 || !centro.Invariant.Closes {
		t.Fatalf("centro: %d", centro.NetTotal)
	}
	// Period filter: up to 05/11 keeps mat-01, men-01 and mat-02 (refund on 12/11 out).
	early, _ := Compute(d, Query{To: time.Date(2026, 11, 5, 23, 59, 59, 0, time.UTC)})
	if early.NetTotal != 310000 || !early.Invariant.Closes {
		t.Fatalf("period: %d", early.NetTotal)
	}
}
