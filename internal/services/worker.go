package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/tracex"
)

type WorkerService struct {
	Repo                 ports.Repository
	ACP                  ports.ACPBridge
	Catalog              *AgentCatalog
	Renderer             ports.Renderer
	Channel              ports.ChannelAdapter
	Renderers            map[string]ports.Renderer
	Channels             map[string]ports.ChannelAdapter
	InboundWebhookURL    string
	InboundWebhookToken  string
	GroupContextLimit    int
	GroupContextMaxChars int
	NotifySessionUpdate  func(sessionID string)
}

type deliveryPreparer interface {
	PrepareDelivery(ctx context.Context, delivery domain.OutboundDelivery) (domain.OutboundDelivery, error)
}

const structuredDataContentType = "application/vnd.nexus.structured-data+json"

func (s WorkerService) withWhatsAppGroupContext(ctx context.Context, session domain.Session, message domain.Message) domain.Message {
	if !strings.EqualFold(session.ChannelType, "whatsapp_web") || !strings.Contains(session.ChannelScopeKey, "@g.us") {
		return message
	}
	limit := s.GroupContextLimit
	if limit <= 0 {
		return message
	}
	page, err := s.Repo.ListMessages(ctx, domain.MessageListQuery{
		TenantID:   session.TenantID,
		SessionID:  session.ID,
		CursorPage: domain.CursorPage{Limit: limit},
	})
	if err != nil {
		tracex.Logger(ctx).Warn("worker.group_context_failed", "session_id", session.ID, "error", err.Error())
		return message
	}
	data := map[string]any{
		"kind":               "whatsapp_group_context",
		"group_id":           session.ChannelScopeKey,
		"current_message_id": message.MessageID,
		"recent_messages":    compactGroupMessages(page.Items, message.MessageID, s.GroupContextMaxChars),
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return message
	}
	message.Parts = append(message.Parts, domain.Part{ContentType: structuredDataContentType, Content: string(raw)})
	return message
}

func compactGroupMessages(messages []domain.Message, currentMessageID string, maxChars int) []map[string]any {
	if maxChars <= 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(messages))
	used := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
		}
		remaining := maxChars - used
		if remaining <= 0 {
			break
		}
		if len(text) > remaining {
			text = text[:remaining]
		}
		item := map[string]any{
			"message_id": msg.MessageID,
			"role":       msg.Role,
			"direction":  msg.Direction,
			"text":       text,
			"is_current": msg.MessageID == currentMessageID,
		}
		if participantID := groupParticipantID(msg.Parts); participantID != "" {
			item["participant_id"] = participantID
		}
		if !msg.CreatedAt.IsZero() {
			item["created_at"] = msg.CreatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, item)
		used += len(text)
	}
	return out
}

func groupParticipantID(parts []domain.Part) string {
	for _, part := range parts {
		if part.ContentType != structuredDataContentType {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(part.Content), &data); err != nil {
			continue
		}
		if data["kind"] != "whatsapp_group_message" {
			continue
		}
		if participantID, _ := data["participant_id"].(string); strings.TrimSpace(participantID) != "" {
			return strings.TrimSpace(participantID)
		}
	}
	return ""
}

func (s WorkerService) ProcessOnce(ctx context.Context, limit int) (err error) {
	ctx, end := tracex.StartSpan(ctx, "worker.process_once", "limit", limit)
	defer func() { end(err) }()
	events, err := s.Repo.ClaimOutbox(ctx, time.Now().UTC(), limit)
	if err != nil {
		tracex.Logger(ctx).Error("worker.claim_outbox_failed", "error", err.Error())
		return err
	}
	tracex.Logger(ctx).Info("worker.claimed_outbox", "count", len(events))
	for _, evt := range events {
		eventCtx, eventEnd := tracex.StartSpan(ctx, "worker.process_event",
			"outbox_event_id", evt.ID,
			"event_type", evt.EventType,
			"aggregate_id", evt.AggregateID,
			"tenant_id", evt.TenantID,
		)
		if err := s.processEvent(eventCtx, evt); err != nil {
			eventEnd(err)
			_ = s.Repo.MarkOutboxFailed(ctx, evt.ID, err, time.Now().UTC().Add(10*time.Second))
			tracex.Logger(eventCtx).Error("worker.event_failed", "outbox_event_id", evt.ID, "event_type", evt.EventType, "error", err.Error())
			continue
		}
		eventEnd(nil)
		if err := s.Repo.MarkOutboxDone(ctx, evt.ID); err != nil {
			tracex.Logger(eventCtx).Error("worker.mark_outbox_done_failed", "outbox_event_id", evt.ID, "error", err.Error())
			return err
		}
		tracex.Logger(eventCtx).Info("worker.event_completed", "outbox_event_id", evt.ID, "event_type", evt.EventType)
	}
	return nil
}

func (s WorkerService) rendererFor(channelType string) ports.Renderer {
	if renderer, ok := s.Renderers[channelType]; ok {
		return renderer
	}
	return s.Renderer
}

func (s WorkerService) channelFor(channelType string) ports.ChannelAdapter {
	if adapter, ok := s.Channels[channelType]; ok {
		return adapter
	}
	return s.Channel
}

func (s WorkerService) processEvent(ctx context.Context, evt domain.OutboxEvent) error {
	switch evt.EventType {
	case "queue.start":
		return s.processQueueStart(ctx, evt)
	case "await.resume":
		return s.processAwaitResume(ctx, evt)
	case "delivery.send":
		return s.processDelivery(ctx, evt)
	case "channel.inbound.webhook", "laju.inbound.forward":
		return s.processInboundWebhook(ctx, evt)
	default:
		return nil
	}
}

func (s WorkerService) processInboundWebhook(ctx context.Context, evt domain.OutboxEvent) error {
	if strings.TrimSpace(s.InboundWebhookURL) == "" {
		return errors.New("inbound webhook url is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(s.InboundWebhookURL), bytes.NewReader(evt.PayloadJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(s.InboundWebhookToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(s.InboundWebhookToken))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("inbound webhook failed with status %d", res.StatusCode)
	}
	return nil
}

func (s WorkerService) processQueueStart(ctx context.Context, evt domain.OutboxEvent) error {
	tracex.Logger(ctx).Info("worker.queue_start.begin", "queue_item_id", evt.AggregateID, "idempotency_key", evt.IdempotencyKey)
	queued, err := s.Repo.GetQueueItem(ctx, evt.AggregateID)
	if err != nil {
		return err
	}
	if queued.Status != "queued" {
		return nil
	}
	session, err := s.Repo.GetSession(ctx, queued.SessionID)
	if err != nil {
		return err
	}
	active, err := s.Repo.HasActiveRun(ctx, session.ID)
	if err != nil {
		return err
	}
	if active {
		tracex.Logger(ctx).Info("worker.queue_start.skipped_active_run", "session_id", session.ID, "queue_item_id", queued.ID)
		return nil
	}
	route, err := s.Repo.GetRouteDecision(ctx, queued.ID)
	if err != nil {
		return err
	}
	session.ACPConnectionID = route.ACPConnectionID
	session.ACPAgentName = route.ACPAgentName
	session.ACPProfileID = route.AgentProfileID
	var currentCompat *domain.AgentCompatibility
	if s.Catalog != nil {
		compat, err := s.Catalog.ValidateForRoute(ctx, route, false)
		if err != nil {
			return err
		}
		if !compat.Compatible {
			return fmt.Errorf("agent %s is incompatible: %v", route.ACPAgentName, compat.Reasons)
		}
		currentCompat = &compat
	}
	message, err := s.Repo.GetInboundMessage(ctx, queued.InboundMessageID)
	if err != nil {
		return err
	}
	message = s.withWhatsAppGroupContext(ctx, session, message)
	acpSessionID, err := s.ACP.EnsureSession(ctx, session)
	if err != nil {
		return err
	}
	if acpSessionID != "" && acpSessionID != session.ACPSessionID {
		if err := s.Repo.UpdateSessionACPSessionID(ctx, session.ID, acpSessionID); err != nil {
			return err
		}
	}
	session.ACPSessionID = acpSessionID
	if err := s.Repo.UpdateQueueItemStatus(ctx, queued.ID, "starting"); err != nil {
		return err
	}
	run, stream, err := s.ACP.StartRun(ctx, domain.StartRunRequest{
		TenantID:       session.TenantID,
		Session:        session,
		RouteDecision:  route,
		Message:        message,
		IdempotencyKey: evt.IdempotencyKey,
	})
	if err != nil {
		return err
	}
	run.ACPConnectionID = route.ACPConnectionID
	run.ACPAgentName = route.ACPAgentName
	tracex.Logger(ctx).Info("worker.run_started", "queue_item_id", queued.ID, "run_id", run.ID, "acp_run_id", run.ACPRunID)
	if err := s.Repo.CreateRun(ctx, run); err != nil {
		return err
	}
	terminalStatus, err := s.consumeRunEvents(ctx, session, queued.ID, queued.InboundMessageID, run.ID, route, currentCompat, stream)
	if err != nil {
		return err
	}
	if terminalStatus != "" {
		tracex.Logger(ctx).Info("worker.run_terminal", "run_id", run.ID, "status", terminalStatus, "session_id", session.ID)
		if _, err := s.Repo.EnqueueNextQueueItem(ctx, session.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s WorkerService) consumeRunEvents(ctx context.Context, session domain.Session, queueItemID, inboundMessageID, runID string, route domain.RouteDecision, currentCompat *domain.AgentCompatibility, stream domain.RunEventStream) (string, error) {
	terminalStatus := ""
	for runEvent := range stream.Events {
		originalStatus := runEvent.Status
		runEvent = enforceCompatibility(runEvent, currentCompat, session.ChannelType)
		if originalStatus == "awaiting" && runEvent.Status == "failed" && currentCompat != nil && currentCompat.ValidationMode == "opencode_bridge" {
			_ = s.Repo.Audit(ctx, domain.AuditEvent{
				ID:            fmt.Sprintf("audit_worker_opencode_await_block_%s_%d", runID, time.Now().UTC().UnixNano()),
				TenantID:      session.TenantID,
				SessionID:     session.ID,
				RunID:         runID,
				AggregateType: "run",
				AggregateID:   runID,
				EventType:     "worker.await_blocked_opencode_bridge",
				PayloadJSON: mustJSON(map[string]any{
					"agent_name":      route.ACPAgentName,
					"validation_mode": currentCompat.ValidationMode,
					"warning_count":   len(currentCompat.Warnings),
					"original_status": originalStatus,
					"terminal_status": runEvent.Status,
				}),
				CreatedAt: time.Now().UTC(),
			})
		}
		if err := s.persistRunEvent(ctx, session, runEvent); err != nil {
			return "", err
		}
		if err := s.hideModerationDeniedInbound(ctx, inboundMessageID, runEvent); err != nil {
			return "", err
		}
		renderer := s.rendererFor(session.ChannelType)
		if renderer == nil {
			return "", fmt.Errorf("no renderer for channel %s", session.ChannelType)
		}
		deliveries, err := renderer.RenderRunEvent(ctx, session, runEvent)
		if err != nil {
			return "", err
		}
		if runEvent.Status == "awaiting" {
			await := domain.Await{
				ID:               "await_" + runEvent.RunID,
				RunID:            runEvent.RunID,
				SessionID:        session.ID,
				ChannelType:      session.ChannelType,
				Status:           "pending",
				SchemaJSON:       runEvent.AwaitSchema,
				PromptRenderJSON: runEvent.AwaitPrompt,
				TrustPolicyJSON:  marshalTrustPolicy(route),
				ExpiresAt:        time.Now().UTC().Add(24 * time.Hour),
			}
			if err := s.Repo.StoreAwait(ctx, await); err != nil {
				return "", err
			}
		}
		for _, delivery := range deliveries {
			if err := s.Repo.EnqueueDelivery(ctx, delivery); err != nil {
				return "", err
			}
		}
		if err := s.Repo.UpdateRunStatus(ctx, runID, runEvent.Status); err != nil {
			return "", err
		}
		if runEvent.Status != "queued" {
			if err := s.Repo.UpdateQueueItemStatus(ctx, queueItemID, runEvent.Status); err != nil {
				return "", err
			}
		}
		switch runEvent.Status {
		case "completed", "failed", "canceled":
			terminalStatus = runEvent.Status
		}
		if s.NotifySessionUpdate != nil {
			s.NotifySessionUpdate(session.ID)
		}
	}
	if err, ok := <-stream.Err; ok && err != nil {
		return "", err
	}
	return terminalStatus, nil
}

func marshalTrustPolicy(route domain.RouteDecision) []byte {
	payload, _ := json.Marshal(domain.TrustPolicy{
		AgentProfileID:                    route.AgentProfileID,
		RequireLinkedIdentityForExecution: route.RequireLinkedIdentityForExecution,
		RequireLinkedIdentityForApproval:  route.RequireLinkedIdentityForApproval,
		RequireRecentStepUpForApproval:    route.RequireRecentStepUpForApproval,
		AllowedApprovalChannels:           append([]string(nil), route.AllowedApprovalChannels...),
	})
	return payload
}

const openCodeAwaitBlockedReason = "opencode_bridge_structured_await_blocked"

func enforceCompatibility(runEvent domain.RunEvent, compat *domain.AgentCompatibility, channelType string) domain.RunEvent {
	if compat == nil {
		return runEvent
	}
	if compat.ValidationMode != "opencode_bridge" || runEvent.Status != "awaiting" {
		return runEvent
	}
	return domain.RunEvent{
		RunID:     runEvent.RunID,
		Status:    "failed",
		Text:      openCodeAwaitBlockedReason,
		Artifacts: runEvent.Artifacts,
	}
}

func (s WorkerService) processDelivery(ctx context.Context, evt domain.OutboxEvent) error {
	tracex.Logger(ctx).Info("worker.delivery.begin", "delivery_id", evt.AggregateID)
	delivery, err := s.Repo.GetDelivery(ctx, evt.AggregateID)
	if err != nil {
		return err
	}
	adapter := s.channelFor(delivery.ChannelType)
	if adapter == nil {
		return fmt.Errorf("no channel adapter for %s", delivery.ChannelType)
	}
	if preparer, ok := adapter.(deliveryPreparer); ok {
		prepared, err := preparer.PrepareDelivery(ctx, delivery)
		if err != nil {
			if isPermanentDeliveryPreparationError(err) {
				_ = s.Repo.MarkDeliveryFailed(ctx, delivery.ID, err)
				return nil
			}
			return err
		}
		if string(prepared.PayloadJSON) != string(delivery.PayloadJSON) {
			if err := s.Repo.UpdateDeliveryPayload(ctx, delivery.ID, prepared.PayloadJSON); err != nil {
				return err
			}
		}
		delivery = prepared
	}
	if err := s.Repo.MarkDeliverySending(ctx, delivery.ID); err != nil {
		return err
	}
	var (
		result  domain.DeliveryResult
		sendErr error
	)
	if delivery.AwaitID != "" {
		result, sendErr = adapter.SendAwaitPrompt(ctx, delivery)
	} else {
		result, sendErr = adapter.SendMessage(ctx, delivery)
	}
	if sendErr != nil {
		_ = s.Repo.MarkDeliveryFailed(ctx, delivery.ID, sendErr)
		tracex.Logger(ctx).Error("worker.delivery.failed", "delivery_id", delivery.ID, "channel_type", delivery.ChannelType, "error", sendErr.Error())
		return sendErr
	}
	if err := s.Repo.MarkDeliverySent(ctx, delivery.ID, result); err != nil {
		return err
	}
	tracex.Logger(ctx).Info("worker.delivery.sent", "delivery_id", delivery.ID, "channel_type", delivery.ChannelType, "provider_message_id", result.ProviderMessageID)
	return sendErr
}

func isPermanentDeliveryPreparationError(err error) bool {
	return errors.Is(err, domain.ErrWhatsAppPolicyOptedOut) || errors.Is(err, domain.ErrWhatsAppPolicyWindowClosedNoTemplate)
}

func (s WorkerService) processAwaitResume(ctx context.Context, evt domain.OutboxEvent) error {
	tracex.Logger(ctx).Info("worker.await_resume.begin", "outbox_event_id", evt.ID, "await_id", evt.AggregateID)
	var req domain.ResumeRequest
	if err := json.Unmarshal(evt.PayloadJSON, &req); err != nil {
		return fmt.Errorf("unmarshal await resume: %w", err)
	}
	await, err := s.Repo.GetAwait(ctx, req.AwaitID)
	if err != nil {
		return err
	}
	session, err := s.Repo.GetSession(ctx, await.SessionID)
	if err != nil {
		return err
	}
	var currentCompat *domain.AgentCompatibility
	var run domain.Run
	if s.Catalog != nil {
		run, err = s.Repo.GetRun(ctx, await.RunID)
		if err != nil {
			return err
		}
		// Older rows were persisted with a hardcoded default-agent value, so
		// only enforce resume-time compatibility once the real agent name is
		// available on the run record.
		if name := strings.TrimSpace(run.ACPAgentName); name != "" && name != "default-agent" {
			compat, err := s.Catalog.ValidateForConnection(ctx, run.ACPConnectionID, name, false)
			if err != nil {
				return err
			}
			if !compat.Compatible {
				return fmt.Errorf("agent %s is incompatible: %v", name, compat.Reasons)
			}
			currentCompat = &compat
		}
	} else if run, err = s.Repo.GetRun(ctx, await.RunID); err != nil {
		return err
	}
	session.ACPConnectionID = run.ACPConnectionID
	session.ACPAgentName = run.ACPAgentName
	session.ACPProfileID = session.AgentProfileID
	if run.ACPConnectionID == "" {
		session.ACPProfileID = ""
	}
	var runEvents domain.RunEventStream
	if scoped, ok := s.ACP.(interface {
		ResumeRunForSession(context.Context, domain.Session, domain.Await, []byte) (domain.RunEventStream, error)
	}); ok {
		runEvents, err = scoped.ResumeRunForSession(ctx, session, await, req.Payload)
	} else {
		runEvents, err = s.ACP.ResumeRun(ctx, await, req.Payload)
	}
	if err != nil {
		return err
	}
	tracex.Logger(ctx).Info("worker.await_resume.loaded", "await_id", await.ID, "run_id", await.RunID)
	terminalStatus := ""
	for runEvent := range runEvents.Events {
		runEvent = enforceCompatibility(runEvent, currentCompat, session.ChannelType)
		if strings.TrimSpace(runEvent.MessageKey) == "" {
			runEvent.MessageKey = await.ID + ":resume"
		}
		if err := s.persistRunEvent(ctx, session, runEvent); err != nil {
			return err
		}
		renderer := s.rendererFor(session.ChannelType)
		if renderer == nil {
			return fmt.Errorf("no renderer for channel %s", session.ChannelType)
		}
		deliveries, err := renderer.RenderRunEvent(ctx, session, runEvent)
		if err != nil {
			return err
		}
		for _, delivery := range deliveries {
			if err := s.Repo.EnqueueDelivery(ctx, delivery); err != nil {
				return err
			}
		}
		if err := s.Repo.UpdateRunStatus(ctx, await.RunID, runEvent.Status); err != nil {
			return err
		}
		if err := s.Repo.UpdateActiveQueueItemStatus(ctx, session.ID, runEvent.Status); err != nil {
			return err
		}
		switch runEvent.Status {
		case "completed", "failed", "canceled":
			terminalStatus = runEvent.Status
		}
		if s.NotifySessionUpdate != nil {
			s.NotifySessionUpdate(session.ID)
		}
	}
	if err, ok := <-runEvents.Err; ok && err != nil {
		return err
	}
	if terminalStatus != "" {
		tracex.Logger(ctx).Info("worker.await_resume.terminal", "await_id", await.ID, "run_id", await.RunID, "status", terminalStatus)
		if _, err := s.Repo.EnqueueNextQueueItem(ctx, session.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s WorkerService) persistRunEvent(ctx context.Context, session domain.Session, evt domain.RunEvent) error {
	rawPayload, err := json.Marshal(map[string]any{
		"run_id":      evt.RunID,
		"message_key": evt.MessageKey,
		"status":      evt.Status,
		"text":        evt.Text,
		"is_partial":  evt.IsPartial,
		"artifacts":   evt.Artifacts,
		"metadata":    evt.Metadata,
	})
	if err != nil {
		return fmt.Errorf("marshal outbound message payload: %w", err)
	}
	messageKey := strings.TrimSpace(evt.MessageKey)
	if messageKey == "" {
		messageKey = evt.RunID
	}
	messageID, err := s.Repo.StoreOutboundMessage(ctx, session, evt.RunID, messageKey, evt.Text, rawPayload)
	if err != nil {
		return err
	}
	if len(evt.Artifacts) > 0 {
		if err := s.Repo.StoreArtifacts(ctx, messageID, "outbound", evt.Artifacts); err != nil {
			return err
		}
	}
	return nil
}

func (s WorkerService) hideModerationDeniedInbound(ctx context.Context, inboundMessageID string, evt domain.RunEvent) error {
	if strings.TrimSpace(inboundMessageID) == "" || !runEventSourceIs(evt, "moderation_warning") {
		return nil
	}
	return s.Repo.MarkMessageHiddenFromHistory(ctx, inboundMessageID, map[string]any{
		"context_excluded":      true,
		"visible_in_history":    false,
		"visibility":            "hidden",
		"source":                "moderation_denied",
		"moderation_category":   evt.Metadata["moderation_category"],
		"moderation_policy_id":  evt.Metadata["moderation_policy_id"],
		"moderation_confidence": evt.Metadata["moderation_confidence"],
		"moderation_reason":     evt.Metadata["moderation_reason"],
	})
}

func runEventSourceIs(evt domain.RunEvent, source string) bool {
	if evt.Metadata == nil {
		return false
	}
	value, _ := evt.Metadata["source"].(string)
	return strings.EqualFold(strings.TrimSpace(value), source)
}
