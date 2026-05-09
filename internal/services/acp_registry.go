package services

import (
	"context"
	"fmt"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type ACPResolver interface {
	DefaultBridge() ports.ACPBridge
	BridgeForConnection(connectionID string) (ports.ACPBridge, error)
	BridgeForSession(session domain.Session) (ports.ACPBridge, error)
	BridgeForRoute(route domain.RouteDecision) (ports.ACPBridge, error)
	DiscoverAgentsForConnection(ctx context.Context, connectionID string) ([]domain.AgentManifest, error)
	Close() error
}

type ACPRegistry struct {
	DefaultConnectionID string
	Bridges             map[string]ports.ACPBridge
	ProfileBridges      map[string]ports.ACPBridge
}

func (r *ACPRegistry) DefaultBridge() ports.ACPBridge {
	if r == nil {
		return nil
	}
	if bridge, ok := r.Bridges[r.DefaultConnectionID]; ok {
		return bridge
	}
	for _, bridge := range r.Bridges {
		return bridge
	}
	return nil
}

func (r *ACPRegistry) BridgeForConnection(connectionID string) (ports.ACPBridge, error) {
	if r == nil {
		return nil, fmt.Errorf("acp registry unavailable")
	}
	id := strings.TrimSpace(connectionID)
	if id == "" {
		id = r.DefaultConnectionID
	}
	bridge, ok := r.Bridges[id]
	if !ok || bridge == nil {
		return nil, fmt.Errorf("acp connection %q is not configured", id)
	}
	return bridge, nil
}

func (r *ACPRegistry) BridgeForSession(session domain.Session) (ports.ACPBridge, error) {
	if r != nil && strings.TrimSpace(session.ACPProfileID) != "" {
		if bridge, ok := r.ProfileBridges[session.ACPProfileID]; ok && bridge != nil {
			return bridge, nil
		}
	}
	return r.BridgeForConnection(session.ACPConnectionID)
}

func (r *ACPRegistry) BridgeForRoute(route domain.RouteDecision) (ports.ACPBridge, error) {
	if r != nil && strings.TrimSpace(route.AgentProfileID) != "" {
		if bridge, ok := r.ProfileBridges[route.AgentProfileID]; ok && bridge != nil {
			return bridge, nil
		}
	}
	return r.BridgeForConnection(route.ACPConnectionID)
}

func (r *ACPRegistry) DiscoverAgentsForConnection(ctx context.Context, connectionID string) ([]domain.AgentManifest, error) {
	if r != nil && strings.TrimSpace(connectionID) != "" {
		if bridge, ok := r.ProfileBridges[connectionID]; ok && bridge != nil {
			return bridge.DiscoverAgents(ctx)
		}
	}
	bridge, err := r.BridgeForConnection(connectionID)
	if err != nil {
		return nil, err
	}
	return bridge.DiscoverAgents(ctx)
}

func (r *ACPRegistry) Close() error {
	if r == nil {
		return nil
	}
	var firstErr error
	for _, bridge := range r.Bridges {
		if closer, ok := bridge.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	for _, bridge := range r.ProfileBridges {
		if closer, ok := bridge.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

type ResolvingACPBridge struct {
	Resolver ACPResolver
}

func (b ResolvingACPBridge) DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error) {
	if registry, ok := b.Resolver.(*ACPRegistry); ok && registry != nil {
		out := []domain.AgentManifest{}
		for connectionID, bridge := range registry.Bridges {
			agents, err := bridge.DiscoverAgents(ctx)
			if err != nil {
				return nil, err
			}
			for _, agent := range agents {
				if agent.Protocol == "" {
					agent.Protocol = connectionID
				}
				out = append(out, agent)
			}
		}
		return out, nil
	}
	return b.Resolver.DiscoverAgentsForConnection(ctx, "")
}

func (b ResolvingACPBridge) EnsureSession(ctx context.Context, session domain.Session) (string, error) {
	bridge, err := b.Resolver.BridgeForSession(session)
	if err != nil {
		return "", err
	}
	return bridge.EnsureSession(ctx, session)
}

func (b ResolvingACPBridge) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, domain.RunEventStream, error) {
	bridge, err := b.Resolver.BridgeForRoute(req.RouteDecision)
	if err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	return bridge.StartRun(ctx, req)
}

func (b ResolvingACPBridge) DiscoverAgentsForConnection(ctx context.Context, connectionID string) ([]domain.AgentManifest, error) {
	agents, err := b.Resolver.DiscoverAgentsForConnection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		if agents[i].Protocol == "" {
			agents[i].Protocol = connectionID
		}
	}
	return agents, nil
}

func (b ResolvingACPBridge) ResumeRun(ctx context.Context, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	bridge := b.Resolver.DefaultBridge()
	if bridge == nil {
		return domain.RunEventStream{}, fmt.Errorf("default acp bridge unavailable")
	}
	return bridge.ResumeRun(ctx, await, payload)
}

func (b ResolvingACPBridge) ResumeRunForSession(ctx context.Context, session domain.Session, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	bridge, err := b.Resolver.BridgeForSession(session)
	if err != nil {
		return domain.RunEventStream{}, err
	}
	if scoped, ok := bridge.(interface {
		ResumeRunForSession(context.Context, domain.Session, domain.Await, []byte) (domain.RunEventStream, error)
	}); ok {
		return scoped.ResumeRunForSession(ctx, session, await, payload)
	}
	return bridge.ResumeRun(ctx, await, payload)
}

func (b ResolvingACPBridge) GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error) {
	bridge := b.Resolver.DefaultBridge()
	if bridge == nil {
		return domain.RunStatusSnapshot{}, fmt.Errorf("default acp bridge unavailable")
	}
	return bridge.GetRun(ctx, acpRunID)
}

func (b ResolvingACPBridge) FindRunByIdempotencyKey(ctx context.Context, session domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error) {
	bridge, err := b.Resolver.BridgeForSession(session)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	return bridge.FindRunByIdempotencyKey(ctx, session, idempotencyKey)
}

func (b ResolvingACPBridge) FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error) {
	bridge, err := b.Resolver.BridgeForSession(session)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	return bridge.FindLatestRunForSession(ctx, session)
}

func (b ResolvingACPBridge) CancelRun(ctx context.Context, run domain.Run) error {
	bridge, err := b.Resolver.BridgeForConnection(run.ACPConnectionID)
	if err != nil {
		return err
	}
	return bridge.CancelRun(ctx, run)
}
