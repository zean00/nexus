package telegram

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"nexus/internal/domain"
)

type Adapter struct {
	BotToken      string
	WebhookSecret string
	HTTP          *http.Client
}

func New(botToken, webhookSecret string) Adapter {
	return Adapter{
		BotToken:      botToken,
		WebhookSecret: webhookSecret,
		HTTP:          &http.Client{Timeout: 10 * time.Second},
	}
}

func (a Adapter) Channel() string { return "telegram" }

func (a Adapter) VerifyInbound(_ context.Context, r *http.Request, _ []byte) error {
	if a.WebhookSecret == "" {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))), []byte(a.WebhookSecret)) != 1 {
		return errors.New("invalid telegram webhook secret")
	}
	return nil
}

func (a Adapter) ParseInbound(_ context.Context, _ *http.Request, body []byte, tenantID string) (domain.CanonicalInboundEvent, error) {
	var update struct {
		UpdateID      int64 `json:"update_id"`
		Message       *struct {
			MessageID int64  `json:"message_id"`
			Date      int64  `json:"date"`
			Text      string `json:"text"`
			Chat      struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
		} `json:"message"`
		CallbackQuery *struct {
			ID   string `json:"id"`
			Data string `json:"data"`
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
			Message struct {
				MessageID int64 `json:"message_id"`
				Date      int64 `json:"date"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
	}
	if err := json.Unmarshal(body, &update); err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	switch {
	case update.Message != nil:
		chatID := strconv.FormatInt(update.Message.Chat.ID, 10)
		msgID := strconv.FormatInt(update.Message.MessageID, 10)
		userID := strconv.FormatInt(update.Message.From.ID, 10)
		command := ""
		if strings.HasPrefix(strings.TrimSpace(update.Message.Text), "/") {
			command = strings.Fields(strings.TrimSpace(update.Message.Text))[0]
		}
		return domain.CanonicalInboundEvent{
			EventID:         fmt.Sprintf("tg_%d", update.UpdateID),
			TenantID:        tenantID,
			Channel:         "telegram",
			Interaction:     "message",
			ProviderEventID: fmt.Sprintf("tg_%d", update.UpdateID),
			ReceivedAt:      time.Now().UTC(),
			Sender: domain.Sender{
				ChannelUserID:       userID,
				IsAuthenticated:     true,
				IdentityAssurance:   "provider_verified",
				AllowedResponderIDs: []string{userID},
			},
			Conversation: domain.Conversation{
				ChannelConversationID: chatID,
				ChannelThreadID:       chatID,
				ChannelSurfaceKey:     chatID,
			},
			Message: domain.Message{
				MessageID:   "tg_msg_" + msgID,
				MessageType: "text",
				Text:        update.Message.Text,
				Parts:       []domain.Part{{ContentType: "text/plain", Content: update.Message.Text}},
			},
			Metadata: domain.Metadata{RawPayload: body, Command: command},
		}, nil
	case update.CallbackQuery != nil:
		var action struct {
			AwaitID string `json:"await_id"`
			Choice  string `json:"choice"`
		}
		if err := json.Unmarshal([]byte(update.CallbackQuery.Data), &action); err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		payload, err := json.Marshal(map[string]string{"choice": action.Choice})
		if err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		chatID := strconv.FormatInt(update.CallbackQuery.Message.Chat.ID, 10)
		userID := strconv.FormatInt(update.CallbackQuery.From.ID, 10)
		return domain.CanonicalInboundEvent{
			EventID:         fmt.Sprintf("tg_cb_%s_%d", action.AwaitID, update.UpdateID),
			TenantID:        tenantID,
			Channel:         "telegram",
			Interaction:     "await_response",
			ProviderEventID: fmt.Sprintf("tg_cb_%d", update.UpdateID),
			ReceivedAt:      time.Now().UTC(),
			Sender: domain.Sender{
				ChannelUserID:       userID,
				IsAuthenticated:     true,
				IdentityAssurance:   "provider_verified",
				AllowedResponderIDs: []string{userID},
			},
			Conversation: domain.Conversation{
				ChannelConversationID: chatID,
				ChannelThreadID:       chatID,
				ChannelSurfaceKey:     chatID,
			},
			Message: domain.Message{
				MessageID:   "tg_cb_msg_" + action.AwaitID,
				MessageType: "interactive",
				Text:        action.Choice,
				Parts:       []domain.Part{{ContentType: "application/json", Content: string(payload)}},
			},
			Metadata: domain.Metadata{
				AwaitID:       action.AwaitID,
				ResumePayload: payload,
				RawPayload:    body,
			},
		}, nil
	default:
		return domain.CanonicalInboundEvent{}, errors.New("unsupported telegram update")
	}
}

func (a Adapter) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(delivery.PayloadJSON)
}

func (a Adapter) SendAwaitPrompt(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(delivery.PayloadJSON)
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
		return a.uploadDocument(body)
	}
	method := "sendMessage"
	if _, ok := body["message_id"]; ok {
		method = "editMessageText"
	}
	return a.postJSON(method, body)
}

func (a Adapter) postJSON(method string, payload map[string]any) (domain.DeliveryResult, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	req, err := http.NewRequest(http.MethodPost, a.apiURL(method), bytes.NewReader(raw))
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.DeliveryResult{}, fmt.Errorf("telegram %s failed: status=%d body=%s", method, resp.StatusCode, string(body))
	}
	var parsed struct {
		OK     bool   `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return domain.DeliveryResult{}, err
	}
	if !parsed.OK {
		return domain.DeliveryResult{}, fmt.Errorf("telegram %s returned not ok", method)
	}
	return domain.DeliveryResult{ProviderMessageID: strconv.FormatInt(parsed.Result.MessageID, 10)}, nil
}

func (a Adapter) uploadDocument(payload map[string]any) (domain.DeliveryResult, error) {
	storageURI, _ := payload["storage_uri"].(string)
	chatID, _ := payload["chat_id"].(string)
	if !strings.HasPrefix(storageURI, "file://") {
		return domain.DeliveryResult{}, fmt.Errorf("unsupported telegram storage uri: %s", storageURI)
	}
	path := strings.TrimPrefix(storageURI, "file://")
	file, err := os.Open(path)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", chatID); err != nil {
		return domain.DeliveryResult{}, err
	}
	if caption, _ := payload["caption"].(string); caption != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return domain.DeliveryResult{}, err
		}
	}
	part, err := writer.CreateFormFile("document", filepathBase(path))
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return domain.DeliveryResult{}, err
	}
	if err := writer.Close(); err != nil {
		return domain.DeliveryResult{}, err
	}
	req, err := http.NewRequest(http.MethodPost, a.apiURL("sendDocument"), &body)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.DeliveryResult{}, fmt.Errorf("telegram sendDocument failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return domain.DeliveryResult{}, err
	}
	if !parsed.OK {
		return domain.DeliveryResult{}, errors.New("telegram sendDocument returned not ok")
	}
	return domain.DeliveryResult{ProviderMessageID: strconv.FormatInt(parsed.Result.MessageID, 10)}, nil
}

func (a Adapter) apiURL(method string) string {
	return "https://api.telegram.org/bot" + a.BotToken + "/" + method
}

func filepathBase(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
