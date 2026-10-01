package schedule

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// MigrationVesselPosition shows one vessel's queue slot before and after moving.
// Positions are 1-based within their chamber queue.
type MigrationVesselPosition struct {
	Vessel       model.Vessel `json:"vessel"`
	FromPosition int          `json:"from_position"`
	ToPosition   int          `json:"to_position"`
}

// MigrationCheck is one precondition evaluated for a migration.
type MigrationCheck struct {
	Scope  string `json:"scope"`
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

// Migration is the observable result of relocating one convoy plan. When
// Migrated is false every failed check carries an operator-readable reason.
type Migration struct {
	PlanID        uuid.UUID                 `json:"plan_id"`
	FromChamber   string                    `json:"from_chamber"`
	ToChamber     string                    `json:"to_chamber"`
	Direction     model.Direction           `json:"direction"`
	Migrated      bool                      `json:"migrated"`
	Checks        []MigrationCheck          `json:"checks"`
	Vessels       []MigrationVesselPosition `json:"vessels,omitempty"`
	FromQueueSize int                       `json:"from_queue_size_before"`
	ToQueueSize   int                       `json:"to_queue_size_before"`
}

// Rejected reports whether preconditions stopped the migration.
func (m Migration) Rejected() bool {
	for _, check := range m.Checks {
		if !check.Passed {
			return true
		}
	}
	return false
}

// Failures returns the reasons of every failed check.
func (m Migration) Failures() []string {
	var reasons []string
	for _, check := range m.Checks {
		if !check.Passed {
			reasons = append(reasons, check.Scope+"/"+check.Name+": "+check.Reason)
		}
	}
	return reasons
}

// MigrationGuard isolates the planner from the cycle, signal and equipment
// subsystems it must consult before relocating a convoy.
type MigrationGuard interface {
	// CurrentCycle returns the chamber's latest transit, if one exists.
	CurrentCycle(chamberID string) (model.TransitCycle, bool)
	// CancelOwnedPlannedCycle closes an empty StagePlanned transit that still
	// references a plan after the plan has left the chamber.
	CancelOwnedPlannedCycle(ctx context.Context, chamberID string, planID uuid.UUID, reason string) error
	// SignalForVessel reports whether a field signal has been published.
	SignalForVessel(vesselID uuid.UUID) (model.SignalState, bool)
	// DevicesReady reports whether all monitored devices are operational.
	DevicesReady(chamberID string) bool
	// EquipmentIdle reports whether no actuator work is outstanding.
	EquipmentIdle(chamberID string) bool
}

func (p *Planner) BindMigrationGuard(guard MigrationGuard) {
	p.mu.Lock()
	p.guard = guard
	p.mu.Unlock()
}

// BindChambers registers the chambers a migration may target.
func (p *Planner) BindChambers(chambers []string) {
	p.mu.Lock()
	p.chambers = append([]string(nil), chambers...)
	p.mu.Unlock()
}

// Migrate moves every not-yet-started vessel of one plan from its chamber to
// targetChamber. All preconditions are evaluated before any state changes; the
// convoy leaves the source queue in one atomic step and keeps its vessel order
// at the tail of the target queue.
func (p *Planner) Migrate(ctx context.Context, planID uuid.UUID, targetChamber string) (Migration, error) {
	// Serialize migrations: direction checks and the atomic queue relocation
	// must observe the same target queue.
	p.migrateMu.Lock()
	defer p.migrateMu.Unlock()
	plan, err := p.require(planID)
	if err != nil {
		return Migration{}, err
	}
	migration := Migration{
		PlanID:      plan.ID,
		FromChamber: plan.ChamberID,
		ToChamber:   targetChamber,
		Direction:   plan.Direction,
	}

	p.mu.RLock()
	guard := p.guard
	knownChambers := append([]string(nil), p.chambers...)
	p.mu.RUnlock()

	sourceQueue := p.admission.List(plan.ChamberID)
	migration.FromQueueSize = len(sourceQueue)
	targetDistinct := targetChamber != "" && targetChamber != plan.ChamberID
	if targetDistinct {
		migration.ToQueueSize = len(p.admission.List(targetChamber))
	}

	check := func(scope, name string, passed bool, reason string) {
		if passed {
			reason = ""
		}
		migration.Checks = append(migration.Checks, MigrationCheck{
			Scope: scope, Name: name, Passed: passed, Reason: reason,
		})
	}

	// --- Plan-level preconditions (source chamber) ---
	check("plan", "plan_active", !plan.Cancelled, "cancelled plan cannot be migrated")
	check("plan", "target_chamber", targetDistinct, "target chamber must differ from the source chamber")
	check("plan", "guard_bound", guard != nil, "migration guard is not bound")

	sourceEntryCount := 0
	releasedCallSigns := []string{}
	publishedCallSigns := []string{}
	for _, entry := range sourceQueue {
		if entry.PlanID != plan.ID {
			continue
		}
		sourceEntryCount++
		if entry.Released {
			releasedCallSigns = append(releasedCallSigns, entry.Vessel.CallSign)
		}
	}
	if guard != nil {
		for _, vessel := range plan.Vessels {
			if _, published := guard.SignalForVessel(vessel.ID); published {
				publishedCallSigns = append(publishedCallSigns, vessel.CallSign)
			}
		}
	}
	check("plan", "vessels_queued", sourceEntryCount == len(plan.Vessels) && sourceEntryCount > 0,
		"plan vessels are not all waiting in the source queue; partial convoys cannot be migrated")
	check("plan", "not_entered_lock", len(releasedCallSigns) == 0,
		"vessels already released into the lock ("+strings.Join(releasedCallSigns, ", ")+") cannot be migrated")
	check("plan", "no_published_signal", len(publishedCallSigns) == 0,
		"a signal has already been published for vessels ("+strings.Join(publishedCallSigns, ", ")+"); such plans cannot be migrated")

	if guard != nil {
		if current, ok := guard.CurrentCycle(plan.ChamberID); ok && current.PlanID == plan.ID {
			check("plan", "own_cycle_not_started", current.Stage == model.StagePlanned,
				"plan owns a transit already in stage "+string(current.Stage)+"; a lockage in progress cannot be migrated")
		} else {
			check("plan", "own_cycle_not_started", true, "")
		}
	}

	// --- Target chamber preconditions ---
	knownChamber := false
	for _, chamberID := range knownChambers {
		if chamberID == targetChamber {
			knownChamber = true
			break
		}
	}
	check("target", "target_known", knownChamber, "target chamber is not a known chamber")

	directionConflict := ""
	for _, other := range p.Active(targetChamber) {
		if other.ID != plan.ID && other.Direction != plan.Direction {
			directionConflict = "target queue already serves " + string(other.Direction) + " (plan " + other.ID.String()[:8] + ")"
			break
		}
	}
	check("target", "queue_direction", directionConflict == "",
		failReason(directionConflict, "target queue direction is incompatible"))

	if guard != nil && targetDistinct && knownChamber {
		if current, ok := guard.CurrentCycle(targetChamber); ok && !current.Stage.Terminal() {
			check("target", "cycle_direction", current.Direction == plan.Direction,
				"target chamber current lockage runs "+string(current.Direction)+", expected "+string(plan.Direction))
		} else {
			check("target", "cycle_direction", true, "")
		}
		check("target", "devices_ready", guard.DevicesReady(targetChamber),
			"target chamber has devices reporting offline")
		check("target", "equipment_idle", guard.EquipmentIdle(targetChamber),
			"target chamber has pending device commands or unsettled equipment")
	}

	if migration.Rejected() {
		return migration, nil
	}

	// --- Apply: one atomic queue relocation, then plan update, then journal ---
	moved, fromIndex, toIndex, err := p.admission.RelocatePlan(ctx, plan.ChamberID, targetChamber, plan)
	if err != nil {
		return Migration{}, err
	}
	for index, entry := range moved {
		migration.Vessels = append(migration.Vessels, MigrationVesselPosition{
			Vessel:       entry.Vessel,
			FromPosition: fromIndex + index + 1,
			ToPosition:   toIndex + index + 1,
		})
	}

	previousChamber := plan.ChamberID
	plan.ChamberID = targetChamber
	p.replace(plan)
	if _, err := p.recorder.Record(ctx, targetChamber, model.EventPlanMigrated, plan.ID, 0, map[string]any{
		"plan_id": plan.ID, "from_chamber": previousChamber, "to_chamber": targetChamber,
		"direction": plan.Direction, "vessels": len(plan.Vessels),
		"from_index": fromIndex, "to_index": toIndex,
	}); err != nil {
		p.admission.RestoreRelocation(previousChamber, targetChamber, plan, fromIndex, moved)
		plan.ChamberID = previousChamber
		p.replace(plan)
		return Migration{}, err
	}

	if guard != nil {
		// An empty StagePlanned transit left behind in the decommissioned
		// chamber would block its return to service; close it out.
		_ = guard.CancelOwnedPlannedCycle(context.WithoutCancel(ctx), previousChamber, plan.ID,
			"plan migrated to "+targetChamber)
	}

	migration.Migrated = true
	migration.Checks = append(migration.Checks, MigrationCheck{
		Scope: "plan", Name: "relocated", Passed: true,
	})
	return migration, nil
}

func failReason(reason, fallback string) string {
	if reason != "" {
		return reason
	}
	return fallback
}
