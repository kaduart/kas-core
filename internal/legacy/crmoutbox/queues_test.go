package crmoutbox

import "testing"

func TestDestinations(t *testing.T) {
	q, ok := Destinations("APPOINTMENT_CREATED")
	if !ok || len(q) != 3 || q[0] != "notification" {
		t.Fatalf("APPOINTMENT_CREATED -> %v %v", q, ok)
	}
	q[0] = "mutated"
	if q2, _ := Destinations("APPOINTMENT_CREATED"); q2[0] != "notification" {
		t.Fatal("Destinations leaks the internal slice")
	}
	if q, ok := Destinations("TOTALS_RECALCULATED"); !ok || len(q) != 0 {
		t.Fatalf("TOTALS_RECALCULATED (no queue) -> %v %v", q, ok)
	}
	// Produzidos no Outbox pelo CRM, mas fora do mapa (UNKNOWN_EVENT_TYPE no dispatcher).
	for _, ty := range []string{"APPOINTMENT_CONFIRMED", "INSURANCE_BATCH_PROCESSING", "INSURANCE_ITEM_RETRYING", ""} {
		if _, ok := Destinations(ty); ok {
			t.Fatalf("%q should be unknown", ty)
		}
	}
	if !ExternalSend("notification") || ExternalSend("patient-projection") {
		t.Fatal("external send classification")
	}
	for q := range externalSendQueues {
		found := false
		for _, ty := range KnownTypes() {
			d, _ := Destinations(ty)
			for _, x := range d {
				found = found || x == q
			}
		}
		if !found {
			t.Fatalf("external queue %q is not a destination of any event type", q)
		}
	}
}
