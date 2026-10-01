package admission

import (
	"testing"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

func TestQueueRelocatePlanPreservesOrderAndPositions(t *testing.T) {
	queue := NewQueue()
	planA := uuid.New()
	planB := uuid.New()
	vessel := func(name string) model.Vessel {
		vessel, err := model.NewVessel(name, name, 40)
		if err != nil {
			t.Fatal(err)
		}
		return vessel
	}
	if err := queue.Add("c1", planA, []model.Vessel{vessel("A1"), vessel("A2")}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Add("c1", planB, []model.Vessel{vessel("B1"), vessel("B2")}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Add("c2", uuid.New(), []model.Vessel{vessel("X1")}); err != nil {
		t.Fatal(err)
	}

	removed, fromIndex, toIndex, moved := queue.RelocatePlan("c1", "c2", planB)
	if !moved {
		t.Fatal("expected relocation to move entries")
	}
	if fromIndex != 2 || toIndex != 1 {
		t.Fatalf("unexpected positions from=%d to=%d", fromIndex, toIndex)
	}
	if len(removed) != 2 || removed[0].Vessel.Name != "B1" || removed[1].Vessel.Name != "B2" {
		t.Fatalf("convoy order not preserved: %+v", removed)
	}
	for _, entry := range removed {
		if entry.Released {
			t.Fatal("migrated entries must reset released flag")
		}
	}
	source := queue.List("c1")
	target := queue.List("c2")
	if len(source) != 2 || source[0].Vessel.Name != "A1" || source[1].Vessel.Name != "A2" {
		t.Fatalf("source queue after relocation wrong: %+v", source)
	}
	if len(target) != 3 || target[1].Vessel.Name != "B1" || target[2].Vessel.Name != "B2" {
		t.Fatalf("target queue should keep convoy order at tail: %+v", target)
	}

	queue.RestoreRelocation("c1", "c2", planB, fromIndex, removed)
	if restored := queue.List("c1"); len(restored) != 4 ||
		restored[2].Vessel.Name != "B1" || restored[3].Vessel.Name != "B2" {
		t.Fatalf("rollback did not reinstate convoy at original index: %+v", restored)
	}
	if target := queue.List("c2"); len(target) != 1 || target[0].Vessel.Name != "X1" {
		t.Fatalf("rollback left entries in target queue: %+v", target)
	}
}

func TestQueueRelocatePlanRejectsInvalidInputs(t *testing.T) {
	queue := NewQueue()
	plan := uuid.New()
	if err := queue.Add("c1", plan, []model.Vessel{{ID: uuid.New(), Name: "V", CallSign: "V", LengthM: 10}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		from, to string
		id       uuid.UUID
	}{
		{"", "c2", plan}, {"c1", "", plan}, {"c1", "c1", plan}, {"c1", "c2", uuid.Nil},
	} {
		if _, _, _, moved := queue.RelocatePlan(tc.from, tc.to, tc.id); moved {
			t.Fatalf("relocation should reject %+v", tc)
		}
	}
}
