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

func TestWhatsAppRendererListsFallbackChoices(t *testing.T) {
	renderer := WhatsAppRenderer{}
	prompt, err := json.Marshal(map[string]any{
		"title": "Choose one",
		"choices": []map[string]string{
			{"id": "alpha", "label": "Alpha"},
			{"id": "beta", "label": "Beta"},
			{"id": "gamma", "label": "Gamma"},
			{"id": "delta", "label": "Delta"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "whatsapp",
		ChannelScopeKey: "15551234567",
	}, domain.RunEvent{
		RunID:       "run_1",
		Status:      "awaiting",
		AwaitPrompt: prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	text := payload["text"].(map[string]any)["body"].(string)
	if !strings.Contains(text, "[await:await_run_1] alpha") || !strings.Contains(text, "[await:await_run_1] delta") {
		t.Fatalf("unexpected whatsapp fallback text: %q", text)
	}
}

func TestWhatsAppRendererAddsArtifactUploadsForPublicURLs(t *testing.T) {
	renderer := WhatsAppRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "whatsapp",
		ChannelScopeKey: "15551234567",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "completed",
		Text:   "done",
		Artifacts: []domain.Artifact{
			{ID: "artifact_1", Name: "report.pdf", MIMEType: "application/pdf", StorageURI: "https://cdn.example.com/report.pdf"},
			{ID: "artifact_2", Name: "local.txt", MIMEType: "text/plain", StorageURI: "file:///tmp/local.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("expected text plus one artifact delivery, got %+v", deliveries)
	}
	var artifactPayload map[string]any
	if err := json.Unmarshal(deliveries[1].PayloadJSON, &artifactPayload); err != nil {
		t.Fatal(err)
	}
	if artifactPayload["kind"] != "artifact_upload" || artifactPayload["storage_uri"] != "https://cdn.example.com/report.pdf" {
		t.Fatalf("unexpected whatsapp artifact payload: %+v", artifactPayload)
	}
}
