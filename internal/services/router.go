package services

import (
	"context"
	"errors"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type PolicyRouter struct {
	Repo                  ports.Repository
	DefaultAgentProfileID string
	DefaultACPAgentName   string
	FallbackPolicy        domain.TrustPolicy
}

type StaticRouter = PolicyRouter

func (r PolicyRouter) Route(ctx context.Context, _ domain.CanonicalInboundEvent, _ domain.Session) (domain.RouteDecision, error) {
	policy := r.FallbackPolicy
	policy.AgentProfileID = r.DefaultAgentProfileID
	if r.Repo != nil && r.DefaultAgentProfileID != "" {
		if stored, err := r.Repo.GetTrustPolicy(ctx, policy.TenantID, r.DefaultAgentProfileID); err == nil {
			policy = stored
		} else if !errors.Is(err, domain.ErrTrustPolicyNotFound) {
			return domain.RouteDecision{}, err
		}
	}
	return domain.RouteDecision{
		AgentProfileID:                    r.DefaultAgentProfileID,
		ACPConnectionID:                   "acp_default",
		ACPAgentName:                      r.DefaultACPAgentName,
		Mode:                              "async",
		RequiresLinkedIdentity:            policy.RequireLinkedIdentityForApproval,
		RequiresRecentStepUp:              policy.RequireRecentStepUpForApproval,
		AllowedApprovalChannels:           append([]string(nil), policy.AllowedApprovalChannels...),
		RequireLinkedIdentityForExecution: policy.RequireLinkedIdentityForExecution,
		RequireLinkedIdentityForApproval:  policy.RequireLinkedIdentityForApproval,
		RequireRecentStepUpForApproval:    policy.RequireRecentStepUpForApproval,
	}, nil
}
