package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexus/internal/domain"
)

type catalogBridge struct {
	calls  int
	agents []domain.AgentManifest
	err    error
}

func (b *catalogBridge) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	return append([]domain.AgentManifest(nil), b.agents...), nil
}

func (b *catalogBridge) EnsureSession(context.Context, domain.Session) (string, error) {
	return "", nil
}
func (b *catalogBridge) StartRun(context.Context, domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	return domain.Run{}, nil, nil
}
func (b *catalogBridge) ResumeRun(context.Context, domain.Await, []byte) ([]domain.RunEvent, error) {
	return nil, nil
}
func (b *catalogBridge) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return domain.RunStatusSnapshot{}, nil
}
func (b *catalogBridge) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (b *catalogBridge) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (b *catalogBridge) CancelRun(context.Context, domain.Run) error { return nil }

func TestAgentCatalogCachesDiscoverAgents(t *testing.T) {
	bridge := &catalogBridge{
		agents: []domain.AgentManifest{{
			Name:                "coder",
			SupportsAwaitResume: true,
			SupportsStreaming:   true,
			SupportsArtifacts:   true,
			Healthy:             true,
		}},
	}
	catalog := AgentCatalog{Bridge: bridge, TTL: 60}
	catalog.TTL = time.Hour

	_, err := catalog.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if bridge.calls != 1 {
		t.Fatalf("expected discover agents once, got %d", bridge.calls)
	}
}

func TestAgentCatalogRefreshBypassesCache(t *testing.T) {
	bridge := &catalogBridge{
		agents: []domain.AgentManifest{{
			Name:                "coder",
			SupportsAwaitResume: true,
			SupportsStreaming:   true,
			SupportsArtifacts:   true,
			Healthy:             true,
		}},
	}
	catalog := AgentCatalog{Bridge: bridge, TTL: time.Hour}

	initial, err := catalog.Validate(context.Background(), "coder", false)
	if err != nil {
		t.Fatal(err)
	}
	if !initial.Compatible {
		t.Fatalf("expected initial compatibility, got %+v", initial)
	}

	bridge.agents = []domain.AgentManifest{{
		Name:                "coder",
		SupportsAwaitResume: false,
		SupportsStreaming:   true,
		SupportsArtifacts:   true,
		Healthy:             true,
	}}

	cached, err := catalog.Validate(context.Background(), "coder", false)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.Compatible {
		t.Fatalf("expected cached compatibility before refresh, got %+v", cached)
	}

	refreshed, err := catalog.Validate(context.Background(), "coder", true)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Compatible {
		t.Fatalf("expected refreshed compatibility failure, got %+v", refreshed)
	}
	if bridge.calls != 2 {
		t.Fatalf("expected discover agents twice after refresh, got %d", bridge.calls)
	}
}

func TestAgentCatalogTTlExpiryRefetches(t *testing.T) {
	bridge := &catalogBridge{
		agents: []domain.AgentManifest{{
			Name:                "coder",
			SupportsAwaitResume: true,
			SupportsStreaming:   true,
			SupportsArtifacts:   true,
			Healthy:             true,
		}},
	}
	catalog := AgentCatalog{Bridge: bridge, TTL: 5 * time.Millisecond}

	agents, err := catalog.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name != "coder" {
		t.Fatalf("unexpected initial agents: %+v", agents)
	}

	bridge.agents = []domain.AgentManifest{{
		Name:                "reviewer",
		SupportsAwaitResume: true,
		SupportsStreaming:   true,
		SupportsArtifacts:   true,
		Healthy:             true,
	}}
	time.Sleep(20 * time.Millisecond)

	agents, err = catalog.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name != "reviewer" {
		t.Fatalf("expected refetched agents after TTL expiry, got %+v", agents)
	}
	if bridge.calls != 2 {
		t.Fatalf("expected two discover calls after TTL expiry, got %d", bridge.calls)
	}
}

func TestAgentCatalogCompatibleUsesCachedSnapshotUntilRefresh(t *testing.T) {
	bridge := &catalogBridge{
		agents: []domain.AgentManifest{
			{
				Name:                "coder",
				SupportsAwaitResume: true,
				SupportsStreaming:   true,
				SupportsArtifacts:   true,
				Healthy:             true,
			},
			{
				Name:                "broken",
				SupportsAwaitResume: false,
				SupportsStreaming:   true,
				SupportsArtifacts:   true,
				Healthy:             true,
			},
		},
	}
	catalog := AgentCatalog{Bridge: bridge, TTL: time.Hour}

	compatible, err := catalog.Compatible(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(compatible) != 1 || compatible[0].AgentName != "coder" {
		t.Fatalf("unexpected compatible list: %+v", compatible)
	}

	bridge.agents = []domain.AgentManifest{{
		Name:                "broken",
		SupportsAwaitResume: true,
		SupportsStreaming:   true,
		SupportsArtifacts:   true,
		Healthy:             true,
	}}

	cachedCompatible, err := catalog.Compatible(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cachedCompatible) != 1 || cachedCompatible[0].AgentName != "coder" {
		t.Fatalf("expected cached compatible list before refresh, got %+v", cachedCompatible)
	}

	refreshedCompatible, err := catalog.Compatible(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshedCompatible) != 1 || refreshedCompatible[0].AgentName != "broken" {
		t.Fatalf("expected refreshed compatible list after refresh, got %+v", refreshedCompatible)
	}
}

func TestAgentCatalogStatusTracksLastFetchError(t *testing.T) {
	bridge := &catalogBridge{err: errors.New("acp unavailable")}
	catalog := AgentCatalog{Bridge: bridge, TTL: time.Hour}

	_, err := catalog.List(context.Background(), true)
	if err == nil {
		t.Fatal("expected discover error")
	}

	status := catalog.Status()
	if status.LastFetchError != "acp unavailable" || status.CacheValid || status.CachedAgentCount != 0 {
		t.Fatalf("unexpected catalog status after fetch error: %+v", status)
	}
}
