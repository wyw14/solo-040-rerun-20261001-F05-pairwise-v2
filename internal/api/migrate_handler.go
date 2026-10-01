package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/schedule"
)

type migrateInput struct {
	TargetChamberID string `json:"target_chamber_id"`
}

// runtimeMigrationChecks adapts live runtime components to the read-only
// MigrationChecks interface the schedule layer validates against.
type runtimeMigrationChecks struct {
	runtime *Runtime
}

func (m runtimeMigrationChecks) CurrentCycle(chamberID string) (model.TransitCycle, bool) {
	return m.runtime.Cycles.Current(chamberID)
}

func (m runtimeMigrationChecks) SignalPublished(vesselID uuid.UUID) bool {
	state, ok := m.runtime.SignalStore.Get(vesselID)
	return ok && state.Aspect == model.SignalProceed
}

func (m runtimeMigrationChecks) AllDevicesOnline(chamberID string) bool {
	return m.runtime.Health.AllOnline(chamberID)
}

func (m runtimeMigrationChecks) EmergencyActive(chamberID string) bool {
	return m.runtime.Safety.Status(chamberID).Status == model.EmergencyDraining
}

func migratePlanHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, err := uuid.Parse(chi.URLParam(r, "planID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var input migrateInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		migration, err := runtime.Schedule.Migrate(
			r.Context(), planID, input.TargetChamberID, runtimeMigrationChecks{runtime: runtime},
		)
		if err != nil {
			var migrationErr *schedule.MigrationError
			if errors.As(err, &migrationErr) {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error":   migrationErr.Error(),
					"reasons": migrationErr.Reasons,
				})
				return
			}
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, migration)
	}
}
