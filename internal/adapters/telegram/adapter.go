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
	"nexus/internal/ports"
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
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			MessageID int64  `json:"message_id"`
			Date      int64  `json:"date"`
			Text      string `json:"text"`
			Caption   string `json:"caption"`
			Chat      struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
			Document *struct {
				FileID   string `json:"file_id"`
				FileName string `json:"file_name"`
				MimeType string `json:"mime_type"`
				FileSize int64  `json:"file_size"`
			} `json:"document"`
			Photo []struct {
				FileID   string `json:"file_id"`
				Width    int    `json:"width"`
				Height   int    `json:"height"`
				FileSize int64  `json:"file_size"`
			} `json:"photo"`
			Audio *struct {
				FileID   string `json:"file_id"`
				FileName string `json:"file_name"`
				MimeType string `json:"mime_type"`
				FileSize int64  `json:"file_size"`
			} `json:"audio"`
			Video *struct {
				FileID   string `json:"file_id"`
				FileName string `json:"file_name"`
				MimeType string `json:"mime_type"`
				FileSize int64  `json:"file_size"`
			} `json:"video"`
			Voice *struct {
				FileID   string `json:"file_id"`
				MimeType string `json:"mime_type"`
				FileSize int64  `json:"file_size"`
			} `json:"voice"`
			Location *struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"location"`
			Venue *struct {
				Location struct {
					Latitude  float64 `json:"latitude"`
					Longitude float64 `json:"longitude"`
				} `json:"location"`
				Title   string `json:"title"`
				Address string `json:"address"`
			} `json:"venue"`
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
		text := strings.TrimSpace(firstNonEmpty(update.Message.Text, update.Message.Caption))
		command := ""
		if strings.HasPrefix(text, "/") {
			command = strings.Fields(text)[0]
		}
		parts := []domain.Part{}
		if text != "" {
			parts = append(parts, domain.Part{ContentType: "text/plain", Content: text})
		}
		if location, ok := telegramLocationPart(*update.Message); ok {
			parts = append(parts, domain.NewLocationPart(location))
			if text == "" {
				text = domain.LocationText(location)
			}
		}
		artifacts := telegramArtifacts(*update.Message)
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
				MessageType: telegramMessageType(text, artifacts),
				Text:        text,
				Parts:       parts,
				Artifacts:   artifacts,
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

func telegramArtifacts(message struct {
	MessageID int64  `json:"message_id"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
	Caption   string `json:"caption"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Document *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"document"`
	Photo []struct {
		FileID   string `json:"file_id"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		FileSize int64  `json:"file_size"`
	} `json:"photo"`
	Audio *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"audio"`
	Video *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"video"`
	Voice *struct {
		FileID   string `json:"file_id"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"voice"`
	Location *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location"`
	Venue *struct {
		Location struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
		Title   string `json:"title"`
		Address string `json:"address"`
	} `json:"venue"`
}) []domain.Artifact {
	artifacts := make([]domain.Artifact, 0, 4)
	if message.Document != nil && strings.TrimSpace(message.Document.FileID) != "" {
		name := strings.TrimSpace(firstNonEmpty(message.Document.FileName, message.Document.FileID))
		artifacts = append(artifacts, domain.Artifact{
			ID:        message.Document.FileID,
			Name:      name,
			MIMEType:  strings.TrimSpace(message.Document.MimeType),
			SizeBytes: message.Document.FileSize,
			SourceURL: "telegram-file:" + message.Document.FileID,
		})
	}
	if len(message.Photo) > 0 {
		best := message.Photo[len(message.Photo)-1]
		artifacts = append(artifacts, domain.Artifact{
			ID:        best.FileID,
			Name:      best.FileID + ".jpg",
			MIMEType:  "image/jpeg",
			SizeBytes: best.FileSize,
			SourceURL: "telegram-file:" + best.FileID,
		})
	}
	if message.Audio != nil && strings.TrimSpace(message.Audio.FileID) != "" {
		name := strings.TrimSpace(firstNonEmpty(message.Audio.FileName, message.Audio.FileID+".audio"))
		artifacts = append(artifacts, domain.Artifact{
			ID:        message.Audio.FileID,
			Name:      name,
			MIMEType:  strings.TrimSpace(message.Audio.MimeType),
			SizeBytes: message.Audio.FileSize,
			SourceURL: "telegram-file:" + message.Audio.FileID,
		})
	}
	if message.Video != nil && strings.TrimSpace(message.Video.FileID) != "" {
		name := strings.TrimSpace(firstNonEmpty(message.Video.FileName, message.Video.FileID+".video"))
		artifacts = append(artifacts, domain.Artifact{
			ID:        message.Video.FileID,
			Name:      name,
			MIMEType:  strings.TrimSpace(message.Video.MimeType),
			SizeBytes: message.Video.FileSize,
			SourceURL: "telegram-file:" + message.Video.FileID,
		})
	}
	if message.Voice != nil && strings.TrimSpace(message.Voice.FileID) != "" {
		artifacts = append(artifacts, domain.Artifact{
			ID:        message.Voice.FileID,
			Name:      message.Voice.FileID + ".ogg",
			MIMEType:  strings.TrimSpace(firstNonEmpty(message.Voice.MimeType, "audio/ogg")),
			SizeBytes: message.Voice.FileSize,
			SourceURL: "telegram-file:" + message.Voice.FileID,
		})
	}
	return artifacts
}

func telegramLocationPart(message struct {
	MessageID int64  `json:"message_id"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
	Caption   string `json:"caption"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Document *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"document"`
	Photo []struct {
		FileID   string `json:"file_id"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		FileSize int64  `json:"file_size"`
	} `json:"photo"`
	Audio *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"audio"`
	Video *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"video"`
	Voice *struct {
		FileID   string `json:"file_id"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"voice"`
	Location *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location"`
	Venue *struct {
		Location struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
		Title   string `json:"title"`
		Address string `json:"address"`
	} `json:"venue"`
}) (domain.Location, bool) {
	if message.Venue != nil {
		return domain.Location{
			Latitude:  message.Venue.Location.Latitude,
			Longitude: message.Venue.Location.Longitude,
			Name:      strings.TrimSpace(message.Venue.Title),
			Address:   strings.TrimSpace(message.Venue.Address),
		}, true
	}
	if message.Location != nil {
		return domain.Location{
			Latitude:  message.Location.Latitude,
			Longitude: message.Location.Longitude,
		}, true
	}
	return domain.Location{}, false
}

func telegramMessageType(text string, artifacts []domain.Artifact) string {
	switch {
	case len(artifacts) > 0 && strings.TrimSpace(text) != "":
		return "mixed"
	case len(artifacts) > 0:
		return "artifact"
	default:
		return "text"
	}
}

func (a Adapter) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(delivery.PayloadJSON)
}

func (a Adapter) SendAwaitPrompt(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(delivery.PayloadJSON)
}

func (a Adapter) HydrateInboundArtifacts(ctx context.Context, evt *domain.CanonicalInboundEvent, store ports.InboundArtifactStore) error {
	if len(evt.Message.Artifacts) == 0 {
		return nil
	}
	stored := make([]domain.Artifact, 0, len(evt.Message.Artifacts))
	for _, artifact := range evt.Message.Artifacts {
		content, err := a.downloadArtifact(ctx, artifact)
		if err != nil {
			return err
		}
		saved, err := store.SaveInbound(ctx, artifact.Name, artifact.MIMEType, content)
		if err != nil {
			return err
		}
		saved.ID = artifact.ID
		saved.SourceURL = artifact.SourceURL
		stored = append(stored, saved)
	}
	evt.Message.Artifacts = stored
	return nil
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
	if _, ok := body["latitude"]; ok {
		if _, ok := body["longitude"]; ok {
			return a.postJSON("sendLocation", body)
		}
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
		OK     bool `json:"ok"`
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

func (a Adapter) uploadArtifact(payload map[string]any) (domain.DeliveryResult, error) {
	storageURI, _ := payload["storage_uri"].(string)
	chatID, _ := payload["chat_id"].(string)
	if !strings.HasPrefix(storageURI, "file://") {
		return domain.DeliveryResult{}, fmt.Errorf("unsupported telegram storage uri: %s", storageURI)
	}
	method, formField := telegramUploadTarget(asString(payload["mime_type"]))
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
	part, err := writer.CreateFormFile(formField, filepathBase(path))
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return domain.DeliveryResult{}, err
	}
	if err := writer.Close(); err != nil {
		return domain.DeliveryResult{}, err
	}
	req, err := http.NewRequest(http.MethodPost, a.apiURL(method), &body)
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
		return domain.DeliveryResult{}, fmt.Errorf("telegram %s failed: status=%d body=%s", method, resp.StatusCode, string(raw))
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
		return domain.DeliveryResult{}, fmt.Errorf("telegram %s returned not ok", method)
	}
	return domain.DeliveryResult{ProviderMessageID: strconv.FormatInt(parsed.Result.MessageID, 10)}, nil
}

func (a Adapter) apiURL(method string) string {
	return "https://api.telegram.org/bot" + a.BotToken + "/" + method
}

func (a Adapter) fileURL(path string) string {
	return "https://api.telegram.org/file/bot" + a.BotToken + "/" + strings.TrimPrefix(path, "/")
}

func (a Adapter) downloadArtifact(ctx context.Context, artifact domain.Artifact) ([]byte, error) {
	if a.BotToken == "" {
		return nil, errors.New("missing telegram bot token")
	}
	fileID := strings.TrimSpace(strings.TrimPrefix(artifact.SourceURL, "telegram-file:"))
	if fileID == "" {
		fileID = strings.TrimSpace(artifact.ID)
	}
	reqBody, err := json.Marshal(map[string]string{"file_id": fileID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL("getFile"), bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("telegram getFile failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if !parsed.OK || strings.TrimSpace(parsed.Result.FilePath) == "" {
		return nil, errors.New("telegram getFile returned no file path")
	}
	fileReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.fileURL(parsed.Result.FilePath), nil)
	if err != nil {
		return nil, err
	}
	fileResp, err := a.HTTP.Do(fileReq)
	if err != nil {
		return nil, err
	}
	defer fileResp.Body.Close()
	if fileResp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(fileResp.Body, 4096))
		return nil, fmt.Errorf("telegram file download failed: status=%d body=%s", fileResp.StatusCode, string(raw))
	}
	return io.ReadAll(fileResp.Body)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func filepathBase(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

func telegramUploadTarget(mimeType string) (method, formField string) {
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	switch {
	case mimeType == "image/jpeg", mimeType == "image/jpg", mimeType == "image/png", mimeType == "image/webp":
		return "sendPhoto", "photo"
	case strings.HasPrefix(mimeType, "audio/"):
		return "sendAudio", "audio"
	case strings.HasPrefix(mimeType, "video/"):
		return "sendVideo", "video"
	default:
		return "sendDocument", "document"
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
