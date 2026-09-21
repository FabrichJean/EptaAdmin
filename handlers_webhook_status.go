package main

import (
	"encoding/json"
	"net/http"
)

func (a *App) handleWebhookStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.webhookDeploy.snapshot())
}

// handleWebhookDeliveryProgress is the receiving end of the real-time
// progress convention documented in webhooks.go's webhookPayload: an
// external server doing long work before it can answer the original
// webhook request may POST here, as many times as it likes, with its
// current status. Deliberately forgiving — a malformed body, an unknown or
// already-finished deliveryID, or a receiver that never calls this at all
// are all silently fine, since the whole thing is opportunistic progress
// reporting, not a required part of the delivery contract.
func (a *App) handleWebhookDeliveryProgress(w http.ResponseWriter, r *http.Request) {
	deliveryID := r.PathValue("deliveryID")

	var req struct {
		Message string `json:"message"`
		Percent *int   `json:"percent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	message := req.Message
	if len(message) > 200 {
		message = message[:200]
	}
	percent := -1
	if req.Percent != nil {
		percent = *req.Percent
		if percent < 0 {
			percent = 0
		} else if percent > 100 {
			percent = 100
		}
	}

	applied := a.webhookDeploy.updateProgress(deliveryID, message, percent)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": applied})
}
