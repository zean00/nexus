package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/httpx"
	"nexus/internal/services"
)

type lajuInboundEnqueuer interface {
	EnqueueLajuInbound(ctx context.Context, tenantID, eventID string, payload []byte) error
	EnqueueInboundWebhook(ctx context.Context, tenantID, eventID string, payload []byte) error
}

type lajuChannelContext struct {
	TenantID              string         `json:"tenantId"`
	NexusSessionID        string         `json:"nexusSessionId"`
	ChannelType           string         `json:"channelType"`
	ChannelUserID         string         `json:"channelUserId"`
	SurfaceKey            string         `json:"surfaceKey"`
	ChannelConversationID string         `json:"channelConversationId"`
	ChannelThreadID       string         `json:"channelThreadId"`
	ProviderEventID       string         `json:"providerEventId"`
	IdentityUserID        string         `json:"identityUserId"`
	IdentityLinked        bool           `json:"identityLinked"`
	IdentityAssurance     string         `json:"identityAssurance"`
	AllowedResponderIDs   []string       `json:"allowedResponderIds"`
	AccountKey            string         `json:"accountKey"`
	PolicySnapshot        map[string]any `json:"policySnapshot"`
	Metadata              map[string]any `json:"metadata"`
	LiveStatus            map[string]any `json:"liveStatus"`
}

func (a *App) forwardLajuInbound(ctx context.Context, evt domain.CanonicalInboundEvent, result services.InboundResult) {
	// Skip when no laju instance is configured to receive this tenant's
	// forwards (single-tenant env wiring or multi-tenant registry).
	if _, _, ok := a.inboundTargetFor(ctx, evt.TenantID); !ok {
		return
	}
	body, err := json.Marshal(lajuInboundPayload(evt, result))
	if err != nil {
		slog.WarnContext(ctx, "marshal laju inbound failed", "event_id", evt.EventID, "error", err.Error())
		return
	}
	enqueuer, ok := a.Repo.(lajuInboundEnqueuer)
	if !ok {
		slog.WarnContext(ctx, "laju inbound outbox unavailable", "event_id", evt.EventID)
		return
	}
	if err := enqueuer.EnqueueInboundWebhook(ctx, evt.TenantID, evt.EventID, body); err != nil {
		slog.WarnContext(ctx, "enqueue inbound webhook failed", "event_id", evt.EventID, "error", err.Error())
	}
}

func inboundWebhookURL(cfg config.Config) string {
	if strings.TrimSpace(cfg.InboundWebhookURL) != "" {
		return strings.TrimSpace(cfg.InboundWebhookURL)
	}
	if strings.TrimSpace(cfg.LajuBaseURL) == "" {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.LajuBaseURL), "/") + "/api/integrations/nexus/inbound"
}

func inboundWebhookToken(cfg config.Config) string {
	if strings.TrimSpace(cfg.InboundWebhookBearerToken) != "" {
		return strings.TrimSpace(cfg.InboundWebhookBearerToken)
	}
	return strings.TrimSpace(cfg.LajuBearerToken)
}

func lajuInboundPayload(evt domain.CanonicalInboundEvent, result services.InboundResult) map[string]any {
	sessionID := firstNonEmptyString(result.SessionID, evt.Message.SessionID)
	channelType := firstNonEmptyString(evt.Message.ChannelType, evt.Channel)
	subject := strings.TrimSpace(evt.Message.Text)
	if subject == "" {
		subject = fmt.Sprintf("%s conversation", channelType)
	}
	if len(subject) > 120 {
		subject = strings.TrimSpace(subject[:120])
	}
	customer := firstNonEmptyString(evt.Sender.DisplayName, evt.Sender.ChannelUserID, "Customer")
	chctx := lajuChannelContext{
		TenantID:              evt.TenantID,
		NexusSessionID:        sessionID,
		ChannelType:           channelType,
		ChannelUserID:         evt.Sender.ChannelUserID,
		SurfaceKey:            evt.Conversation.ChannelSurfaceKey,
		ChannelConversationID: evt.Conversation.ChannelConversationID,
		ChannelThreadID:       evt.Conversation.ChannelThreadID,
		ProviderEventID:       firstNonEmptyString(evt.ProviderEventID, evt.EventID),
		IdentityUserID:        evt.Metadata.ActorUserID,
		IdentityLinked:        evt.Sender.IsAuthenticated || evt.Metadata.ActorUserID != "",
		IdentityAssurance:     evt.Sender.IdentityAssurance,
		AllowedResponderIDs:   evt.Sender.AllowedResponderIDs,
		AccountKey:            evt.Metadata.AccountKey,
		PolicySnapshot: map[string]any{
			"artifact_trust":    evt.Metadata.ArtifactTrust,
			"responder_binding": evt.Metadata.ResponderBinding,
		},
		Metadata: map[string]any{
			"source":               "nexus",
			"contract":             "nexus_laju_inbound_v1",
			"event_id":             evt.EventID,
			"provider_event_id":    evt.ProviderEventID,
			"message_id":           evt.Message.MessageID,
			"runtime_agent_id":     result.AgentProfileID,
			"acp_agent_profile_id": result.AgentProfileID,
			"acp_session_id":       sessionID,
			"runtime_mode":         result.AgentMode,
			"agent_mode":           result.AgentMode,
			"response_delivery":    result.ResponseDelivery,
			"interaction":          evt.Interaction,
			"mentions_bot":         evt.Metadata.MentionsBot,
			"command":              evt.Metadata.Command,
			"channel_user_id":      evt.Sender.ChannelUserID,
			"channel_conversation": evt.Conversation.ChannelConversationID,
			"channel_thread":       evt.Conversation.ChannelThreadID,
			"surface_key":          evt.Conversation.ChannelSurfaceKey,
			"account_key":          evt.Metadata.AccountKey,
		},
		LiveStatus: map[string]any{
			"queue_id": result.QueueID,
			"status":   result.Status,
		},
	}
	return map[string]any{
		"channel":               channelType,
		"subject":               subject,
		"status":                "open",
		"customer":              customer,
		"customer_external_ref": evt.Sender.ChannelUserID,
		"body":                  evt.Message.Text,
		"session_id":            sessionID,
		"nexus_session_id":      sessionID,
		"tenantId":              chctx.TenantID,
		"channelType":           chctx.ChannelType,
		"channelUserId":         chctx.ChannelUserID,
		"surfaceKey":            chctx.SurfaceKey,
		"accountKey":            chctx.AccountKey,
		"channelConversationId": chctx.ChannelConversationID,
		"channelThreadId":       chctx.ChannelThreadID,
		"providerEventId":       chctx.ProviderEventID,
		"identityUserId":        chctx.IdentityUserID,
		"identityLinked":        chctx.IdentityLinked,
		"identityAssurance":     chctx.IdentityAssurance,
		"allowedResponderIds":   chctx.AllowedResponderIDs,
		"policySnapshot":        chctx.PolicySnapshot,
		"liveStatus":            chctx.LiveStatus,
		"metadata":              chctx.Metadata,
	}
}

func (a *App) handleLajuContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		httpx.Error(w, http.StatusBadRequest, "session_id is required")
		return
	}
	context, err := a.lajuContextForSession(r.Context(), sessionID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, context)
}

func (a *App) lajuContextForSession(ctx context.Context, sessionID string) (lajuChannelContext, error) {
	detail, err := a.Repo.GetSessionDetail(ctx, sessionID, 20)
	if err != nil {
		return lajuChannelContext{}, err
	}
	session := detail.Session
	chctx := lajuChannelContext{
		TenantID:       session.TenantID,
		NexusSessionID: session.ID,
		ChannelType:    session.ChannelType,
		ChannelUserID:  session.OwnerUserID,
		SurfaceKey:     session.ChannelScopeKey,
		IdentityUserID: "",
		IdentityLinked: false,
		LiveStatus:     map[string]any{"state": session.State, "last_active_at": session.LastActiveAt},
		Metadata:       map[string]any{"source": "nexus", "contract": "nexus_laju_context_v1", "acp_session_id": session.ACPSessionID},
		PolicySnapshot: map[string]any{},
	}
	if policy, err := a.Repo.GetTrustPolicy(ctx, session.TenantID, session.AgentProfileID); err == nil {
		chctx.PolicySnapshot = map[string]any{
			"agent_profile_id":                      policy.AgentProfileID,
			"require_linked_identity_for_execution": policy.RequireLinkedIdentityForExecution,
			"require_linked_identity_for_approval":  policy.RequireLinkedIdentityForApproval,
			"require_recent_step_up_for_approval":   policy.RequireRecentStepUpForApproval,
			"allowed_approval_channels":             policy.AllowedApprovalChannels,
		}
	}
	var identity domain.LinkedIdentity
	var identityErr error
	if a.Identity != nil {
		identity, identityErr = a.Identity.GetLinkedIdentity(ctx, session.TenantID, session.ChannelType, session.OwnerUserID)
	} else if repo, ok := a.Repo.(interface {
		GetLinkedIdentity(context.Context, string, string, string) (domain.LinkedIdentity, error)
	}); ok {
		identity, identityErr = repo.GetLinkedIdentity(ctx, session.TenantID, session.ChannelType, session.OwnerUserID)
	}
	if identityErr == nil {
		chctx.IdentityUserID = identity.UserID
		chctx.IdentityLinked = identity.Status == "linked"
		chctx.IdentityAssurance = identity.Status
	}
	if len(detail.Messages) > 0 {
		msg := detail.Messages[len(detail.Messages)-1]
		chctx.ProviderEventID = msg.MessageID
		chctx.ChannelConversationID = session.ChannelScopeKey
		chctx.ChannelThreadID = msg.SessionID
	}
	return chctx, nil
}
