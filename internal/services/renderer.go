package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"nexus/internal/domain"
)

type SlackRenderer struct{}
type TelegramRenderer struct{}

func (r SlackRenderer) RenderRunEvent(_ context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error) {
	channelID, threadTS := splitSurfaceKey(session.ChannelScopeKey)
	switch evt.Status {
	case "awaiting":
		payload, err := renderAwaitPayload(channelID, threadTS, "await_"+evt.RunID, evt.AwaitPrompt)
		if err != nil {
			return nil, err
		}
		return []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_await",
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			AwaitID:          "await_" + evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "replace",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_await",
			PayloadJSON:      payload,
		}}, nil
	case "completed", "running":
		text := evt.Text
		if len(evt.Artifacts) > 0 {
			text = appendArtifactSummary(text, evt.Artifacts)
		}
		payload, err := json.Marshal(map[string]any{
			"channel":   channelID,
			"thread_ts": threadTS,
			"text":      text,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal text payload: %w", err)
		}
		deliveries := []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_" + evt.Status,
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "replace",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_status",
			PayloadJSON:      payload,
		}}
		for idx, artifact := range evt.Artifacts {
			artifactPayload, err := json.Marshal(map[string]any{
				"kind":        "artifact_upload",
				"channel":     channelID,
				"thread_ts":   threadTS,
				"title":       artifact.Name,
				"file_name":   artifact.Name,
				"storage_uri": artifact.StorageURI,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal artifact payload: %w", err)
			}
			deliveries = append(deliveries, domain.OutboundDelivery{
				ID:               fmt.Sprintf("delivery_%s_artifact_%d", evt.RunID, idx),
				TenantID:         session.TenantID,
				SessionID:        session.ID,
				RunID:            evt.RunID,
				ChannelType:      session.ChannelType,
				DeliveryKind:     "send",
				Status:           "queued",
				LogicalMessageID: fmt.Sprintf("logical_%s_artifact_%d", evt.RunID, idx),
				PayloadJSON:      artifactPayload,
			})
		}
		return deliveries, nil
	default:
		return nil, nil
	}
}

func appendArtifactSummary(text string, artifacts []domain.Artifact) string {
	if len(artifacts) == 0 {
		return text
	}
	lines := make([]string, 0, len(artifacts)+1)
	if text != "" {
		lines = append(lines, text)
	}
	lines = append(lines, "Artifacts:")
	for _, artifact := range artifacts {
		label := artifact.Name
		if label == "" {
			label = artifact.ID
		}
		lines = append(lines, "- "+label)
	}
	return strings.Join(lines, "\n")
}

func renderAwaitPayload(channelID, threadTS, awaitID string, prompt []byte) ([]byte, error) {
	var model struct {
		Title   string                `json:"title"`
		Body    string                `json:"body"`
		Choices []domain.RenderChoice `json:"choices"`
	}
	if len(prompt) > 0 {
		if err := json.Unmarshal(prompt, &model); err != nil {
			return nil, fmt.Errorf("unmarshal await render prompt: %w", err)
		}
	}
	text := model.Title
	if text == "" {
		text = "The agent needs your input to continue."
	}
	if model.Body != "" {
		text = text + "\n" + model.Body
	}
	payload := map[string]any{
		"channel":   channelID,
		"thread_ts": threadTS,
		"text":      text,
	}
	if len(model.Choices) > 0 {
		actions := make([]map[string]any, 0, len(model.Choices))
		for _, choice := range model.Choices {
			value, err := json.Marshal(map[string]string{
				"await_id": awaitID,
				"choice":   choice.ID,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal slack action value: %w", err)
			}
			actions = append(actions, map[string]any{
				"type":      "button",
				"text":      map[string]any{"type": "plain_text", "text": choice.Label},
				"value":     string(value),
				"action_id": "await_choice_" + choice.ID,
			})
		}
		payload["blocks"] = []map[string]any{
			{
				"type": "section",
				"text": map[string]any{"type": "mrkdwn", "text": text},
			},
			{
				"type":     "actions",
				"elements": actions,
			},
		}
	}
	return json.Marshal(payload)
}

func splitSurfaceKey(key string) (string, string) {
	channel, thread, ok := strings.Cut(key, ":")
	if !ok {
		return key, ""
	}
	return channel, thread
}

func (r TelegramRenderer) RenderRunEvent(_ context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error) {
	chatID, messageID := splitSurfaceKey(session.ChannelScopeKey)
	switch evt.Status {
	case "awaiting":
		payload, err := renderTelegramAwaitPayload(chatID, "await_"+evt.RunID, evt.AwaitPrompt)
		if err != nil {
			return nil, err
		}
		return []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_await",
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			AwaitID:          "await_" + evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "replace",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_await",
			PayloadJSON:      payload,
		}}, nil
	case "completed", "running":
		text := evt.Text
		if len(evt.Artifacts) > 0 {
			text = appendArtifactSummary(text, evt.Artifacts)
		}
		payloadMap := map[string]any{
			"chat_id": chatID,
			"text":    text,
		}
		if messageID != "" {
			payloadMap["message_id"] = atoiLoose(messageID)
		}
		payload, err := json.Marshal(payloadMap)
		if err != nil {
			return nil, fmt.Errorf("marshal telegram text payload: %w", err)
		}
		deliveries := []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_" + evt.Status,
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "replace",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_status",
			PayloadJSON:      payload,
		}}
		for idx, artifact := range evt.Artifacts {
			artifactPayload, err := json.Marshal(map[string]any{
				"kind":        "artifact_upload",
				"chat_id":     chatID,
				"caption":     artifact.Name,
				"storage_uri": artifact.StorageURI,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal telegram artifact payload: %w", err)
			}
			deliveries = append(deliveries, domain.OutboundDelivery{
				ID:               fmt.Sprintf("delivery_%s_tg_artifact_%d", evt.RunID, idx),
				TenantID:         session.TenantID,
				SessionID:        session.ID,
				RunID:            evt.RunID,
				ChannelType:      session.ChannelType,
				DeliveryKind:     "send",
				Status:           "queued",
				LogicalMessageID: fmt.Sprintf("logical_%s_tg_artifact_%d", evt.RunID, idx),
				PayloadJSON:      artifactPayload,
			})
		}
		return deliveries, nil
	default:
		return nil, nil
	}
}

func renderTelegramAwaitPayload(chatID, awaitID string, prompt []byte) ([]byte, error) {
	var model struct {
		Title   string                `json:"title"`
		Body    string                `json:"body"`
		Choices []domain.RenderChoice `json:"choices"`
	}
	if len(prompt) > 0 {
		if err := json.Unmarshal(prompt, &model); err != nil {
			return nil, fmt.Errorf("unmarshal telegram await prompt: %w", err)
		}
	}
	text := model.Title
	if text == "" {
		text = "The agent needs your input to continue."
	}
	if model.Body != "" {
		text += "\n" + model.Body
	}
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if len(model.Choices) > 0 {
		rows := make([][]map[string]string, 0, len(model.Choices))
		for _, choice := range model.Choices {
			data, err := json.Marshal(map[string]string{"await_id": awaitID, "choice": choice.ID})
			if err != nil {
				return nil, err
			}
			rows = append(rows, []map[string]string{{
				"text":          choice.Label,
				"callback_data": string(data),
			}})
		}
		payload["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	return json.Marshal(payload)
}

func atoiLoose(in string) int64 {
	n, _ := strconv.ParseInt(in, 10, 64)
	return n
}
