package app

import (
	"context"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/httpx"
	trustadminui "nexus/ui/trustadmin"
)

var trustAdminPageTemplate = template.Must(template.New("trust-admin-page").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Nexus Trust Admin</title>
  <link rel="stylesheet" href="/admin/trust/app.css">
</head>
<body>
  <div id="app"></div>
  <script>window.__NEXUS_TRUST_ADMIN_CONFIG__ = {{ .Config }};</script>
  <script type="module" src="/admin/trust/app.js"></script>
</body>
</html>`))

func (a *App) handleTrustAdminIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	configJSON, _ := json.Marshal(map[string]any{
		"baseUrl": "/admin/trust",
	})
	_ = trustAdminPageTemplate.Execute(w, map[string]any{"Config": template.JS(string(configJSON))})
}

func (a *App) handleTrustAdminJS(w http.ResponseWriter, r *http.Request) {
	a.serveTrustAdminAsset(w, r, "app.js", "application/javascript; charset=utf-8")
}

func (a *App) handleTrustAdminCSS(w http.ResponseWriter, r *http.Request) {
	a.serveTrustAdminAsset(w, r, "app.css", "text/css; charset=utf-8")
}

func (a *App) serveTrustAdminAsset(w http.ResponseWriter, _ *http.Request, name, contentType string) {
	content, err := fs.ReadFile(trustadminui.Dist(), name)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(content)
}

func (a *App) handleTrustSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := a.trustSummary(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, summary, map[string]any{"service": "admin"})
}

func (a *App) handleListTrustPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := a.Repo.ListTrustPolicies(r.Context(), a.Config.DefaultTenantID, 200)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"items": items}, nil)
}

func (a *App) handleUpsertTrustPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body domain.TrustPolicy
	if !decodeJSONBody(w, r, &body) {
		return
	}
	body.TenantID = a.Config.DefaultTenantID
	body.AgentProfileID = strings.TrimSpace(body.AgentProfileID)
	body.UpdatedAt = time.Now().UTC()
	if body.AgentProfileID == "" {
		httpx.Error(w, http.StatusBadRequest, "missing agent_profile_id")
		return
	}
	if err := a.Repo.UpsertTrustPolicy(r.Context(), body); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            "audit_trust_policy_" + body.AgentProfileID + "_" + randomToken(6),
		TenantID:      body.TenantID,
		AggregateType: "trust_policy",
		AggregateID:   body.AgentProfileID,
		EventType:     "trust.policy_upserted",
		PayloadJSON:   mustJSON(body),
		CreatedAt:     body.UpdatedAt,
	})
	httpx.OK(w, body, actionMeta("trust_policy_upserted"))
}

func (a *App) handleListTrustUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.Repo.ListUsers(r.Context(), a.Config.DefaultTenantID, 200)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(users))
	for _, user := range users {
		identities, err := a.Repo.ListLinkedIdentitiesForUser(r.Context(), a.Config.DefaultTenantID, user.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, map[string]any{
			"user":              user,
			"linked_identities": identities,
			"link_hints":        buildWebChatLinkHints(user, identities),
		})
	}
	httpx.OK(w, map[string]any{"items": items}, nil)
}

func (a *App) handleTrustUserDetail(w http.ResponseWriter, r *http.Request) {
	userID := queryString(r, "user_id")
	if userID == "" {
		httpx.Error(w, http.StatusBadRequest, "missing user_id")
		return
	}
	user, err := a.Identity.GetUser(r.Context(), a.Config.DefaultTenantID, userID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, err.Error())
		return
	}
	identities, err := a.Repo.ListLinkedIdentitiesForUser(r.Context(), a.Config.DefaultTenantID, userID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	events, err := a.Repo.ListAuditEvents(r.Context(), domain.AuditEventListQuery{
		TenantID:    a.Config.DefaultTenantID,
		AggregateID: userID,
		CursorPage:  domain.CursorPage{Limit: 50},
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{
		"user":              user,
		"linked_identities": identities,
		"link_hints":        buildWebChatLinkHints(user, identities),
		"events":            events.Items,
	}, nil)
}

func (a *App) handleTrustRevokeLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ChannelType   string `json:"channel_type"`
		ChannelUserID string `json:"channel_user_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	channelType := strings.ToLower(strings.TrimSpace(body.ChannelType))
	channelUserID := strings.TrimSpace(body.ChannelUserID)
	if channelType == "" || channelUserID == "" {
		httpx.Error(w, http.StatusBadRequest, "missing link identity")
		return
	}
	if err := a.Repo.DeleteLinkedIdentity(r.Context(), a.Config.DefaultTenantID, channelType, channelUserID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            trustAdminAuditID("identity_unlinked", a.Config.DefaultTenantID, channelType, channelUserID, time.Now().UTC().Format(time.RFC3339Nano)),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "linked_identity",
		AggregateID:   channelType + ":" + channelUserID,
		EventType:     "trust.identity_unlinked",
		PayloadJSON:   mustJSON(body),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, map[string]any{"status": "revoked"}, actionMeta("trust_identity_revoked"))
}

func (a *App) handleTrustEvents(w http.ResponseWriter, r *http.Request) {
	query := domain.AuditEventListQuery{
		TenantID:      a.Config.DefaultTenantID,
		CursorPage:    domain.CursorPage{Limit: 100},
		AggregateType: queryString(r, "aggregate_type"),
	}
	items, err := a.listTrustEvents(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"items": items}, nil)
}

func (a *App) handleWhatsAppPolicySummary(w http.ResponseWriter, r *http.Request) {
	tenantID := a.Config.DefaultTenantID
	total, err := a.Repo.CountWhatsAppContacts(r.Context(), domain.WhatsAppPolicyListQuery{TenantID: tenantID})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	openCount, _ := a.Repo.CountWhatsAppContacts(r.Context(), domain.WhatsAppPolicyListQuery{TenantID: tenantID, WindowState: "open"})
	closedCount, _ := a.Repo.CountWhatsAppContacts(r.Context(), domain.WhatsAppPolicyListQuery{TenantID: tenantID, WindowState: "closed"})
	optedOut, _ := a.Repo.CountWhatsAppContacts(r.Context(), domain.WhatsAppPolicyListQuery{TenantID: tenantID, ConsentStatus: "opted_out"})
	templateFallbacks, _ := a.Repo.CountAuditEvents(r.Context(), domain.AuditEventListQuery{TenantID: tenantID, EventType: "whatsapp.template_fallback_sent"})
	policyBlocks, _ := a.Repo.CountAuditEvents(r.Context(), domain.AuditEventListQuery{TenantID: tenantID, EventType: "whatsapp.policy_blocked"})
	httpx.OK(w, map[string]any{
		"total_contacts":              total,
		"open_windows":                openCount,
		"closed_windows":              closedCount,
		"opted_out_contacts":          optedOut,
		"template_fallbacks_total":    templateFallbacks,
		"policy_blocks_total":         policyBlocks,
		"enforce_24h_window":          a.Config.WhatsAppEnforce24HWindow,
		"window_hours":                a.Config.WhatsAppCustomerServiceWindowHours,
		"default_template_configured": len(a.Config.WhatsAppClosedWindowTemplateJSON) > 0,
	}, nil)
}

func (a *App) handleWhatsAppPolicyContacts(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r, 100, 200)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	query := domain.WhatsAppPolicyListQuery{
		TenantID:      a.Config.DefaultTenantID,
		ConsentStatus: queryString(r, "consent_status"),
		WindowState:   queryString(r, "window_state"),
		Contains:      queryString(r, "contains"),
		CursorPage:    page,
	}
	items, err := a.Repo.ListWhatsAppContacts(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	total, err := a.Repo.CountWhatsAppContacts(r.Context(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.Page(w, http.StatusOK, decorateWhatsAppContacts(items.Items), items.NextCursor, total)
}

func (a *App) handleWhatsAppPolicyEvents(w http.ResponseWriter, r *http.Request) {
	items, err := a.listWhatsAppPolicyEvents(r.Context(), domain.CursorPage{Limit: 100})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"items": items}, nil)
}

func (a *App) handleWhatsAppConsentUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ChannelUserID string `json:"channel_user_id"`
		ConsentStatus string `json:"consent_status"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	body.ChannelUserID = strings.TrimSpace(body.ChannelUserID)
	body.ConsentStatus = strings.TrimSpace(body.ConsentStatus)
	if body.ChannelUserID == "" || !slices.Contains([]string{"unknown", "opted_in", "opted_out"}, body.ConsentStatus) {
		httpx.Error(w, http.StatusBadRequest, "channel_user_id and consent_status=unknown|opted_in|opted_out required")
		return
	}
	now := time.Now().UTC()
	if err := a.Repo.SetWhatsAppConsentStatus(r.Context(), a.Config.DefaultTenantID, body.ChannelUserID, body.ConsentStatus, now); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            trustAdminAuditID("whatsapp_consent", a.Config.DefaultTenantID, body.ChannelUserID, body.ConsentStatus, now.Format(time.RFC3339Nano)),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "whatsapp_contact",
		AggregateID:   body.ChannelUserID,
		EventType:     "admin.whatsapp_consent_updated",
		PayloadJSON:   mustJSON(body),
		CreatedAt:     now,
	})
	httpx.OK(w, body, actionMeta("whatsapp_consent_updated"))
}

func (a *App) trustSummary(ctx context.Context) (map[string]any, error) {
	users, err := a.Repo.ListUsers(ctx, a.Config.DefaultTenantID, 500)
	if err != nil {
		return nil, err
	}
	policies, err := a.Repo.ListTrustPolicies(ctx, a.Config.DefaultTenantID, 200)
	if err != nil {
		return nil, err
	}
	linkedByChannel, err := a.Repo.CountLinkedIdentitiesByChannel(ctx, a.Config.DefaultTenantID)
	if err != nil {
		return nil, err
	}
	blockedCount, _ := a.Repo.CountAuditEvents(ctx, domain.AuditEventListQuery{TenantID: a.Config.DefaultTenantID, EventType: "trust.approval_blocked"})
	stepUpFailures, _ := a.Repo.CountAuditEvents(ctx, domain.AuditEventListQuery{TenantID: a.Config.DefaultTenantID, EventType: "trust.step_up_rejected"})
	return map[string]any{
		"user_count":                   len(users),
		"policy_count":                 len(policies),
		"linked_identities_by_channel": linkedByChannel,
		"blocked_approval_count":       blockedCount,
		"step_up_failure_count":        stepUpFailures,
	}, nil
}

func (a *App) listTrustEvents(ctx context.Context, base domain.AuditEventListQuery) ([]domain.AuditEvent, error) {
	eventTypes := []string{
		"trust.policy_upserted",
		"trust.link_code_issued",
		"trust.identity_linked",
		"trust.identity_unlinked",
		"trust.step_up_requested",
		"trust.step_up_verified",
		"trust.step_up_rejected",
		"trust.approval_blocked",
	}
	seen := map[string]struct{}{}
	items := make([]domain.AuditEvent, 0, 100)
	for _, eventType := range eventTypes {
		query := base
		query.EventType = eventType
		page, err := a.Repo.ListAuditEvents(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > base.Limit && base.Limit > 0 {
		items = items[:base.Limit]
	}
	return items, nil
}

func (a *App) listWhatsAppPolicyEvents(ctx context.Context, page domain.CursorPage) ([]domain.AuditEvent, error) {
	eventTypes := []string{
		"whatsapp.consent_opted_out",
		"whatsapp.consent_opted_in",
		"whatsapp.template_fallback_sent",
		"whatsapp.policy_blocked",
		"admin.whatsapp_consent_updated",
	}
	seen := map[string]struct{}{}
	items := make([]domain.AuditEvent, 0, page.Limit)
	for _, eventType := range eventTypes {
		result, err := a.Repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:      a.Config.DefaultTenantID,
			AggregateType: "whatsapp_contact",
			EventType:     eventType,
			CursorPage:    page,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range result.Items {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if page.Limit > 0 && len(items) > page.Limit {
		items = items[:page.Limit]
	}
	return items, nil
}

func decorateWhatsAppContacts(items []domain.WhatsAppContactPolicy) []map[string]any {
	now := time.Now().UTC()
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"tenant_id":              item.TenantID,
			"channel_user_id":        item.ChannelUserID,
			"last_inbound_at":        item.LastInboundAt,
			"window_expires_at":      item.WindowExpiresAt,
			"window_open":            !item.WindowExpiresAt.IsZero() && item.WindowExpiresAt.After(now),
			"consent_status":         item.ConsentStatus,
			"consent_updated_at":     item.ConsentUpdatedAt,
			"last_template_sent_at":  item.LastTemplateSentAt,
			"last_policy_blocked_at": item.LastPolicyBlockedAt,
			"updated_at":             item.UpdatedAt,
		})
	}
	return out
}

func trustAdminAuditID(parts ...string) string {
	return "audit_" + sha256Hex(strings.Join(parts, "|"))[:24]
}
