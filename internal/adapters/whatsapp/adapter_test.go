package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/services"
)

func TestParseInboundInteractive(t *testing.T) {
	adapter := New("verify", "", "", "", "https://graph.example.com")
	body := `{
		"entry":[{
			"changes":[{
				"field":"messages",
				"value":{
					"contacts":[{"wa_id":"15551234567","profile":{"name":"Ava"}}],
					"messages":[{
						"id":"wamid.1",
						"from":"15551234567",
						"timestamp":"1710000000",
						"type":"interactive",
						"interactive":{"button_reply":{"id":"await:await_run_1:yes","title":"Yes"}}
					}]
				}
			}]
		}]
	}`

	evt, err := adapter.ParseInbound(context.Background(), httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp", nil), []byte(body), "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	if evt.Channel != "whatsapp" || evt.Interaction != "await_response" {
		t.Fatalf("unexpected event %+v", evt)
	}
	if evt.Metadata.AwaitID != "await_run_1" || evt.Message.Text != "yes" {
		t.Fatalf("unexpected await payload %+v", evt)
	}
}

func TestParseInboundLocation(t *testing.T) {
	adapter := New("verify", "", "", "", "https://graph.example.com")
	body := `{
		"entry":[{
			"changes":[{
				"field":"messages",
				"value":{
					"contacts":[{"wa_id":"15551234567","profile":{"name":"Ava"}}],
					"messages":[{
						"id":"wamid.location",
						"from":"15551234567",
						"timestamp":"1710000000",
						"type":"location",
						"location":{
							"latitude":-6.2,
							"longitude":106.816666,
							"name":"Jakarta",
							"address":"Central Jakarta"
						}
					}]
				}
			}]
		}]
	}`

	evt, err := adapter.ParseInbound(context.Background(), httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp", nil), []byte(body), "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	locations := domain.ExtractLocations(evt.Message)
	if len(locations) != 1 || locations[0].Name != "Jakarta" || locations[0].Address != "Central Jakarta" {
		t.Fatalf("unexpected location parts %+v in message %+v", locations, evt.Message)
	}
	if evt.Message.MessageType != "text" || !strings.Contains(evt.Message.Text, "https://maps.google.com") {
		t.Fatalf("unexpected location text fallback: %+v", evt.Message)
	}
}

func TestPrepareDeliveryAllowsFreeFormInsideWindow(t *testing.T) {
	adapter := New("verify", "token", "", "12345", "https://graph.example.com")
	adapter.GetContactPolicy = func(context.Context, string, string) (domain.WhatsAppContactPolicy, error) {
		return domain.WhatsAppContactPolicy{ConsentStatus: "unknown", WindowExpiresAt: time.Now().UTC().Add(time.Hour)}, nil
	}
	payload := mustTestJSON(t, map[string]any{
		"messaging_product": "whatsapp",
		"to":                "15551234567",
		"type":              "text",
		"text":              map[string]any{"body": "hello"},
	})
	prepared, err := adapter.PrepareDelivery(context.Background(), domain.OutboundDelivery{TenantID: "tenant", PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	if string(prepared.PayloadJSON) != string(payload) {
		t.Fatalf("expected unchanged payload, got %s", prepared.PayloadJSON)
	}
}

func TestPrepareDeliveryConvertsClosedWindowToExplicitTemplate(t *testing.T) {
	adapter := New("verify", "token", "", "12345", "https://graph.example.com")
	adapter.GetContactPolicy = func(context.Context, string, string) (domain.WhatsAppContactPolicy, error) {
		return domain.WhatsAppContactPolicy{ConsentStatus: "unknown", WindowExpiresAt: time.Now().UTC().Add(-time.Hour)}, nil
	}
	payload := mustTestJSON(t, map[string]any{
		"messaging_product": "whatsapp",
		"to":                "15551234567",
		"type":              "text",
		"text":              map[string]any{"body": "hello"},
		"whatsapp_template": map[string]any{
			"name":     "follow_up",
			"language": map[string]any{"code": "en_US"},
		},
	})
	prepared, err := adapter.PrepareDelivery(context.Background(), domain.OutboundDelivery{TenantID: "tenant", PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(prepared.PayloadJSON, &out); err != nil {
		t.Fatal(err)
	}
	template := out["template"].(map[string]any)
	if out["type"] != "template" || template["name"] != "follow_up" {
		t.Fatalf("unexpected template payload: %+v", out)
	}
}

func TestPrepareDeliveryBlocksClosedWindowWithoutTemplate(t *testing.T) {
	adapter := New("verify", "token", "", "12345", "https://graph.example.com")
	adapter.GetContactPolicy = func(context.Context, string, string) (domain.WhatsAppContactPolicy, error) {
		return domain.WhatsAppContactPolicy{ConsentStatus: "unknown"}, nil
	}
	payload := mustTestJSON(t, map[string]any{
		"messaging_product": "whatsapp",
		"to":                "15551234567",
		"type":              "text",
		"text":              map[string]any{"body": "hello"},
	})
	if _, err := adapter.PrepareDelivery(context.Background(), domain.OutboundDelivery{TenantID: "tenant", PayloadJSON: payload}); err == nil || err.Error() != "whatsapp_policy_window_closed_no_template" {
		t.Fatalf("expected closed-window policy error, got %v", err)
	}
}

func TestPrepareDeliveryBlocksOptedOutContact(t *testing.T) {
	adapter := New("verify", "token", "", "12345", "https://graph.example.com")
	adapter.GetContactPolicy = func(context.Context, string, string) (domain.WhatsAppContactPolicy, error) {
		return domain.WhatsAppContactPolicy{ConsentStatus: "opted_out", WindowExpiresAt: time.Now().UTC().Add(time.Hour)}, nil
	}
	payload := mustTestJSON(t, map[string]any{
		"messaging_product": "whatsapp",
		"to":                "15551234567",
		"type":              "template",
		"template":          map[string]any{"name": "follow_up", "language": map[string]any{"code": "en_US"}},
	})
	if _, err := adapter.PrepareDelivery(context.Background(), domain.OutboundDelivery{TenantID: "tenant", PayloadJSON: payload}); err == nil || err.Error() != "whatsapp_policy_opted_out" {
		t.Fatalf("expected opted-out policy error, got %v", err)
	}
}

func mustTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifyInboundValidatesSignature(t *testing.T) {
	adapter := New("verify", "", "app-secret", "", "https://graph.example.com")
	body := []byte(`{"entry":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp", nil)
	mac := hmac.New(sha256.New, []byte("app-secret"))
	_, _ = mac.Write(body)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))

	if err := adapter.VerifyInbound(context.Background(), req, body); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	if err := adapter.VerifyInbound(context.Background(), req, body); err == nil {
		t.Fatal("expected invalid signature error")
	}
}

func TestParseInboundBatchPreservesAllMessages(t *testing.T) {
	adapter := New("verify", "", "", "", "https://graph.example.com")
	body := `{
		"entry":[{
			"changes":[{
				"field":"messages",
				"value":{
					"contacts":[{"wa_id":"15551234567","profile":{"name":"Ava"}}],
					"messages":[
						{"id":"wamid.1","from":"15551234567","timestamp":"1710000000","type":"text","text":{"body":"first"}},
						{"id":"wamid.2","from":"15551234567","timestamp":"1710000001","type":"text","text":{"body":"second"}}
					]
				}
			}]
		}]
	}`
	events, err := adapter.ParseInboundBatch(context.Background(), httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp", nil), []byte(body), "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Message.Text != "first" || events[1].Message.Text != "second" {
		t.Fatalf("unexpected events %+v", events)
	}
}

func TestParseInboundTextAwaitReply(t *testing.T) {
	adapter := New("verify", "", "", "", "https://graph.example.com")
	body := `{
		"entry":[{
			"changes":[{
				"field":"messages",
				"value":{
					"contacts":[{"wa_id":"15551234567","profile":{"name":"Ava"}}],
					"messages":[{
						"id":"wamid.3",
						"from":"15551234567",
						"timestamp":"1710000002",
						"type":"text",
						"text":{"body":"[await:await_run_9] yes"}
					}]
				}
			}]
		}]
	}`
	evt, err := adapter.ParseInbound(context.Background(), httptest.NewRequest(http.MethodPost, "/webhooks/whatsapp", nil), []byte(body), "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	if evt.Interaction != "await_response" || evt.Metadata.AwaitID != "await_run_9" || evt.Message.Text != "yes" {
		t.Fatalf("unexpected await text reply %+v", evt)
	}
}

func TestHydrateInboundArtifactsDownloadsMedia(t *testing.T) {
	var authHeaders []string
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/mid_1":
			_, _ = io.WriteString(w, `{"url":"`+serverURL+`/download/mid_1"}`)
		case "/download/mid_1":
			_, _ = io.WriteString(w, "image-body")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	adapter := New("verify", "token", "", "", server.URL)
	adapter.HTTP = server.Client()
	evt := domain.CanonicalInboundEvent{
		Message: domain.Message{
			Artifacts: []domain.Artifact{{
				ID:        "mid_1",
				Name:      "photo.jpg",
				MIMEType:  "image/jpeg",
				SourceURL: "whatsapp-media:mid_1",
			}},
		},
	}
	store := services.ArtifactService{Store: noopArtifactStore{dir: t.TempDir()}}

	if err := adapter.HydrateInboundArtifacts(context.Background(), &evt, store); err != nil {
		t.Fatal(err)
	}
	if len(evt.Message.Artifacts) != 1 || evt.Message.Artifacts[0].StorageURI == "" {
		t.Fatalf("expected hydrated artifact, got %+v", evt.Message.Artifacts)
	}
	if len(authHeaders) != 2 || authHeaders[0] != "Bearer token" || authHeaders[1] != "Bearer token" {
		t.Fatalf("unexpected auth headers: %+v", authHeaders)
	}
}

func TestSendMessagePostsGraphPayload(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"messages":[{"id":"wamid.sent"}]}`)
	}))
	defer server.Close()

	adapter := New("verify", "token", "", "12345", server.URL)
	adapter.HTTP = server.Client()
	payload, err := json.Marshal(map[string]any{
		"messaging_product": "whatsapp",
		"to":                "15551234567",
		"type":              "text",
		"text":              map[string]any{"body": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.SendMessage(context.Background(), domain.OutboundDelivery{PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	if requestBody["to"] != "15551234567" || result.ProviderMessageID != "wamid.sent" {
		t.Fatalf("unexpected send result %+v body %+v", result, requestBody)
	}
}

func TestSendArtifactPostsWhatsAppDocumentPayload(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"messages":[{"id":"wamid.artifact"}]}`)
	}))
	defer server.Close()

	adapter := New("verify", "token", "", "12345", server.URL)
	adapter.HTTP = server.Client()
	payload, err := json.Marshal(map[string]any{
		"kind":        "artifact_upload",
		"to":          "15551234567",
		"storage_uri": "https://cdn.example.com/report.pdf",
		"file_name":   "report.pdf",
		"mime_type":   "application/pdf",
		"caption":     "report.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.SendMessage(context.Background(), domain.OutboundDelivery{PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	document := requestBody["document"].(map[string]any)
	if requestBody["type"] != "document" || document["link"] != "https://cdn.example.com/report.pdf" || result.ProviderMessageID != "wamid.artifact" {
		t.Fatalf("unexpected artifact send result %+v body %+v", result, requestBody)
	}
}
