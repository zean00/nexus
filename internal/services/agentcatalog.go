package services

import (
	"context"
	"strings"
	"sync"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type AgentCatalog struct {
	Bridge ports.ACPBridge
	TTL    time.Duration

	mu                  sync.Mutex
	agents              []domain.AgentManifest
	agentsByConnection  map[string][]domain.AgentManifest
	expiresByConnection map[string]time.Time
	expiresAt           time.Time
	lastFetchedAt       time.Time
	lastFetchError      string
	lastRefresh         bool
}

type connectionAgentDiscoverer interface {
	DiscoverAgentsForConnection(ctx context.Context, connectionID string) ([]domain.AgentManifest, error)
}

type AgentCatalogStatus struct {
	CachedAgentCount int       `json:"cached_agent_count"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	LastFetchedAt    time.Time `json:"last_fetched_at,omitempty"`
	LastFetchError   string    `json:"last_fetch_error,omitempty"`
	LastRefresh      bool      `json:"last_refresh"`
	CacheValid       bool      `json:"cache_valid"`
}

func (c *AgentCatalog) List(ctx context.Context, refresh bool) ([]domain.AgentManifest, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if !refresh && len(c.agents) > 0 && now.Before(c.expiresAt) {
		return append([]domain.AgentManifest(nil), c.agents...), nil
	}

	agents, err := c.Bridge.DiscoverAgents(ctx)
	if err != nil {
		c.lastFetchedAt = now
		c.lastFetchError = err.Error()
		c.lastRefresh = refresh
		return nil, err
	}
	c.agents = append([]domain.AgentManifest(nil), agents...)
	c.expiresAt = now.Add(c.TTL)
	c.lastFetchedAt = now
	c.lastFetchError = ""
	c.lastRefresh = refresh
	return append([]domain.AgentManifest(nil), c.agents...), nil
}

func (c *AgentCatalog) Validate(ctx context.Context, agentName string, refresh bool) (domain.AgentCompatibility, error) {
	agents, err := c.List(ctx, refresh)
	if err != nil {
		return domain.AgentCompatibility{}, err
	}
	return ValidateAgentCompatibility(agents, agentName), nil
}

func (c *AgentCatalog) ValidateForConnection(ctx context.Context, connectionID, agentName string, refresh bool) (domain.AgentCompatibility, error) {
	if connectionID == "" {
		return c.Validate(ctx, agentName, refresh)
	}
	discoverer, ok := c.Bridge.(connectionAgentDiscoverer)
	if !ok {
		return c.Validate(ctx, agentName, refresh)
	}
	agents, err := c.listForConnection(ctx, discoverer, connectionID, refresh)
	if err != nil {
		return domain.AgentCompatibility{}, err
	}
	return ValidateAgentCompatibility(agents, agentName), nil
}

func (c *AgentCatalog) ValidateForRoute(ctx context.Context, route domain.RouteDecision, refresh bool) (domain.AgentCompatibility, error) {
	connectionID := route.ACPConnectionID
	if strings.TrimSpace(route.ACPProfileID) != "" {
		connectionID = route.ACPProfileID
	}
	return c.ValidateForConnection(ctx, connectionID, route.ACPAgentName, refresh)
}

func (c *AgentCatalog) listForConnection(ctx context.Context, discoverer connectionAgentDiscoverer, connectionID string, refresh bool) ([]domain.AgentManifest, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.agentsByConnection == nil {
		c.agentsByConnection = map[string][]domain.AgentManifest{}
		c.expiresByConnection = map[string]time.Time{}
	}
	if !refresh && len(c.agentsByConnection[connectionID]) > 0 && now.Before(c.expiresByConnection[connectionID]) {
		return append([]domain.AgentManifest(nil), c.agentsByConnection[connectionID]...), nil
	}
	agents, err := discoverer.DiscoverAgentsForConnection(ctx, connectionID)
	if err != nil {
		c.lastFetchedAt = now
		c.lastFetchError = err.Error()
		c.lastRefresh = refresh
		return nil, err
	}
	c.agentsByConnection[connectionID] = append([]domain.AgentManifest(nil), agents...)
	c.expiresByConnection[connectionID] = now.Add(c.TTL)
	c.lastFetchedAt = now
	c.lastFetchError = ""
	c.lastRefresh = refresh
	return append([]domain.AgentManifest(nil), agents...), nil
}

func (c *AgentCatalog) Compatible(ctx context.Context, refresh bool) ([]domain.AgentCompatibility, error) {
	agents, err := c.List(ctx, refresh)
	if err != nil {
		return nil, err
	}
	result := make([]domain.AgentCompatibility, 0, len(agents))
	for _, agent := range agents {
		compat := ValidateAgentCompatibility(agents, agent.Name)
		if compat.Compatible {
			result = append(result, compat)
		}
	}
	return result, nil
}

func (c *AgentCatalog) Status() AgentCatalogStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	return AgentCatalogStatus{
		CachedAgentCount: len(c.agents),
		ExpiresAt:        c.expiresAt,
		LastFetchedAt:    c.lastFetchedAt,
		LastFetchError:   c.lastFetchError,
		LastRefresh:      c.lastRefresh,
		CacheValid:       len(c.agents) > 0 && now.Before(c.expiresAt),
	}
}

func (c *AgentCatalog) CachedValidate(agentName string) (domain.AgentCompatibility, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if len(c.agents) == 0 || now.After(c.expiresAt) {
		return domain.AgentCompatibility{}, false
	}
	return ValidateAgentCompatibility(c.agents, agentName), true
}
