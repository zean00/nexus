package email

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/services"
)

func TestParseInboundAwaitReply(t *testing.T) {
	adapter := New("secret", "", "", "", "nexus@example.com")
	raw := `{
		"event_id":"evt_email_1",
		"message_id":"<msg-1@example.com>",
		"from":"Alice <alice@example.com>",
		"subject":"Re: Approval needed [await:await_run_1]",
		"text":"yes, proceed",
		"thread_id":"<thread-1@example.com>"
	}`

	evt, err := adapter.ParseInbound(context.Background(), nil, []byte(raw), "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	if evt.Interaction != "await_response" {
		t.Fatalf("expected await_response, got %s", evt.Interaction)
	}
	if evt.Metadata.AwaitID != "await_run_1" {
		t.Fatalf("expected await_run_1, got %s", evt.Metadata.AwaitID)
	}
	if evt.Conversation.ChannelSurfaceKey != "alice@example.com|<thread-1@example.com>" {
		t.Fatalf("unexpected surface key %q", evt.Conversation.ChannelSurfaceKey)
	}
}

func TestHydrateInboundArtifactsDecodesAttachment(t *testing.T) {
	adapter := New("secret", "", "", "", "nexus@example.com")
	content := base64.StdEncoding.EncodeToString([]byte("attachment-body"))
	raw := `{
		"event_id":"evt_email_2",
		"message_id":"<msg-2@example.com>",
		"from":"alice@example.com",
		"subject":"Attachments",
		"text":"see attached",
		"thread_id":"<thread-2@example.com>",
		"attachments":[{"id":"att_1","name":"note.txt","mime_type":"text/plain","content_base64":"` + content + `"}]
	}`
	evt, err := adapter.ParseInbound(context.Background(), nil, []byte(raw), "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	store := services.ArtifactService{Store: noopArtifactStore{dir: t.TempDir()}}
	if err := adapter.HydrateInboundArtifacts(context.Background(), &evt, store); err != nil {
		t.Fatal(err)
	}
	if len(evt.Message.Artifacts) != 1 || evt.Message.Artifacts[0].StorageURI == "" {
		t.Fatalf("expected hydrated artifact, got %+v", evt.Message.Artifacts)
	}
}

func TestBuildMessageIncludesAwaitAttachment(t *testing.T) {
	file := t.TempDir() + "/report.txt"
	if err := os.WriteFile(file, []byte("artifact body"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, messageID, err := buildMessage(
		"nexus@example.com",
		"alice@example.com",
		"Report",
		"Done",
		"",
		map[string]any{
			"kind":        "artifact_upload",
			"storage_uri": "file://" + file,
			"file_name":   "report.txt",
			"thread_id":   "<thread-3@example.com>",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Content-Disposition: attachment; filename=\"report.txt\"") {
		t.Fatalf("expected attachment in message: %s", string(raw))
	}
	if messageID == "" {
		t.Fatal("expected message id")
	}
}

func TestSendMessageNoopsWithoutSMTP(t *testing.T) {
	adapter := New("secret", "", "", "", "nexus@example.com")
	payload, err := json.Marshal(map[string]any{"to": "alice@example.com", "subject": "Hi", "text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.SendMessage(context.Background(), domain.OutboundDelivery{PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
}
