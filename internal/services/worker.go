package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/tracex"
)

type WorkerService struct {
	Repo      ports.Repository
	ACP       ports.ACPBridge
	Catalog   *AgentCatalog
	Renderer  ports.Renderer
	Channel   ports.ChannelAdapter
	Renderers map[string]ports.Renderer
	Channels  map[string]ports.ChannelAdapter
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
	default:
		return nil
	}
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
	var currentCompat *domain.AgentCompatibility
	if s.Catalog != nil {
		compat, err := s.Catalog.Validate(ctx, route.ACPAgentName, false)
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
	tracex.Logger(ctx).Info("worker.run_started", "queue_item_id", queued.ID, "run_id", run.ID, "acp_run_id", run.ACPRunID, "event_count", len(stream))
	if err := s.Repo.CreateRun(ctx, run); err != nil {
		return err
	}
	terminalStatus := ""
	for _, runEvent := range stream {
		originalStatus := runEvent.Status
		runEvent = enforceCompatibility(runEvent, currentCompat, session.ChannelType)
		if originalStatus == "awaiting" && runEvent.Status == "failed" && currentCompat != nil && currentCompat.ValidationMode == "opencode_bridge" {
			_ = s.Repo.Audit(ctx, domain.AuditEvent{
				ID:            fmt.Sprintf("audit_worker_opencode_await_block_%s_%d", run.ID, time.Now().UTC().UnixNano()),
				TenantID:      session.TenantID,
				SessionID:     session.ID,
				RunID:         run.ID,
				AggregateType: "run",
				AggregateID:   run.ID,
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
		if runEvent.Status == "awaiting" {
			await := domain.Await{
				ID:               "await_" + runEvent.RunID,
				RunID:            runEvent.RunID,
				SessionID:        session.ID,
				ChannelType:      session.ChannelType,
				Status:           "pending",
				SchemaJSON:       runEvent.AwaitSchema,
				PromptRenderJSON: runEvent.AwaitPrompt,
				ExpiresAt:        time.Now().UTC().Add(24 * time.Hour),
			}
			if err := s.Repo.StoreAwait(ctx, await); err != nil {
				return err
			}
		}
		for _, delivery := range deliveries {
			if err := s.Repo.EnqueueDelivery(ctx, delivery); err != nil {
				return err
			}
		}
		if err := s.Repo.UpdateRunStatus(ctx, run.ID, runEvent.Status); err != nil {
			return err
		}
		if err := s.Repo.UpdateQueueItemStatus(ctx, queued.ID, runEvent.Status); err != nil {
			return err
		}
		switch runEvent.Status {
		case "completed", "failed", "canceled":
			terminalStatus = runEvent.Status
		}
	}
	if terminalStatus != "" {
		tracex.Logger(ctx).Info("worker.run_terminal", "run_id", run.ID, "status", terminalStatus, "session_id", session.ID)
		if _, err := s.Repo.EnqueueNextQueueItem(ctx, session.ID); err != nil {
			return err
		}
	}
	return nil
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
	runEvents, err := s.ACP.ResumeRun(ctx, await, req.Payload)
	if err != nil {
		return err
	}
	tracex.Logger(ctx).Info("worker.await_resume.loaded", "await_id", await.ID, "run_id", await.RunID, "event_count", len(runEvents))
	terminalStatus := ""
	for _, runEvent := range runEvents {
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
		"run_id":    evt.RunID,
		"status":    evt.Status,
		"text":      evt.Text,
		"artifacts": evt.Artifacts,
	})
	if err != nil {
		return fmt.Errorf("marshal outbound message payload: %w", err)
	}
	messageID, err := s.Repo.StoreOutboundMessage(ctx, session, evt.RunID, evt.Text, rawPayload)
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
