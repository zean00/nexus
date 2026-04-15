package services

import (
	"context"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"strconv"
	"strings"

	"nexus/internal/domain"
)

type SlackRenderer struct{}
type TelegramRenderer struct{}
type WhatsAppRenderer struct{}
type WhatsAppWebRenderer struct{}
type EmailRenderer struct{}
type WebChatRenderer struct{}

func (r SlackRenderer) RenderRunEvent(_ context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error) {
	if evt.IsPartial {
		return nil, nil
	}
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
	case "completed", "running", "failed":
		text := renderSlackRunText(evt)
		if len(evt.Artifacts) > 0 {
			text = appendArtifactSummary(text, evt.Artifacts)
		}
		formatted := renderMarkdownVariants(text)
		payload, err := json.Marshal(map[string]any{
			"channel":   channelID,
			"thread_ts": threadTS,
			"text":      firstNonEmpty(formatted.Plain, text),
			"blocks": []map[string]any{{
				"type": "section",
				"text": map[string]any{"type": "mrkdwn", "text": firstNonEmpty(formatted.Slack, formatted.Plain, text)},
			}},
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

func artifactDeliveryURL(artifact domain.Artifact) string {
	for _, candidate := range []string{artifact.StorageURI, artifact.SourceURL} {
		value := strings.TrimSpace(candidate)
		if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
			return value
		}
	}
	return ""
}

func renderAwaitPayload(channelID, threadTS, awaitID string, prompt []byte) ([]byte, error) {
	text, choices, err := parseAwaitPrompt(prompt)
	if err != nil {
		return nil, fmt.Errorf("unmarshal await render prompt: %w", err)
	}
	formatted := renderMarkdownVariants(text)
	payload := map[string]any{
		"channel":   channelID,
		"thread_ts": threadTS,
		"text":      firstNonEmpty(formatted.Plain, text),
	}
	if len(choices) > 0 {
		actions := make([]map[string]any, 0, len(choices))
		for _, choice := range choices {
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
				"text": map[string]any{"type": "mrkdwn", "text": firstNonEmpty(formatted.Slack, formatted.Plain, text)},
			},
			{
				"type":     "actions",
				"elements": actions,
			},
		}
	} else {
		payload["blocks"] = []map[string]any{{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": firstNonEmpty(formatted.Slack, formatted.Plain, text)},
		}}
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
	if evt.IsPartial {
		return nil, nil
	}
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
	case "completed", "running", "failed":
		text := renderTelegramRunText(evt)
		if len(evt.Artifacts) > 0 {
			text = appendArtifactSummary(text, evt.Artifacts)
		}
		formatted := renderMarkdownVariants(text)
		payloadMap := map[string]any{
			"chat_id":    chatID,
			"text":       firstNonEmpty(formatted.TelegramHTML, stdhtml.EscapeString(text)),
			"parse_mode": "HTML",
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

func (r WhatsAppRenderer) RenderRunEvent(_ context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error) {
	if evt.IsPartial {
		return nil, nil
	}
	recipient := session.ChannelScopeKey
	switch evt.Status {
	case "awaiting":
		payload, err := renderWhatsAppAwaitPayload(recipient, "await_"+evt.RunID, evt.AwaitPrompt)
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
			DeliveryKind:     "send",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_await",
			PayloadJSON:      payload,
		}}, nil
	case "completed", "running", "failed":
		text := evt.Text
		if len(evt.Artifacts) > 0 {
			text = appendArtifactSummary(text, evt.Artifacts)
		}
		formatted := renderMarkdownVariants(text)
		payload, err := json.Marshal(map[string]any{
			"messaging_product": "whatsapp",
			"to":                recipient,
			"type":              "text",
			"text":              map[string]any{"body": firstNonEmpty(formatted.WhatsApp, formatted.Plain, text)},
		})
		if err != nil {
			return nil, err
		}
		deliveries := []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_" + evt.Status,
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "send",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_status",
			PayloadJSON:      payload,
		}}
		for idx, artifact := range evt.Artifacts {
			link := artifactDeliveryURL(artifact)
			if link == "" {
				continue
			}
			artifactPayload, err := json.Marshal(map[string]any{
				"kind":        "artifact_upload",
				"to":          recipient,
				"storage_uri": link,
				"file_name":   artifact.Name,
				"mime_type":   artifact.MIMEType,
				"caption":     artifact.Name,
			})
			if err != nil {
				return nil, err
			}
			deliveries = append(deliveries, domain.OutboundDelivery{
				ID:               fmt.Sprintf("delivery_%s_wa_artifact_%d", evt.RunID, idx),
				TenantID:         session.TenantID,
				SessionID:        session.ID,
				RunID:            evt.RunID,
				ChannelType:      session.ChannelType,
				DeliveryKind:     "send",
				Status:           "queued",
				LogicalMessageID: fmt.Sprintf("logical_%s_wa_artifact_%d", evt.RunID, idx),
				PayloadJSON:      artifactPayload,
			})
		}
		return deliveries, nil
	default:
		return nil, nil
	}
}

func (r WhatsAppWebRenderer) RenderRunEvent(_ context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error) {
	if evt.IsPartial {
		return nil, nil
	}
	recipient := session.ChannelScopeKey
	switch evt.Status {
	case "awaiting":
		payload, err := renderWhatsAppWebAwaitPayload(recipient, "await_"+evt.RunID, evt.AwaitPrompt)
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
			DeliveryKind:     "send",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_await",
			PayloadJSON:      payload,
		}}, nil
	case "completed", "running", "failed":
		text := evt.Text
		if len(evt.Artifacts) > 0 {
			text = appendArtifactSummary(text, evt.Artifacts)
		}
		formatted := renderMarkdownVariants(text)
		payload, err := json.Marshal(map[string]any{
			"chatId": recipient,
			"text":   firstNonEmpty(formatted.WhatsApp, formatted.Plain, text),
		})
		if err != nil {
			return nil, err
		}
		deliveries := []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_" + evt.Status,
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "send",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_status",
			PayloadJSON:      payload,
		}}
		for idx, artifact := range evt.Artifacts {
			artifactPayload, err := json.Marshal(map[string]any{
				"kind":        "artifact_upload",
				"chatId":      recipient,
				"storage_uri": artifact.StorageURI,
				"file_name":   artifact.Name,
				"mime_type":   artifact.MIMEType,
				"caption":     artifact.Name,
			})
			if err != nil {
				return nil, err
			}
			deliveries = append(deliveries, domain.OutboundDelivery{
				ID:               fmt.Sprintf("delivery_%s_waha_artifact_%d", evt.RunID, idx),
				TenantID:         session.TenantID,
				SessionID:        session.ID,
				RunID:            evt.RunID,
				ChannelType:      session.ChannelType,
				DeliveryKind:     "send",
				Status:           "queued",
				LogicalMessageID: fmt.Sprintf("logical_%s_waha_artifact_%d", evt.RunID, idx),
				PayloadJSON:      artifactPayload,
			})
		}
		return deliveries, nil
	default:
		return nil, nil
	}
}

func (r EmailRenderer) RenderRunEvent(_ context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error) {
	if evt.IsPartial {
		return nil, nil
	}
	recipient, threadID := splitEmailSurfaceKey(session.ChannelScopeKey)
	subject := "Nexus update"
	switch evt.Status {
	case "awaiting":
		payload, err := renderEmailAwaitPayload(recipient, threadID, "await_"+evt.RunID, evt.AwaitPrompt)
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
			DeliveryKind:     "send",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_await",
			PayloadJSON:      payload,
		}}, nil
	case "completed", "running", "failed":
		text := evt.Text
		if len(evt.Artifacts) > 0 {
			text = appendArtifactSummary(text, evt.Artifacts)
		}
		formatted := renderMarkdownVariants(text)
		payloadMap := map[string]any{
			"to":        recipient,
			"thread_id": threadID,
			"subject":   subjectForStatus(evt.Status),
			"text":      firstNonEmpty(formatted.Plain, text),
			"html":      formatted.EmailHTML,
		}
		payload, err := json.Marshal(payloadMap)
		if err != nil {
			return nil, err
		}
		deliveries := []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_" + evt.Status,
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "send",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_status",
			PayloadJSON:      payload,
		}}
		for idx, artifact := range evt.Artifacts {
			artifactPayload, err := json.Marshal(map[string]any{
				"kind":        "artifact_upload",
				"to":          recipient,
				"thread_id":   threadID,
				"subject":     subject,
				"text":        artifact.Name,
				"file_name":   artifact.Name,
				"storage_uri": artifact.StorageURI,
			})
			if err != nil {
				return nil, err
			}
			deliveries = append(deliveries, domain.OutboundDelivery{
				ID:               fmt.Sprintf("delivery_%s_email_artifact_%d", evt.RunID, idx),
				TenantID:         session.TenantID,
				SessionID:        session.ID,
				RunID:            evt.RunID,
				ChannelType:      session.ChannelType,
				DeliveryKind:     "send",
				Status:           "queued",
				LogicalMessageID: fmt.Sprintf("logical_%s_email_artifact_%d", evt.RunID, idx),
				PayloadJSON:      artifactPayload,
			})
		}
		return deliveries, nil
	default:
		return nil, nil
	}
}

func (r WebChatRenderer) RenderRunEvent(_ context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error) {
	if evt.IsPartial {
		return nil, nil
	}
	switch evt.Status {
	case "awaiting":
		return []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_await",
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			AwaitID:          "await_" + evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "send",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_await",
			PayloadJSON:      evt.AwaitPrompt,
		}}, nil
	case "completed", "running", "failed":
		payload, err := json.Marshal(map[string]any{
			"text":      evt.Text,
			"status":    evt.Status,
			"artifacts": evt.Artifacts,
		})
		if err != nil {
			return nil, err
		}
		return []domain.OutboundDelivery{{
			ID:               "delivery_" + evt.RunID + "_" + evt.Status,
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            evt.RunID,
			ChannelType:      session.ChannelType,
			DeliveryKind:     "send",
			Status:           "queued",
			LogicalMessageID: "logical_" + evt.RunID + "_status",
			PayloadJSON:      payload,
		}}, nil
	default:
		return nil, nil
	}
}

func renderSlackRunText(evt domain.RunEvent) string {
	if evt.Status == "failed" && evt.Text == openCodeAwaitBlockedReason {
		return "This Slack route is backed by the OpenCode bridge, and that backend cannot pause for structured approval prompts. The request was stopped instead of leaving a broken pending interaction. Retry with a direct instruction, or use a route that supports native await/resume."
	}
	return evt.Text
}

func renderTelegramRunText(evt domain.RunEvent) string {
	if evt.Status == "failed" && evt.Text == openCodeAwaitBlockedReason {
		return "This Telegram route is backed by the OpenCode bridge, and that backend cannot pause for structured approval prompts. The request was stopped instead of leaving a broken pending interaction. Retry with a direct instruction, or use a route that supports native await/resume."
	}
	return evt.Text
}

func renderTelegramAwaitPayload(chatID, awaitID string, prompt []byte) ([]byte, error) {
	text, choices, err := parseAwaitPrompt(prompt)
	if err != nil {
		return nil, fmt.Errorf("unmarshal telegram await prompt: %w", err)
	}
	formatted := renderMarkdownVariants(text)
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       firstNonEmpty(formatted.TelegramHTML, stdhtml.EscapeString(text)),
		"parse_mode": "HTML",
	}
	if len(choices) > 0 {
		rows := make([][]map[string]string, 0, len(choices))
		for _, choice := range choices {
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

func renderWhatsAppAwaitPayload(recipient, awaitID string, prompt []byte) ([]byte, error) {
	text, choices, err := renderAwaitTextChoices(awaitID, prompt, false)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"to":                recipient,
		"type":              "text",
		"text":              map[string]any{"body": text},
	}
	if len(choices) > 0 && len(choices) <= 3 {
		buttons := make([]map[string]any, 0, len(choices))
		for _, choice := range choices {
			buttons = append(buttons, map[string]any{
				"type": "reply",
				"reply": map[string]any{
					"id":    fmt.Sprintf("await:%s:%s", awaitID, choice.ID),
					"title": truncate(choice.Label, 20),
				},
			})
		}
		payload["type"] = "interactive"
		payload["interactive"] = map[string]any{
			"type": "button",
			"body": map[string]any{"text": text},
			"action": map[string]any{
				"buttons": buttons,
			},
		}
	}
	return json.Marshal(payload)
}

func renderWhatsAppWebAwaitPayload(recipient, awaitID string, prompt []byte) ([]byte, error) {
	text, _, err := renderAwaitTextChoices(awaitID, prompt, true)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"chatId": recipient,
		"text":   text,
	})
}

func renderAwaitTextChoices(awaitID string, prompt []byte, alwaysIncludeChoices bool) (string, []domain.RenderChoice, error) {
	text, choices, err := parseAwaitPrompt(prompt)
	if err != nil {
		return "", nil, err
	}
	formatted := renderMarkdownVariants(text)
	text = firstNonEmpty(formatted.WhatsApp, formatted.Plain, text)
	if len(choices) > 3 || (alwaysIncludeChoices && len(choices) > 0) {
		lines := make([]string, 0, len(choices)+3)
		lines = append(lines, text, "", "Reply with one of:")
		for _, choice := range choices {
			lines = append(lines, fmt.Sprintf("[await:%s] %s", awaitID, choice.ID))
		}
		text = strings.Join(lines, "\n")
	}
	return text, choices, nil
}

func renderEmailAwaitPayload(recipient, threadID, awaitID string, prompt []byte) ([]byte, error) {
	text, choices, err := parseAwaitPrompt(prompt)
	if err != nil {
		return nil, err
	}
	if len(choices) > 0 {
		lines := make([]string, 0, len(choices)+2)
		lines = append(lines, text, "")
		for _, choice := range choices {
			lines = append(lines, "- "+choice.Label+" ("+choice.ID+")")
		}
		text = strings.Join(lines, "\n")
	}
	formatted := renderMarkdownVariants(text)
	html := formatted.EmailHTML
	text += "\n\nReply to this email and keep [await:" + awaitID + "] in the subject."
	if html != "" {
		html += "<p>Reply to this email and keep <code>[await:" + stdhtml.EscapeString(awaitID) + "]</code> in the subject.</p>"
	}
	return json.Marshal(map[string]any{
		"to":        recipient,
		"thread_id": threadID,
		"subject":   "Input needed [await:" + awaitID + "]",
		"text":      firstNonEmpty(formatted.Plain, text) + "\n\nReply to this email and keep [await:" + awaitID + "] in the subject.",
		"html":      html,
	})
}

func splitEmailSurfaceKey(key string) (string, string) {
	conversation, thread, ok := strings.Cut(key, "|")
	if !ok {
		return key, ""
	}
	return conversation, thread
}

func subjectForStatus(status string) string {
	switch status {
	case "failed":
		return "Nexus request failed"
	case "running":
		return "Nexus request running"
	default:
		return "Nexus request complete"
	}
}

func truncate(in string, limit int) string {
	if len(in) <= limit {
		return in
	}
	return in[:limit]
}

func atoiLoose(in string) int64 {
	n, _ := strconv.ParseInt(in, 10, 64)
	return n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
