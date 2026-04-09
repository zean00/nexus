package acp

import (
	"testing"

	"nexus/internal/domain"
)

func collectRunEvents(t *testing.T, stream domain.RunEventStream) []domain.RunEvent {
	t.Helper()
	var events []domain.RunEvent
	for evt := range stream.Events {
		events = append(events, evt)
	}
	if err, ok := <-stream.Err; ok && err != nil {
		t.Fatalf("stream error: %v", err)
	}
	return events
}
