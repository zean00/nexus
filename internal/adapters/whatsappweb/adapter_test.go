package whatsappweb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/services"
)

type noopArtifactStore struct {
	dir string
}

func (s noopArtifactStore) Save(_ context.Context, objectKey string, content []byte) (string, error) {
	path := filepath.Join(s.dir, objectKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return "file://" + path, nil
}

func (s noopArtifactStore) Read(_ context.Context, storageURI string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected read for %s", storageURI)
}

func TestVerifyInboundValidatesSignature(t *testing.T) {
	adapter := New("http://waha.example", "", "default", "", "secret", "https://nexus.example")
	body := []byte(`{"event":"message"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp-web", nil)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	req.Header.Set("X-Webhook-Hmac", hex.EncodeToString(mac.Sum(nil)))
	if err := adapter.VerifyInbound(context.Background(), req, body); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Webhook-Hmac", "deadbeef")
	if err := adapter.VerifyInbound(context.Background(), req, body); err == nil {
		t.Fatal("expected invalid signature")
	}
}

func TestParseInboundBatchMessage(t *testing.T) {
	adapter := New("http://waha.example", "", "default", "", "secret", "https://nexus.example")
	body := []byte(`{
		"id":"evt_1",
		"event":"message",
		"session":"default",
		"payload":{
			"id":"msg_1",
			"timestamp":1710000000,
			"from":"628123456789@c.us",
			"body":"hello"
		}
	}`)
	events, err := adapter.ParseInboundBatch(context.Background(), httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp-web", nil), body, "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one event, got %+v", events)
	}
	if events[0].Channel != "whatsapp_web" || events[0].Sender.ChannelUserID != "628123456789" || events[0].Message.Text != "hello" {
		t.Fatalf("unexpected event %+v", events[0])
	}
	if events[0].Conversation.ChannelSurfaceKey != "628123456789@c.us" {
		t.Fatalf("expected surface key to preserve JID, got %+v", events[0].Conversation)
	}
}

func TestParseInboundBatchLocationMessage(t *testing.T) {
	adapter := New("http://waha.example", "", "default", "", "secret", "https://nexus.example")
	body := []byte(`{
		"id":"evt_location",
		"event":"message",
		"session":"default",
		"payload":{
			"id":"msg_location",
			"timestamp":1710000000,
			"from":"628123456789@c.us",
			"location":{
				"latitude":-6.2,
				"longitude":106.816666,
				"name":"Jakarta",
				"address":"Central Jakarta"
			}
		}
	}`)
	events, err := adapter.ParseInboundBatch(context.Background(), httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp-web", nil), body, "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	locations := domain.ExtractLocations(events[0].Message)
	if len(locations) != 1 || locations[0].Name != "Jakarta" || locations[0].Address != "Central Jakarta" {
		t.Fatalf("unexpected location parts %+v in message %+v", locations, events[0].Message)
	}
	if !strings.Contains(events[0].Message.Text, "https://maps.google.com") {
		t.Fatalf("expected text fallback with maps link, got %q", events[0].Message.Text)
	}
}

func TestHydrateInboundArtifactsDownloadsMedia(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "image-body")
	}))
	defer server.Close()
	adapter := New(server.URL, "token", "default", "", "secret", "https://nexus.example")
	adapter.HTTP = server.Client()
	evt := domain.CanonicalInboundEvent{
		Message: domain.Message{Artifacts: []domain.Artifact{{
			ID:        "msg_1",
			Name:      "photo.jpg",
			MIMEType:  "image/jpeg",
			SourceURL: server.URL + "/media/photo.jpg",
		}}},
	}
	store := services.ArtifactService{Store: noopArtifactStore{dir: t.TempDir()}}
	if err := adapter.HydrateInboundArtifacts(context.Background(), &evt, store); err != nil {
		t.Fatal(err)
	}
	if len(evt.Message.Artifacts) != 1 || evt.Message.Artifacts[0].StorageURI == "" {
		t.Fatalf("expected hydrated artifact, got %+v", evt.Message.Artifacts)
	}
}

func TestSendMessageAppliesAntiBlockSequence(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		_, _ = io.WriteString(w, `{"id":"wamid.1"}`)
	}))
	defer server.Close()
	adapter := New(server.URL, "token", "default", "", "secret", "https://nexus.example")
	adapter.HTTP = server.Client()
	adapter.Sleep = func(time.Duration) {}
	adapter.CountSentDeliveriesSince = func(context.Context, string, time.Time) (int, error) { return 0, nil }
	adapter.HasRecentInboundMessageSince = func(context.Context, string, time.Time) (bool, error) { return true, nil }
	payload, err := json.Marshal(map[string]any{"chatId": "628123456789@c.us", "text": "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.SendMessage(context.Background(), domain.OutboundDelivery{
		ID:          "delivery_1",
		SessionID:   "session_1",
		ChannelType: "whatsapp_web",
		PayloadJSON: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(calls, ",")
	if !strings.Contains(got, "/api/sendSeen") || !strings.Contains(got, "/api/default/presence") || !strings.Contains(got, "/api/sendText") {
		t.Fatalf("unexpected WAHA call sequence %q", got)
	}
	if !strings.HasSuffix(got, "/api/default/presence") {
		t.Fatalf("expected offline presence reset at end, got %q", got)
	}
	if result.ProviderMessageID != "wamid.1" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestSendMessageRequiresRecentInboundActivity(t *testing.T) {
	adapter := New("http://waha.example", "token", "default", "", "secret", "https://nexus.example")
	adapter.CountSentDeliveriesSince = func(context.Context, string, time.Time) (int, error) { return 0, nil }
	adapter.HasRecentInboundMessageSince = func(context.Context, string, time.Time) (bool, error) { return false, nil }
	adapter.Sleep = func(time.Duration) {}
	payload, err := json.Marshal(map[string]any{"chatId": "628123456789@c.us", "text": "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.SendMessage(context.Background(), domain.OutboundDelivery{
		ID:          "delivery_1",
		SessionID:   "session_1",
		ChannelType: "whatsapp_web",
		PayloadJSON: payload,
	})
	if err == nil || !strings.Contains(err.Error(), "recent inbound activity required") {
		t.Fatalf("expected recent inbound activity error, got %v", err)
	}
}

func TestSendMessageAppliesBurstCap(t *testing.T) {
	adapter := New("http://waha.example", "token", "default", "", "secret", "https://nexus.example")
	adapter.BurstWindow = time.Minute
	adapter.BurstMessageCap = 2
	adapter.CountSentDeliveriesSince = func(_ context.Context, _ string, since time.Time) (int, error) {
		if time.Since(since) < 2*time.Minute {
			return 2, nil
		}
		return 0, nil
	}
	adapter.HasRecentInboundMessageSince = func(context.Context, string, time.Time) (bool, error) { return true, nil }
	payload, err := json.Marshal(map[string]any{"chatId": "628123456789@c.us", "text": "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.SendMessage(context.Background(), domain.OutboundDelivery{
		ID:          "delivery_1",
		SessionID:   "session_1",
		ChannelType: "whatsapp_web",
		PayloadJSON: payload,
	})
	if err == nil || !strings.Contains(err.Error(), "burst delivery cap reached") {
		t.Fatalf("expected burst cap error, got %v", err)
	}
}
