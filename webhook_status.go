package main

import (
	"sync"
	"time"
)

type webhookDeployStatus struct {
	mu        sync.Mutex
	active    int
	event     string
	startedAt time.Time
	lastError string
}

func (s *webhookDeployStatus) start(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active++
	s.event = event
	if s.active == 1 {
		s.startedAt = time.Now()
		s.lastError = ""
	}
}

func (s *webhookDeployStatus) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active > 0 {
		s.active--
	}
	if err != nil {
		s.lastError = err.Error()
	}
}

func (s *webhookDeployStatus) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"active": s.active > 0, "event": s.event, "startedAt": s.startedAt, "error": s.lastError}
}
