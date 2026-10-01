package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type migrateInput struct {
	TargetChamber string `json:"target_chamber"`
}

// migratePlanHandler relocates a not-yet-started convoy plan to another
// chamber. Precondition failures are reported with HTTP 422 and the full check
// list so the page can show each failure reason and before/after positions.
func migratePlanHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, err := uuid.Parse(chi.URLParam(r, "planID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid plan id"))
			return
		}
		var input migrateInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if input.TargetChamber == "" {
			writeError(w, http.StatusBadRequest, errors.New("target_chamber is required"))
			return
		}
		if _, ok := runtime.Planner.Get(planID); !ok {
			writeError(w, http.StatusNotFound, errors.New("plan not found"))
			return
		}
		migration, err := runtime.Schedule.Migrate(r.Context(), planID, input.TargetChamber)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		if !migration.Migrated {
			writeJSON(w, http.StatusUnprocessableEntity, migration)
			return
		}
		writeJSON(w, http.StatusOK, migration)
	}
}
