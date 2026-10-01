package api

import (
	"context"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/schedule"
)

// migrationGuard adapts the runtime component graph to schedule.MigrationGuard.
type migrationGuard struct {
	runtime *Runtime
}

var _ schedule.MigrationGuard = (*migrationGuard)(nil)

func (g *migrationGuard) CurrentCycle(chamberID string) (model.TransitCycle, bool) {
	return g.runtime.Cycles.Current(chamberID)
}

func (g *migrationGuard) CancelOwnedPlannedCycle(ctx context.Context, chamberID string, planID uuid.UUID, reason string) error {
	return g.runtime.Cycles.CancelPlannedCycleForPlan(ctx, chamberID, planID, reason)
}

func (g *migrationGuard) SignalForVessel(vesselID uuid.UUID) (model.SignalState, bool) {
	return g.runtime.SignalStore.Get(vesselID)
}

func (g *migrationGuard) DevicesReady(chamberID string) bool {
	return g.runtime.Health.AllOnline(chamberID)
}

func (g *migrationGuard) EquipmentIdle(chamberID string) bool {
	return len(g.runtime.GateCommands.Pending(chamberID)) == 0 &&
		g.runtime.Safety.Barrier().Pending(chamberID) == 0
}
