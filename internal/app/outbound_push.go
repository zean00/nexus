package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/httpx"
	"nexus/internal/ports"
	"nexus/internal/services"
)

type outboundPushRequest struct {
	CustomerID        string                 `json:"customer_id"`
	TenantID          string                 `json:"tenant_id"`
	SessionID         string                 `json:"session_id"`
	UserID            string                 `json:"user_id"`
	AccountID         string                 `json:"account_id"`
	ChannelType       string                 `json:"channel_type"`
	ChannelUserID     string                 `json:"channel_user_id"`
	SurfaceKey        string                 `json:"surface_key"`
	RunID             string                 `json:"run_id"`
	MessageID         string                 `json:"message_id"`
	LogicalMessageID  string                 `json:"logical_message_id"`
	DeliveryKind      string                 `json:"delivery_kind"`
	IntentType        string                 `json:"intent_type"`
	Text              string                 `json:"text"`
	Artifacts         []outboundPushArtifact `json:"artifacts"`
	Payload           map[string]any         `json:"payload"`
	Metadata          map[string]any         `json:"metadata"`
	IdempotencyKey    string                 `json:"idempotency_key"`
	RawChannelPayload map[string]any         `json:"raw_channel_payload"`
	WhatsAppTemplate  map[string]any         `json:"whatsapp_template"`
}

type outboundPushArtifact struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	StorageURI string `json:"storage_uri"`
	SourceURL  string `json:"source_url"`
}

type outboundPushBulkRequest struct {
	TenantID string                `json:"tenant_id"`
	Items    []outboundPushRequest `json:"items"`
}

type outboundPushResult struct {
	Index       int      `json:"index,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	ChannelType string   `json:"channel_type,omitempty"`
	DeliveryIDs []string `json:"delivery_ids,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func (a *App) handlePushOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body outboundPushRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	result, err := a.enqueueOutboundPush(r.Context(), body, 0)
	if err != nil {
		httpx.Error(w, outboundPushStatus(err), err.Error())
		return
	}
	httpx.Accepted(w, result, actionMeta("outbound_push_queued"))
}

func (a *App) handlePushOutboundBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body outboundPushBulkRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if len(body.Items) == 0 {
		httpx.Error(w, http.StatusBadRequest, "items required")
		return
	}
	results := make([]outboundPushResult, 0, len(body.Items))
	failed := 0
	for idx, item := range body.Items {
		if item.TenantID == "" {
			item.TenantID = body.TenantID
		}
		result, err := a.enqueueOutboundPush(r.Context(), item, idx)
		if err != nil {
			failed++
			results = append(results, outboundPushResult{Index: idx, Error: err.Error()})
			continue
		}
		result.Index = idx
		results = append(results, result)
	}
	status := http.StatusAccepted
	if failed == len(body.Items) {
		status = http.StatusBadRequest
	}
	httpx.Respond(w, status, map[string]any{
		"items":       results,
		"accepted":    len(body.Items) - failed,
		"failed":      failed,
		"total_count": len(body.Items),
	}, actionMeta("outbound_push_bulk_queued"))
}

func (a *App) enqueueOutboundPush(ctx context.Context, req outboundPushRequest, idx int) (outboundPushResult, error) {
	req = normalizeOutboundPushRequest(req)
	if strings.TrimSpace(req.Text) == "" && len(req.Artifacts) == 0 && len(req.RawChannelPayload) == 0 {
		return outboundPushResult{}, fmt.Errorf("text, artifacts, or raw_channel_payload required")
	}
	tenantID := firstNonEmptyString(req.TenantID, req.CustomerID)
	if tenantID == "" {
		tenantID = a.Config.DefaultTenantID
	}
	session, err := a.resolveOutboundPushSession(ctx, tenantID, req)
	if err != nil {
		return outboundPushResult{}, err
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = outboundPushRunID(req, idx)
	}
	deliveries, err := a.renderOutboundPush(ctx, session, runID, req)
	if err != nil {
		return outboundPushResult{}, err
	}
	if len(deliveries) == 0 {
		return outboundPushResult{}, fmt.Errorf("no outbound deliveries rendered")
	}
	ids := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		if err := a.Repo.EnqueueDelivery(ctx, delivery); err != nil {
			return outboundPushResult{}, err
		}
		ids = append(ids, delivery.ID)
	}
	if err := a.persistWebChatOutboundPush(ctx, session, runID, req); err != nil {
		return outboundPushResult{}, err
	}
	_ = a.Repo.Audit(ctx, domain.AuditEvent{
		ID:            "audit_outbound_push_" + runID + "_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		TenantID:      tenantID,
		SessionID:     session.ID,
		RunID:         runID,
		AggregateType: "outbound_push",
		AggregateID:   runID,
		EventType:     "admin.outbound_push_queued",
		PayloadJSON: mustJSON(map[string]any{
			"delivery_ids": ids,
			"message_id":   req.MessageID,
			"metadata":     req.Metadata,
		}),
		CreatedAt: time.Now().UTC(),
	})
	return outboundPushResult{SessionID: session.ID, ChannelType: session.ChannelType, DeliveryIDs: ids}, nil
}

func (a *App) persistWebChatOutboundPush(ctx context.Context, session domain.Session, runID string, req outboundPushRequest) error {
	if session.ChannelType != "webchat" {
		return nil
	}
	text := strings.TrimSpace(req.Text)
	if text == "" && len(req.Artifacts) == 0 {
		return nil
	}
	rawPayload, err := json.Marshal(map[string]any{
		"run_id":      runID,
		"message_key": firstNonEmptyString(req.MessageID, req.LogicalMessageID, runID),
		"status":      "completed",
		"text":        text,
		"is_partial":  false,
		"artifacts":   outboundPushArtifacts(req.Artifacts),
		"metadata":    req.Metadata,
	})
	if err != nil {
		return err
	}
	messageKey := firstNonEmptyString(req.MessageID, req.LogicalMessageID, runID)
	messageID, err := a.Repo.StoreOutboundMessage(ctx, session, runID, messageKey, text, rawPayload)
	if err != nil {
		return err
	}
	if len(req.Artifacts) > 0 {
		if err := a.Repo.StoreArtifacts(ctx, messageID, "outbound", outboundPushArtifacts(req.Artifacts)); err != nil {
			return err
		}
	}
	if a.WebChatHub != nil {
		a.WebChatHub.Notify(session.ID)
	}
	return nil
}

func (a *App) resolveOutboundPushSession(ctx context.Context, tenantID string, req outboundPushRequest) (domain.Session, error) {
	if sessionID := strings.TrimSpace(req.SessionID); sessionID != "" {
		session, err := a.Repo.GetSession(ctx, sessionID)
		if err != nil {
			return domain.Session{}, err
		}
		if session.TenantID != "" && session.TenantID != tenantID {
			return domain.Session{}, fmt.Errorf("session tenant mismatch")
		}
		return session, nil
	}
	channelType := strings.ToLower(strings.TrimSpace(req.ChannelType))
	surfaceKey := strings.TrimSpace(req.SurfaceKey)
	ownerUserID := firstNonEmptyString(req.UserID, req.AccountID)
	if ownerUserID == "" && req.ChannelUserID != "" {
		ownerUserID = strings.TrimSpace(req.ChannelUserID)
	}
	if surfaceKey == "" && req.ChannelUserID != "" {
		surfaceKey = strings.TrimSpace(req.ChannelUserID)
	}
	if ownerUserID != "" && (channelType == "" || surfaceKey == "") {
		identity, err := a.resolveOutboundPushIdentity(ctx, tenantID, ownerUserID, channelType)
		if err != nil {
			return domain.Session{}, err
		}
		channelType = identity.ChannelType
		surfaceKey = identity.ChannelUserID
	}
	if channelType == "" || surfaceKey == "" || ownerUserID == "" {
		return domain.Session{}, fmt.Errorf("session_id or target identity required")
	}
	return a.Repo.EnsureNotificationSession(ctx, tenantID, channelType, surfaceKey, ownerUserID)
}

func (a *App) resolveOutboundPushIdentity(ctx context.Context, tenantID, userID, channelType string) (domain.LinkedIdentity, error) {
	var (
		items []domain.LinkedIdentity
		err   error
	)
	if a.Identity != nil {
		items, err = a.Identity.ListLinkedIdentitiesForUser(ctx, tenantID, userID)
	} else {
		items, err = a.Repo.ListLinkedIdentitiesForUser(ctx, tenantID, userID)
	}
	if err != nil {
		return domain.LinkedIdentity{}, err
	}
	for _, item := range items {
		if item.Status != "" && item.Status != "linked" {
			continue
		}
		if channelType == "" || item.ChannelType == channelType {
			return item, nil
		}
	}
	return domain.LinkedIdentity{}, fmt.Errorf("no linked identity found for user")
}

func (a *App) renderOutboundPush(ctx context.Context, session domain.Session, runID string, req outboundPushRequest) ([]domain.OutboundDelivery, error) {
	if len(req.RawChannelPayload) > 0 {
		payload := mustJSON(req.RawChannelPayload)
		deliveryID := outboundPushDeliveryID(runID, "raw")
		return []domain.OutboundDelivery{{
			ID:               deliveryID,
			TenantID:         session.TenantID,
			SessionID:        session.ID,
			RunID:            runID,
			LogicalMessageID: firstNonEmptyString(req.LogicalMessageID, "logical_"+runID),
			ChannelType:      session.ChannelType,
			DeliveryKind:     firstNonEmptyString(req.DeliveryKind, "send"),
			Status:           "queued",
			PayloadJSON:      payload,
		}}, nil
	}
	renderer := a.rendererForOutboundPush(session.ChannelType)
	if renderer == nil {
		return nil, fmt.Errorf("no renderer for channel %s", session.ChannelType)
	}
	deliveries, err := renderer.RenderRunEvent(ctx, session, domain.RunEvent{
		RunID:      runID,
		MessageKey: firstNonEmptyString(req.MessageID, runID),
		Status:     "completed",
		Text:       strings.TrimSpace(req.Text),
		Artifacts:  outboundPushArtifacts(req.Artifacts),
	})
	if err != nil {
		return nil, err
	}
	for i := range deliveries {
		if session.ChannelType == "whatsapp" && len(req.WhatsAppTemplate) > 0 {
			var payload map[string]any
			if err := json.Unmarshal(deliveries[i].PayloadJSON, &payload); err == nil {
				payload["whatsapp_template"] = req.WhatsAppTemplate
				deliveries[i].PayloadJSON = mustJSON(payload)
			}
		}
		if req.DeliveryKind != "" {
			deliveries[i].DeliveryKind = req.DeliveryKind
		}
		if req.LogicalMessageID != "" && len(deliveries) == 1 {
			deliveries[i].LogicalMessageID = req.LogicalMessageID
		}
	}
	return deliveries, nil
}

func (a *App) rendererForOutboundPush(channelType string) ports.Renderer {
	if a.Worker.Renderers != nil {
		if renderer, ok := a.Worker.Renderers[channelType]; ok {
			return renderer
		}
	}
	if a.Worker.Renderer != nil {
		return a.Worker.Renderer
	}
	return servicesDefaultRenderer(channelType)
}

func servicesDefaultRenderer(channelType string) ports.Renderer {
	switch channelType {
	case "slack":
		return services.SlackRenderer{}
	case "telegram":
		return services.TelegramRenderer{}
	case "whatsapp":
		return services.WhatsAppRenderer{}
	case "whatsapp-web":
		return services.WhatsAppWebRenderer{}
	case "email":
		return services.EmailRenderer{}
	case "webchat":
		return services.WebChatRenderer{}
	default:
		return nil
	}
}

func outboundPushArtifacts(in []outboundPushArtifact) []domain.Artifact {
	out := make([]domain.Artifact, 0, len(in))
	for _, item := range in {
		out = append(out, domain.Artifact{
			ID:         strings.TrimSpace(item.ID),
			Name:       strings.TrimSpace(item.Name),
			MIMEType:   strings.TrimSpace(item.MIMEType),
			SizeBytes:  item.SizeBytes,
			SHA256:     strings.TrimSpace(item.SHA256),
			StorageURI: strings.TrimSpace(item.StorageURI),
			SourceURL:  strings.TrimSpace(item.SourceURL),
		})
	}
	return out
}

func outboundPushRunID(req outboundPushRequest, idx int) string {
	for _, candidate := range []string{req.MessageID, req.IdempotencyKey, req.LogicalMessageID} {
		if clean := cleanOutboundPushID(candidate); clean != "" {
			return "push_" + clean
		}
	}
	return "push_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10) + "_" + strconv.Itoa(idx)
}

func outboundPushDeliveryID(runID, suffix string) string {
	return "delivery_" + cleanOutboundPushID(runID) + "_" + cleanOutboundPushID(suffix)
}

func cleanOutboundPushID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func outboundPushStatus(err error) int {
	if err == nil {
		return http.StatusAccepted
	}
	msg := err.Error()
	if strings.Contains(msg, "required") || strings.Contains(msg, "not found") || strings.Contains(msg, "mismatch") || strings.Contains(msg, "no renderer") || strings.Contains(msg, "no outbound") {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if clean := strings.TrimSpace(value); clean != "" {
			return clean
		}
	}
	return ""
}

func normalizeOutboundPushRequest(req outboundPushRequest) outboundPushRequest {
	req.TenantID = firstNonEmptyString(req.TenantID, req.CustomerID)
	req.UserID = firstNonEmptyString(req.UserID, req.AccountID)
	if req.Payload == nil {
		return req
	}
	if looksLikeOutboundIntent(req.Payload) {
		req = mergeOutboundIntentEnvelope(req, req.Payload)
	}
	req.Text = firstNonEmptyString(req.Text, stringFromMap(req.Payload, "text"), stringFromMap(req.Payload, "message"))
	if req.WhatsAppTemplate == nil {
		if template, ok := mapFromMap(req.Payload, "whatsapp_template"); ok {
			req.WhatsAppTemplate = template
		}
	}
	if req.Text == "" {
		if nested, ok := mapFromMap(req.Payload, "payload"); ok {
			req.Text = firstNonEmptyString(stringFromMap(nested, "text"), stringFromMap(nested, "message"), stringFromMap(req.Payload, "title"))
		}
	}
	return req
}

func looksLikeOutboundIntent(payload map[string]any) bool {
	for _, key := range []string{"customer_id", "session_id", "user_id", "intent_type", "outbound_intent_id"} {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	return false
}

func mergeOutboundIntentEnvelope(req outboundPushRequest, payload map[string]any) outboundPushRequest {
	req.CustomerID = firstNonEmptyString(req.CustomerID, stringFromMap(payload, "customer_id"))
	req.TenantID = firstNonEmptyString(req.TenantID, req.CustomerID)
	req.UserID = firstNonEmptyString(req.UserID, stringFromMap(payload, "user_id"))
	req.AccountID = firstNonEmptyString(req.AccountID, req.UserID)
	req.SessionID = firstNonEmptyString(req.SessionID, stringFromMap(payload, "session_id"))
	req.RunID = firstNonEmptyString(req.RunID, stringFromMap(payload, "run_id"))
	req.IntentType = firstNonEmptyString(req.IntentType, stringFromMap(payload, "intent_type"))
	req.MessageID = firstNonEmptyString(req.MessageID, stringFromMap(payload, "outbound_intent_id"))
	if nested, ok := mapFromMap(payload, "payload"); ok {
		req.Payload = nested
	}
	return req
}

func stringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

func mapFromMap(values map[string]any, key string) (map[string]any, bool) {
	if values == nil {
		return nil, false
	}
	value, ok := values[key]
	if !ok || value == nil {
		return nil, false
	}
	typed, ok := value.(map[string]any)
	return typed, ok
}
