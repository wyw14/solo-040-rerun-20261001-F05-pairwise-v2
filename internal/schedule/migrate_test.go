package schedule

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/admission"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type fakeGuard struct {
	mu             sync.Mutex
	cycles         map[string]model.TransitCycle
	signals        map[uuid.UUID]model.SignalState
	devicesReady   bool
	equipmentIdle  bool
	cancelledPlans []uuid.UUID
}

func (g *fakeGuard) CurrentCycle(chamberID string) (model.TransitCycle, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cycleValue, ok := g.cycles[chamberID]
	return cycleValue, ok
}

func (g *fakeGuard) CancelOwnedPlannedCycle(_ context.Context, chamberID string, planID uuid.UUID, _ string) error {
	g.mu.Lock()
	g.cancelledPlans = append(g.cancelledPlans, planID)
	delete(g.cycles, chamberID)
	g.mu.Unlock()
	return nil
}

func (g *fakeGuard) SignalForVessel(vesselID uuid.UUID) (model.SignalState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.signals[vesselID]
	return state, ok
}

func (g *fakeGuard) DevicesReady(string) bool  { return g.devicesReady }
func (g *fakeGuard) EquipmentIdle(string) bool { return g.equipmentIdle }

func newTestPlanner(t *testing.T, guard MigrationGuard) (*Planner, *admission.Service, *admission.Queue, *fakeGuard, *signal.Store) {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC) }
	recorder := journal.NewRecorder(journal.NewMemoryStore(), now)
	queue := admission.NewQueue()
	gates := gate.NewState("c1", "c2")
	signals := signal.NewStore()
	controller := admission.NewController(queue, gates, signals, now)
	admissionService := admission.NewService(controller, recorder, now)
	planner := NewPlanner(admissionService, recorder, now)
	planner.BindChambers([]string{"c1", "c2"})
	fake, _ := guard.(*fakeGuard)
	planner.BindMigrationGuard(guard)
	return planner, admissionService, queue, fake, signals
}

func vessels(names ...string) []model.Vessel {
	result := make([]model.Vessel, 0, len(names))
	for _, name := range names {
		vessel, err := model.NewVessel(name, name+"-cs", 50)
		if err != nil {
			panic(err)
		}
		result = append(result, vessel)
	}
	return result
}

func TestMigrateSuccessMovesConvoyAtomically(t *testing.T) {
	guard := &fakeGuard{devicesReady: true, equipmentIdle: true, cycles: map[string]model.TransitCycle{}}
	planner, admissionService, _, fake, _ := newTestPlanner(t, guard)
	ctx := context.Background()

	plan, err := planner.Create(ctx, "c1", model.DirectionUpstream, vessels("V1", "V2"))
	if err != nil {
		t.Fatal(err)
	}
	// A planned, not-started cycle references the plan in the source chamber.
	cycleValue, err := model.NewTransitCycle("c1", plan.ID, model.DirectionUpstream, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	guard.cycles["c1"] = cycleValue

	result, err := planner.Migrate(ctx, plan.ID, "c2")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Migrated {
		t.Fatalf("migration rejected: %v", result.Failures())
	}
	if len(result.Vessels) != 2 || result.Vessels[0].FromPosition != 1 || result.Vessels[0].ToPosition != 1 ||
		result.Vessels[1].FromPosition != 2 || result.Vessels[1].ToPosition != 2 {
		t.Fatalf("unexpected before/after positions: %+v", result.Vessels)
	}
	moved, ok := planner.Get(plan.ID)
	if !ok || moved.ChamberID != "c2" {
		t.Fatalf("plan not reassigned to target chamber: %+v", moved)
	}
	if queue := admissionService.List("c1"); len(queue) != 0 {
		t.Fatalf("source queue must be emptied atomically: %+v", queue)
	}
	target := admissionService.List("c2")
	if len(target) != 2 || target[0].Vessel.Name != "V1" || target[1].Vessel.Name != "V2" {
		t.Fatalf("convoy order not preserved in target: %+v", target)
	}
	if len(fake.cancelledPlans) != 1 || fake.cancelledPlans[0] != plan.ID {
		t.Fatalf("expected leftover planned cycle to be cancelled, got %v", fake.cancelledPlans)
	}
}

func TestMigrateRejectsPublishedSignalAndEnteredVessel(t *testing.T) {
	guard := &fakeGuard{devicesReady: true, equipmentIdle: true, cycles: map[string]model.TransitCycle{}}
	planner, admissionService, queue, _, _ := newTestPlanner(t, guard)
	ctx := context.Background()
	plan, err := planner.Create(ctx, "c1", model.DirectionUpstream, vessels("V1", "V2"))
	if err != nil {
		t.Fatal(err)
	}

	// Publish a proceed signal for the first vessel and mark it released.
	cycleValue, err := model.NewTransitCycle("c1", plan.ID, model.DirectionUpstream, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	guard.signals = map[uuid.UUID]model.SignalState{
		plan.Vessels[0].ID: {
			VesselID: plan.Vessels[0].ID, CycleID: cycleValue.ID,
			Generation: 1, Aspect: model.SignalProceed,
		},
	}
	if !queue.MarkReleased("c1", plan.Vessels[0].ID) {
		t.Fatal("mark released failed")
	}

	result, err := planner.Migrate(ctx, plan.ID, "c2")
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{}
	for _, check := range result.Checks {
		if !check.Passed {
			failed[check.Name] = true
		}
	}
	if !failed["no_published_signal"] || !failed["not_entered_lock"] {
		t.Fatalf("expected signal and entry rejection, got %v", result.Failures())
	}
	if queue := admissionService.List("c1"); len(queue) != 2 {
		t.Fatalf("rejected migration must not touch source queue: %+v", queue)
	}
}

func TestMigrateRejectsTargetReadinessFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("devices offline", func(t *testing.T) {
		guard := &fakeGuard{devicesReady: false, equipmentIdle: true, cycles: map[string]model.TransitCycle{}}
		planner, _, _, _, _ := newTestPlanner(t, guard)
		plan, err := planner.Create(ctx, "c1", model.DirectionUpstream, vessels("V1"))
		if err != nil {
			t.Fatal(err)
		}
		result, err := planner.Migrate(ctx, plan.ID, "c2")
		if err != nil {
			t.Fatal(err)
		}
		if result.Migrated || !containsFailure(result.Failures(), "devices_ready") {
			t.Fatalf("expected devices_ready failure, got %v", result.Failures())
		}
	})

	t.Run("equipment busy", func(t *testing.T) {
		guard := &fakeGuard{devicesReady: true, equipmentIdle: false, cycles: map[string]model.TransitCycle{}}
		planner, _, _, _, _ := newTestPlanner(t, guard)
		plan, err := planner.Create(ctx, "c1", model.DirectionUpstream, vessels("V1"))
		if err != nil {
			t.Fatal(err)
		}
		result, err := planner.Migrate(ctx, plan.ID, "c2")
		if err != nil {
			t.Fatal(err)
		}
		if result.Migrated || !containsFailure(result.Failures(), "equipment_idle") {
			t.Fatalf("expected equipment_idle failure, got %v", result.Failures())
		}
	})

	t.Run("target cycle opposite direction", func(t *testing.T) {
		guard := &fakeGuard{devicesReady: true, equipmentIdle: true, cycles: map[string]model.TransitCycle{}}
		planner, _, _, _, _ := newTestPlanner(t, guard)
		plan, err := planner.Create(ctx, "c1", model.DirectionUpstream, vessels("V1"))
		if err != nil {
			t.Fatal(err)
		}
		targetCycle, err := model.NewTransitCycle("c2", uuid.New(), model.DirectionDownstream, 1, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		guard.cycles["c2"] = targetCycle
		result, err := planner.Migrate(ctx, plan.ID, "c2")
		if err != nil {
			t.Fatal(err)
		}
		if result.Migrated || !containsFailure(result.Failures(), "cycle_direction") {
			t.Fatalf("expected cycle_direction failure, got %v", result.Failures())
		}
	})

	t.Run("source cycle already admitting", func(t *testing.T) {
		guard := &fakeGuard{devicesReady: true, equipmentIdle: true, cycles: map[string]model.TransitCycle{}}
		planner, _, _, _, _ := newTestPlanner(t, guard)
		plan, err := planner.Create(ctx, "c1", model.DirectionUpstream, vessels("V1"))
		if err != nil {
			t.Fatal(err)
		}
		cycleValue, err := model.NewTransitCycle("c1", plan.ID, model.DirectionUpstream, 1, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := cycleValue.Advance(model.StageAdmitting, "gate settled", time.Now()); err != nil {
			t.Fatal(err)
		}
		guard.cycles["c1"] = cycleValue
		result, err := planner.Migrate(ctx, plan.ID, "c2")
		if err != nil {
			t.Fatal(err)
		}
		if result.Migrated || !containsFailure(result.Failures(), "own_cycle_not_started") {
			t.Fatalf("expected own_cycle_not_started failure, got %v", result.Failures())
		}
	})
}

func TestMigrateRejectsQueueDirectionConflict(t *testing.T) {
	guard := &fakeGuard{devicesReady: true, equipmentIdle: true, cycles: map[string]model.TransitCycle{}}
	planner, _, _, _, _ := newTestPlanner(t, guard)
	ctx := context.Background()
	upstream, err := planner.Create(ctx, "c1", model.DirectionUpstream, vessels("U1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Create(ctx, "c2", model.DirectionDownstream, vessels("D1")); err != nil {
		t.Fatal(err)
	}
	result, err := planner.Migrate(ctx, upstream.ID, "c2")
	if err != nil {
		t.Fatal(err)
	}
	if result.Migrated || !containsFailure(result.Failures(), "queue_direction") {
		t.Fatalf("expected queue_direction failure, got %v", result.Failures())
	}
}

func TestMigrateRejectsSameOrUnknownChamber(t *testing.T) {
	guard := &fakeGuard{devicesReady: true, equipmentIdle: true, cycles: map[string]model.TransitCycle{}}
	planner, _, _, _, _ := newTestPlanner(t, guard)
	ctx := context.Background()
	plan, err := planner.Create(ctx, "c1", model.DirectionUpstream, vessels("V1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"c1", "c3", ""} {
		result, err := planner.Migrate(ctx, plan.ID, target)
		if err != nil {
			t.Fatal(err)
		}
		if result.Migrated {
			t.Fatalf("migration to %q must be rejected", target)
		}
	}
}

func containsFailure(reasons []string, name string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, "/"+name+":") {
			return true
		}
	}
	return false
}
