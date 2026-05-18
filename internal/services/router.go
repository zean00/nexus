package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/ports"
)

type PolicyRouter struct {
	Repo                   ports.Repository
	DefaultAgentProfileID  string
	DefaultACPAgentName    string
	ACPMode                string
	AgentProfiles          map[string]domain.AgentProfile
	DefaultAgentByChannel  map[string]string
	AllowedAgentsByChannel map[string][]string
	FileRules              []domain.AgentRoutingRule
	WebChatAgentByIdentity map[string]string
	FallbackPolicy         domain.TrustPolicy
}

type StaticRouter = PolicyRouter

type routeOverrideRepository interface {
	GetAgentRouteOverride(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string) (string, error)
	ListAgentRoutingRules(ctx context.Context, tenantID string) ([]domain.AgentRoutingRule, error)
}

func NewPolicyRouter(repo ports.Repository, cfg config.Config) PolicyRouter {
	router := PolicyRouter{
		Repo:                   repo,
		DefaultAgentProfileID:  cfg.DefaultAgentProfileID,
		DefaultACPAgentName:    cfg.DefaultACPAgentName,
		ACPMode:                cfg.ACPMode,
		AgentProfiles:          map[string]domain.AgentProfile{},
		DefaultAgentByChannel:  cloneStringMap(cfg.AgentRouting.DefaultAgentByChannel),
		AllowedAgentsByChannel: cloneStringSliceMap(cfg.AgentRouting.AllowedAgentsByChannel),
		WebChatAgentByIdentity: map[string]string{},
		FallbackPolicy: domain.TrustPolicy{
			TenantID:                          cfg.DefaultTenantID,
			AgentProfileID:                    cfg.DefaultAgentProfileID,
			RequireLinkedIdentityForExecution: false,
			RequireLinkedIdentityForApproval:  cfg.RequireLinkedIdentity,
			RequireRecentStepUpForApproval:    cfg.RequireRecentStepUp,
			AllowedApprovalChannels:           append([]string(nil), cfg.AllowedApprovalChannels...),
		},
	}
	for _, profile := range cfg.ACPAgentProfiles {
		router.AgentProfiles[profile.ID] = domain.AgentProfile{
			ID:           profile.ID,
			ConnectionID: profile.ConnectionID,
			AgentName:    profile.AgentName,
			Description:  profile.Description,
			Headers:      cloneHeaderMap(profile.Headers),
			PathPrefix:   profile.PathPrefix,
		}
	}
	if len(router.AgentProfiles) == 0 {
		router.AgentProfiles[cfg.DefaultAgentProfileID] = domain.AgentProfile{
			ID:           cfg.DefaultAgentProfileID,
			ConnectionID: "acp_default",
			AgentName:    cfg.DefaultACPAgentName,
		}
	}
	for _, rule := range cfg.AgentRouting.Rules {
		enabled := true
		if rule.Enabled != nil {
			enabled = *rule.Enabled
		}
		router.FileRules = append(router.FileRules, domain.AgentRoutingRule{
			ID:             rule.ID,
			TenantID:       cfg.DefaultTenantID,
			Priority:       rule.Priority,
			Enabled:        enabled,
			Match:          rule.Match,
			AgentProfileID: rule.AgentProfileID,
		})
	}
	for _, identity := range cfg.WebChatIdentities {
		router.WebChatAgentByIdentity[identity.ID] = identity.AgentProfileID
	}
	sort.SliceStable(router.FileRules, func(i, j int) bool { return router.FileRules[i].Priority < router.FileRules[j].Priority })
	return router
}

func (r PolicyRouter) Route(ctx context.Context, evt domain.CanonicalInboundEvent, _ domain.Session) (domain.RouteDecision, error) {
	agentProfileID, source, match, err := r.resolveAgentProfile(ctx, evt)
	if err != nil {
		return domain.RouteDecision{}, err
	}
	profile := r.AgentProfiles[agentProfileID]
	if profile.ID == "" {
		profile = domain.AgentProfile{ID: agentProfileID, ConnectionID: "acp_default", AgentName: r.DefaultACPAgentName}
	}
	if strings.ToLower(strings.TrimSpace(r.ACPMode)) != "multiple" {
		profile.ConnectionID = "acp_default"
		if strings.TrimSpace(profile.AgentName) == "" {
			profile.AgentName = r.DefaultACPAgentName
		}
	}
	policy := r.FallbackPolicy
	policy.AgentProfileID = agentProfileID
	if r.Repo != nil && agentProfileID != "" {
		if stored, err := r.Repo.GetTrustPolicy(ctx, policy.TenantID, agentProfileID); err == nil {
			policy = stored
		} else if !errors.Is(err, domain.ErrTrustPolicyNotFound) {
			return domain.RouteDecision{}, err
		}
	}
	agentMode := normalizeRouteAgentMode(routeString(match, "agent_mode", "agentMode"))
	responseDelivery := strings.ToLower(strings.TrimSpace(routeString(match, "response_delivery", "responseDelivery")))
	if responseDelivery == "" && (agentMode == "manual" || agentMode == "supervised") {
		responseDelivery = "operator_review"
	}
	return domain.RouteDecision{
		AgentProfileID:                    agentProfileID,
		ACPConnectionID:                   firstNonEmptyRouteValue(profile.ConnectionID, "acp_default"),
		ACPAgentName:                      firstNonEmptyRouteValue(profile.AgentName, r.DefaultACPAgentName),
		ACPProfileID:                      agentProfileID,
		Mode:                              "async",
		AgentMode:                         agentMode,
		ResponseDelivery:                  responseDelivery,
		AllowFirstMessageResponse:         routeBool(match, "allow_first_message_response", "allowFirstMessageResponse"),
		RequiresLinkedIdentity:            policy.RequireLinkedIdentityForApproval,
		RequiresRecentStepUp:              policy.RequireRecentStepUpForApproval,
		AllowedApprovalChannels:           append([]string(nil), policy.AllowedApprovalChannels...),
		RequireLinkedIdentityForExecution: policy.RequireLinkedIdentityForExecution,
		RequireLinkedIdentityForApproval:  policy.RequireLinkedIdentityForApproval,
		RequireRecentStepUpForApproval:    policy.RequireRecentStepUpForApproval,
		Source:                            source,
	}, nil
}

func (r PolicyRouter) resolveAgentProfile(ctx context.Context, evt domain.CanonicalInboundEvent) (string, string, map[string]any, error) {
	if strings.ToLower(strings.TrimSpace(r.ACPMode)) != "multiple" {
		if profileID, source, match, err := r.resolveRuleAgentProfile(ctx, evt, false); err != nil {
			return "", "", nil, err
		} else if profileID != "" {
			return profileID, "single_" + source, match, nil
		}
		return firstNonEmptyRouteValue(r.DefaultAgentProfileID, "agent_profile_default"), "single", nil, nil
	}
	if repo, ok := r.Repo.(routeOverrideRepository); ok {
		if profileID, err := repo.GetAgentRouteOverride(ctx, evt.TenantID, evt.Channel, evt.Conversation.ChannelSurfaceKey, evt.Sender.ChannelUserID); err == nil && profileID != "" {
			if err := r.ensureAllowed(evt.Channel, profileID); err != nil {
				return "", "", nil, err
			}
			return profileID, "override", nil, nil
		}
	}
	if profileID, source, match, err := r.resolveRuleAgentProfile(ctx, evt, true); err != nil {
		return "", "", nil, err
	} else if profileID != "" {
		return profileID, source, match, nil
	}
	if strings.EqualFold(evt.Channel, "webchat") && strings.TrimSpace(evt.Metadata.WebChatIdentityID) != "" {
		if profileID := r.WebChatAgentByIdentity[evt.Metadata.WebChatIdentityID]; profileID != "" {
			if err := r.ensureAllowed(evt.Channel, profileID); err != nil {
				return "", "", nil, err
			}
			return profileID, "webchat_identity", nil, nil
		}
	}
	profileID := r.DefaultAgentByChannel[strings.ToLower(strings.TrimSpace(evt.Channel))]
	if profileID == "" {
		profileID = r.DefaultAgentProfileID
	}
	if err := r.ensureAllowed(evt.Channel, profileID); err != nil {
		return "", "", nil, err
	}
	return profileID, "channel_default", nil, nil
}

func (r PolicyRouter) resolveRuleAgentProfile(ctx context.Context, evt domain.CanonicalInboundEvent, enforceAllowed bool) (string, string, map[string]any, error) {
	if repo, ok := r.Repo.(routeOverrideRepository); ok {
		rules, err := repo.ListAgentRoutingRules(ctx, evt.TenantID)
		if err != nil {
			return "", "", nil, err
		}
		if profileID, match := firstMatchingRule(evt, rules); profileID != "" {
			if enforceAllowed {
				if err := r.ensureAllowed(evt.Channel, profileID); err != nil {
					return "", "", nil, err
				}
			}
			return profileID, "db_rule", match, nil
		}
	}
	if profileID, match := firstMatchingRule(evt, r.FileRules); profileID != "" {
		if enforceAllowed {
			if err := r.ensureAllowed(evt.Channel, profileID); err != nil {
				return "", "", nil, err
			}
		}
		return profileID, "file_rule", match, nil
	}
	return "", "", nil, nil
}

func (r PolicyRouter) ensureAllowed(channel, profileID string) error {
	if profileID == "" {
		return fmt.Errorf("agent profile is not configured for channel %s", channel)
	}
	if _, ok := r.AgentProfiles[profileID]; !ok {
		return fmt.Errorf("agent profile %q is not configured", profileID)
	}
	allowed := r.AllowedAgentsByChannel[strings.ToLower(strings.TrimSpace(channel))]
	if len(allowed) == 0 {
		return nil
	}
	for _, candidate := range allowed {
		if candidate == profileID {
			return nil
		}
	}
	return fmt.Errorf("agent profile %q is not available on channel %s", profileID, channel)
}

func firstMatchingRule(evt domain.CanonicalInboundEvent, rules []domain.AgentRoutingRule) (string, map[string]any) {
	for _, rule := range rules {
		if !rule.Enabled || rule.AgentProfileID == "" || !ruleMatches(evt, rule.Match) {
			continue
		}
		return rule.AgentProfileID, rule.Match
	}
	return "", nil
}

func ruleMatches(evt domain.CanonicalInboundEvent, match map[string]any) bool {
	for key, value := range match {
		want := strings.TrimSpace(fmt.Sprint(value))
		if want == "" {
			continue
		}
		var got string
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "tenant_id":
			got = evt.TenantID
		case "channel":
			got = evt.Channel
		case "surface_key":
			got = evt.Conversation.ChannelSurfaceKey
		case "webchat_identity", "webchat_identity_id":
			got = evt.Metadata.WebChatIdentityID
		case "account_key", "provider_account_id":
			got = evt.Metadata.AccountKey
		case "owner_user_id", "channel_user_id":
			got = evt.Sender.ChannelUserID
		default:
			continue
		}
		if strings.EqualFold(evt.Channel, "webchat") && strings.EqualFold(key, "surface_key") {
			continue
		}
		if got == "" && strings.EqualFold(evt.Channel, "webchat") && (strings.EqualFold(key, "account_key") || strings.EqualFold(key, "provider_account_id")) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(got), want) {
			return false
		}
	}
	return true
}

func firstNonEmptyRouteValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func routeString(match map[string]any, keys ...string) string {
	for _, key := range keys {
		if match == nil {
			return ""
		}
		if value, ok := match[key]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func routeBool(match map[string]any, keys ...string) bool {
	for _, key := range keys {
		if match == nil {
			return false
		}
		value, ok := match[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			return strings.EqualFold(strings.TrimSpace(typed), "true")
		default:
			return strings.EqualFold(strings.TrimSpace(fmt.Sprint(value)), "true")
		}
	}
	return false
}

func normalizeRouteAgentMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manual", "supervised", "auto", "unattended":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, values := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneHeaderMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
