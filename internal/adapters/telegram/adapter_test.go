package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/services"
)

type noopArtifactStore struct {
	dir string
}

func (s noopArtifactStore) Save(_ context.Context, objectKey string, content []byte) (string, error) {
	target := filepath.Join(s.dir, objectKey)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return "", err
	}
	return "file://" + target, nil
}

func (s noopArtifactStore) Read(_ context.Context, storageURI string) ([]byte, error) {
	return os.ReadFile(strings.TrimPrefix(storageURI, "file://"))
}

func (s noopArtifactStore) SaveInbound(ctx context.Context, filename, mimeType string, content []byte) (domain.Artifact, error) {
	return services.ArtifactService{Store: s}.SaveInbound(ctx, filename, mimeType, content)
}

func TestParseInboundDocumentMessageIncludesArtifact(t *testing.T) {
	adapter := New("token", "")
	body := []byte(`{
		"update_id": 123,
		"message": {
			"message_id": 77,
			"date": 1710000000,
			"caption": "see attachment",
			"chat": {"id": 12345, "type": "private"},
			"from": {"id": 999},
			"document": {
				"file_id": "file_1",
				"file_name": "report.pdf",
				"mime_type": "application/pdf",
				"file_size": 321
			}
		}
	}`)
	evt, err := adapter.ParseInbound(context.Background(), nil, body, "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	if evt.Message.MessageType != "mixed" || evt.Message.Text != "see attachment" {
		t.Fatalf("unexpected telegram inbound message %+v", evt.Message)
	}
	if len(evt.Message.Artifacts) != 1 || evt.Message.Artifacts[0].SourceURL != "telegram-file:file_1" {
		t.Fatalf("unexpected telegram artifacts %+v", evt.Message.Artifacts)
	}
}

func TestParseInboundLocationMessageIncludesLocationPart(t *testing.T) {
	adapter := New("token", "")
	body := []byte(`{
		"update_id": 124,
		"message": {
			"message_id": 78,
			"date": 1710000000,
			"chat": {"id": 12345, "type": "private"},
			"from": {"id": 999},
			"venue": {
				"location": {"latitude": -6.2, "longitude": 106.816666},
				"title": "Jakarta",
				"address": "Central Jakarta"
			}
		}
	}`)
	evt, err := adapter.ParseInbound(context.Background(), nil, body, "tenant_default")
	if err != nil {
		t.Fatal(err)
	}
	locations := domain.ExtractLocations(evt.Message)
	if len(locations) != 1 || locations[0].Name != "Jakarta" || locations[0].Address != "Central Jakarta" {
		t.Fatalf("unexpected location parts %+v in message %+v", locations, evt.Message)
	}
	if !strings.Contains(evt.Message.Text, "https://maps.google.com") {
		t.Fatalf("expected text fallback with maps link, got %q", evt.Message.Text)
	}
}

func TestHydrateInboundArtifactsDownloadsTelegramFile(t *testing.T) {
	var serverURL string
	adapter := New("token", "")
	server := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"file_path":"documents/report.pdf"}}`)),
			}, nil
		case r.URL.String() == serverURL+"/file/bottoken/documents/report.pdf":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("telegram artifact body")),
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("not found")),
			}, nil
		}
	})
	adapter.HTTP = &http.Client{Transport: server}
	serverURL = "https://api.telegram.org"
	evt := domain.CanonicalInboundEvent{
		Message: domain.Message{
			Artifacts: []domain.Artifact{{
				ID:        "file_1",
				Name:      "report.pdf",
				MIMEType:  "application/pdf",
				SourceURL: "telegram-file:file_1",
			}},
		},
	}
	store := noopArtifactStore{dir: t.TempDir()}
	if err := adapter.HydrateInboundArtifacts(context.Background(), &evt, store); err != nil {
		t.Fatal(err)
	}
	if len(evt.Message.Artifacts) != 1 || evt.Message.Artifacts[0].StorageURI == "" {
		t.Fatalf("expected hydrated artifact, got %+v", evt.Message.Artifacts)
	}
}

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

func TestSendArtifactUsesSendPhotoForImages(t *testing.T) {
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

	tmp, err := os.CreateTemp(t.TempDir(), "telegram-photo-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("image-body"); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"kind":        "artifact_upload",
		"chat_id":     "123",
		"caption":     "photo.jpg",
		"storage_uri": "file://" + tmp.Name(),
		"mime_type":   "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.SendMessage(t.Context(), domain.OutboundDelivery{PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	if path != "/bottoken/sendPhoto" {
		t.Fatalf("expected sendPhoto path, got %s", path)
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("unexpected media type %s", mediaType)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	formNames := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		formNames[part.FormName()] = true
	}
	if !formNames["photo"] {
		t.Fatalf("expected multipart field photo, got %+v", formNames)
	}
}

func TestSendArtifactKeepsGifOnDocumentEndpoint(t *testing.T) {
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

	tmp, err := os.CreateTemp(t.TempDir(), "telegram-gif-*.gif")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("gif-body"); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"kind":        "artifact_upload",
		"chat_id":     "123",
		"caption":     "animation.gif",
		"storage_uri": "file://" + tmp.Name(),
		"mime_type":   "image/gif",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.SendMessage(t.Context(), domain.OutboundDelivery{PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	if path != "/bottoken/sendDocument" {
		t.Fatalf("expected gif artifacts to stay on sendDocument, got %s", path)
	}
}

func TestSendArtifactUsesSendAudioForAudio(t *testing.T) {
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

	tmp, err := os.CreateTemp(t.TempDir(), "telegram-audio-*.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("audio-body"); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"kind":        "artifact_upload",
		"chat_id":     "123",
		"storage_uri": "file://" + tmp.Name(),
		"mime_type":   "audio/mpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.SendMessage(t.Context(), domain.OutboundDelivery{PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	if path != "/bottoken/sendAudio" {
		t.Fatalf("expected sendAudio path, got %s", path)
	}
}

func TestSendArtifactUsesSendVideoForVideo(t *testing.T) {
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

	tmp, err := os.CreateTemp(t.TempDir(), "telegram-video-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("video-body"); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"kind":        "artifact_upload",
		"chat_id":     "123",
		"storage_uri": "file://" + tmp.Name(),
		"mime_type":   "video/mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.SendMessage(t.Context(), domain.OutboundDelivery{PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	if path != "/bottoken/sendVideo" {
		t.Fatalf("expected sendVideo path, got %s", path)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
