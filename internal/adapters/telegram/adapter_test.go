package telegram

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func TestSendUsesEditMessageTextWhenPayloadContainsMessageID(t *testing.T) {
	adapter := New("token", "")
	var path string
	adapter.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":42}}`)),
		}, nil
	})}

	payload, err := json.Marshal(map[string]any{
		"chat_id":    "123",
		"message_id": 99,
		"text":       "updated",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.SendMessage(t.Context(), domain.OutboundDelivery{PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bottoken/editMessageText" {
		t.Fatalf("expected editMessageText path, got %s", path)
	}
}

func TestSendArtifactUsesSendDocumentMultipart(t *testing.T) {
	adapter := New("token", "")
	var (
		path        string
		contentType string
		body        []byte
	)
	adapter.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		contentType = r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":42}}`)),
		}, nil
	})}

	tmp, err := os.CreateTemp(t.TempDir(), "telegram-artifact-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("telegram artifact body"); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"kind":        "artifact_upload",
		"chat_id":     "123",
		"caption":     "report.txt",
		"storage_uri": "file://" + tmp.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.SendMessage(t.Context(), domain.OutboundDelivery{PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bottoken/sendDocument" {
		t.Fatalf("expected sendDocument path, got %s", path)
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
		if part.FormName() == "document" {
			fileBody = string(raw)
			continue
		}
		fields[part.FormName()] = string(raw)
	}
	if fields["chat_id"] != "123" || fields["caption"] != "report.txt" {
		t.Fatalf("unexpected telegram multipart fields: %+v", fields)
	}
	if fileBody != "telegram artifact body" {
		t.Fatalf("unexpected telegram uploaded content: %q", fileBody)
	}
	if result.ProviderMessageID != "42" {
		t.Fatalf("expected provider message id 42, got %s", result.ProviderMessageID)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
