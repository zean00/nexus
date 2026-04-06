package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"nexus/internal/domain"
)

type Adapter struct {
	SigningSecret string
	BotToken      string
	HTTP          *http.Client
}

var slackAPIBaseURL = "https://slack.com/api/"

func New(signingSecret, botToken string) Adapter {
	return Adapter{
		SigningSecret: signingSecret,
		BotToken:      botToken,
		HTTP:          &http.Client{Timeout: 10 * time.Second},
	}
}

func (a Adapter) Channel() string { return "slack" }

func (a Adapter) VerifyInbound(_ context.Context, r *http.Request, body []byte) error {
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")
	if ts == "" || sig == "" {
		return errors.New("missing slack signature headers")
	}
	mac := hmac.New(sha256.New, []byte(a.SigningSecret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return errors.New("invalid slack signature")
	}
	return nil
}

func (a Adapter) ParseInbound(_ context.Context, r *http.Request, body []byte, tenantID string) (domain.CanonicalInboundEvent, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return a.parseInteractive(body, tenantID)
	}
	var envelope struct {
		Type      string          `json:"type"`
		Challenge string          `json:"challenge"`
		EventID   string          `json:"event_id"`
		Event     json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	if envelope.Type == "url_verification" {
		return domain.CanonicalInboundEvent{
			EventID:     envelope.EventID,
			TenantID:    tenantID,
			Channel:     "slack",
			Interaction: "challenge",
			Message:     domain.Message{Text: envelope.Challenge},
			Metadata:    domain.Metadata{RawPayload: body},
			ReceivedAt:  time.Now().UTC(),
		}, nil
	}

	var evt struct {
		Type     string `json:"type"`
		User     string `json:"user"`
		Text     string `json:"text"`
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts"`
		Files    []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			Mimetype           string `json:"mimetype"`
			Size               int64  `json:"size"`
			URLPrivateDownload string `json:"url_private_download"`
		} `json:"files"`
	}
	if err := json.Unmarshal(envelope.Event, &evt); err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	threadID := evt.ThreadTS
	if threadID == "" {
		threadID = evt.TS
	}
	command := ""
	if strings.HasPrefix(strings.TrimSpace(evt.Text), "/") {
		command = strings.Fields(strings.TrimSpace(evt.Text))[0]
	}
	artifacts := make([]domain.Artifact, 0, len(evt.Files))
	for _, file := range evt.Files {
		artifacts = append(artifacts, domain.Artifact{
			ID:        file.ID,
			Name:      file.Name,
			MIMEType:  file.Mimetype,
			SizeBytes: file.Size,
			SourceURL: file.URLPrivateDownload,
		})
	}
	return domain.CanonicalInboundEvent{
		EventID:         nonEmpty(envelope.EventID, "evt_"+evt.TS),
		TenantID:        tenantID,
		Channel:         "slack",
		Interaction:     evt.Type,
		ProviderEventID: nonEmpty(envelope.EventID, "evt_"+evt.TS),
		ReceivedAt:      time.Now().UTC(),
		Sender: domain.Sender{
			ChannelUserID:       evt.User,
			IsAuthenticated:     true,
			IdentityAssurance:   "provider_verified",
			AllowedResponderIDs: []string{evt.User},
		},
		Conversation: domain.Conversation{
			ChannelConversationID: evt.Channel,
			ChannelThreadID:       threadID,
			ChannelSurfaceKey:     fmt.Sprintf("%s:%s", evt.Channel, threadID),
		},
		Message: domain.Message{
			MessageID:   "msg_" + evt.TS,
			MessageType: messageTypeForSlackEvent(evt.Text, artifacts),
			Text:        evt.Text,
			Parts: []domain.Part{{
				ContentType: "text/plain",
				Content:     evt.Text,
			}},
			Artifacts: artifacts,
		},
		Metadata: domain.Metadata{
			Command:       command,
			MentionsBot:   strings.Contains(evt.Text, "<@"),
			ArtifactTrust: "trusted-channel-ingress",
			ResponderBinding: domain.ResponderBinding{
				Mode:                  "same-user-only",
				AllowedChannelUserIDs: []string{evt.User},
			},
			RawPayload: body,
		},
	}, nil
}

func (a Adapter) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(delivery.PayloadJSON)
}

func (a Adapter) SendAwaitPrompt(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(delivery.PayloadJSON)
}

func (a Adapter) parseInteractive(body []byte, tenantID string) (domain.CanonicalInboundEvent, error) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	payload := form.Get("payload")
	var callback struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		Message struct {
			TS string `json:"ts"`
		} `json:"message"`
		Container struct {
			ThreadTS string `json:"thread_ts"`
		} `json:"container"`
		Actions []struct {
			Value string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(payload), &callback); err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	if len(callback.Actions) == 0 {
		return domain.CanonicalInboundEvent{}, errors.New("missing slack action")
	}
	var action struct {
		AwaitID string `json:"await_id"`
		Choice  string `json:"choice"`
	}
	if err := json.Unmarshal([]byte(callback.Actions[0].Value), &action); err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	threadID := callback.Container.ThreadTS
	if threadID == "" {
		threadID = callback.Message.TS
	}
	resumePayload, err := json.Marshal(map[string]string{"choice": action.Choice})
	if err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	return domain.CanonicalInboundEvent{
		EventID:         "interactive_" + action.AwaitID + "_" + callback.User.ID,
		TenantID:        tenantID,
		Channel:         "slack",
		Interaction:     "await_response",
		ProviderEventID: "interactive_" + action.AwaitID + "_" + callback.User.ID,
		ReceivedAt:      time.Now().UTC(),
		Sender: domain.Sender{
			ChannelUserID:       callback.User.ID,
			IsAuthenticated:     true,
			IdentityAssurance:   "provider_verified",
			AllowedResponderIDs: []string{callback.User.ID},
		},
		Conversation: domain.Conversation{
			ChannelConversationID: callback.Channel.ID,
			ChannelThreadID:       threadID,
			ChannelSurfaceKey:     fmt.Sprintf("%s:%s", callback.Channel.ID, threadID),
		},
		Message: domain.Message{
			MessageID:   "interactive_msg_" + action.AwaitID,
			MessageType: "interactive",
			Text:        action.Choice,
			Parts: []domain.Part{{
				ContentType: "application/json",
				Content:     string(resumePayload),
			}},
		},
		Metadata: domain.Metadata{
			ArtifactTrust: "trusted-channel-ingress",
			AwaitID:       action.AwaitID,
			ResumePayload: resumePayload,
			RawPayload:    []byte(payload),
		},
	}, nil
}

func (a Adapter) send(payload []byte) (domain.DeliveryResult, error) {
	if a.BotToken == "" {
		return domain.DeliveryResult{}, nil
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return domain.DeliveryResult{}, err
	}
	if kind, _ := body["kind"].(string); kind == "artifact_upload" {
		return a.uploadArtifact(body)
	}
	method := "chat.postMessage"
	if _, ok := body["ts"]; ok {
		method = "chat.update"
	}
	req, err := http.NewRequest(http.MethodPost, slackAPIBaseURL+method, bytes.NewReader(payload))
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.BotToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.DeliveryResult{}, fmt.Errorf("slack post failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		TS      string `json:"ts"`
		Channel string `json:"channel"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.DeliveryResult{}, err
	}
	if !out.OK {
		return domain.DeliveryResult{}, fmt.Errorf("slack api error: %s", out.Error)
	}
	return domain.DeliveryResult{
		ProviderMessageID: out.TS,
		ProviderRequestID: out.Channel,
	}, nil
}

func (a Adapter) uploadArtifact(body map[string]any) (domain.DeliveryResult, error) {
	storageURI, _ := body["storage_uri"].(string)
	fileName, _ := body["file_name"].(string)
	title, _ := body["title"].(string)
	channel, _ := body["channel"].(string)
	threadTS, _ := body["thread_ts"].(string)
	content, err := readStorageURI(storageURI)
	if err != nil {
		return domain.DeliveryResult{}, err
	}

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	for key, value := range map[string]string{
		"channels":  channel,
		"thread_ts": threadTS,
		"filename":  fileName,
		"title":     title,
	} {
		if value == "" {
			continue
		}
		if err := writer.WriteField(key, value); err != nil {
			return domain.DeliveryResult{}, err
		}
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	if _, err := part.Write(content); err != nil {
		return domain.DeliveryResult{}, err
	}
	if err := writer.Close(); err != nil {
		return domain.DeliveryResult{}, err
	}

	req, err := http.NewRequest(http.MethodPost, slackAPIBaseURL+"files.upload", &requestBody)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.BotToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.DeliveryResult{}, fmt.Errorf("slack upload failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		File  struct {
			ID string `json:"id"`
		} `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.DeliveryResult{}, err
	}
	if !out.OK {
		return domain.DeliveryResult{}, fmt.Errorf("slack api error: %s", out.Error)
	}
	return domain.DeliveryResult{
		ProviderMessageID: out.File.ID,
		ProviderRequestID: channel,
	}, nil
}

func (a Adapter) DownloadArtifact(ctx context.Context, sourceURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	if a.BotToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.BotToken)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("slack file download failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	return io.ReadAll(resp.Body)
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func messageTypeForSlackEvent(text string, artifacts []domain.Artifact) string {
	switch {
	case len(artifacts) > 0 && strings.TrimSpace(text) != "":
		return "mixed"
	case len(artifacts) > 0:
		return "artifact"
	default:
		return "text"
	}
}

func readStorageURI(uri string) ([]byte, error) {
	if !strings.HasPrefix(uri, "file://") {
		return nil, fmt.Errorf("unsupported storage uri: %s", uri)
	}
	return os.ReadFile(strings.TrimPrefix(uri, "file://"))
}
