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
type webhookDeployStatus struct {
	mu         sync.Mutex
	active     int
	target     string
	startedAt  time.Time
	finishedAt time.Time
	lastResult string // "success" or "error", meaningful only once active drops back to 0
	lastError  string
}

func (s *webhookDeployStatus) start(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active++
	s.target = target
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

func (s *webhookDeployStatus) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"active":     s.active > 0,
		"target":     s.target,
		"startedAt":  s.startedAt,
		"finishedAt": s.finishedAt,
		"result":     s.lastResult,
		"error":      s.lastError,
	}
}
