package admission

import (
	"context"
	"errors"
	"time"

	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// Service records queue changes and delegates safe release decisions.
type Service struct {
	controller *Controller
	recorder   *journal.Recorder
	now        func() time.Time
}

func NewService(controller *Controller, recorder *journal.Recorder, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{controller: controller, recorder: recorder, now: now}
}

func (s *Service) Enqueue(ctx context.Context, plan model.ConvoyPlan) error {
	if err := s.controller.queue.Add(plan.ChamberID, plan.ID, plan.Vessels); err != nil {
		return err
	}
	_, err := s.recorder.Record(ctx, plan.ChamberID, model.EventPlanCreated, plan.ID, 0, map[string]any{
		"plan_id": plan.ID, "queued": len(plan.Vessels), "at": s.now().UTC(),
	})
	return err
}

func (s *Service) Admit(ctx context.Context, cycle model.TransitCycle) (Decision, error) {
	decision, err := s.controller.Evaluate(ctx, cycle)
	if err != nil || !decision.Allowed {
		return decision, err
	}
	_, err = s.recorder.Record(ctx, cycle.ChamberID, model.EventSignalChanged, cycle.ID, cycle.Generation, decision)
	return decision, err
}

func (s *Service) RemovePlan(chamberID string, plan model.ConvoyPlan) []QueueEntry {
	return s.controller.queue.RemovePlan(chamberID, plan.ID)
}

// RelocatePlan moves every queued vessel of a plan to another chamber as one
// atomic queue operation and records the queue change.
func (s *Service) RelocatePlan(ctx context.Context, fromChamber, toChamber string, plan model.ConvoyPlan) ([]QueueEntry, int, int, error) {
	entries, fromIndex, toIndex, moved := s.controller.queue.RelocatePlan(fromChamber, toChamber, plan.ID)
	if !moved {
		return nil, -1, -1, errors.New("plan has no queued vessels to migrate")
	}
	_, err := s.recorder.Record(ctx, toChamber, model.EventPlanMigrated, plan.ID, 0, map[string]any{
		"plan_id": plan.ID, "from_chamber": fromChamber, "to_chamber": toChamber,
		"vessels": len(entries), "from_index": fromIndex, "to_index": toIndex, "at": s.now().UTC(),
	})
	if err != nil {
		s.controller.queue.RestoreRelocation(fromChamber, toChamber, plan.ID, fromIndex, entries)
		return nil, -1, -1, err
	}
	return entries, fromIndex, toIndex, nil
}

// RestoreRelocation reverses a previously applied queue relocation.
func (s *Service) RestoreRelocation(fromChamber, toChamber string, plan model.ConvoyPlan, fromIndex int, entries []QueueEntry) {
	s.controller.queue.RestoreRelocation(fromChamber, toChamber, plan.ID, fromIndex, entries)
}

func (s *Service) Restore(chamberID string, entries []QueueEntry) {
	s.controller.queue.Restore(chamberID, entries)
}

func (s *Service) List(chamberID string) []QueueEntry {
	return s.controller.queue.List(chamberID)
}
