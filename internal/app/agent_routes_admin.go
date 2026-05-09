package app

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/httpx"
)

type agentRouteAdminRepository interface {
	ListAgentRoutingRulesPage(ctx context.Context, tenantID string, includeDisabled bool, limit int) ([]domain.AgentRoutingRule, error)
	UpsertAgentRoutingRule(ctx context.Context, rule domain.AgentRoutingRule) (domain.AgentRoutingRule, error)
	DeleteAgentRoutingRule(ctx context.Context, tenantID, ruleID string) error
	GetAgentRouteOverride(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string) (string, error)
	SetAgentRouteOverride(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID, agentProfileID string) error
	ResetAgentRouteOverride(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string) error
}

func (a *App) handleAgentRoutes(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.Repo.(agentRouteAdminRepository)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "agent routing repository unavailable")
		return
	}
	tenantID := firstNonEmptyString(strings.TrimSpace(r.URL.Query().Get("tenant_id")), a.Config.DefaultTenantID)
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
		if limit <= 0 {
			limit = 100
		}
		includeDisabled := strings.EqualFold(r.URL.Query().Get("include_disabled"), "true")
		rules, err := repo.ListAgentRoutingRulesPage(r.Context(), tenantID, includeDisabled, limit)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.OK(w, rules, map[string]any{"count": len(rules)})
	case http.MethodPost, http.MethodPut:
		var body domain.AgentRoutingRule
		if !decodeJSONBody(w, r, &body) {
			return
		}
		if body.TenantID == "" {
			body.TenantID = tenantID
		}
		if body.AgentProfileID == "" {
			httpx.Error(w, http.StatusBadRequest, "agent_profile_id is required")
			return
		}
		if body.ID == "" && r.Method == http.MethodPut {
			httpx.Error(w, http.StatusBadRequest, "id is required")
			return
		}
		stored, err := repo.UpsertAgentRoutingRule(r.Context(), body)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.OK(w, stored, nil)
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			httpx.Error(w, http.StatusBadRequest, "id is required")
			return
		}
		if err := repo.DeleteAgentRoutingRule(r.Context(), tenantID, id); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.OK(w, map[string]any{"deleted": true}, nil)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAgentEffectiveRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	repo, ok := a.Repo.(agentRouteAdminRepository)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "agent routing repository unavailable")
		return
	}
	tenantID := firstNonEmptyString(strings.TrimSpace(r.URL.Query().Get("tenant_id")), a.Config.DefaultTenantID)
	channel := strings.TrimSpace(r.URL.Query().Get("channel_type"))
	surface := strings.TrimSpace(r.URL.Query().Get("surface_key"))
	owner := strings.TrimSpace(r.URL.Query().Get("owner_user_id"))
	if channel == "" || surface == "" || owner == "" {
		httpx.Error(w, http.StatusBadRequest, "channel_type, surface_key, and owner_user_id are required")
		return
	}
	if r.Method == http.MethodDelete {
		if err := repo.ResetAgentRouteOverride(r.Context(), tenantID, channel, surface, owner); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.OK(w, map[string]any{"reset": true}, nil)
		return
	}
	active, err := repo.GetAgentRouteOverride(r.Context(), tenantID, channel, surface, owner)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if active == "" {
		active = a.Config.AgentRouting.DefaultAgentByChannel[strings.ToLower(channel)]
	}
	httpx.OK(w, map[string]any{
		"tenant_id":        tenantID,
		"channel_type":     channel,
		"surface_key":      surface,
		"owner_user_id":    owner,
		"agent_profile_id": active,
		"available_agents": a.Config.AgentRouting.AllowedAgentsByChannel[strings.ToLower(channel)],
	}, nil)
}
