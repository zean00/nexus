package services

import (
	"context"
	"testing"

	"nexus/internal/config"
	"nexus/internal/domain"
)

func TestPolicyRouterSingleModeUsesDefaultConnection(t *testing.T) {
	router := NewPolicyRouter(nil, config.Config{
		ACPMode:               "single",
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "agent_default",
		DefaultACPAgentName:   "support",
	})
	route, err := router.Route(context.Background(), domain.CanonicalInboundEvent{TenantID: "tenant_default", Channel: "webchat"}, domain.Session{})
	if err != nil {
		t.Fatal(err)
	}
	if route.AgentProfileID != "agent_default" || route.ACPConnectionID != "acp_default" || route.ACPAgentName != "support" {
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestPolicyRouterMultipleModeChannelDefault(t *testing.T) {
	router := NewPolicyRouter(nil, config.Config{
		ACPMode:               "multiple",
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "support",
		ACPAgentProfiles: []config.ACPAgentProfileConfig{
			{ID: "support", ConnectionID: "primary", AgentName: "support-agent"},
		},
		AgentRouting: config.AgentRoutingConfig{
			DefaultAgentByChannel: map[string]string{"whatsapp": "support"},
		},
	})
	route, err := router.Route(context.Background(), domain.CanonicalInboundEvent{TenantID: "tenant_default", Channel: "whatsapp"}, domain.Session{})
	if err != nil {
		t.Fatal(err)
	}
	if route.AgentProfileID != "support" || route.ACPConnectionID != "primary" || route.ACPAgentName != "support-agent" || route.Source != "channel_default" {
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestPolicyRouterRejectsUnavailableAgent(t *testing.T) {
	router := NewPolicyRouter(nil, config.Config{
		ACPMode:               "multiple",
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "support",
		ACPAgentProfiles: []config.ACPAgentProfileConfig{
			{ID: "support", ConnectionID: "primary", AgentName: "support-agent"},
			{ID: "finance", ConnectionID: "primary", AgentName: "finance-agent"},
		},
		AgentRouting: config.AgentRoutingConfig{
			DefaultAgentByChannel:  map[string]string{"whatsapp": "finance"},
			AllowedAgentsByChannel: map[string][]string{"whatsapp": []string{"support"}},
		},
	})
	_, err := router.Route(context.Background(), domain.CanonicalInboundEvent{TenantID: "tenant_default", Channel: "whatsapp"}, domain.Session{})
	if err == nil {
		t.Fatal("expected unavailable agent error")
	}
}

func TestPolicyRouterUsesWebChatIdentityDefault(t *testing.T) {
	router := NewPolicyRouter(nil, config.Config{
		ACPMode:               "multiple",
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "support",
		ACPAgentProfiles: []config.ACPAgentProfileConfig{
			{ID: "support", ConnectionID: "primary", AgentName: "support-agent"},
			{ID: "product", ConnectionID: "primary", AgentName: "product-agent"},
		},
		AgentRouting: config.AgentRoutingConfig{
			DefaultAgentByChannel: map[string]string{"webchat": "support"},
		},
		WebChatIdentities: []config.WebChatIdentityConfig{
			{ID: "product_inquiry", Path: "product", AgentProfileID: "product"},
		},
	})
	route, err := router.Route(context.Background(), domain.CanonicalInboundEvent{
		TenantID: "tenant_default",
		Channel:  "webchat",
		Metadata: domain.Metadata{WebChatIdentityID: "product_inquiry"},
	}, domain.Session{})
	if err != nil {
		t.Fatal(err)
	}
	if route.AgentProfileID != "product" || route.ACPAgentName != "product-agent" || route.Source != "webchat_identity" {
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestPolicyRouterFileRuleCanMatchWebChatIdentity(t *testing.T) {
	enabled := true
	router := NewPolicyRouter(nil, config.Config{
		ACPMode:               "multiple",
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "support",
		ACPAgentProfiles: []config.ACPAgentProfileConfig{
			{ID: "support", ConnectionID: "primary", AgentName: "support-agent"},
			{ID: "sales", ConnectionID: "primary", AgentName: "sales-agent"},
		},
		AgentRouting: config.AgentRoutingConfig{
			DefaultAgentByChannel: map[string]string{"webchat": "support"},
			Rules: []config.AgentRoutingRuleConfig{{
				Priority:       10,
				Enabled:        &enabled,
				Match:          map[string]any{"webchat_identity": "product"},
				AgentProfileID: "sales",
			}},
		},
		WebChatIdentities: []config.WebChatIdentityConfig{
			{ID: "product", Path: "product", AgentProfileID: "support"},
		},
	})
	route, err := router.Route(context.Background(), domain.CanonicalInboundEvent{
		TenantID: "tenant_default",
		Channel:  "webchat",
		Metadata: domain.Metadata{WebChatIdentityID: "product"},
	}, domain.Session{})
	if err != nil {
		t.Fatal(err)
	}
	if route.AgentProfileID != "sales" || route.Source != "file_rule" {
		t.Fatalf("unexpected route: %+v", route)
	}
}
