package services

import (
	"context"

	"nexus/internal/domain"
)

type StaticRouter struct {
	DefaultAgentProfileID string
	DefaultACPAgentName   string
}

func (r StaticRouter) Route(_ context.Context, _ domain.CanonicalInboundEvent, _ domain.Session) (domain.RouteDecision, error) {
	return domain.RouteDecision{
		AgentProfileID:  r.DefaultAgentProfileID,
		ACPConnectionID: "acp_default",
		ACPAgentName:    r.DefaultACPAgentName,
		Mode:            "async",
	}, nil
}
