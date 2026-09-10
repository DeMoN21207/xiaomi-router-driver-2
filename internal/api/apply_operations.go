package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

type applyOperation struct {
	ID         string       `json:"id"`
	Status     string       `json:"status"`
	Result     *applyResult `json:"result,omitempty"`
	Error      string       `json:"error,omitempty"`
	StartedAt  string       `json:"startedAt"`
	FinishedAt string       `json:"finishedAt,omitempty"`
}

func (h *Handler) startApplyOperation() (applyOperation, error) {
	h.applyOperationsMu.Lock()
	for _, operation := range h.applyOperations {
		if operation.Status == "queued" || operation.Status == "running" {
			h.applyOperationsMu.Unlock()
			return applyOperation{}, errors.New("rule application is already running")
		}
	}
	operation := applyOperation{
		ID:        newID("apply"),
		Status:    "queued",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if len(h.applyOperations) >= 32 {
		for id, candidate := range h.applyOperations {
			if candidate.Status != "queued" && candidate.Status != "running" {
				delete(h.applyOperations, id)
				break
			}
		}
	}
	h.applyOperations[operation.ID] = operation
	h.applyOperationsMu.Unlock()

	go h.runApplyOperation(operation.ID)
	return operation, nil
}

func (h *Handler) runApplyOperation(id string) {
	h.updateApplyOperation(id, func(operation *applyOperation) {
		operation.Status = "running"
	})
	ctx, cancel := context.WithTimeout(h.appContext, 5*time.Minute)
	defer cancel()
	result, err := h.applyCurrentRules(ctx)
	h.updateApplyOperation(id, func(operation *applyOperation) {
		operation.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			operation.Status = "failed"
			operation.Error = err.Error()
			return
		}
		operation.Status = "succeeded"
		operation.Result = &result
	})
}

func (h *Handler) updateApplyOperation(id string, mutate func(*applyOperation)) {
	h.applyOperationsMu.Lock()
	defer h.applyOperationsMu.Unlock()
	operation, exists := h.applyOperations[id]
	if !exists {
		return
	}
	mutate(&operation)
	h.applyOperations[id] = operation
}

func (h *Handler) handleApplyOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/rules/apply/"))
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, errors.New("invalid apply operation ID"))
		return
	}
	h.applyOperationsMu.Lock()
	operation, exists := h.applyOperations[id]
	h.applyOperationsMu.Unlock()
	if !exists {
		writeError(w, http.StatusNotFound, errors.New("apply operation not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": operation})
}
