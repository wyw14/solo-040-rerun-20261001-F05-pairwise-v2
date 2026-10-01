package admission

import (
	"testing"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

func testVessels(names ...string) []model.Vessel {
	vessels := make([]model.Vessel, 0, len(names))
	for _, name := range names {
		vessel, _ := model.NewVessel(name, "CS-"+name, 50)
		vessels = append(vessels, vessel)
	}
	return vessels
}

func TestQueueMovePlanPreservesOrderAndPositions(t *testing.T) {
	queue := NewQueue()
	planA := uuid.New()
	planB := uuid.New()
	planC := uuid.New()

	if err := queue.Add("chamber-1", planA, testVessels("A1", "A2")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Add("chamber-1", planB, testVessels("B1")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Add("chamber-2", planC, testVessels("C1", "C2")); err != nil {
		t.Fatal(err)
	}

	moves, ok := queue.MovePlan("chamber-1", "chamber-2", planA)
	if !ok {
		t.Fatal("expected move to be applied")
	}
	if len(moves) != 2 {
		t.Fatalf("expected 2 moved vessels, got %d", len(moves))
	}
	if moves[0].Vessel.Name != "A1" || moves[1].Vessel.Name != "A2" {
		t.Fatalf("convoy order not preserved: %q, %q", moves[0].Vessel.Name, moves[1].Vessel.Name)
	}
	if moves[0].SourceIndex != 0 || moves[1].SourceIndex != 1 {
		t.Fatalf("unexpected source positions: %+v", moves)
	}
	if moves[0].TargetIndex != 2 || moves[1].TargetIndex != 3 {
		t.Fatalf("expected append behind C1/C2 at positions 2,3: %+v", moves)
	}

	source := queue.List("chamber-1")
	if len(source) != 1 || source[0].PlanID != planB {
		t.Fatalf("source chamber should only retain plan B, got %+v", source)
	}
	target := queue.List("chamber-2")
	got := []string{}
	for _, entry := range target {
		got = append(got, entry.Vessel.Name)
	}
	want := []string{"C1", "C2", "A1", "A2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("target order = %v, want %v", got, want)
		}
	}
	for _, entry := range target[2:] {
		if entry.Released {
			t.Fatal("moved entries must be marked not released at target")
		}
	}
}

func TestQueueMovePlanRejectsUnknownAndSameChamber(t *testing.T) {
	queue := NewQueue()
	planA := uuid.New()
	if err := queue.Add("chamber-1", planA, testVessels("A1")); err != nil {
		t.Fatal(err)
	}
	if _, ok := queue.MovePlan("chamber-1", "chamber-2", uuid.New()); ok {
		t.Fatal("moving an absent plan must report failure")
	}
	if _, ok := queue.MovePlan("chamber-1", "chamber-1", planA); ok {
		t.Fatal("moving to the same chamber must report failure")
	}
	if _, ok := queue.MovePlan("", "chamber-2", planA); ok {
		t.Fatal("empty source chamber must report failure")
	}
}

func TestQueueUndoMoveRestoresSourceOrder(t *testing.T) {
	queue := NewQueue()
	planA := uuid.New()
	planB := uuid.New()
	if err := queue.Add("chamber-1", planA, testVessels("A1", "A2")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Add("chamber-1", planB, testVessels("B1")); err != nil {
		t.Fatal(err)
	}
	moves, _ := queue.MovePlan("chamber-1", "chamber-2", planA)
	queue.UndoMove("chamber-2", "chamber-1", moves)

	source := queue.List("chamber-1")
	got := []uuid.UUID{}
	for _, entry := range source {
		got = append(got, entry.PlanID)
	}
	if len(got) != 3 || got[0] != planA || got[1] != planA || got[2] != planB {
		t.Fatalf("source order not restored: %v", got)
	}
	if len(queue.List("chamber-2")) != 0 {
		t.Fatal("target chamber must be empty after undo")
	}
}
