package services

import (
	"context"

	"nexus/internal/domain"
)

type StaticRouter struct {
	DefaultAgentProfileID   string
	DefaultACPAgentName     string
	RequireLinkedIdentity   bool
	RequireRecentStepUp     bool
	AllowedApprovalChannels []string
}

func (r StaticRouter) Route(_ context.Context, _ domain.CanonicalInboundEvent, _ domain.Session) (domain.RouteDecision, error) {
	return domain.RouteDecision{
		AgentProfileID:          r.DefaultAgentProfileID,
		ACPConnectionID:         "acp_default",
		ACPAgentName:            r.DefaultACPAgentName,
		Mode:                    "async",
		RequiresLinkedIdentity:  r.RequireLinkedIdentity,
		RequiresRecentStepUp:    r.RequireRecentStepUp,
		AllowedApprovalChannels: append([]string(nil), r.AllowedApprovalChannels...),
	}, nil
}
