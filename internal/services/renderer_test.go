package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func TestSlackRendererFormatsOpenCodeAwaitBlockFailure(t *testing.T) {
	renderer := SlackRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "slack",
		ChannelScopeKey: "C123:1712400000.000100",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "failed",
		Text:   openCodeAwaitBlockedReason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected one delivery, got %+v", deliveries)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "Slack route") || !strings.Contains(text, "native await/resume") {
		t.Fatalf("unexpected slack failure text: %q", text)
	}
}

func TestTelegramRendererFormatsOpenCodeAwaitBlockFailure(t *testing.T) {
	renderer := TelegramRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "telegram",
		ChannelScopeKey: "123:55",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "failed",
		Text:   openCodeAwaitBlockedReason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected one delivery, got %+v", deliveries)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "Telegram route") || !strings.Contains(text, "native await/resume") {
		t.Fatalf("unexpected telegram failure text: %q", text)
	}
}
