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

// RelocatePlan removes every entry of a plan from one chamber and appends the
// convoy to another chamber in a single locked step, preserving vessel order.
// It reports the queue positions (0-based) occupied before and after the move.
func (q *Queue) RelocatePlan(fromChamber, toChamber string, planID uuid.UUID) (removed []QueueEntry, fromIndex, toIndex int, moved bool) {
	if fromChamber == "" || toChamber == "" || fromChamber == toChamber || planID == uuid.Nil {
		return nil, -1, -1, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	source := q.entries[fromChamber]
	fromIndex = -1
	for index, entry := range source {
		if entry.PlanID != planID {
			continue
		}
		if fromIndex == -1 {
			fromIndex = index
		}
		removed = append(removed, entry)
	}
	if len(removed) == 0 {
		return nil, -1, -1, false
	}
	kept := make([]QueueEntry, 0, len(source)-len(removed))
	for _, entry := range source {
		if entry.PlanID != planID {
			kept = append(kept, entry)
		}
	}
	q.entries[fromChamber] = kept
	// Migrated vessels have not been released into the new chamber yet.
	for index := range removed {
		removed[index].Released = false
	}
	toIndex = len(q.entries[toChamber])
	q.entries[toChamber] = append(q.entries[toChamber], removed...)
	return removed, fromIndex, toIndex, true
}

// RestoreRelocation reverses RelocatePlan: it pulls the plan's entries back
// from the destination and reinserts them at their original source position.
func (q *Queue) RestoreRelocation(fromChamber, toChamber string, planID uuid.UUID, fromIndex int, moved []QueueEntry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	destination := q.entries[toChamber]
	kept := make([]QueueEntry, 0, len(destination)-len(moved))
	for _, entry := range destination {
		if entry.PlanID != planID {
			kept = append(kept, entry)
		}
	}
	q.entries[toChamber] = kept
	if fromIndex < 0 || fromIndex > len(q.entries[fromChamber]) {
		fromIndex = len(q.entries[fromChamber])
	}
	restored := make([]QueueEntry, 0, len(q.entries[fromChamber])+len(moved))
	restored = append(restored, q.entries[fromChamber][:fromIndex]...)
	restored = append(restored, moved...)
	restored = append(restored, q.entries[fromChamber][fromIndex:]...)
	q.entries[fromChamber] = restored
}

func (q *Queue) List(chamberID string) []QueueEntry {
	q.mu.RLock()
	result := append([]QueueEntry(nil), q.entries[chamberID]...)
	q.mu.RUnlock()
	return result
}
