package schedule

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// MigrationChecks exposes the read-only chamber state a migration validates
// against: the current lockage, published field signals, queue direction and
// equipment/emergency status of the target chamber.
type MigrationChecks interface {
	CurrentCycle(chamberID string) (model.TransitCycle, bool)
	SignalPublished(vesselID uuid.UUID) bool
	AllDevicesOnline(chamberID string) bool
	EmergencyActive(chamberID string) bool
}

// VesselPosition records where one vessel sat before and after migration.
type VesselPosition struct {
	VesselID    uuid.UUID `json:"vessel_id"`
	Name        string    `json:"name"`
	SourceIndex int       `json:"source_index"`
	TargetIndex int       `json:"target_index"`
}

// Migration is the observable result of relocating one convoy plan.
type Migration struct {
	PlanID         uuid.UUID        `json:"plan_id"`
	SourceChamber  string           `json:"source_chamber"`
	TargetChamber  string           `json:"target_chamber"`
	Direction      model.Direction  `json:"direction"`
	Positions      []VesselPosition `json:"positions"`
	SourceQueueLen int              `json:"source_queue_len"`
	TargetQueueLen int              `json:"target_queue_len"`
}

// MigrationError reports every validation rule that blocked a migration,
// so the operator page can show concrete failure reasons instead of one.
type MigrationError struct {
	Reasons []string `json:"reasons"`
}

func (e *MigrationError) Error() string {
	if len(e.Reasons) == 0 {
		return "migration rejected"
	}
	return "migration rejected: " + strings.Join(e.Reasons, "; ")
}

func migrationError(reasons []string) error {
	if len(reasons) == 0 {
		return nil
	}
	return &MigrationError{Reasons: reasons}
}

// Migrate moves one not-yet-started convoy plan to another chamber. The whole
// convoy is detached and re-appended in one queue operation; a published
// signal, an in-lock transit or an unsuitable target chamber blocks the move.
func (p *Planner) Migrate(ctx context.Context, planID uuid.UUID, targetChamberID string, checks MigrationChecks) (Migration, error) {
	plan, err := p.require(planID)
	if err != nil {
		return Migration{}, err
	}
	sourceChamberID := plan.ChamberID
	if err := validateMigration(plan, targetChamberID, p, checks); err != nil {
		return Migration{}, err
	}

	moves, ok := p.admission.MovePlan(sourceChamberID, targetChamberID, plan)
	if !ok {
		return Migration{}, errors.New("convoy is not present in source chamber queue")
	}

	plan.ChamberID = targetChamberID
	p.replace(plan)

	payload := map[string]any{
		"plan_id": plan.ID, "from": sourceChamberID, "to": targetChamberID,
		"direction": plan.Direction, "vessels": len(plan.Vessels), "moves": moves,
	}
	recorded := func() bool {
		if _, err := p.recorder.Record(ctx, sourceChamberID, model.EventPlanMigrated, plan.ID, 0, payload); err != nil {
			return false
		}
		if _, err := p.recorder.Record(ctx, targetChamberID, model.EventPlanMigrated, plan.ID, 0, payload); err != nil {
			return false
		}
		return true
	}
	if !recorded() {
		// Partial journal write: reverse the queue and plan mutation.
		p.admission.UndoMove(targetChamberID, sourceChamberID, moves)
		plan.ChamberID = sourceChamberID
		p.replace(plan)
		return Migration{}, errors.New("failed to record migration; queue change rolled back")
	}

	result := Migration{
		PlanID: plan.ID, SourceChamber: sourceChamberID, TargetChamber: targetChamberID,
		Direction: plan.Direction, Positions: make([]VesselPosition, 0, len(moves)),
		SourceQueueLen: len(p.admission.List(sourceChamberID)),
		TargetQueueLen: len(p.admission.List(targetChamberID)),
	}
	for _, move := range moves {
		result.Positions = append(result.Positions, VesselPosition{
			VesselID: move.Vessel.ID, Name: move.Vessel.Name,
			SourceIndex: move.SourceIndex, TargetIndex: move.TargetIndex,
		})
	}
	return result, nil
}

func validateMigration(plan model.ConvoyPlan, targetChamberID string, planner *Planner, checks MigrationChecks) error {
	var reasons []string
	sourceChamberID := plan.ChamberID
	if plan.Cancelled {
		reasons = append(reasons, "cancelled plans cannot be migrated")
	}
	if targetChamberID == "" {
		reasons = append(reasons, "target chamber is required")
	}
	if targetChamberID == sourceChamberID {
		reasons = append(reasons, "target chamber must differ from the current chamber")
	}
	if targetChamberID != "" && targetChamberID != sourceChamberID && !planner.knowsChamber(targetChamberID) {
		reasons = append(reasons, "target chamber is not in service")
	}
	if len(reasons) > 0 {
		return migrationError(reasons)
	}

	// A signal already shown to the crew commits the plan to its chamber.
	var signaled []string
	for _, vessel := range plan.Vessels {
		if checks.SignalPublished(vessel.ID) {
			signaled = append(signaled, vessel.Name)
		}
	}
	if len(signaled) > 0 {
		reasons = append(reasons, "signal already published for vessels: "+strings.Join(signaled, ", "))
	}

	// A non-terminal lockage bound to this plan means the convoy is currently
	// in the chamber. A terminal lockage has released the chamber, so surviving
	// queued vessels (e.g. after an emergency stop) remain migrable.
	if current, ok := checks.CurrentCycle(sourceChamberID); ok &&
		current.PlanID == plan.ID && !current.Stage.Terminal() {
		reasons = append(reasons, "plan already entered the chamber (active lockage stage "+string(current.Stage)+")")
	}

	// Target chamber: current lockage, queued traffic direction, equipment.
	if targetCycle, ok := checks.CurrentCycle(targetChamberID); ok && !targetCycle.Stage.Terminal() {
		reasons = append(reasons, "target chamber is running lockage stage "+string(targetCycle.Stage))
	} else {
		if !checks.AllDevicesOnline(targetChamberID) {
			reasons = append(reasons, "target chamber has offline equipment")
		}
		if checks.EmergencyActive(targetChamberID) {
			reasons = append(reasons, "target chamber emergency procedure is active")
		}
	}

	for _, entry := range planner.admission.List(targetChamberID) {
		if queued, ok := planner.Get(entry.PlanID); ok && queued.Direction != plan.Direction {
			reasons = append(reasons, "target queue carries "+string(queued.Direction)+" traffic conflicting with convoy direction "+string(plan.Direction))
			break
		}
	}
	return migrationError(reasons)
}
