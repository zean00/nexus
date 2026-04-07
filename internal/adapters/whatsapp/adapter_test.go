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
	"testing"

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
