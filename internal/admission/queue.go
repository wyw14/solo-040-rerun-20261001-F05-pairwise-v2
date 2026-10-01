package admission

import (
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

type QueueEntry struct {
	Vessel   model.Vessel `json:"vessel"`
	PlanID   uuid.UUID    `json:"plan_id"`
	Released bool         `json:"released"`
}

// Queue preserves FIFO order while supporting convoy cancellation recovery.
type Queue struct {
	mu      sync.RWMutex
	entries map[string][]QueueEntry
}

func NewQueue() *Queue { return &Queue{entries: make(map[string][]QueueEntry)} }

func (q *Queue) Add(chamberID string, planID uuid.UUID, vessels []model.Vessel) error {
	if chamberID == "" || planID == uuid.Nil || len(vessels) == 0 {
		return errors.New("queue addition requires chamber, plan and vessels")
	}
	q.mu.Lock()
	for _, vessel := range vessels {
		q.entries[chamberID] = append(q.entries[chamberID], QueueEntry{Vessel: vessel, PlanID: planID})
	}
	q.mu.Unlock()
	return nil
}

func (q *Queue) Peek(chamberID string) (QueueEntry, bool) {
	q.mu.RLock()
	entries := q.entries[chamberID]
	q.mu.RUnlock()
	if len(entries) == 0 {
		return QueueEntry{}, false
	}
	return entries[0], true
}

func (q *Queue) MarkReleased(chamberID string, vesselID uuid.UUID) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.entries[chamberID] {
		if q.entries[chamberID][index].Vessel.ID == vesselID {
			q.entries[chamberID][index].Released = true
			return true
		}
	}
	return false
}

func (q *Queue) RemovePlan(chamberID string, planID uuid.UUID) []QueueEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.entries[chamberID]
	kept := entries[:0]
	var removed []QueueEntry
	for _, entry := range entries {
		if entry.PlanID == planID {
			removed = append(removed, entry)
		} else {
			kept = append(kept, entry)
		}
	}
	q.entries[chamberID] = append([]QueueEntry(nil), kept...)
	return removed
}

type QueueMove struct {
	Vessel      model.Vessel `json:"vessel"`
	PlanID      uuid.UUID    `json:"plan_id"`
	SourceIndex int          `json:"source_index"`
	TargetIndex int          `json:"target_index"`
}

// MovePlan detaches a plan's vessels from one chamber and appends them to
// another in a single critical section, preserving convoy order.
func (q *Queue) MovePlan(sourceChamberID, targetChamberID string, planID uuid.UUID) ([]QueueMove, bool) {
	if sourceChamberID == "" || targetChamberID == "" || sourceChamberID == targetChamberID || planID == uuid.Nil {
		return nil, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	source := q.entries[sourceChamberID]
	kept := source[:0]
	moved := make([]QueueEntry, 0)
	sourceIndexes := make([]int, 0)
	for index, entry := range source {
		if entry.PlanID == planID {
			moved = append(moved, entry)
			sourceIndexes = append(sourceIndexes, index)
		} else {
			kept = append(kept, entry)
		}
	}
	if len(moved) == 0 {
		return nil, false
	}
	q.entries[sourceChamberID] = append([]QueueEntry(nil), kept...)
	targetStart := len(q.entries[targetChamberID])
	result := make([]QueueMove, 0, len(moved))
	for offset, entry := range moved {
		entry.Released = false
		q.entries[targetChamberID] = append(q.entries[targetChamberID], entry)
		result = append(result, QueueMove{
			Vessel: entry.Vessel, PlanID: planID,
			SourceIndex: sourceIndexes[offset], TargetIndex: targetStart + offset,
		})
	}
	return result, true
}

// UndoMove reverses a previously committed MovePlan, restoring source order.
func (q *Queue) UndoMove(targetChamberID, sourceChamberID string, moves []QueueMove) {
	if len(moves) == 0 {
		return
	}
	planID := moves[0].PlanID
	q.mu.Lock()
	defer q.mu.Unlock()
	target := q.entries[targetChamberID]
	restored := make([]QueueEntry, 0, len(moves))
	kept := target[:0]
	for _, entry := range target {
		if entry.PlanID == planID {
			restored = append(restored, entry)
		} else {
			kept = append(kept, entry)
		}
	}
	q.entries[targetChamberID] = append([]QueueEntry(nil), kept...)
	insertAt := moves[0].SourceIndex
	if current := len(q.entries[sourceChamberID]); insertAt > current {
		insertAt = current
	}
	if insertAt < 0 {
		insertAt = 0
	}
	current := append([]QueueEntry(nil), q.entries[sourceChamberID]...)
	q.entries[sourceChamberID] = append(current[:insertAt:insertAt], append(restored, current[insertAt:]...)...)
}

func (q *Queue) Restore(chamberID string, entries []QueueEntry) {
	q.mu.Lock()
	current := q.entries[chamberID]
	restored := make([]QueueEntry, 0, len(entries)+len(current))
	for _, entry := range entries {
		entry.Released = false
		restored = append(restored, entry)
	}
	q.entries[chamberID] = append(restored, current...)
	q.mu.Unlock()
}

func (q *Queue) List(chamberID string) []QueueEntry {
	q.mu.RLock()
	result := append([]QueueEntry(nil), q.entries[chamberID]...)
	q.mu.RUnlock()
	return result
}
