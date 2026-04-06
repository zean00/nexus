package slack

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func TestParseInboundInteractive(t *testing.T) {
	adapter := New("secret", "")
	callback := map[string]any{
		"type":      "block_actions",
		"user":      map[string]any{"id": "U123"},
		"channel":   map[string]any{"id": "C123"},
		"message":   map[string]any{"ts": "111.222"},
		"container": map[string]any{"thread_ts": "111.222"},
		"actions": []map[string]any{
			{
				"value": `{"await_id":"await_run_1","choice":"staging"}`,
			},
		},
	}
	raw, err := json.Marshal(callback)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	form.Set("payload", string(raw))
	req := httptest.NewRequest("POST", "/webhooks/slack", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	evt, err := adapter.ParseInbound(req.Context(), req, []byte(form.Encode()), "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	if evt.Interaction != "await_response" {
		t.Fatalf("expected await_response, got %s", evt.Interaction)
	}
	if evt.Metadata.AwaitID != "await_run_1" {
		t.Fatalf("expected await id await_run_1, got %s", evt.Metadata.AwaitID)
	}
	if evt.Message.Text != "staging" {
		t.Fatalf("expected choice staging, got %s", evt.Message.Text)
	}
}

func TestSendUsesUpdateWhenPayloadContainsTS(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"ts":"123.456","channel":"C123"}`)
	}))
	defer server.Close()

	adapter := New("secret", "token")
	adapter.HTTP = server.Client()

	original := slackAPIBaseURL
	slackAPIBaseURL = server.URL + "/"
	defer func() { slackAPIBaseURL = original }()

	payload, err := json.Marshal(map[string]any{
		"channel": "C123",
		"ts":      "111.222",
		"text":    "updated",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.SendMessage(t.Context(), domain.OutboundDelivery{PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/chat.update" {
		t.Fatalf("expected /chat.update, got %s", path)
	}
}

func TestSendArtifactUsesFilesUpload(t *testing.T) {
	var (
		path        string
		contentType string
		body        []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		contentType = r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"file":{"id":"F123"}}`)
	}))
	defer server.Close()

	tmp, err := os.CreateTemp(t.TempDir(), "artifact-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("artifact body"); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}

	adapter := New("secret", "token")
	adapter.HTTP = server.Client()

	original := slackAPIBaseURL
	slackAPIBaseURL = server.URL + "/"
	defer func() { slackAPIBaseURL = original }()

	payload, err := json.Marshal(map[string]any{
		"kind":        "artifact_upload",
		"channel":     "C123",
		"thread_ts":   "111.222",
		"title":       "report.txt",
		"file_name":   "report.txt",
		"storage_uri": "file://" + tmp.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.SendMessage(t.Context(), domain.OutboundDelivery{PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/files.upload" {
		t.Fatalf("expected /files.upload, got %s", path)
	}
	if !strings.HasPrefix(contentType, "multipart/form-data;") {
		t.Fatalf("expected multipart content type, got %s", contentType)
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("unexpected media type %s", mediaType)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	fields := map[string]string{}
	fileBody := ""
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() == "file" {
			fileBody = string(raw)
			continue
		}
		fields[part.FormName()] = string(raw)
	}
	if fields["channels"] != "C123" || fields["thread_ts"] != "111.222" || fields["filename"] != "report.txt" || fields["title"] != "report.txt" {
		t.Fatalf("unexpected multipart fields: %+v", fields)
	}
	if fileBody != "artifact body" {
		t.Fatalf("unexpected uploaded artifact content: %q", fileBody)
	}
	if result.ProviderMessageID != "F123" {
		t.Fatalf("expected file id F123, got %s", result.ProviderMessageID)
	}
}

func TestDownloadArtifactUsesAuthorizationHeader(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "downloaded body")
	}))
	defer server.Close()

	adapter := New("secret", "token")
	adapter.HTTP = server.Client()

	content, err := adapter.DownloadArtifact(t.Context(), server.URL+"/private-file")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer token" {
		t.Fatalf("expected bearer auth header, got %q", auth)
	}
	if string(content) != "downloaded body" {
		t.Fatalf("unexpected download content %q", string(content))
	}
}
