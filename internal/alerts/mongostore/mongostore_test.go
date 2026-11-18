package mongostore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/alerts"
	"kas-core-pilot/internal/mongotest"
)

// Reinício real: duas centrais, um documento no Mongo descartável. Assuntos
// com "." e "$" (ids reais do WhatsApp têm ponto) passam pela gravação.
func TestRestartThroughMongo(t *testing.T) {
	uri := mongotest.URI(t)
	ctx := context.Background()
	cli, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Disconnect(ctx) })
	db := cli.Database(fmt.Sprintf("kas_alerts_test_%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = db.Drop(ctx) })
	store := Store{Coll: db.Collection("kas_alert_center"), Name: "outbox-recovery"}

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	sink := &alerts.SimulatedSink{FailNext: 1}
	a := func(tenant, subj string) alerts.Alert {
		return alerts.Alert{Tenant: tenant, Source: "crm.outbox", Kind: "entry_exhausted", Subject: subj, Severity: alerts.Critical, Summary: subj}
	}

	c1 := alerts.New(alerts.DefaultConfig(), sink, clk)
	c1.SetStore(store)
	c1.Raise(ctx, a("clinica.a", "wamid.HBg$1")) // destino fora: pendente
	c1.Raise(ctx, a("clinica.b", "evt.2"))       // outro tenant: entregue
	if c1.PersistErrors() != 0 {
		t.Fatal("gravação no Mongo falhou")
	}

	c2 := alerts.New(alerts.DefaultConfig(), sink, clk)
	if err := c2.Restore(ctx, store); err != nil {
		t.Fatal(err)
	}
	if c2.Pending() != 1 {
		t.Fatalf("pendente perdido: %d", c2.Pending())
	}
	if d, _ := c2.Raise(ctx, a("clinica.b", "evt.2")); d != alerts.Duplicate {
		t.Fatalf("dedup perdido: %s", d)
	}
	if err := c2.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got := sink.Delivered()
	if len(got) != 2 || got[1].Tenant != "clinica.a" || got[1].Subjects[0] != "wamid.HBg$1" {
		t.Fatalf("%+v", got)
	}
}
