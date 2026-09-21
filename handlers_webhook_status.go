package main

import "net/http"

func (a *App) handleWebhookStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.webhookDeploy.snapshot())
}
