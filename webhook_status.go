package main

import (
	"sync"
	"time"
)

// webhookDeployStatus is a coarse, global "a webhook delivery is happening
// right now" indicator, polled by every page's layout (see /api/webhook-status)
// to show live progress instead of leaving the person with no feedback while
// a delivery — which can take up to webhookDeliveryTimeout — is in flight.
// It intentionally doesn't try to be exact about concurrent deliveries to
// different webhooks at once (each webhook's own last-status is already
// tracked precisely in the webhooks table); this is just a friendly banner.
//
// deliveryID additionally lets the *receiving* external server report back
// real-time progress of its own — see webhooks.go's progressUrl convention
// and handleWebhookDeliveryProgress — which updateProgress applies only if
// it matches the currently in-flight delivery, so a late report from an
// older, already-finished delivery can't clobber a newer one's progress.
type webhookDeployStatus struct {
	mu              sync.Mutex
	active          int
	target          string
	deliveryID      string
	progressMessage string
	progressPercent int // -1 means "unknown"
	startedAt       time.Time
	finishedAt      time.Time
	lastResult      string // "success" or "error", meaningful only once active drops back to 0
	lastError       string
}

func (s *webhookDeployStatus) start(target, deliveryID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active++
	s.target = target
	s.deliveryID = deliveryID
	s.progressMessage = ""
	s.progressPercent = -1
	if s.active == 1 {
		s.startedAt = time.Now()
	}
}

func (s *webhookDeployStatus) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active > 0 {
		s.active--
	}
	if s.active == 0 {
		s.finishedAt = time.Now()
		if err != nil {
			s.lastResult = "error"
			s.lastError = err.Error()
		} else {
			s.lastResult = "success"
			s.lastError = ""
		}
	}
}

// updateProgress applies a progress report from the external receiver of
// deliveryID, if that delivery is still the one currently in flight. It
// reports whether the update was applied, so the caller can tell a live
// receiver from one reporting after the fact.
func (s *webhookDeployStatus) updateProgress(deliveryID, message string, percent int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == 0 || deliveryID == "" || deliveryID != s.deliveryID {
		return false
	}
	s.progressMessage = message
	s.progressPercent = percent
	return true
}

func (s *webhookDeployStatus) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"active":          s.active > 0,
		"target":          s.target,
		"deliveryId":      s.deliveryID,
		"progressMessage": s.progressMessage,
		"progressPercent": s.progressPercent,
		"startedAt":       s.startedAt,
		"finishedAt":      s.finishedAt,
		"result":          s.lastResult,
		"error":           s.lastError,
	}
}
