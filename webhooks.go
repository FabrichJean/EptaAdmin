package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// webhookDeliveryTimeout bounds how long a single delivery attempt can
// take — fire-and-forget deliveries (see fireWebhooks) run in their own
// goroutine so this never blocks the request that triggered them, but a
// hung external server should still not leak the connection forever.
const webhookDeliveryTimeout = 10 * time.Second

// webhookPayload is the JSON body posted to every webhook URL. Details
// mirrors whatever was passed to logActivity for the triggering action —
// same shape a member would see described on the Activité page, just
// structured instead of pre-rendered into a sentence.
type webhookPayload struct {
	Event     string         `json:"event"`
	Manual    bool           `json:"manual"`
	Timestamp string         `json:"timestamp"`
	Details   map[string]any `json:"details,omitempty"`
}

// computeWebhookSignature signs the exact request body with the
// webhook's own secret (never the account's API key), so the receiving
// server can verify a payload really came from this EptaAdmin instance —
// the same HMAC-over-body pattern used elsewhere (see upload_signing.go),
// carried in a header instead of a query string since this is a POST a
// server makes to another server, not a link a browser follows.
func computeWebhookSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// deliverWebhook sends one HTTP POST and records the outcome. Called
// either directly (a manual "Envoyer" click, so the click gets an
// immediate success/failure) or from its own goroutine (an automatic
// trigger from logActivity, which must never block the request that
// caused it).
func (a *App) deliverWebhook(hook *Webhook, event string, manual bool, details map[string]any) error {
	payload := webhookPayload{
		Event:     event,
		Manual:    manual,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Details:   details,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		a.store.MarkWebhookTriggered(hook.ID, "invalid URL")
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-EptaAdmin-Signature", computeWebhookSignature(hook.Secret, body))
	req.Header.Set("X-EptaAdmin-Event", event)

	client := &http.Client{Timeout: webhookDeliveryTimeout}
	resp, err := client.Do(req)
	if err != nil {
		a.store.MarkWebhookTriggered(hook.ID, "error: "+err.Error())
		return err
	}
	defer resp.Body.Close()

	status := resp.Status
	if err := a.store.MarkWebhookTriggered(hook.ID, status); err != nil {
		log.Printf("mark webhook triggered error: %v", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook responded with %s", status)
	}
	return nil
}

// fireWebhooks delivers a workspace-scoped event to every enabled webhook
// on that workspace, each in its own goroutine so a slow or unreachable
// external server never delays the request that triggered them — see the
// call from logActivity, which fires on every workspace-scoped activity
// (a "workspace update" in the broadest sense: any data or membership
// change), not just data mutations.
func (a *App) fireWebhooks(workspaceID int64, event string, details map[string]any) {
	if workspaceID == 0 {
		return
	}
	hooks, err := a.store.ListWebhooks(workspaceID)
	if err != nil {
		log.Printf("list webhooks error: %v", err)
		return
	}
	for _, h := range hooks {
		if !h.Enabled {
			continue
		}
		hook := h
		go func() {
			if err := a.deliverWebhook(hook, event, false, details); err != nil {
				log.Printf("webhook delivery error (workspace %d, url %s): %v", hook.WorkspaceID, hook.URL, err)
			}
		}()
	}
}
