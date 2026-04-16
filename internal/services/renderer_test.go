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
	blocks, ok := payload["blocks"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("expected slack blocks payload, got %+v", payload["blocks"])
	}
}

func TestSlackRendererFormatsHeadingWithoutBrokenAsterisks(t *testing.T) {
	renderer := SlackRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "slack",
		ChannelScopeKey: "C123:1712400000.000100",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "completed",
		Text:   "## Title",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	blocks := payload["blocks"].([]any)
	block := blocks[0].(map[string]any)
	text := block["text"].(map[string]any)["text"].(string)
	if text != "*Title*" {
		t.Fatalf("unexpected slack heading formatting: %q", text)
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
	if payload["parse_mode"] != "HTML" {
		t.Fatalf("expected telegram html parse mode, got %+v", payload)
	}
}

func TestTelegramRendererFormatsMarkdownAsHTML(t *testing.T) {
	renderer := TelegramRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "telegram",
		ChannelScopeKey: "123:55",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "completed",
		Text:   "## Hello\n\n**bold** and _italic_ with [link](https://example.com)",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "<b>Hello</b>") || !strings.Contains(text, `<a href="https://example.com">link</a>`) {
		t.Fatalf("unexpected telegram formatted text: %q", text)
	}
}

func TestTelegramRendererKeepsInlineFormattingInsideListItems(t *testing.T) {
	renderer := TelegramRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "telegram",
		ChannelScopeKey: "123:55",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "completed",
		Text:   "- **bold** item with [link](https://example.com)",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "<b>bold</b>") || !strings.Contains(text, `<a href="https://example.com">link</a>`) || strings.Contains(text, "&lt;b&gt;") || strings.Contains(text, "&lt;a") {
		t.Fatalf("unexpected telegram list formatting: %q", text)
	}
}

func TestTelegramRendererIncludesMimeTypeForArtifactUploads(t *testing.T) {
	renderer := TelegramRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "telegram",
		ChannelScopeKey: "123:55",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "completed",
		Text:   "done",
		Artifacts: []domain.Artifact{
			{ID: "artifact_1", Name: "photo.jpg", MIMEType: "image/jpeg", StorageURI: "file:///tmp/photo.jpg"},
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
	if artifactPayload["mime_type"] != "image/jpeg" {
		t.Fatalf("expected telegram artifact payload to preserve mime_type, got %+v", artifactPayload)
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

func TestWhatsAppRendererFormatsMarkdownToReadableText(t *testing.T) {
	renderer := WhatsAppRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "whatsapp",
		ChannelScopeKey: "15551234567",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "completed",
		Text:   "**bold** and [link](https://example.com)",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	text := payload["text"].(map[string]any)["body"].(string)
	if !strings.Contains(text, "*bold*") || !strings.Contains(text, "link: https://example.com") {
		t.Fatalf("unexpected whatsapp markdown text: %q", text)
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

func TestWhatsAppWebRendererAddsArtifactUploadsForStoredFiles(t *testing.T) {
	renderer := WhatsAppWebRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "whatsapp_web",
		ChannelScopeKey: "628123456789@c.us",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "completed",
		Text:   "done",
		Artifacts: []domain.Artifact{
			{ID: "artifact_1", Name: "report.pdf", MIMEType: "application/pdf", StorageURI: "file:///tmp/report.pdf"},
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
	if artifactPayload["kind"] != "artifact_upload" || artifactPayload["storage_uri"] != "file:///tmp/report.pdf" {
		t.Fatalf("unexpected whatsapp_web artifact payload: %+v", artifactPayload)
	}
}

func TestWhatsAppWebRendererKeepsAwaitReplySyntax(t *testing.T) {
	renderer := WhatsAppWebRenderer{}
	prompt, err := json.Marshal(map[string]any{
		"title": "Approval needed",
		"body":  "Choose one option.",
		"choices": []domain.RenderChoice{
			{ID: "approve", Label: "Approve"},
			{ID: "reject", Label: "Reject"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "whatsapp_web",
		ChannelScopeKey: "628123456789@c.us",
	}, domain.RunEvent{
		RunID:       "run_1",
		Status:      "awaiting",
		AwaitPrompt: prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected one await delivery, got %+v", deliveries)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "[await:await_run_1] approve") || !strings.Contains(text, "[await:await_run_1] reject") {
		t.Fatalf("expected await reply syntax in whatsapp_web text, got %q", text)
	}
}

func TestEmailRendererAddsHTMLAlternative(t *testing.T) {
	renderer := EmailRenderer{}
	deliveries, err := renderer.RenderRunEvent(context.Background(), domain.Session{
		ID:              "session_1",
		TenantID:        "tenant_default",
		ChannelType:     "email",
		ChannelScopeKey: "user@example.com|thread_1",
	}, domain.RunEvent{
		RunID:  "run_1",
		Status: "completed",
		Text:   "## Hello\n\n**bold** and [link](https://example.com)",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(deliveries[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	html, _ := payload["html"].(string)
	text, _ := payload["text"].(string)
	if !strings.Contains(html, "<h2>Hello</h2>") || !strings.Contains(html, `<a href="https://example.com">link</a>`) {
		t.Fatalf("unexpected email html payload: %q", html)
	}
	if !strings.Contains(text, "**bold**") {
		t.Fatalf("unexpected email text fallback: %q", text)
	}
}
