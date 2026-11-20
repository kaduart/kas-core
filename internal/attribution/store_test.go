package attribution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"kas-core-pilot/internal/pgtest"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), pgtest.SchemaDSN(t), true, fingerprinter(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// importSample sends testdata/attribution/<dir> as four batches.
func importSample(t *testing.T, s *Store, tenant, dir, prefix string) map[string]BatchResult {
	t.Helper()
	out := map[string]BatchResult{}
	for _, kind := range []string{KindCustomer, KindContact, KindReceipt, KindRefund} {
		f, err := os.Open(filepath.Join("..", "..", "testdata", "attribution", dir, kind+".csv"))
		if err != nil {
			t.Fatal(err)
		}
		items, err := ParseCSV(kind, f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		res, _, err := s.Import(context.Background(), Batch{TenantID: tenant, ServiceID: "sis", Kind: kind,
			BatchID: prefix + "-" + kind, Actor: "user.fin-07", Items: items})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		out[kind] = res
	}
	return out
}

func report(t *testing.T, s *Store, tenant string) Result {
	t.Helper()
	d, err := s.Current(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Compute(d, Query{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestImportIsIdempotentAndVersioned(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	first := importSample(t, s, "escola-demo", "amostra-fono", "imp-1")
	if first[KindReceipt].Created != 10 || first[KindCustomer].Created != 10 || first[KindContact].Created != 9 || first[KindRefund].Created != 1 {
		t.Fatalf("first import: %+v", first)
	}
	before := report(t, s, "escola-demo")
	if before.NetTotal != 129000 || !before.Invariant.Closes {
		t.Fatalf("report after first import: %+v", before)
	}

	// Same batch ids, same content: the original answers come back, nothing is written.
	replay := importSample(t, s, "escola-demo", "amostra-fono", "imp-1")
	if replay[KindReceipt].Created != 10 {
		t.Fatalf("batch replay must return the original answer: %+v", replay[KindReceipt])
	}
	// New batch ids, same file: every line unchanged, same totals.
	again := importSample(t, s, "escola-demo", "amostra-fono", "imp-2")
	for kind, r := range again {
		if r.Created != 0 || r.Updated != 0 || r.Unchanged == 0 {
			t.Fatalf("%s reimport: %+v", kind, r)
		}
	}
	if after := report(t, s, "escola-demo"); after.NetTotal != before.NetTotal || after.ByLevel[LevelProven] != before.ByLevel[LevelProven] {
		t.Fatalf("reimport changed totals: %+v", after)
	}

	// The same batch id with other content is a conflict.
	_, _, err := s.Import(ctx, Batch{TenantID: "escola-demo", ServiceID: "sis", Kind: KindReceipt, BatchID: "imp-1-receipts",
		Actor: "user.fin-07", Items: []Item{{ExternalID: "rec-10", CustomerRef: "cli-ana", AmountCents: 1, ReceivedAt: "2026-11-10T15:00:00Z"}}})
	if !errors.Is(err, ErrBatchConflict) {
		t.Fatalf("batch conflict: %v", err)
	}

	// A changed line is a new version; the history keeps both.
	res, _, err := s.Import(ctx, Batch{TenantID: "escola-demo", ServiceID: "sis", Kind: KindReceipt, BatchID: "imp-3", Actor: "user.fin-07",
		Items: []Item{{ExternalID: "rec-10", CustomerRef: "cli-ana", AmountCents: 15000, ReceivedAt: "2026-11-10T15:00:00Z", PaymentRef: "pay-90"}}})
	if err != nil || res.Updated != 1 {
		t.Fatalf("update: %+v %v", res, err)
	}
	if n, _ := s.VersionsOf(ctx, "escola-demo", KindReceipt, "rec-10"); n != 2 {
		t.Fatalf("versions: %d", n)
	}
	if r := report(t, s, "escola-demo"); r.NetTotal != 132000 {
		t.Fatalf("updated total: %d", r.NetTotal)
	}

	// Voiding removes it from the report as a new version, without deleting.
	res, _, err = s.Import(ctx, Batch{TenantID: "escola-demo", ServiceID: "sis", Kind: KindReceipt, BatchID: "imp-4", Actor: "user.fin-07",
		Items: []Item{{ExternalID: "rec-10", CustomerRef: "cli-ana", AmountCents: 15000, ReceivedAt: "2026-11-10T15:00:00Z", PaymentRef: "pay-90", Voided: true}}})
	if err != nil || res.Updated != 1 {
		t.Fatalf("void: %+v %v", res, err)
	}
	if r := report(t, s, "escola-demo"); r.NetTotal != 117000 || !r.Invariant.Closes {
		t.Fatalf("voided total: %d", r.NetTotal)
	}
	if n, _ := s.VersionsOf(ctx, "escola-demo", KindReceipt, "rec-10"); n != 3 {
		t.Fatalf("versions after void: %d", n)
	}
}

func TestImportRejectsLinesWithoutStoppingAndIsolatesTenants(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	res, _, err := s.Import(ctx, Batch{TenantID: "escola-demo", ServiceID: "sis", Kind: KindReceipt, BatchID: "mix", Actor: "user.fin-07",
		Items: []Item{
			{ExternalID: "ok-1", CustomerRef: "c1", AmountCents: 5000, ReceivedAt: "2026-11-10T15:00:00Z"},
			{ExternalID: "bad", CustomerRef: "c1", AmountCents: -1, ReceivedAt: "2026-11-10T15:00:00Z"},
			{ExternalID: "ok-1", CustomerRef: "c1", AmountCents: 6000, ReceivedAt: "2026-11-10T15:00:00Z"},
		}})
	if err != nil || res.Created != 1 || len(res.Rejected) != 2 || res.Rejected[0].Code != "amount_invalid" || res.Rejected[1].Code != "duplicate_in_batch" {
		t.Fatalf("mixed batch: %+v %v", res, err)
	}
	// The same external ids in another tenant do not collide or leak.
	importSample(t, s, "fono-demo", "amostra-fono", "f")
	importSample(t, s, "escola-demo", "amostra-fono", "e")
	if r := report(t, s, "fono-demo"); r.NetTotal != 129000 {
		t.Fatalf("fono total: %d", r.NetTotal)
	}
	if r := report(t, s, "escola-demo"); r.NetTotal != 134000 {
		t.Fatalf("escola total (sample + ok-1): %d", r.NetTotal)
	}
	if missing, err := s.Missing(ctx, "loja-demo"); err != nil || len(missing) != 2 {
		t.Fatalf("empty tenant readiness: %v %v", missing, err)
	}
	if missing, _ := s.Missing(ctx, "fono-demo"); len(missing) != 0 {
		t.Fatalf("ready tenant: %v", missing)
	}
	// Raw phones are never stored.
	var leaked int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM attribution_items WHERE record::text LIKE '%99999%' OR record::text LIKE '%phone%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("raw phone stored: %d %v", leaked, err)
	}
	if _, _, err := s.Import(ctx, Batch{TenantID: "t", Kind: "pacientes", BatchID: "x", Actor: "user.a", Items: []Item{{ExternalID: "a"}}}); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("unknown kind: %v", err)
	}
}
