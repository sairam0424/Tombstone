package v1

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// VariationHandler manages multivariate flag variations.
type VariationHandler struct {
	db     *sql.DB
	logger *zap.Logger
}

// NewVariationHandler returns a new VariationHandler.
func NewVariationHandler(db *sql.DB, logger *zap.Logger) *VariationHandler {
	return &VariationHandler{db: db, logger: logger}
}

// FlagVariation represents a single variation row.
type FlagVariation struct {
	ID          string `json:"id"`
	FlagID      string `json:"flag_id"`
	Key         string `json:"key"`
	Value       string `json:"value"`
	Weight      int    `json:"weight"`
	Description string `json:"description,omitempty"`
}

// AddVariationRequest is the body for POST /flags/{key}/variations.
type AddVariationRequest struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Weight      int    `json:"weight"`
	Description string `json:"description,omitempty"`
}

// UpdateVariationWeightRequest is the body for PATCH /flags/{key}/variations/{id}.
type UpdateVariationWeightRequest struct {
	Weight int `json:"weight"`
}

// AddVariation handles POST /api/v1/flags/{key}/variations.
func (h *VariationHandler) AddVariation(w http.ResponseWriter, r *http.Request) {
	flagKey := chi.URLParam(r, "key")

	var req AddVariationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Key == "" || req.Value == "" {
		writeError(w, http.StatusBadRequest, "key and value are required")
		return
	}
	if req.Weight < 0 || req.Weight > 10000 {
		writeError(w, http.StatusBadRequest, "weight must be between 0 and 10000")
		return
	}

	// Resolve flag UUID from key
	var flagID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM flags WHERE key = $1 AND state != 'ARCHIVED'`, flagKey,
	).Scan(&flagID); err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	} else if err != nil {
		h.logger.Error("resolve flag id", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	var v FlagVariation
	err := h.db.QueryRowContext(r.Context(), `
		INSERT INTO flag_variations (id, flag_id, key, value, weight, description)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, flag_id, key, value, weight, COALESCE(description, '')
	`, uuid.New().String(), flagID, req.Key, req.Value, req.Weight, req.Description).
		Scan(&v.ID, &v.FlagID, &v.Key, &v.Value, &v.Weight, &v.Description)
	if err != nil {
		h.logger.Error("insert variation", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, v)
}

// ListVariations handles GET /api/v1/flags/{key}/variations.
func (h *VariationHandler) ListVariations(w http.ResponseWriter, r *http.Request) {
	flagKey := chi.URLParam(r, "key")

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT fv.id, fv.flag_id, fv.key, fv.value, fv.weight, COALESCE(fv.description, '')
		FROM flag_variations fv
		JOIN flags f ON f.id = fv.flag_id
		WHERE f.key = $1 AND f.state != 'ARCHIVED'
		ORDER BY fv.weight DESC, fv.key ASC
	`, flagKey)
	if err != nil {
		h.logger.Error("list variations", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	variations := []FlagVariation{}
	for rows.Next() {
		var v FlagVariation
		if err := rows.Scan(&v.ID, &v.FlagID, &v.Key, &v.Value, &v.Weight, &v.Description); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		variations = append(variations, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"variations": variations, "total": len(variations)})
}

// UpdateVariationWeight handles PATCH /api/v1/flags/{key}/variations/{id}.
func (h *VariationHandler) UpdateVariationWeight(w http.ResponseWriter, r *http.Request) {
	flagKey := chi.URLParam(r, "key")
	variationID := chi.URLParam(r, "id")

	var req UpdateVariationWeightRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Weight < 0 || req.Weight > 10000 {
		writeError(w, http.StatusBadRequest, "weight must be between 0 and 10000")
		return
	}

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE flag_variations fv
		SET weight = $1
		FROM flags f
		WHERE f.id = fv.flag_id AND f.key = $2 AND fv.id = $3
	`, req.Weight, flagKey, variationID)
	if err != nil {
		h.logger.Error("update variation weight", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "variation not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": variationID, "weight": req.Weight, "updated": true})
}

// DeleteVariation handles DELETE /api/v1/flags/{key}/variations/{id}.
func (h *VariationHandler) DeleteVariation(w http.ResponseWriter, r *http.Request) {
	flagKey := chi.URLParam(r, "key")
	variationID := chi.URLParam(r, "id")

	res, err := h.db.ExecContext(r.Context(), `
		DELETE FROM flag_variations fv
		USING flags f
		WHERE f.id = fv.flag_id AND f.key = $1 AND fv.id = $2
	`, flagKey, variationID)
	if err != nil {
		h.logger.Error("delete variation", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "variation not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": variationID, "deleted": true})
}
