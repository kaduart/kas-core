package crmagenda

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"kas-core-pilot/internal/scheduling"
)

// Unit tests of the mapping (no database).

func mapper(t *testing.T) *Mapper {
	t.Helper()
	m, err := NewMapper(keyA, zone)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMapperRefusesWeakKeyAndBadZone(t *testing.T) {
	if _, err := NewMapper([]byte("curta"), zone); err == nil {
		t.Fatal("short key accepted")
	}
	for _, z := range []string{"", "Local", "Marte/Base"} {
		if _, err := NewMapper(keyA, z); err == nil {
			t.Fatalf("zone %q accepted", z)
		}
	}
}

func TestRefsAreOpaqueStableAndKeyed(t *testing.T) {
	m := mapper(t)
	id := bson.NewObjectID()
	a, b := m.BookingRef(id), m.BookingRef(id)
	other, _ := NewMapper(keyB, zone)
	if a != b || a == other.BookingRef(id) || strings.Contains(a, id.Hex()) || !scheduling.ValidRef(a) {
		t.Fatalf("ref %q", a)
	}
	if m.Ref("pr", id.Hex()) == m.Ref("cl", id.Hex()) {
		t.Fatal("kinds must not collide")
	}
}

func TestStatusMapFollowsTheCRMSlotRule(t *testing.T) {
	cases := map[string]string{
		"canceled": scheduling.StatusCanceled, "cancelado": scheduling.StatusCanceled, "cancelada": scheduling.StatusCanceled,
		"completed": scheduling.StatusCompleted, "pre_agendado": scheduling.StatusTentative, "confirmed": scheduling.StatusConfirmed,
		"scheduled": scheduling.StatusScheduled, "paid": scheduling.StatusScheduled, "missed": scheduling.StatusScheduled,
		"suspended": scheduling.StatusScheduled, "": scheduling.StatusScheduled,
	}
	for crm, want := range cases {
		if got := KASStatus(crm, false); got != want {
			t.Errorf("%q -> %q, want %q", crm, got, want)
		}
		if got := KASStatus(crm, true); got != scheduling.StatusCanceled {
			t.Errorf("deleted %q -> %q", crm, got)
		}
	}
}

func TestMapUsesCRMDayAndClinicZone(t *testing.T) {
	m := mapper(t)
	doc, pat := bson.NewObjectID(), bson.NewObjectID()
	d := Doc{ID: bson.NewObjectID(), Doctor: &doc, Patient: &pat, Date: time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC),
		Time: "08:00", Duration: int32(60), Status: "Weird Status", UpdatedAt: time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)}
	it, _, ok := m.Map(d)
	if !ok {
		t.Fatal("not mapped")
	}
	if want := time.Date(2027, 3, 1, 11, 0, 0, 0, time.UTC); !it.Start.Equal(want) || it.End.Sub(it.Start) != time.Hour {
		t.Fatalf("start %v end %v", it.Start, it.End)
	}
	if it.SourceStatus != "unknown" {
		t.Fatalf("unsafe source status kept: %q", it.SourceStatus)
	}
	// A CRM day saved late at night UTC still belongs to the same calendar day.
	d.Date = time.Date(2027, 3, 1, 23, 59, 0, 0, time.UTC)
	if it2, _, _ := m.Map(d); !it2.Start.Equal(it.Start) {
		t.Fatalf("day shifted: %v", it2.Start)
	}
	d.UpdatedAt = time.Time{}
	if it3, _, _ := m.Map(d); !it3.SourceUpdatedAt.Equal(d.ID.Timestamp().UTC()) {
		t.Fatal("missing updatedAt must fall back to the id time")
	}
}

func TestProjectionReadsNoPersonalField(t *testing.T) {
	allowed := map[string]bool{"_id": true, "doctor": true, "patient": true, "date": true, "time": true, "duration": true,
		"operationalStatus": true, "updatedAt": true, "isDeleted": true}
	for _, f := range Projection {
		if !allowed[f.Key] {
			t.Fatalf("projection reads %q", f.Key)
		}
	}
}
