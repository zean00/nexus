package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"nexus/internal/adapters/acp"
	"nexus/internal/domain"
	"nexus/internal/httpx"
	"nexus/internal/ports"
	"nexus/internal/services"
)

type acpRuntimeStatusProvider interface {
	RuntimeStatus() acp.StdioRuntimeStatus
}

func (a *App) handleSlackWebhook(w http.ResponseWriter, r *http.Request) {
	a.handleChannelWebhook(w, r, a.Slack)
}

func (a *App) handleWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	if a.WhatsApp.WriteVerification(w, r) {
		return
	}
	a.handleChannelWebhook(w, r, a.WhatsApp)
}

func (a *App) handleEmailWebhook(w http.ResponseWriter, r *http.Request) {
	a.handleChannelWebhook(w, r, a.Email)
}

func (a *App) handleTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	a.handleChannelWebhook(w, r, a.Telegram)
}

func (a *App) handleChannelWebhook(w http.ResponseWriter, r *http.Request, adapter ports.ChannelAdapter) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "read request")
		return
	}
	if err := adapter.VerifyInbound(r.Context(), r, body); err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if batcher, ok := adapter.(ports.BatchInboundParser); ok {
		events, err := batcher.ParseInboundBatch(r.Context(), r, body, a.Config.DefaultTenantID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(events) == 0 {
			httpx.Error(w, http.StatusBadRequest, "no inbound events")
			return
		}
		for i := range events {
			if err := a.processBatchChannelEvent(r.Context(), adapter, events[i]); err != nil {
				httpx.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		httpx.Accepted(w, map[string]any{"status": "accepted", "processed": len(events)}, map[string]any{
			"channel": adapter.Channel(),
		})
		return
	}
	evt, err := adapter.ParseInbound(r.Context(), r, body, a.Config.DefaultTenantID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.persistInboundArtifacts(r.Context(), adapter, &evt); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if evt.Channel == "telegram" && !a.telegramUserAllowed(r.Context(), evt.Sender.ChannelUserID) {
		if existing, err := a.Repo.GetTelegramUserAccess(r.Context(), a.Config.DefaultTenantID, evt.Sender.ChannelUserID); err == nil {
			switch existing.Status {
			case "pending":
				httpx.Accepted(w, map[string]any{"status": "pairing_pending"}, webhookActorMeta(evt))
				return
			case "denied":
				if noticeSession, noticeErr := a.Repo.EnsureNotificationSession(r.Context(), a.Config.DefaultTenantID, "telegram", evt.Conversation.ChannelConversationID, evt.Sender.ChannelUserID); noticeErr == nil {
					_ = a.Repo.EnqueueDelivery(r.Context(), telegramNoticeDeliveryWithKind(
						a.Config.DefaultTenantID,
						noticeSession.ID,
						evt.Conversation.ChannelConversationID,
						"delivery_denied_"+evt.ProviderEventID,
						"logical_access_denied_"+evt.Sender.ChannelUserID,
						"Your Telegram access request has been denied. Contact an operator if you need this decision reviewed.",
						"replace",
					))
				}
				httpx.Accepted(w, map[string]any{"status": "access_denied"}, webhookActorMeta(evt))
				return
			}
		}
		noticeSession, noticeErr := a.Repo.EnsureNotificationSession(r.Context(), a.Config.DefaultTenantID, "telegram", evt.Conversation.ChannelConversationID, evt.Sender.ChannelUserID)
		if _, err := a.Repo.RequestTelegramAccess(r.Context(), domain.TelegramUserAccess{
			TenantID:       a.Config.DefaultTenantID,
			TelegramUserID: evt.Sender.ChannelUserID,
			DisplayName:    evt.Sender.DisplayName,
			AddedBy:        "self",
		}); err == nil {
			_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
				a.Config.DefaultTenantID,
				"audit_telegram_request_"+evt.ProviderEventID,
				evt.Sender.ChannelUserID,
				"telegram.access_requested",
				map[string]any{"provider_event_id": evt.ProviderEventID},
			))
		}
		_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
			a.Config.DefaultTenantID,
			"audit_telegram_deny_"+evt.ProviderEventID,
			evt.Sender.ChannelUserID,
			"telegram.allowlist_denied",
			map[string]any{"provider_event_id": evt.ProviderEventID},
		))
		if noticeErr == nil {
			_ = a.Repo.EnqueueDelivery(r.Context(), telegramNoticeDelivery(
				a.Config.DefaultTenantID,
				noticeSession.ID,
				evt.Conversation.ChannelConversationID,
				"delivery_pairing_"+evt.ProviderEventID,
				"logical_pairing_"+evt.ProviderEventID,
				"Access request submitted. An operator must approve this Telegram account before you can use the bot.",
			))
		}
		httpx.Accepted(w, map[string]any{"status": "pairing_requested"}, webhookActorMeta(evt))
		return
	}
	if evt.Interaction == "challenge" {
		httpx.OK(w, map[string]any{"challenge": evt.Message.Text}, webhookMeta(evt))
		return
	}
	if evt.Interaction == "await_response" {
		if err := a.authorizeAwaitResponse(r.Context(), evt); err != nil {
			httpx.Error(w, http.StatusForbidden, err.Error())
			return
		}
		if err := a.Await.HandleResponse(r.Context(), evt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.OK(w, map[string]any{"status": "accepted"}, webhookAwaitMeta(evt))
		return
	}
	result, err := a.Inbound.Handle(r.Context(), evt)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Accepted(w, result, webhookResultMeta(evt))
}

func (a *App) processBatchChannelEvent(ctx context.Context, adapter ports.ChannelAdapter, evt domain.CanonicalInboundEvent) error {
	if err := a.persistInboundArtifacts(ctx, adapter, &evt); err != nil {
		return err
	}
	if evt.Interaction == "await_response" {
		if err := a.authorizeAwaitResponse(ctx, evt); err != nil {
			return err
		}
		return a.Await.HandleResponse(ctx, evt)
	}
	if evt.Interaction == "challenge" {
		return nil
	}
	_, err := a.Inbound.Handle(ctx, evt)
	return err
}

func (a *App) authorizeAwaitResponse(ctx context.Context, evt domain.CanonicalInboundEvent) error {
	if a.Identity == nil {
		return nil
	}
	if len(a.Config.AllowedApprovalChannels) > 0 && !slices.Contains(a.Config.AllowedApprovalChannels, evt.Channel) {
		return domain.ErrApprovalChannelNotAllowed
	}
	user, err := a.resolveCanonicalUser(ctx, evt)
	if err != nil {
		if a.Config.RequireLinkedIdentity {
			return domain.ErrLinkedIdentityRequired
		}
		return nil
	}
	if a.Config.RequireRecentStepUp {
		ok, err := a.Identity.HasRecentStepUp(ctx, evt.TenantID, user.ID, time.Now().UTC().Add(-time.Duration(a.Config.StepUpWindowMinutes)*time.Minute))
		if err != nil {
			return err
		}
		if !ok {
			return domain.ErrRecentStepUpRequired
		}
	}
	return nil
}

func (a *App) resolveCanonicalUser(ctx context.Context, evt domain.CanonicalInboundEvent) (domain.User, error) {
	switch evt.Channel {
	case "webchat", "email":
		user, err := a.Identity.EnsureUserByEmail(ctx, evt.TenantID, evt.Sender.ChannelUserID)
		if err != nil {
			return domain.User{}, err
		}
		channelType := evt.Channel
		if err := a.Identity.UpsertLinkedIdentity(ctx, domain.LinkedIdentity{
			TenantID:       evt.TenantID,
			UserID:         user.ID,
			ChannelType:    channelType,
			ChannelUserID:  strings.ToLower(strings.TrimSpace(evt.Sender.ChannelUserID)),
			Status:         "linked",
			LinkedAt:       time.Now().UTC(),
			LastVerifiedAt: time.Now().UTC(),
		}); err != nil {
			return domain.User{}, err
		}
		return user, nil
	default:
		identity, err := a.Identity.GetLinkedIdentity(ctx, evt.TenantID, evt.Channel, evt.Sender.ChannelUserID)
		if err != nil {
			return domain.User{}, err
		}
		return a.Identity.GetUser(ctx, evt.TenantID, identity.UserID)
	}
}

func (a *App) persistInboundArtifacts(ctx context.Context, adapter ports.ChannelAdapter, evt *domain.CanonicalInboundEvent) error {
	if len(evt.Message.Artifacts) == 0 {
		return nil
	}
	if hydrator, ok := adapter.(ports.InboundArtifactHydrator); ok {
		return hydrator.HydrateInboundArtifacts(ctx, evt, a.Artifacts)
	}
	return nil
}

func (a *App) handleListSessions(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := buildSessionListQuery(r, a.Config.DefaultTenantID, page)
	sessions, err := a.Repo.ListSessions(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountSessions(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, sessions.Items, sessions.NextCursor, totalCount)
}

func (a *App) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := requiredQueryParam(w, r, "session_id")
	if !ok {
		return
	}
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := a.Repo.GetSessionDetail(r.Context(), sessionID, page.Limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, detail, detailMeta("session_id", sessionID, page.Limit))
}

func (a *App) handleListCompatibleACPAgents(w http.ResponseWriter, r *http.Request) {
	refresh := acpRefresh(r)
	agents, err := a.Catalog.Compatible(r.Context(), refresh)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, agents, acpCompatibleListMeta(r.Context(), a.Catalog, a.Repo, a.Config.DefaultTenantID, refresh, agents))
}

func (a *App) handleValidateACPAgent(w http.ResponseWriter, r *http.Request) {
	agentName := acpAgentName(r, a.Config.DefaultACPAgentName)
	refresh := acpRefresh(r)
	compat, err := a.Catalog.Validate(r.Context(), agentName, refresh)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, compat, acpValidateMeta(r.Context(), a.Catalog, a.Repo, a.Config.DefaultTenantID, agentName, refresh, compat))
}

func (a *App) handleListACPAgents(w http.ResponseWriter, r *http.Request) {
	refresh := acpRefresh(r)
	agents, err := a.Catalog.List(r.Context(), refresh)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, agents, acpListMeta(r.Context(), a.Catalog, a.Repo, a.Config.DefaultTenantID, refresh, len(agents)))
}

func (a *App) handleACPAdminSummary(w http.ResponseWriter, r *http.Request) {
	refresh := acpRefresh(r)
	agents, err := a.Catalog.List(r.Context(), refresh)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	compat, compatibleCount := acpCompatibilitySnapshot(agents)
	data, err := acpAdminSummaryData(r.Context(), a.Catalog, a.Repo, a.Config.DefaultTenantID, a.Config.DefaultACPAgentName, agents, compat)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	data["compatible_count"] = compatibleCount
	data["incompatible_count"] = len(agents) - compatibleCount
	httpx.OK(w, data, map[string]any{"refresh": refresh})
}

func (a *App) handleListACPBridgeBlocks(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := buildAuditEventListQuery(r, a.Config.DefaultTenantID, page)
	query.EventType = "worker.await_blocked_opencode_bridge"
	events, err := a.Repo.ListAuditEvents(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountAuditEvents(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, events.Items, events.NextCursor, totalCount)
}

func (a *App) handleListRuns(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := buildRunListQuery(r, a.Config.DefaultTenantID, page)
	runs, err := a.Repo.ListRuns(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountRuns(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, runs.Items, runs.NextCursor, totalCount)
}

func (a *App) handleListMessages(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := buildMessageListQuery(r, a.Config.DefaultTenantID, page)
	messages, err := a.Repo.ListMessages(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountMessages(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, messages.Items, messages.NextCursor, totalCount)
}

func (a *App) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := buildArtifactListQuery(r, a.Config.DefaultTenantID, page)
	artifacts, err := a.Repo.ListArtifacts(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountArtifacts(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, artifacts.Items, artifacts.NextCursor, totalCount)
}

func (a *App) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := buildDeliveryListQuery(r, a.Config.DefaultTenantID, page)
	deliveries, err := a.Repo.ListDeliveries(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountDeliveries(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, deliveries.Items, deliveries.NextCursor, totalCount)
}

func (a *App) handleRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	persisted, _ := persistentLifecycleCounts(r.Context(), a.Repo, a.Config.DefaultTenantID)
	data := map[string]any{
		"runtime":   a.Runtime.Status(),
		"health":    healthStatus(a.Catalog, a.Runtime, a.Config.DefaultACPAgentName, a.Config.WorkerPollInterval, a.Config.ReconcilerInterval),
		"readiness": readinessStatus(a.Catalog, a.Runtime, a.Config.DefaultACPAgentName, a.Config.WorkerPollInterval, a.Config.ReconcilerInterval),
		"persisted": persisted,
	}
	if status, ok := acpRuntimeStatus(a.ACP); ok {
		data["acp_runtime"] = status
	}
	if summary, err := acpCompactSummary(r.Context(), a.Catalog, a.Repo, a.Config.DefaultTenantID, a.Config.DefaultACPAgentName); err == nil {
		data["acp"] = summary
	}
	httpx.OK(w, data, map[string]any{"service": "admin"})
}

func (a *App) handleRetentionStatus(w http.ResponseWriter, r *http.Request) {
	tenantID := queryString(r, "tenant_id")
	if tenantID == "" {
		tenantID = a.Config.DefaultTenantID
	}
	override, effective, err := a.Retention.ResolvePolicy(r.Context(), tenantID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	dryRun, err := a.Retention.DryRun(r.Context(), tenantID, a.Config.RetentionBatchSize)
	if err != nil {
		status := http.StatusInternalServerError
		if services.IsRetentionLockBusy(err) {
			status = http.StatusConflict
		}
		httpx.Error(w, status, err.Error())
		return
	}
	httpx.OK(w, map[string]any{
		"tenant_id": tenantID,
		"override":  override,
		"effective": effective,
		"runtime":   a.Runtime.Status(),
		"dry_run":   dryRun,
	}, map[string]any{"service": "admin"})
}

func (a *App) handleRunRetention(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		TenantID string `json:"tenant_id"`
		DryRun   bool   `json:"dry_run"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	summary, err := a.Retention.RunOnce(r.Context(), body.TenantID, body.DryRun, a.Config.RetentionBatchSize)
	if err != nil {
		code := http.StatusInternalServerError
		if services.IsRetentionLockBusy(err) {
			code = http.StatusConflict
		}
		httpx.Error(w, code, err.Error())
		return
	}
	if !body.DryRun {
		a.Runtime.MarkRetentionRun(time.Now().UTC(), summary.Totals)
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            "audit_retention_run_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "retention",
		AggregateID:   strings.TrimSpace(body.TenantID),
		EventType:     "retention.run_completed",
		PayloadJSON:   mustJSON(summary),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, summary, actionMeta("retention_run_completed"))
}

func (a *App) handleUpsertRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body domain.RetentionPolicy
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.TenantID) == "" {
		httpx.Error(w, http.StatusBadRequest, "tenant_id required")
		return
	}
	body.TenantID = strings.TrimSpace(body.TenantID)
	if err := a.Retention.UpsertPolicy(r.Context(), body); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            "audit_retention_policy_upsert_" + body.TenantID + "_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		TenantID:      body.TenantID,
		AggregateType: "retention_policy",
		AggregateID:   body.TenantID,
		EventType:     "retention.policy_upserted",
		PayloadJSON:   mustJSON(body),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, body, actionMeta("retention_policy_upserted"))
}

func (a *App) handleDeleteRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		TenantID string `json:"tenant_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.TenantID) == "" {
		httpx.Error(w, http.StatusBadRequest, "tenant_id required")
		return
	}
	body.TenantID = strings.TrimSpace(body.TenantID)
	if err := a.Retention.DeletePolicy(r.Context(), body.TenantID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            "audit_retention_policy_delete_" + body.TenantID + "_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		TenantID:      body.TenantID,
		AggregateType: "retention_policy",
		AggregateID:   body.TenantID,
		EventType:     "retention.policy_deleted",
		PayloadJSON:   mustJSON(body),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, body, actionMeta("retention_policy_deleted"))
}

func (a *App) handleListAwaits(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := buildAwaitListQuery(r, a.Config.DefaultTenantID, page)
	awaits, err := a.Repo.ListAwaits(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountAwaits(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, awaits.Items, awaits.NextCursor, totalCount)
}

func (a *App) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := buildAuditEventListQuery(r, a.Config.DefaultTenantID, page)
	events, err := a.Repo.ListAuditEvents(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountAuditEvents(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, events.Items, events.NextCursor, totalCount)
}

func (a *App) handleListTelegramDenials(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	events, err := a.Repo.ListAuditEvents(r.Context(), domain.AuditEventListQuery{
		CursorPage:    page,
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "telegram.allowlist_denied",
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	totalCount, err := a.Repo.CountAuditEvents(r.Context(), domain.AuditEventListQuery{
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "telegram.allowlist_denied",
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"items": events.Items, "next_cursor": events.NextCursor}, map[string]any{
		"event_type":  "telegram.allowlist_denied",
		"limit":       page.Limit,
		"total_count": totalCount,
	})
}

func (a *App) handleListTelegramFailures(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	eventType, ok := telegramFailureEventType(queryString(r, "failure_type"))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "failure_type must be one of: all, not_found, not_pending, internal")
		return
	}
	query := domain.AuditEventListQuery{
		CursorPage:    page,
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     eventType,
	}
	events, err := a.Repo.ListAuditEvents(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	countQuery := query
	countQuery.CursorPage = domain.CursorPage{}
	totalCount, err := a.Repo.CountAuditEvents(r.Context(), countQuery)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"items": events.Items, "next_cursor": events.NextCursor}, map[string]any{
		"event_type":   eventType,
		"failure_type": telegramFailureTypeLabel(eventType),
		"limit":        page.Limit,
		"total_count":  totalCount,
	})
}

func (a *App) handleTelegramTrustSummary(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 25, 100)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	pendingLimit, err := parseNamedLimit(r, "pending_limit", page.Limit, 100)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	decisionLimit, err := parseNamedLimit(r, "decision_limit", page.Limit, 100)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	failureLimit, err := parseNamedLimit(r, "failure_limit", page.Limit, 100)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	resolutionLimit, err := parseNamedLimit(r, "resolution_limit", page.Limit, 100)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	sectionPage := telegramTrustSectionPage(r, pendingLimit, decisionLimit, failureLimit, resolutionLimit)
	pendingPage, approvedPage, deniedPage, failures, resolutions, failureBreakdown, err := a.loadTelegramTrustSummaryPages(r.Context(), sectionPage)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := a.loadTelegramTrustSummaryCounts(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	recentDecisionsPage := paginateTelegramDecisions(approvedPage.Items, deniedPage.Items, decisionLimit)
	httpx.OK(w, buildTelegramTrustSummaryData(pendingPage, approvedPage, deniedPage, failures, resolutions, failureBreakdown, recentDecisionsPage, decisionLimit, counts), telegramTrustSummaryMeta(page.Limit, pendingLimit, decisionLimit, failureLimit, resolutionLimit))
}

func (a *App) handleTelegramTrustDecisions(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 25, 100)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	approvedPage, deniedPage, err := a.loadTelegramDecisionPages(r.Context(), page)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := paginateTelegramDecisions(approvedPage.Items, deniedPage.Items, page.Limit)
	httpx.OK(w, map[string]any{
		"items":       result.Items,
		"has_more":    result.NextCursor != "",
		"next_cursor": result.NextCursor,
	}, map[string]any{"limit": page.Limit})
}

func (a *App) handleListSurfaceSessions(w http.ResponseWriter, r *http.Request) {
	channelType, ok := requiredQueryParam(w, r, "channel_type")
	if !ok {
		return
	}
	surfaceKey, ok := requiredQueryParam(w, r, "surface_key")
	if !ok {
		return
	}
	ownerUserID, ok := requiredQueryParam(w, r, "owner_user_id")
	if !ok {
		return
	}
	if channelType == "" || surfaceKey == "" || ownerUserID == "" {
		httpx.Error(w, http.StatusBadRequest, "channel_type, surface_key, and owner_user_id required")
		return
	}
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := a.Repo.ListSurfaceSessions(r.Context(), a.Config.DefaultTenantID, channelType, surfaceKey, ownerUserID, page.Limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"items": items}, surfaceSessionListMeta(channelType, surfaceKey, ownerUserID, page.Limit, len(items)))
}

func (a *App) handleSwitchSurfaceSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ChannelType string `json:"channel_type"`
		SurfaceKey  string `json:"surface_key"`
		OwnerUserID string `json:"owner_user_id"`
		AliasOrID   string `json:"alias_or_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !requireFields(w,
		requiredStringField("channel_type", body.ChannelType),
		requiredStringField("surface_key", body.SurfaceKey),
		requiredStringField("owner_user_id", body.OwnerUserID),
		requiredStringField("alias_or_id", body.AliasOrID),
	) {
		httpx.Error(w, http.StatusBadRequest, "channel_type, surface_key, owner_user_id, and alias_or_id required")
		return
	}
	session, err := a.Repo.SwitchActiveSession(r.Context(), a.Config.DefaultTenantID, body.ChannelType, body.SurfaceKey, body.OwnerUserID, body.AliasOrID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), newSurfaceSessionAuditEvent(
		a.Config.DefaultTenantID,
		"audit_surface_switch_"+session.ID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		session.ID,
		body.ChannelType,
		body.SurfaceKey,
		"admin.surface_session_switched",
		body,
	))
	httpx.OK(w, session, actionSurfaceSessionMeta("surface_session_switched", body.ChannelType, body.SurfaceKey, body.OwnerUserID))
}

func (a *App) handleCloseSurfaceSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ChannelType string `json:"channel_type"`
		SurfaceKey  string `json:"surface_key"`
		OwnerUserID string `json:"owner_user_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !requireFields(w,
		requiredStringField("channel_type", body.ChannelType),
		requiredStringField("surface_key", body.SurfaceKey),
		requiredStringField("owner_user_id", body.OwnerUserID),
	) {
		httpx.Error(w, http.StatusBadRequest, "channel_type, surface_key, and owner_user_id required")
		return
	}
	session, err := a.Repo.CloseActiveSession(r.Context(), a.Config.DefaultTenantID, body.ChannelType, body.SurfaceKey, body.OwnerUserID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), newSurfaceSessionAuditEvent(
		a.Config.DefaultTenantID,
		"audit_surface_close_"+session.ID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		session.ID,
		body.ChannelType,
		body.SurfaceKey,
		"admin.surface_session_closed",
		body,
	))
	httpx.OK(w, session, actionSurfaceSessionMeta("surface_session_closed", body.ChannelType, body.SurfaceKey, body.OwnerUserID))
}

func (a *App) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	runID, ok := requiredQueryParam(w, r, "run_id")
	if !ok {
		return
	}
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := a.Repo.GetRunDetail(r.Context(), runID, page.Limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, detail, detailMeta("run_id", runID, page.Limit))
}

func (a *App) handleListTelegramUsers(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := a.Repo.ListTelegramUserAccessPage(r.Context(), domain.TelegramUserAccessListQuery{
		TenantID:   a.Config.DefaultTenantID,
		CursorPage: page,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	totalCount, err := a.Repo.CountTelegramUserAccess(r.Context(), a.Config.DefaultTenantID, "")
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, items.Items, items.NextCursor, totalCount)
}

func (a *App) handleTelegramUserDetail(w http.ResponseWriter, r *http.Request) {
	telegramUserID, ok := requiredQueryParam(w, r, "telegram_user_id")
	if !ok {
		return
	}
	match, auditItems, err := a.loadTelegramUserAuditBundle(r.Context(), telegramUserID)
	if err != nil {
		a.writeTelegramUserBundleError(w, err)
		return
	}
	httpx.OK(w, map[string]any{"user": match, "audit": auditItems}, telegramUserAuditMeta(telegramUserID, len(auditItems)))
}

func (a *App) handleTelegramUserSummary(w http.ResponseWriter, r *http.Request) {
	telegramUserID, ok := requiredQueryParam(w, r, "telegram_user_id")
	if !ok {
		return
	}
	match, auditItems, err := a.loadTelegramUserAuditBundle(r.Context(), telegramUserID)
	if err != nil {
		a.writeTelegramUserBundleError(w, err)
		return
	}
	summary := buildTelegramUserSummaryData(match, auditItems)
	httpx.OK(w, summary, telegramUserSummaryMeta(telegramUserID, len(summary.RecentEvents)))
}

func (a *App) handleListTelegramRequests(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := a.Repo.ListTelegramUserAccessPage(r.Context(), domain.TelegramUserAccessListQuery{
		TenantID:   a.Config.DefaultTenantID,
		Status:     "pending",
		CursorPage: page,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	totalCount, err := a.Repo.CountTelegramUserAccess(r.Context(), a.Config.DefaultTenantID, "pending")
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, items.Items, items.NextCursor, totalCount)
}

func (a *App) handleUpsertTelegramUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		TelegramUserID string `json:"telegram_user_id"`
		DisplayName    string `json:"display_name"`
		Allowed        bool   `json:"allowed"`
		AddedBy        string `json:"added_by"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !requireFields(w, requiredStringField("telegram_user_id", body.TelegramUserID)) {
		httpx.Error(w, http.StatusBadRequest, "telegram_user_id required")
		return
	}
	entry := domain.TelegramUserAccess{
		TenantID:       a.Config.DefaultTenantID,
		TelegramUserID: body.TelegramUserID,
		DisplayName:    body.DisplayName,
		Allowed:        body.Allowed,
		AddedBy:        body.AddedBy,
		UpdatedAt:      time.Now().UTC(),
	}
	if err := a.Repo.UpsertTelegramUserAccess(r.Context(), entry); err != nil {
		_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
			a.Config.DefaultTenantID,
			"audit_telegram_user_upsert_failed_"+body.TelegramUserID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
			body.TelegramUserID,
			"admin.telegram_user_upsert_failed",
			map[string]any{
				"telegram_user_id": body.TelegramUserID,
				"display_name":     body.DisplayName,
				"allowed":          body.Allowed,
				"added_by":         body.AddedBy,
			},
		))
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
		a.Config.DefaultTenantID,
		"audit_telegram_user_upsert_"+body.TelegramUserID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		body.TelegramUserID,
		"admin.telegram_user_upserted",
		entry,
	))
	httpx.OK(w, entry, actionMeta("telegram_user_upserted"))
}

func (a *App) handleDeleteTelegramUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		TelegramUserID string `json:"telegram_user_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !requireFields(w, requiredStringField("telegram_user_id", body.TelegramUserID)) {
		httpx.Error(w, http.StatusBadRequest, "telegram_user_id required")
		return
	}
	if err := a.Repo.DeleteTelegramUserAccess(r.Context(), a.Config.DefaultTenantID, body.TelegramUserID); err != nil {
		_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
			a.Config.DefaultTenantID,
			"audit_telegram_user_delete_failed_"+body.TelegramUserID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
			body.TelegramUserID,
			"admin.telegram_user_delete_failed",
			body,
		))
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
		a.Config.DefaultTenantID,
		"audit_telegram_user_delete_"+body.TelegramUserID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		body.TelegramUserID,
		"admin.telegram_user_deleted",
		body,
	))
	httpx.OK(w, map[string]any{"telegram_user_id": body.TelegramUserID}, actionMeta("telegram_user_deleted"))
}

func (a *App) handleResolveTelegramRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		TelegramUserID string `json:"telegram_user_id"`
		Status         string `json:"status"`
		AddedBy        string `json:"added_by"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !requireFields(w, requiredStringField("telegram_user_id", body.TelegramUserID)) || (body.Status != "approved" && body.Status != "denied") {
		httpx.Error(w, http.StatusBadRequest, "telegram_user_id and status=approved|denied required")
		return
	}
	entry, err := a.Repo.ResolveTelegramAccessRequest(r.Context(), a.Config.DefaultTenantID, body.TelegramUserID, body.Status, body.AddedBy)
	if err != nil {
		errorCode := "internal"
		switch {
		case errors.Is(err, domain.ErrTelegramAccessRequestNotFound):
			errorCode = "not_found"
			httpx.Error(w, http.StatusNotFound, "telegram request not found")
		case errors.Is(err, domain.ErrTelegramAccessRequestNotPending):
			errorCode = "not_pending"
			httpx.Error(w, http.StatusConflict, "telegram request is not pending")
		default:
			httpx.Error(w, http.StatusInternalServerError, err.Error())
		}
		_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
			a.Config.DefaultTenantID,
			"audit_telegram_request_resolve_failed_"+body.TelegramUserID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
			body.TelegramUserID,
			"admin.telegram_request_resolve_failed",
			map[string]any{
				"telegram_user_id": body.TelegramUserID,
				"status":           body.Status,
				"added_by":         body.AddedBy,
				"error_code":       errorCode,
			},
		))
		specificFailureType := "admin.telegram_request_resolve_internal_failed"
		switch errorCode {
		case "not_found":
			specificFailureType = "admin.telegram_request_resolve_not_found"
		case "not_pending":
			specificFailureType = "admin.telegram_request_resolve_not_pending"
		}
		_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
			a.Config.DefaultTenantID,
			"audit_telegram_request_resolve_failed_"+errorCode+"_"+body.TelegramUserID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
			body.TelegramUserID,
			specificFailureType,
			map[string]any{
				"telegram_user_id": body.TelegramUserID,
				"status":           body.Status,
				"added_by":         body.AddedBy,
				"error_code":       errorCode,
			},
		))
		return
	}
	_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
		a.Config.DefaultTenantID,
		"audit_telegram_request_resolve_"+body.TelegramUserID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		body.TelegramUserID,
		"admin.telegram_request_resolved",
		entry,
	))
	specificEventType := "admin.telegram_request_denied"
	if entry.Status == "approved" {
		specificEventType = "admin.telegram_request_approved"
	}
	_ = a.Repo.Audit(r.Context(), newTelegramUserAuditEvent(
		a.Config.DefaultTenantID,
		"audit_telegram_request_"+entry.Status+"_"+body.TelegramUserID+"_"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		body.TelegramUserID,
		specificEventType,
		entry,
	))
	text := "Your Telegram access request has been denied."
	if entry.Status == "approved" {
		text = "Your Telegram access request has been approved. You can now use the bot."
	}
	if noticeSession, err := a.Repo.EnsureNotificationSession(r.Context(), a.Config.DefaultTenantID, "telegram", entry.TelegramUserID, entry.TelegramUserID); err == nil {
		_ = a.Repo.EnqueueDelivery(r.Context(), telegramNoticeDelivery(
			a.Config.DefaultTenantID,
			noticeSession.ID,
			entry.TelegramUserID,
			"delivery_telegram_request_resolve_"+body.TelegramUserID,
			"logical_telegram_request_resolve_"+body.TelegramUserID,
			text,
		))
	}
	httpx.OK(w, entry, actionMeta("telegram_request_resolved"))
}

func (a *App) handleAwaitDetail(w http.ResponseWriter, r *http.Request) {
	awaitID, ok := requiredQueryParam(w, r, "await_id")
	if !ok {
		return
	}
	page, err := parsePage(r, 50, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := a.Repo.GetAwaitDetail(r.Context(), awaitID, page.Limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, detail, detailMeta("await_id", awaitID, page.Limit))
}

func (a *App) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !requireFields(w, requiredStringField("run_id", body.RunID)) {
		httpx.Error(w, http.StatusBadRequest, "run_id required")
		return
	}
	if err := a.Repo.ForceCancelRun(r.Context(), body.RunID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"run_id": body.RunID, "status": "canceled"}, actionMeta("run_canceled"))
}

func (a *App) handleRetryDelivery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		DeliveryID string `json:"delivery_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !requireFields(w, requiredStringField("delivery_id", body.DeliveryID)) {
		httpx.Error(w, http.StatusBadRequest, "delivery_id required")
		return
	}
	if err := a.Repo.RetryDelivery(r.Context(), body.DeliveryID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"delivery_id": body.DeliveryID, "status": "queued"}, actionMeta("delivery_retry_queued"))
}

func parsePage(r *http.Request, defaultLimit, maxLimit int) (domain.CursorPage, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return domain.CursorPage{Limit: defaultLimit, After: r.URL.Query().Get("after")}, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return domain.CursorPage{}, httpError("invalid limit")
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return domain.CursorPage{Limit: limit, After: r.URL.Query().Get("after")}, nil
}

func parseNamedLimit(r *http.Request, key string, defaultLimit, maxLimit int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return defaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, httpError("invalid " + key)
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit, nil
}

type httpError string

func (e httpError) Error() string {
	return string(e)
}

type requiredField struct {
	name  string
	value string
}

func requiredStringField(name, value string) requiredField {
	return requiredField{name: name, value: value}
}

func requireFields(_ http.ResponseWriter, fields ...requiredField) bool {
	for _, field := range fields {
		if field.value == "" {
			return false
		}
	}
	return true
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func queryString(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

func queryBool(r *http.Request, key string) bool {
	return queryString(r, key) == "true"
}

func acpRefresh(r *http.Request) bool {
	return queryBool(r, "refresh")
}

func acpAgentName(r *http.Request, fallback string) string {
	if agentName := queryString(r, "agent_name"); agentName != "" {
		return agentName
	}
	return fallback
}

func acpListMeta(ctx context.Context, catalog *services.AgentCatalog, repo ports.Repository, tenantID string, refresh bool, count int) map[string]any {
	meta := map[string]any{
		"refresh": refresh,
		"count":   count,
	}
	if catalog != nil {
		status := catalog.Status()
		meta["catalog"] = status
	}
	if blocks, err := acpBridgeBlocksMeta(ctx, repo, tenantID, 5); err == nil {
		meta["bridge_blocks"] = blocks
	}
	return meta
}

func acpCompatibleListMeta(ctx context.Context, catalog *services.AgentCatalog, repo ports.Repository, tenantID string, refresh bool, compat []domain.AgentCompatibility) map[string]any {
	meta := acpListMeta(ctx, catalog, repo, tenantID, refresh, len(compat))
	meta["compatibility"] = acpCompatibilitySummary(compat)
	return meta
}

func acpValidateMeta(ctx context.Context, catalog *services.AgentCatalog, repo ports.Repository, tenantID, agentName string, refresh bool, compat domain.AgentCompatibility) map[string]any {
	meta := map[string]any{
		"agent_name": agentName,
		"refresh":    refresh,
		"compatibility": map[string]any{
			"validation_mode": compat.ValidationMode,
			"warning_count":   len(compat.Warnings),
			"degraded":        len(compat.Warnings) > 0,
			"reason_count":    len(compat.Reasons),
		},
	}
	if catalog != nil {
		status := catalog.Status()
		meta["catalog"] = status
	}
	if blocks, err := acpBridgeBlocksMeta(ctx, repo, tenantID, 5); err == nil {
		meta["bridge_blocks"] = blocks
	}
	return meta
}

func acpBridgeBlocksMeta(ctx context.Context, repo ports.Repository, tenantID string, limit int) (map[string]any, error) {
	total, events, err := acpBridgeBlockSnapshot(ctx, repo, tenantID, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"total_count":   total,
		"recent_events": events,
	}, nil
}

func acpAdminSummaryData(ctx context.Context, catalog *services.AgentCatalog, repo ports.Repository, tenantID, defaultAgentName string, agents []domain.AgentManifest, compat []domain.AgentCompatibility) (map[string]any, error) {
	summary := map[string]any{
		"agent_count":   len(agents),
		"agents":        agents,
		"compatibility": acpCompatibilitySummary(compat),
		"compatible":    compat,
		"default_agent": defaultAgentName,
	}
	if catalog != nil {
		summary["catalog"] = catalog.Status()
	}
	if defaultAgentName != "" {
		for key, value := range defaultAgentSummaryMap(defaultAgentName, services.ValidateAgentCompatibility(agents, defaultAgentName)) {
			summary[key] = value
		}
	}
	blocks, err := acpBridgeBlocksMeta(ctx, repo, tenantID, 5)
	if err != nil {
		return nil, err
	}
	summary["bridge_blocks"] = blocks
	return summary, nil
}

func acpCompactSummary(ctx context.Context, catalog *services.AgentCatalog, repo ports.Repository, tenantID, defaultAgentName string) (map[string]any, error) {
	if catalog == nil {
		return nil, nil
	}
	agents, err := catalog.List(ctx, false)
	if err != nil {
		return nil, err
	}
	compat, compatibleCount := acpCompatibilitySnapshot(agents)
	summary := map[string]any{
		"agent_count":        len(agents),
		"compatible_count":   compatibleCount,
		"incompatible_count": len(agents) - compatibleCount,
		"compatibility":      acpCompatibilitySummary(compat),
	}
	if defaultAgentName != "" {
		for key, value := range defaultAgentSummaryMap(defaultAgentName, services.ValidateAgentCompatibility(agents, defaultAgentName)) {
			summary[key] = value
		}
	}
	if status := catalog.Status(); status.CachedAgentCount > 0 || status.CacheValid || status.LastFetchError != "" || !status.LastFetchedAt.IsZero() || !status.ExpiresAt.IsZero() || status.LastRefresh {
		summary["catalog"] = status
	}
	if blocks, _, err := acpBridgeBlockSnapshot(ctx, repo, tenantID, 0); err == nil {
		summary["bridge_block_count"] = blocks
	} else {
		return nil, err
	}
	return summary, nil
}

func acpCompatibilitySnapshot(agents []domain.AgentManifest) ([]domain.AgentCompatibility, int) {
	compat := make([]domain.AgentCompatibility, 0, len(agents))
	compatibleCount := 0
	for _, agent := range agents {
		item := services.ValidateAgentCompatibility(agents, agent.Name)
		if item.Compatible {
			compatibleCount++
		}
		compat = append(compat, item)
	}
	return compat, compatibleCount
}

func acpBridgeBlockSnapshot(ctx context.Context, repo ports.Repository, tenantID string, limit int) (int, []domain.AuditEvent, error) {
	if repo == nil || tenantID == "" {
		return 0, nil, nil
	}
	total, err := repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:  tenantID,
		EventType: "worker.await_blocked_opencode_bridge",
	})
	if err != nil {
		return 0, nil, err
	}
	if limit <= 0 {
		return total, nil, nil
	}
	events, err := acpRecentBridgeBlocks(ctx, repo, tenantID, limit)
	if err != nil {
		return 0, nil, err
	}
	return total, events, nil
}

func defaultAgentSummaryMap(agentName string, compat domain.AgentCompatibility) map[string]any {
	return map[string]any{
		"default_agent":               agentName,
		"default_validation":          compat,
		"default_agent_ready":         compat.Compatible,
		"default_agent_reason_count":  len(compat.Reasons),
		"default_agent_warning_count": len(compat.Warnings),
	}
}

func acpRecentBridgeBlocks(ctx context.Context, repo ports.Repository, tenantID string, limit int) ([]domain.AuditEvent, error) {
	if repo == nil || tenantID == "" || limit <= 0 {
		return nil, nil
	}
	page, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
		CursorPage: domain.CursorPage{Limit: limit},
		TenantID:   tenantID,
		EventType:  "worker.await_blocked_opencode_bridge",
	})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func acpCompatibilitySummary(items []domain.AgentCompatibility) map[string]any {
	degraded := 0
	bridge := 0
	totalWarnings := 0
	for _, item := range items {
		if item.ValidationMode == "opencode_bridge" {
			bridge++
		}
		if len(item.Warnings) > 0 {
			degraded++
			totalWarnings += len(item.Warnings)
		}
	}
	return map[string]any{
		"bridge_compatible_count": bridge,
		"degraded_count":          degraded,
		"warning_count":           totalWarnings,
	}
}

func healthMeta(service string, catalog *services.AgentCatalog, runtime *RuntimeState, defaultAgentName string) map[string]any {
	meta := map[string]any{"service": service}
	if catalog != nil {
		meta["catalog"] = catalog.Status()
		if defaultAgentName != "" {
			if compat, ok := catalog.CachedValidate(defaultAgentName); ok {
				meta["default_agent"] = defaultAgentProbeMeta(defaultAgentName, compat)
			}
		}
		if status, ok := acpRuntimeStatus(catalog.Bridge); ok {
			meta["acp_runtime"] = status
		}
	}
	if runtime != nil {
		meta["runtime"] = runtime.Status()
	}
	return meta
}

func acpRuntimeStatus(bridge ports.ACPBridge) (acp.StdioRuntimeStatus, bool) {
	if bridge == nil {
		return acp.StdioRuntimeStatus{}, false
	}
	provider, ok := bridge.(acpRuntimeStatusProvider)
	if !ok {
		return acp.StdioRuntimeStatus{}, false
	}
	return provider.RuntimeStatus(), true
}

func defaultAgentProbeMeta(agentName string, compat domain.AgentCompatibility) map[string]any {
	return map[string]any{
		"name":            agentName,
		"ready":           compat.Compatible,
		"compatible":      compat.Compatible,
		"validation_mode": compat.ValidationMode,
		"reason_count":    len(compat.Reasons),
		"warning_count":   len(compat.Warnings),
	}
}

func healthStatus(catalog *services.AgentCatalog, runtime *RuntimeState, defaultAgentName string, workerInterval, reconcileInterval time.Duration) string {
	if catalog == nil {
		if runtime == nil {
			return "ok"
		}
		return runtimeHealthStatus(runtime, workerInterval, reconcileInterval)
	}
	status := catalog.Status()
	if status.LastFetchError != "" {
		return "degraded"
	}
	if !status.CacheValid && !status.LastFetchedAt.IsZero() {
		return "degraded"
	}
	if defaultAgentName != "" {
		if compat, ok := catalog.CachedValidate(defaultAgentName); ok && !compat.Compatible {
			return "degraded"
		}
	}
	if status, ok := acpRuntimeStatus(catalog.Bridge); ok && acpRuntimeUnhealthy(status) {
		return "degraded"
	}
	return runtimeHealthStatus(runtime, workerInterval, reconcileInterval)
}

func readinessStatus(catalog *services.AgentCatalog, runtime *RuntimeState, defaultAgentName string, workerInterval, reconcileInterval time.Duration) string {
	if catalog == nil {
		if runtime == nil {
			return "ready"
		}
		return runtimeReadinessStatus(runtime, workerInterval, reconcileInterval)
	}
	status := catalog.Status()
	if status.LastFetchError != "" {
		return "not_ready"
	}
	if !status.CacheValid && !status.LastFetchedAt.IsZero() {
		return "not_ready"
	}
	if defaultAgentName != "" {
		if compat, ok := catalog.CachedValidate(defaultAgentName); ok && !compat.Compatible {
			return "not_ready"
		}
	}
	if status, ok := acpRuntimeStatus(catalog.Bridge); ok && acpRuntimeUnhealthy(status) {
		return "not_ready"
	}
	return runtimeReadinessStatus(runtime, workerInterval, reconcileInterval)
}

func acpRuntimeUnhealthy(status acp.StdioRuntimeStatus) bool {
	if status.Implementation == "" || status.StartedAt.IsZero() {
		return false
	}
	if status.LastError != "" {
		return true
	}
	return !status.Running || !status.Initialized
}

func runtimeHealthStatus(runtime *RuntimeState, workerInterval, reconcileInterval time.Duration) string {
	if runtime == nil {
		return "ok"
	}
	status := runtime.Status()
	if status.LastWorkerError != "" || status.LastReconcileError != "" {
		return "degraded"
	}
	if runtimeStale(status.LastWorkerRunAt, workerInterval) || runtimeStale(status.LastReconcileRunAt, reconcileInterval) {
		return "degraded"
	}
	return "ok"
}

func runtimeReadinessStatus(runtime *RuntimeState, workerInterval, reconcileInterval time.Duration) string {
	if runtime == nil {
		return "ready"
	}
	status := runtime.Status()
	if status.LastWorkerError != "" || status.LastReconcileError != "" {
		return "not_ready"
	}
	if runtimeStale(status.LastWorkerRunAt, workerInterval) || runtimeStale(status.LastReconcileRunAt, reconcileInterval) {
		return "not_ready"
	}
	return "ready"
}

func runtimeStale(last time.Time, interval time.Duration) bool {
	if interval <= 0 || last.IsZero() {
		return false
	}
	return time.Since(last) > interval*3
}

func writeMetrics(w http.ResponseWriter, ctx context.Context, service, tenantID string, repo ports.Repository, catalog *services.AgentCatalog, runtime *RuntimeState, defaultAgentName string, workerInterval, reconcileInterval time.Duration) {
	health := healthStatus(catalog, runtime, defaultAgentName, workerInterval, reconcileInterval)
	readiness := readinessStatus(catalog, runtime, defaultAgentName, workerInterval, reconcileInterval)
	if runtime != nil {
		runtime.RecordProbeStatus("health", health, time.Now().UTC())
		runtime.RecordProbeStatus("readiness", readiness, time.Now().UTC())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "nexus_health_status{service=%q} %d\n", service, boolMetric(health == "ok"))
	fmt.Fprintf(&b, "nexus_readiness_status{service=%q} %d\n", service, boolMetric(readiness == "ready"))
	if catalog != nil {
		status := catalog.Status()
		fmt.Fprintf(&b, "nexus_acp_catalog_cache_valid{service=%q} %d\n", service, boolMetric(status.CacheValid))
		fmt.Fprintf(&b, "nexus_acp_catalog_cached_agents{service=%q} %d\n", service, status.CachedAgentCount)
		fmt.Fprintf(&b, "nexus_acp_catalog_last_fetch_error{service=%q} %d\n", service, boolMetric(status.LastFetchError != ""))
		if !status.LastFetchedAt.IsZero() {
			fmt.Fprintf(&b, "nexus_acp_catalog_last_fetched_unix{service=%q} %d\n", service, status.LastFetchedAt.Unix())
		}
		if defaultAgentName != "" {
			if compat, ok := catalog.CachedValidate(defaultAgentName); ok {
				fmt.Fprintf(&b, "nexus_acp_default_agent_compatible{service=%q} %d\n", service, boolMetric(compat.Compatible))
				fmt.Fprintf(&b, "nexus_acp_default_agent_ready{service=%q} %d\n", service, boolMetric(compat.Compatible))
				fmt.Fprintf(&b, "nexus_acp_default_agent_reason_count{service=%q} %d\n", service, len(compat.Reasons))
				fmt.Fprintf(&b, "nexus_acp_default_agent_warning_count{service=%q} %d\n", service, len(compat.Warnings))
			}
		}
		if status, ok := acpRuntimeStatus(catalog.Bridge); ok {
			fmt.Fprintf(&b, "nexus_acp_runtime_running{service=%q} %d\n", service, boolMetric(status.Running))
			fmt.Fprintf(&b, "nexus_acp_runtime_initialized{service=%q} %d\n", service, boolMetric(status.Initialized))
			fmt.Fprintf(&b, "nexus_acp_runtime_error{service=%q} %d\n", service, boolMetric(status.LastError != ""))
			fmt.Fprintf(&b, "nexus_acp_runtime_permission_requests{service=%q} %d\n", service, status.CallbackCounts.PermissionRequests)
			fmt.Fprintf(&b, "nexus_acp_runtime_fs_read_text_file{service=%q} %d\n", service, status.CallbackCounts.FSReadTextFile)
			fmt.Fprintf(&b, "nexus_acp_runtime_fs_write_text_file{service=%q} %d\n", service, status.CallbackCounts.FSWriteTextFile)
			fmt.Fprintf(&b, "nexus_acp_runtime_terminal_create{service=%q} %d\n", service, status.CallbackCounts.TerminalCreate)
			fmt.Fprintf(&b, "nexus_acp_runtime_terminal_output{service=%q} %d\n", service, status.CallbackCounts.TerminalOutput)
			fmt.Fprintf(&b, "nexus_acp_runtime_terminal_wait{service=%q} %d\n", service, status.CallbackCounts.TerminalWait)
			fmt.Fprintf(&b, "nexus_acp_runtime_terminal_kill{service=%q} %d\n", service, status.CallbackCounts.TerminalKill)
			fmt.Fprintf(&b, "nexus_acp_runtime_terminal_release{service=%q} %d\n", service, status.CallbackCounts.TerminalRelease)
			if !status.StartedAt.IsZero() {
				fmt.Fprintf(&b, "nexus_acp_runtime_started_unix{service=%q} %d\n", service, status.StartedAt.Unix())
			}
		}
	}
	if acp, err := acpCompactSummary(ctx, catalog, repo, tenantID, ""); err == nil && acp != nil {
		if count, ok := acp["compatible_count"].(int); ok {
			fmt.Fprintf(&b, "nexus_acp_compatible_agents{service=%q} %d\n", service, count)
		}
		if count, ok := acp["incompatible_count"].(int); ok {
			fmt.Fprintf(&b, "nexus_acp_incompatible_agents{service=%q} %d\n", service, count)
		}
		if count, ok := acp["bridge_block_count"].(int); ok {
			fmt.Fprintf(&b, "nexus_acp_bridge_block_count{service=%q,tenant=%q} %d\n", service, tenantID, count)
		}
		if summary, ok := acp["compatibility"].(map[string]any); ok {
			if count, ok := summary["bridge_compatible_count"].(int); ok {
				fmt.Fprintf(&b, "nexus_acp_bridge_compatible_agents{service=%q} %d\n", service, count)
			}
			if count, ok := summary["degraded_count"].(int); ok {
				fmt.Fprintf(&b, "nexus_acp_degraded_agents{service=%q} %d\n", service, count)
			}
			if count, ok := summary["warning_count"].(int); ok {
				fmt.Fprintf(&b, "nexus_acp_warning_count{service=%q} %d\n", service, count)
			}
		}
	}
	if runtime != nil {
		status := runtime.Status()
		fmt.Fprintf(&b, "nexus_worker_error{service=%q} %d\n", service, boolMetric(status.LastWorkerError != ""))
		fmt.Fprintf(&b, "nexus_reconciler_error{service=%q} %d\n", service, boolMetric(status.LastReconcileError != ""))
		fmt.Fprintf(&b, "nexus_retention_error{service=%q} %d\n", service, boolMetric(status.LastRetentionError != ""))
		fmt.Fprintf(&b, "nexus_probe_transitions_total{service=%q} %d\n", service, len(status.RecentTransitions))
		fmt.Fprintf(&b, "nexus_outbox_requeues_total{service=%q} %d\n", service, status.OutboxRequeueCount)
		fmt.Fprintf(&b, "nexus_queue_repairs_recovered_total{service=%q} %d\n", service, status.QueueRepairRecoveredCount)
		fmt.Fprintf(&b, "nexus_queue_repairs_requeued_total{service=%q} %d\n", service, status.QueueRepairRequeuedCount)
		fmt.Fprintf(&b, "nexus_run_refreshes_total{service=%q} %d\n", service, status.RunRefreshCount)
		fmt.Fprintf(&b, "nexus_await_expiries_total{service=%q} %d\n", service, status.AwaitExpiryCount)
		fmt.Fprintf(&b, "nexus_delivery_retries_total{service=%q} %d\n", service, status.DeliveryRetryCount)
		fmt.Fprintf(&b, "nexus_retention_payload_redactions_total{service=%q} %d\n", service, status.RetentionPayloadCount)
		fmt.Fprintf(&b, "nexus_retention_artifact_blobs_deleted_total{service=%q} %d\n", service, status.RetentionArtifactCount)
		fmt.Fprintf(&b, "nexus_retention_audit_rows_deleted_total{service=%q} %d\n", service, status.RetentionAuditCount)
		fmt.Fprintf(&b, "nexus_retention_sessions_deleted_total{service=%q} %d\n", service, status.RetentionSessionCount)
		fmt.Fprintf(&b, "nexus_retention_history_rows_deleted_total{service=%q} %d\n", service, status.RetentionHistoryRowCount)
		if !status.LastWorkerRunAt.IsZero() {
			fmt.Fprintf(&b, "nexus_worker_last_run_unix{service=%q} %d\n", service, status.LastWorkerRunAt.Unix())
		}
		if !status.LastReconcileRunAt.IsZero() {
			fmt.Fprintf(&b, "nexus_reconciler_last_run_unix{service=%q} %d\n", service, status.LastReconcileRunAt.Unix())
		}
		if !status.LastRetentionRunAt.IsZero() {
			fmt.Fprintf(&b, "nexus_retention_last_run_unix{service=%q} %d\n", service, status.LastRetentionRunAt.Unix())
		}
	}
	repoError := false
	persisted, persistedErr := persistentLifecycleCounts(ctx, repo, tenantID)
	if persistedErr != nil {
		repoError = true
	}
	if repo != nil && tenantID != "" {
		queuedDeliveries, err := repo.CountDeliveries(ctx, domain.DeliveryListQuery{TenantID: tenantID, Status: "queued"})
		if err != nil {
			repoError = true
		} else {
			fmt.Fprintf(&b, "nexus_queued_deliveries{service=%q,tenant=%q} %d\n", service, tenantID, queuedDeliveries)
		}
		sendingDeliveries, err := repo.CountDeliveries(ctx, domain.DeliveryListQuery{TenantID: tenantID, Status: "sending"})
		if err != nil {
			repoError = true
		} else {
			fmt.Fprintf(&b, "nexus_sending_deliveries{service=%q,tenant=%q} %d\n", service, tenantID, sendingDeliveries)
		}
		pendingAwaits, err := repo.CountAwaits(ctx, domain.AwaitListQuery{TenantID: tenantID, Status: "pending"})
		if err != nil {
			repoError = true
		} else {
			fmt.Fprintf(&b, "nexus_pending_awaits{service=%q,tenant=%q} %d\n", service, tenantID, pendingAwaits)
		}
		activeRuns := 0
		for _, status := range []string{"starting", "running", "awaiting"} {
			count, err := repo.CountRuns(ctx, domain.RunListQuery{TenantID: tenantID, Status: status})
			if err != nil {
				repoError = true
				continue
			}
			activeRuns += count
		}
		fmt.Fprintf(&b, "nexus_active_runs{service=%q,tenant=%q} %d\n", service, tenantID, activeRuns)
	}
	for key, value := range persisted {
		fmt.Fprintf(&b, "nexus_%s_total{service=%q,tenant=%q} %d\n", key, service, tenantID, value)
	}
	fmt.Fprintf(&b, "nexus_metrics_repo_error{service=%q} %d\n", service, boolMetric(repoError))
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

func persistentLifecycleCounts(ctx context.Context, repo ports.Repository, tenantID string) (map[string]int, error) {
	counts := map[string]int{
		"persisted_outbox_requeues":                       0,
		"persisted_queue_repairs_recovered":               0,
		"persisted_queue_repairs_requeued":                0,
		"persisted_run_refreshes":                         0,
		"persisted_await_expiries":                        0,
		"persisted_delivery_retries":                      0,
		"persisted_bridge_await_blocks":                   0,
		"persisted_operator_run_cancels":                  0,
		"persisted_operator_surface_switches":             0,
		"persisted_operator_surface_closures":             0,
		"persisted_operator_telegram_approvals":           0,
		"persisted_operator_telegram_denials":             0,
		"persisted_operator_telegram_resolve_not_found":   0,
		"persisted_operator_telegram_resolve_not_pending": 0,
		"persisted_operator_telegram_resolve_internal":    0,
	}
	if repo == nil || tenantID == "" {
		return counts, nil
	}
	eventTypes := map[string]string{
		"persisted_outbox_requeues":                       "reconciler.outbox_requeued",
		"persisted_queue_repairs_recovered":               "reconciler.queue_repair_recovered",
		"persisted_queue_repairs_requeued":                "reconciler.queue_repair_requeued",
		"persisted_run_refreshes":                         "reconciler.run_refreshed",
		"persisted_await_expiries":                        "reconciler.await_expired",
		"persisted_delivery_retries":                      "delivery.retry_requested",
		"persisted_bridge_await_blocks":                   "worker.await_blocked_opencode_bridge",
		"persisted_operator_run_cancels":                  "admin.run_canceled",
		"persisted_operator_surface_switches":             "admin.surface_session_switched",
		"persisted_operator_surface_closures":             "admin.surface_session_closed",
		"persisted_operator_telegram_resolve_not_found":   "admin.telegram_request_resolve_not_found",
		"persisted_operator_telegram_resolve_not_pending": "admin.telegram_request_resolve_not_pending",
		"persisted_operator_telegram_resolve_internal":    "admin.telegram_request_resolve_internal_failed",
	}
	for key, eventType := range eventTypes {
		count, err := repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:  tenantID,
			EventType: eventType,
		})
		if err != nil {
			return counts, err
		}
		counts[key] = count
	}
	approvalCount, err := repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:      tenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_approved",
	})
	if err != nil {
		return counts, err
	}
	denialCount, err := repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:      tenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_denied",
	})
	if err != nil {
		return counts, err
	}
	counts["persisted_operator_telegram_approvals"] = approvalCount
	counts["persisted_operator_telegram_denials"] = denialCount
	return counts, nil
}

func boolMetric(v bool) int {
	if v {
		return 1
	}
	return 0
}

func buildSessionListQuery(r *http.Request, tenantID string, page domain.CursorPage) domain.SessionListQuery {
	return domain.SessionListQuery{
		CursorPage:  page,
		TenantID:    tenantID,
		State:       queryString(r, "state"),
		ChannelType: queryString(r, "channel_type"),
		OwnerUserID: queryString(r, "owner_user_id"),
	}
}

func buildRunListQuery(r *http.Request, tenantID string, page domain.CursorPage) domain.RunListQuery {
	return domain.RunListQuery{
		CursorPage: page,
		TenantID:   tenantID,
		Status:     queryString(r, "status"),
		SessionID:  queryString(r, "session_id"),
	}
}

func buildMessageListQuery(r *http.Request, tenantID string, page domain.CursorPage) domain.MessageListQuery {
	return domain.MessageListQuery{
		CursorPage: page,
		TenantID:   tenantID,
		Type:       queryString(r, "type"),
		Contains:   queryString(r, "contains"),
		SessionID:  queryString(r, "session_id"),
	}
}

func buildArtifactListQuery(r *http.Request, tenantID string, page domain.CursorPage) domain.ArtifactListQuery {
	return domain.ArtifactListQuery{
		CursorPage:   page,
		TenantID:     tenantID,
		MIMEType:     queryString(r, "mime_type"),
		NameContains: queryString(r, "name_contains"),
		SessionID:    queryString(r, "session_id"),
		Direction:    queryString(r, "direction"),
	}
}

func buildDeliveryListQuery(r *http.Request, tenantID string, page domain.CursorPage) domain.DeliveryListQuery {
	return domain.DeliveryListQuery{
		CursorPage:   page,
		TenantID:     tenantID,
		Status:       queryString(r, "status"),
		SessionID:    queryString(r, "session_id"),
		RunID:        queryString(r, "run_id"),
		DeliveryKind: queryString(r, "delivery_kind"),
	}
}

func buildAwaitListQuery(r *http.Request, tenantID string, page domain.CursorPage) domain.AwaitListQuery {
	return domain.AwaitListQuery{
		CursorPage: page,
		TenantID:   tenantID,
		Status:     queryString(r, "status"),
		SessionID:  queryString(r, "session_id"),
		RunID:      queryString(r, "run_id"),
	}
}

func buildAuditEventListQuery(r *http.Request, tenantID string, page domain.CursorPage) domain.AuditEventListQuery {
	return domain.AuditEventListQuery{
		CursorPage:    page,
		TenantID:      tenantID,
		SessionID:     queryString(r, "session_id"),
		RunID:         queryString(r, "run_id"),
		AwaitID:       queryString(r, "await_id"),
		AggregateType: queryString(r, "aggregate_type"),
		AggregateID:   queryString(r, "aggregate_id"),
		EventType:     queryString(r, "event_type"),
	}
}

type telegramTrustSectionPages struct {
	PendingAfter           string
	DecisionAfter          string
	FailureAfter           string
	FailureNotFoundAfter   string
	FailureNotPendingAfter string
	FailureInternalAfter   string
	ResolutionAfter        string
	PendingLimit           int
	DecisionLimit          int
	FailureLimit           int
	ResolutionLimit        int
}

type telegramTrustSummaryCounts struct {
	Pending           int
	Approved          int
	Denied            int
	Failures          int
	FailureNotFound   int
	FailureNotPending int
	FailureInternal   int
	Resolutions       int
}

type telegramUserSummaryData struct {
	User         domain.TelegramUserAccess `json:"user"`
	CurrentState string                    `json:"current_state"`
	LastRequest  *domain.AuditEvent        `json:"last_request"`
	LastDecision *domain.AuditEvent        `json:"last_decision"`
	RecentEvents []domain.AuditEvent       `json:"recent_events"`
}

func telegramTrustSectionPage(r *http.Request, pendingLimit, decisionLimit, failureLimit, resolutionLimit int) telegramTrustSectionPages {
	return telegramTrustSectionPages{
		PendingAfter:           queryString(r, "pending_after"),
		DecisionAfter:          queryString(r, "decision_after"),
		FailureAfter:           queryString(r, "failure_after"),
		FailureNotFoundAfter:   queryString(r, "failure_not_found_after"),
		FailureNotPendingAfter: queryString(r, "failure_not_pending_after"),
		FailureInternalAfter:   queryString(r, "failure_internal_after"),
		ResolutionAfter:        queryString(r, "resolution_after"),
		PendingLimit:           pendingLimit,
		DecisionLimit:          decisionLimit,
		FailureLimit:           failureLimit,
		ResolutionLimit:        resolutionLimit,
	}
}

func (a *App) loadTelegramTrustSummaryPages(ctx context.Context, page telegramTrustSectionPages) (domain.PagedResult[domain.TelegramUserAccess], domain.PagedResult[domain.TelegramUserAccess], domain.PagedResult[domain.TelegramUserAccess], domain.PagedResult[domain.AuditEvent], domain.PagedResult[domain.AuditEvent], map[string]domain.PagedResult[domain.AuditEvent], error) {
	pendingPage, err := a.Repo.ListTelegramUserAccessPage(ctx, domain.TelegramUserAccessListQuery{
		TenantID: a.Config.DefaultTenantID,
		Status:   "pending",
		CursorPage: domain.CursorPage{
			Limit: page.PendingLimit,
			After: page.PendingAfter,
		},
	})
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.AuditEvent]{}, domain.PagedResult[domain.AuditEvent]{}, nil, err
	}
	approvedPage, deniedPage, err := a.loadTelegramDecisionPages(ctx, domain.CursorPage{Limit: page.DecisionLimit, After: page.DecisionAfter})
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.AuditEvent]{}, domain.PagedResult[domain.AuditEvent]{}, nil, err
	}
	failures, err := a.Repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
		CursorPage:    domain.CursorPage{Limit: page.FailureLimit, After: page.FailureAfter},
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_resolve_failed",
	})
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.AuditEvent]{}, domain.PagedResult[domain.AuditEvent]{}, nil, err
	}
	resolutions, err := a.Repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
		CursorPage:    domain.CursorPage{Limit: page.ResolutionLimit, After: page.ResolutionAfter},
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_resolved",
	})
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.AuditEvent]{}, domain.PagedResult[domain.AuditEvent]{}, nil, err
	}
	failureBreakdown, err := a.loadTelegramFailureBreakdownPages(ctx, page)
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.AuditEvent]{}, domain.PagedResult[domain.AuditEvent]{}, nil, err
	}
	return pendingPage, approvedPage, deniedPage, failures, resolutions, failureBreakdown, nil
}

func (a *App) loadTelegramFailureBreakdownPages(ctx context.Context, page telegramTrustSectionPages) (map[string]domain.PagedResult[domain.AuditEvent], error) {
	queries := map[string]domain.CursorPage{
		"not_found":   {Limit: page.FailureLimit, After: page.FailureNotFoundAfter},
		"not_pending": {Limit: page.FailureLimit, After: page.FailureNotPendingAfter},
		"internal":    {Limit: page.FailureLimit, After: page.FailureInternalAfter},
	}
	result := make(map[string]domain.PagedResult[domain.AuditEvent], len(queries))
	for kind, cursor := range queries {
		eventType, _ := telegramFailureEventType(kind)
		items, err := a.Repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			CursorPage:    cursor,
			TenantID:      a.Config.DefaultTenantID,
			AggregateType: "telegram_user",
			EventType:     eventType,
		})
		if err != nil {
			return nil, err
		}
		result[kind] = items
	}
	return result, nil
}

func (a *App) loadTelegramDecisionPages(ctx context.Context, page domain.CursorPage) (domain.PagedResult[domain.TelegramUserAccess], domain.PagedResult[domain.TelegramUserAccess], error) {
	approvedPage, err := a.Repo.ListTelegramUserAccessPage(ctx, domain.TelegramUserAccessListQuery{
		TenantID: a.Config.DefaultTenantID,
		Status:   "approved",
		CursorPage: domain.CursorPage{
			Limit: page.Limit + 1,
			After: page.After,
		},
	})
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, err
	}
	deniedPage, err := a.Repo.ListTelegramUserAccessPage(ctx, domain.TelegramUserAccessListQuery{
		TenantID: a.Config.DefaultTenantID,
		Status:   "denied",
		CursorPage: domain.CursorPage{
			Limit: page.Limit + 1,
			After: page.After,
		},
	})
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, domain.PagedResult[domain.TelegramUserAccess]{}, err
	}
	return approvedPage, deniedPage, nil
}

func (a *App) loadTelegramTrustSummaryCounts(ctx context.Context) (telegramTrustSummaryCounts, error) {
	pending, err := a.Repo.CountTelegramUserAccess(ctx, a.Config.DefaultTenantID, "pending")
	if err != nil {
		return telegramTrustSummaryCounts{}, err
	}
	approved, err := a.Repo.CountTelegramUserAccess(ctx, a.Config.DefaultTenantID, "approved")
	if err != nil {
		return telegramTrustSummaryCounts{}, err
	}
	denied, err := a.Repo.CountTelegramUserAccess(ctx, a.Config.DefaultTenantID, "denied")
	if err != nil {
		return telegramTrustSummaryCounts{}, err
	}
	failures, err := a.Repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_resolve_failed",
	})
	if err != nil {
		return telegramTrustSummaryCounts{}, err
	}
	failureNotFound, err := a.Repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_resolve_not_found",
	})
	if err != nil {
		return telegramTrustSummaryCounts{}, err
	}
	failureNotPending, err := a.Repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_resolve_not_pending",
	})
	if err != nil {
		return telegramTrustSummaryCounts{}, err
	}
	failureInternal, err := a.Repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_resolve_internal_failed",
	})
	if err != nil {
		return telegramTrustSummaryCounts{}, err
	}
	resolutions, err := a.Repo.CountAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		EventType:     "admin.telegram_request_resolved",
	})
	if err != nil {
		return telegramTrustSummaryCounts{}, err
	}
	return telegramTrustSummaryCounts{
		Pending:           pending,
		Approved:          approved,
		Denied:            denied,
		Failures:          failures,
		FailureNotFound:   failureNotFound,
		FailureNotPending: failureNotPending,
		FailureInternal:   failureInternal,
		Resolutions:       resolutions,
	}, nil
}

func buildTelegramTrustSummaryData(pendingPage, approvedPage, deniedPage domain.PagedResult[domain.TelegramUserAccess], failures, resolutions domain.PagedResult[domain.AuditEvent], failureBreakdown map[string]domain.PagedResult[domain.AuditEvent], recentDecisionsPage domain.PagedResult[domain.TelegramUserAccess], decisionLimit int, counts telegramTrustSummaryCounts) map[string]any {
	return map[string]any{
		"counts": map[string]int{
			"pending":             counts.Pending,
			"approved":            counts.Approved,
			"denied":              counts.Denied,
			"failures":            counts.Failures,
			"failure_not_found":   counts.FailureNotFound,
			"failure_not_pending": counts.FailureNotPending,
			"failure_internal":    counts.FailureInternal,
			"resolutions":         counts.Resolutions,
			"decisions":           counts.Approved + counts.Denied,
		},
		"has_more": map[string]bool{
			"pending_requests":    pendingPage.NextCursor != "",
			"recent_approved":     approvedPage.NextCursor != "",
			"recent_denied":       deniedPage.NextCursor != "",
			"recent_decisions":    recentDecisionsPage.NextCursor != "",
			"recent_failures":     failures.NextCursor != "",
			"failure_not_found":   failureBreakdown["not_found"].NextCursor != "",
			"failure_not_pending": failureBreakdown["not_pending"].NextCursor != "",
			"failure_internal":    failureBreakdown["internal"].NextCursor != "",
			"recent_resolutions":  resolutions.NextCursor != "",
		},
		"next_cursors": map[string]string{
			"pending_requests":    pendingPage.NextCursor,
			"recent_approved":     approvedPage.NextCursor,
			"recent_denied":       deniedPage.NextCursor,
			"recent_decisions":    recentDecisionsPage.NextCursor,
			"recent_failures":     failures.NextCursor,
			"failure_not_found":   failureBreakdown["not_found"].NextCursor,
			"failure_not_pending": failureBreakdown["not_pending"].NextCursor,
			"failure_internal":    failureBreakdown["internal"].NextCursor,
			"recent_resolutions":  resolutions.NextCursor,
		},
		"pending_requests": pendingPage.Items,
		"recent_approved":  trimTelegramUserAccessItems(approvedPage.Items, decisionLimit),
		"recent_denied":    trimTelegramUserAccessItems(deniedPage.Items, decisionLimit),
		"recent_decisions": recentDecisionsPage.Items,
		"recent_failures":  failures.Items,
		"recent_failure_breakdown": map[string][]domain.AuditEvent{
			"not_found":   failureBreakdown["not_found"].Items,
			"not_pending": failureBreakdown["not_pending"].Items,
			"internal":    failureBreakdown["internal"].Items,
		},
		"recent_resolutions": resolutions.Items,
	}
}

func (a *App) loadTelegramUserAuditBundle(ctx context.Context, telegramUserID string) (domain.TelegramUserAccess, []domain.AuditEvent, error) {
	match, err := a.Repo.GetTelegramUserAccess(ctx, a.Config.DefaultTenantID, telegramUserID)
	if err != nil {
		return domain.TelegramUserAccess{}, nil, err
	}
	audit, err := a.Repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
		CursorPage:    domain.CursorPage{Limit: 100},
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "telegram_user",
		AggregateID:   telegramUserID,
	})
	if err != nil {
		return domain.TelegramUserAccess{}, nil, err
	}
	return match, audit.Items, nil
}

func (a *App) writeTelegramUserBundleError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		httpx.Error(w, http.StatusNotFound, "telegram user not found")
		return
	}
	httpx.Error(w, http.StatusInternalServerError, err.Error())
}

func buildTelegramUserSummaryData(user domain.TelegramUserAccess, auditItems []domain.AuditEvent) telegramUserSummaryData {
	recent := make([]domain.AuditEvent, 0, len(auditItems))
	var lastRequest *domain.AuditEvent
	var lastDecision *domain.AuditEvent
	for _, item := range auditItems {
		if !isTelegramTrustEvent(item.EventType) {
			continue
		}
		recent = append(recent, item)
		switch item.EventType {
		case "telegram.access_requested":
			if lastRequest == nil {
				copied := item
				lastRequest = &copied
			}
		case "admin.telegram_request_resolved", "telegram.allowlist_denied":
			if lastDecision == nil {
				copied := item
				lastDecision = &copied
			}
		}
	}
	return telegramUserSummaryData{
		User:         user,
		CurrentState: user.Status,
		LastRequest:  lastRequest,
		LastDecision: lastDecision,
		RecentEvents: recent,
	}
}

func requiredQueryParam(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	value := queryString(r, key)
	if value == "" {
		httpx.Error(w, http.StatusBadRequest, key+" required")
		return "", false
	}
	return value, true
}

func (a *App) telegramUserAllowed(ctx context.Context, userID string) bool {
	allowed, err := a.Repo.IsTelegramUserAllowed(ctx, a.Config.DefaultTenantID, userID)
	if err == nil {
		if allowed {
			return true
		}
		if len(a.Config.TelegramAllowedUserIDs) == 0 {
			return false
		}
	}
	for _, allowed := range a.Config.TelegramAllowedUserIDs {
		if allowed == userID {
			return true
		}
	}
	return false
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func webhookActorMeta(evt domain.CanonicalInboundEvent) map[string]any {
	return map[string]any{
		"channel":         evt.Channel,
		"channel_user_id": evt.Sender.ChannelUserID,
		"conversation_id": evt.Conversation.ChannelConversationID,
	}
}

func webhookMeta(evt domain.CanonicalInboundEvent) map[string]any {
	return map[string]any{
		"interaction": evt.Interaction,
		"channel":     evt.Channel,
	}
}

func webhookAwaitMeta(evt domain.CanonicalInboundEvent) map[string]any {
	meta := webhookMeta(evt)
	meta["await_id"] = evt.Metadata.AwaitID
	return meta
}

func webhookResultMeta(evt domain.CanonicalInboundEvent) map[string]any {
	meta := webhookMeta(evt)
	meta["provider_event_id"] = evt.ProviderEventID
	return meta
}

func detailMeta(key, value string, limit int) map[string]any {
	return map[string]any{
		key:     value,
		"limit": limit,
	}
}

func actionMeta(action string) map[string]any {
	return map[string]any{"action": action}
}

func surfaceSessionMeta(channelType, surfaceKey, ownerUserID string) map[string]any {
	return map[string]any{
		"channel_type":  channelType,
		"surface_key":   surfaceKey,
		"owner_user_id": ownerUserID,
	}
}

func surfaceSessionListMeta(channelType, surfaceKey, ownerUserID string, limit, count int) map[string]any {
	meta := surfaceSessionMeta(channelType, surfaceKey, ownerUserID)
	meta["limit"] = limit
	meta["count"] = count
	return meta
}

func actionSurfaceSessionMeta(action, channelType, surfaceKey, ownerUserID string) map[string]any {
	meta := surfaceSessionMeta(channelType, surfaceKey, ownerUserID)
	meta["action"] = action
	return meta
}

func telegramUserAuditMeta(telegramUserID string, auditCount int) map[string]any {
	return map[string]any{
		"telegram_user_id": telegramUserID,
		"audit_count":      auditCount,
	}
}

func telegramUserSummaryMeta(telegramUserID string, recentEventCount int) map[string]any {
	return map[string]any{
		"telegram_user_id":   telegramUserID,
		"recent_event_count": recentEventCount,
	}
}

func telegramTrustSummaryMeta(limit, pendingLimit, decisionLimit, failureLimit, resolutionLimit int) map[string]any {
	return map[string]any{
		"limit":            limit,
		"pending_limit":    pendingLimit,
		"decision_limit":   decisionLimit,
		"failure_limit":    failureLimit,
		"resolution_limit": resolutionLimit,
	}
}

func newTelegramUserAuditEvent(tenantID, id, telegramUserID, eventType string, payload any) domain.AuditEvent {
	return domain.AuditEvent{
		ID:            id,
		TenantID:      tenantID,
		AggregateType: "telegram_user",
		AggregateID:   telegramUserID,
		EventType:     eventType,
		PayloadJSON:   mustJSON(payload),
		CreatedAt:     time.Now().UTC(),
	}
}

func newSurfaceSessionAuditEvent(tenantID, id, sessionID, channelType, surfaceKey, eventType string, payload any) domain.AuditEvent {
	return domain.AuditEvent{
		ID:            id,
		TenantID:      tenantID,
		SessionID:     sessionID,
		AggregateType: "channel_surface_state",
		AggregateID:   channelType + ":" + surfaceKey,
		EventType:     eventType,
		PayloadJSON:   mustJSON(payload),
		CreatedAt:     time.Now().UTC(),
	}
}

func telegramNoticeDelivery(tenantID, sessionID, chatID, deliveryID, logicalMessageID, text string) domain.OutboundDelivery {
	return telegramNoticeDeliveryWithKind(tenantID, sessionID, chatID, deliveryID, logicalMessageID, text, "send")
}

func telegramNoticeDeliveryWithKind(tenantID, sessionID, chatID, deliveryID, logicalMessageID, text, kind string) domain.OutboundDelivery {
	return domain.OutboundDelivery{
		ID:               deliveryID,
		TenantID:         tenantID,
		SessionID:        sessionID,
		ChannelType:      "telegram",
		DeliveryKind:     kind,
		Status:           "queued",
		LogicalMessageID: logicalMessageID,
		PayloadJSON:      mustJSON(map[string]any{"chat_id": chatID, "text": text}),
	}
}

func telegramFailureEventType(kind string) (string, bool) {
	switch kind {
	case "", "all":
		return "admin.telegram_request_resolve_failed", true
	case "not_found":
		return "admin.telegram_request_resolve_not_found", true
	case "not_pending":
		return "admin.telegram_request_resolve_not_pending", true
	case "internal":
		return "admin.telegram_request_resolve_internal_failed", true
	default:
		return "", false
	}
}

func telegramFailureTypeLabel(eventType string) string {
	switch eventType {
	case "admin.telegram_request_resolve_not_found":
		return "not_found"
	case "admin.telegram_request_resolve_not_pending":
		return "not_pending"
	case "admin.telegram_request_resolve_internal_failed":
		return "internal"
	default:
		return "all"
	}
}

func isTelegramTrustEvent(eventType string) bool {
	switch eventType {
	case "telegram.access_requested",
		"telegram.allowlist_denied",
		"admin.telegram_request_resolved",
		"admin.telegram_request_resolve_failed",
		"admin.telegram_request_resolve_not_found",
		"admin.telegram_request_resolve_not_pending",
		"admin.telegram_request_resolve_internal_failed",
		"admin.telegram_request_approved",
		"admin.telegram_request_denied",
		"admin.telegram_user_upserted",
		"admin.telegram_user_upsert_failed",
		"admin.telegram_user_deleted",
		"admin.telegram_user_delete_failed":
		return true
	default:
		return false
	}
}

func mergeTelegramDecisionLists(approved, denied []domain.TelegramUserAccess, limit int) []domain.TelegramUserAccess {
	items := make([]domain.TelegramUserAccess, 0, len(approved)+len(denied))
	items = append(items, approved...)
	items = append(items, denied...)
	slices.SortFunc(items, func(a, b domain.TelegramUserAccess) int {
		at := decisionTime(a)
		bt := decisionTime(b)
		if at.Equal(bt) {
			switch {
			case a.TelegramUserID < b.TelegramUserID:
				return -1
			case a.TelegramUserID > b.TelegramUserID:
				return 1
			default:
				return 0
			}
		}
		if at.After(bt) {
			return -1
		}
		return 1
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func filterAuditEventsByType(items []domain.AuditEvent, eventType string) []domain.AuditEvent {
	filtered := make([]domain.AuditEvent, 0, len(items))
	for _, item := range items {
		if item.EventType == eventType {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func trimTelegramUserAccessItems(items []domain.TelegramUserAccess, limit int) []domain.TelegramUserAccess {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

func paginateTelegramDecisions(approved, denied []domain.TelegramUserAccess, limit int) domain.PagedResult[domain.TelegramUserAccess] {
	merged := mergeTelegramDecisionLists(approved, denied, limit+1)
	result := domain.PagedResult[domain.TelegramUserAccess]{Items: merged}
	if len(merged) <= limit {
		return result
	}
	result.Items = merged[:limit]
	last := result.Items[len(result.Items)-1]
	result.NextCursor = formatLocalCursor(decisionTime(last), last.TelegramUserID)
	return result
}

func formatLocalCursor(ts time.Time, id string) string {
	return ts.UTC().Format(time.RFC3339Nano) + "|" + id
}

func decisionTime(item domain.TelegramUserAccess) time.Time {
	if item.DecidedAt != nil {
		return *item.DecidedAt
	}
	return item.UpdatedAt
}
