package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"eptaadmin/internal/store"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"
)

// webhookDeliveryTimeout bounds how long a single delivery attempt can
// take — fire-and-forget deliveries (see fireWebhooks) run in their own
// goroutine so this never blocks the request that triggered them, but a
// hung external server should still not leak the connection forever.
const webhookDeliveryTimeout = 15 * time.Minute

// webhookPayload is the JSON body posted to every webhook URL. Details
// mirrors whatever was passed to LogActivity for the triggering action —
// same shape a member would see described on the Activité page, just
// structured instead of pre-rendered into a sentence.
//
// DeliveryID and ProgressURL are the real-time progress convention: a
// receiver doing long work before it can respond (a deploy, a build...) MAY
// POST JSON {"message": "...", "percent": 0-100} to ProgressURL any number
// of times while it works — EptaAdmin surfaces the latest one in its global
// delivery banner and on the "Envoyer" button, instead of just a blind
// elapsed-time counter. Entirely optional: a receiver that ignores both
// fields still gets delivered to exactly as before. ProgressURL is only
// present when this instance knows its own public URL (EPTAADMIN_PUBLIC_URL).
type webhookPayload struct {
	Event       string         `json:"event"`
	Manual      bool           `json:"manual"`
	Timestamp   string         `json:"timestamp"`
	DeliveryID  string         `json:"deliveryId"`
	ProgressURL string         `json:"progressUrl,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// newDeliveryID generates the unguessable token identifying one delivery
// attempt — included in the payload and required (as a path segment) on
// every progress report, the same "possession of the token is proof enough"
// trust model as a signed one-off upload URL.
func newDeliveryID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// webhookTargetLabel is the friendly, short label shown in the global
// delivery banner — the host, not the whole URL with its path/query, and a
// safe fallback to the raw string if it doesn't even parse as a URL.
func webhookTargetLabel(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// computeWebhookSignature signs the exact request body with the
// webhook's own secret (never the account's API key), so the receiving
// server can verify a payload really came from this EptaAdmin instance —
// the same HMAC-over-body pattern used elsewhere (see internal/uploads),
// carried in a header instead of a query string since this is a POST a
// server makes to another server, not a link a browser follows.
func computeWebhookSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// DeliverWebhook sends one HTTP POST and records the outcome. Called
// either directly (a manual "Envoyer" click, so the click gets an
// immediate success/failure) or from its own goroutine (an automatic
// trigger from LogActivity, which must never block the request that
// caused it).
func (a *App) DeliverWebhook(hook *store.Webhook, event string, manual bool, details map[string]any) (err error) {
	deliveryID, err := newDeliveryID()
	if err != nil {
		return err
	}
	a.WebhookDeploy.start(webhookTargetLabel(hook.URL), deliveryID)
	defer func() { a.WebhookDeploy.finish(err) }()

	var progressURL string
	if a.PublicURL != "" {
		progressURL = a.PublicURL + "/api/webhooks/deliveries/" + deliveryID + "/progress"
	}

	payload := webhookPayload{
		Event:       event,
		Manual:      manual,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		DeliveryID:  deliveryID,
		ProgressURL: progressURL,
		Details:     details,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		a.Store.MarkWebhookTriggered(hook.ID, "invalid URL")
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-EptaAdmin-Signature", computeWebhookSignature(hook.Secret, body))
	req.Header.Set("X-EptaAdmin-Event", event)

	client := &http.Client{Timeout: webhookDeliveryTimeout}
	resp, err := client.Do(req)
	if err != nil {
		a.Store.MarkWebhookTriggered(hook.ID, "error: "+err.Error())
		return err
	}
	defer resp.Body.Close()

	status := resp.Status
	if err := a.Store.MarkWebhookTriggered(hook.ID, status); err != nil {
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
// call from LogActivity, which fires on every workspace-scoped activity
// (a "workspace update" in the broadest sense: any data or membership
// change), not just data mutations.
func (a *App) fireWebhooks(workspaceID int64, event string, details map[string]any) {
	if workspaceID == 0 {
		return
	}
	hooks, err := a.Store.ListWebhooks(workspaceID)
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
			deliverErr := a.DeliverWebhook(hook, event, false, details)
			if deliverErr != nil {
				log.Printf("webhook delivery error (workspace %d, url %s): %v", hook.WorkspaceID, hook.URL, deliverErr)
			}
			errText := ""
			if deliverErr != nil {
				errText = deliverErr.Error()
			}
			a.LogActivity(LogActivityParams{
				WorkspaceID: workspaceID,
				Action:      store.ActionWebhookAutoTrigger,
				Details:     map[string]any{"url": hook.URL, "triggeringAction": event, "success": deliverErr == nil, "error": errText},
			})
		}()
	}
}
