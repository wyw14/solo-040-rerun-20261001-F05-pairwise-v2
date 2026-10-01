package schedule

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/admission"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type stubChecks struct {
	cycles    map[string]model.TransitCycle
	published map[uuid.UUID]bool
	online    map[string]bool
	emergency map[string]bool
}

func newStubChecks() *stubChecks {
	return &stubChecks{
		cycles:    map[string]model.TransitCycle{},
		published: map[uuid.UUID]bool{},
		online:    map[string]bool{"chamber-1": true, "chamber-2": true},
		emergency: map[string]bool{},
	}
}

func (s *stubChecks) CurrentCycle(chamberID string) (model.TransitCycle, bool) {
	cycle, ok := s.cycles[chamberID]
	return cycle, ok
}

func (s *stubChecks) SignalPublished(vesselID uuid.UUID) bool { return s.published[vesselID] }
func (s *stubChecks) AllDevicesOnline(chamberID string) bool  { return s.online[chamberID] }
func (s *stubChecks) EmergencyActive(chamberID string) bool   { return s.emergency[chamberID] }

func newTestPlanner(t *testing.T) (*Planner, *admission.Service, journal.Store) {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	store := journal.NewMemoryStore()
	recorder := journal.NewRecorder(store, now)
	queue := admission.NewQueue()
	controller := admission.NewController(queue, gate.NewState("chamber-1", "chamber-2"), signal.NewStore(), now)
	admissionService := admission.NewService(controller, recorder, now)
	planner := NewPlanner(admissionService, recorder, []string{"chamber-1", "chamber-2"}, now)
	return planner, admissionService, store
}

func vessels(names ...string) []model.Vessel {
	result := make([]model.Vessel, 0, len(names))
	for _, name := range names {
		vessel, _ := model.NewVessel(name, "CS-"+name, 60)
		result = append(result, vessel)
	}
	return result
}

func createPlan(t *testing.T, planner *Planner, chamberID string, direction model.Direction, names ...string) model.ConvoyPlan {
	t.Helper()
	plan, err := planner.Create(context.Background(), chamberID, direction, vessels(names...))
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return plan
}

func reasonContains(t *testing.T, err error, fragment string) {
	t.Helper()
	var migrationErr *MigrationError
	if !errors.As(err, &migrationErr) {
		t.Fatalf("expected MigrationError, got %v", err)
	}
	joined := strings.Join(migrationErr.Reasons, "|")
	if !strings.Contains(joined, fragment) {
		t.Fatalf("expected reason containing %q, got %v", fragment, migrationErr.Reasons)
	}
}

func TestMigrateMovesConvoyAtomicallyAndPreservesOrder(t *testing.T) {
	planner, admissionService, _ := newTestPlanner(t)
	checks := newStubChecks()

	planMoving := createPlan(t, planner, "chamber-1", model.DirectionUpstream, "A1", "A2")
	createPlan(t, planner, "chamber-2", model.DirectionUpstream, "C1", "C2")

	migration, err := planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migration.SourceChamber != "chamber-1" || migration.TargetChamber != "chamber-2" {
		t.Fatalf("unexpected chambers: %+v", migration)
	}
	if len(migration.Positions) != 2 {
		t.Fatalf("expected 2 vessel positions, got %d", len(migration.Positions))
	}
	if migration.Positions[0].Name != "A1" || migration.Positions[0].SourceIndex != 0 || migration.Positions[0].TargetIndex != 2 {
		t.Fatalf("unexpected A1 position: %+v", migration.Positions[0])
	}
	if migration.Positions[1].Name != "A2" || migration.Positions[1].SourceIndex != 1 || migration.Positions[1].TargetIndex != 3 {
		t.Fatalf("unexpected A2 position: %+v", migration.Positions[1])
	}

	moved, ok := planner.Get(planMoving.ID)
	if !ok || moved.ChamberID != "chamber-2" {
		t.Fatalf("plan chamber not updated: %+v", moved)
	}
	targetQueue := admissionService.List("chamber-2")
	got := []string{}
	for _, entry := range targetQueue {
		got = append(got, entry.Vessel.Name)
	}
	want := []string{"C1", "C2", "A1", "A2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("target queue order = %v, want %v", got, want)
		}
	}
	if len(admissionService.List("chamber-1")) != 0 {
		t.Fatal("source chamber queue must be empty after migration")
	}
	if migration.TargetQueueLen != 4 || migration.SourceQueueLen != 0 {
		t.Fatalf("unexpected queue lengths: %+v", migration)
	}
}

func TestMigrateRejectsConflictingQueueDirection(t *testing.T) {
	planner, admissionService, _ := newTestPlanner(t)
	checks := newStubChecks()

	planMoving := createPlan(t, planner, "chamber-1", model.DirectionUpstream, "A1")
	createPlan(t, planner, "chamber-2", model.DirectionDownstream, "D1")

	_, err := planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks)
	reasonContains(t, err, "conflicting with convoy direction")

	if len(admissionService.List("chamber-1")) != 1 {
		t.Fatal("source queue must be untouched after rejection")
	}
	if moved, _ := planner.Get(planMoving.ID); moved.ChamberID != "chamber-1" {
		t.Fatal("plan must stay on source chamber after rejection")
	}
}

func TestMigrateRejectsPublishedSignalAndEnteredLockage(t *testing.T) {
	planner, _, _ := newTestPlanner(t)
	planMoving := createPlan(t, planner, "chamber-1", model.DirectionUpstream, "A1", "A2")

	// Signal already shown to the lead vessel.
	checks := newStubChecks()
	checks.published[planMoving.Vessels[0].ID] = true
	_, err := planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks)
	reasonContains(t, err, "signal already published")

	// Lockage already opened for the plan (vessels entered the chamber).
	checks = newStubChecks()
	checks.cycles["chamber-1"] = model.TransitCycle{
		ID: uuid.New(), ChamberID: "chamber-1", PlanID: planMoving.ID, Stage: model.StageAdmitting,
	}
	_, err = planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks)
	reasonContains(t, err, "already entered the chamber")
}

func TestMigrateAllowsQueuedPlanAfterTerminalLockage(t *testing.T) {
	planner, _, _ := newTestPlanner(t)
	planMoving := createPlan(t, planner, "chamber-1", model.DirectionUpstream, "A1")

	// The chamber was taken out of service by an emergency stop; the convoy
	// never entered, so its queued plan must still be evacuable.
	checks := newStubChecks()
	checks.cycles["chamber-1"] = model.TransitCycle{
		ID: uuid.New(), ChamberID: "chamber-1", PlanID: planMoving.ID, Stage: model.StageEmergency,
	}
	migration, err := planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks)
	if err != nil {
		t.Fatalf("queued plan after a terminal lockage should migrate, got %v", err)
	}
	if migration.TargetChamber != "chamber-2" || migration.Positions[0].SourceIndex != 0 {
		t.Fatalf("unexpected migration: %+v", migration)
	}
}

func TestMigrateRejectsBusyOfflineOrEmergencyTarget(t *testing.T) {
	planner, _, _ := newTestPlanner(t)
	planMoving := createPlan(t, planner, "chamber-1", model.DirectionUpstream, "A1")

	// Target running a lockage.
	checks := newStubChecks()
	checks.cycles["chamber-2"] = model.TransitCycle{
		ID: uuid.New(), ChamberID: "chamber-2", PlanID: uuid.New(), Stage: model.StageLeveling,
	}
	_, err := planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks)
	reasonContains(t, err, "running lockage")

	// Offline equipment on an otherwise idle target.
	checks = newStubChecks()
	checks.online["chamber-2"] = false
	_, err = planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks)
	reasonContains(t, err, "offline equipment")

	// Emergency draining on target.
	checks = newStubChecks()
	checks.emergency["chamber-2"] = true
	_, err = planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks)
	reasonContains(t, err, "emergency")
}

func TestMigrateRejectsInvalidTargetsAndCancelledPlan(t *testing.T) {
	planner, _, _ := newTestPlanner(t)
	checks := newStubChecks()
	planMoving := createPlan(t, planner, "chamber-1", model.DirectionUpstream, "A1")

	if _, err := planner.Migrate(context.Background(), planMoving.ID, "chamber-1", checks); err == nil {
		t.Fatal("migration to the same chamber must fail")
	}
	if _, err := planner.Migrate(context.Background(), planMoving.ID, "chamber-9", checks); err == nil {
		t.Fatal("migration to an unknown chamber must fail")
	}
	if _, err := planner.Migrate(context.Background(), planMoving.ID, "", checks); err == nil {
		t.Fatal("migration without target chamber must fail")
	}

	cancelled := createPlan(t, planner, "chamber-1", model.DirectionUpstream, "X1")
	cancelled.Cancelled = true
	planner.replace(cancelled)
	_, err := planner.Migrate(context.Background(), cancelled.ID, "chamber-2", checks)
	reasonContains(t, err, "cancelled")
}

func TestMigrateRecordsEventOnBothPartitions(t *testing.T) {
	planner, _, store := newTestPlanner(t)
	checks := newStubChecks()
	planMoving := createPlan(t, planner, "chamber-1", model.DirectionUpstream, "A1")

	if _, err := planner.Migrate(context.Background(), planMoving.ID, "chamber-2", checks); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	for _, chamberID := range []string{"chamber-1", "chamber-2"} {
		events, err := store.Events(ctx, chamberID, 0)
		if err != nil {
			t.Fatalf("events %s: %v", chamberID, err)
		}
		found := false
		for _, event := range events {
			if event.Type == model.EventPlanMigrated {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected plan.migrated event in %s partition, got %d events", chamberID, len(events))
		}
	}
}
