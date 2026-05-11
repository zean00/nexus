package services

import (
	"context"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type nonComparableACPBridge struct {
	headers map[string]string
}

func (nonComparableACPBridge) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) {
	return nil, nil
}

func (nonComparableACPBridge) EnsureSession(context.Context, domain.Session) (string, error) {
	return "", nil
}

func (nonComparableACPBridge) StartRun(context.Context, domain.StartRunRequest) (domain.Run, domain.RunEventStream, error) {
	return domain.Run{}, domain.RunEventStream{}, nil
}

func (nonComparableACPBridge) ResumeRun(context.Context, domain.Await, []byte) (domain.RunEventStream, error) {
	return domain.RunEventStream{}, nil
}

func (nonComparableACPBridge) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return domain.RunStatusSnapshot{}, nil
}

func (nonComparableACPBridge) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}

func (nonComparableACPBridge) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}

func (nonComparableACPBridge) CancelRun(context.Context, domain.Run) error {
	return nil
}

type closeCountingACPBridge struct {
	count *int
}

func (b *closeCountingACPBridge) Close() error {
	(*b.count)++
	return nil
}

func (b *closeCountingACPBridge) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) {
	return nil, nil
}

func (b *closeCountingACPBridge) EnsureSession(context.Context, domain.Session) (string, error) {
	return "", nil
}

func (b *closeCountingACPBridge) StartRun(context.Context, domain.StartRunRequest) (domain.Run, domain.RunEventStream, error) {
	return domain.Run{}, domain.RunEventStream{}, nil
}

func (b *closeCountingACPBridge) ResumeRun(context.Context, domain.Await, []byte) (domain.RunEventStream, error) {
	return domain.RunEventStream{}, nil
}

func (b *closeCountingACPBridge) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return domain.RunStatusSnapshot{}, nil
}

func (b *closeCountingACPBridge) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}

func (b *closeCountingACPBridge) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}

func (b *closeCountingACPBridge) CancelRun(context.Context, domain.Run) error {
	return nil
}

var _ ports.ACPBridge = nonComparableACPBridge{}
var _ ports.ACPBridge = (*closeCountingACPBridge)(nil)

func TestACPRegistryCloseHandlesNonComparableBridges(t *testing.T) {
	registry := &ACPRegistry{
		Bridges: map[string]ports.ACPBridge{
			"default": nonComparableACPBridge{headers: map[string]string{"X-Test": "one"}},
		},
		ProfileBridges: map[string]ports.ACPBridge{
			"profile": nonComparableACPBridge{headers: map[string]string{"X-Test": "two"}},
		},
	}

	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestACPRegistryCloseClosesSharedPointerBridgeOnce(t *testing.T) {
	count := 0
	bridge := &closeCountingACPBridge{count: &count}
	registry := &ACPRegistry{
		Bridges:        map[string]ports.ACPBridge{"default": bridge},
		ProfileBridges: map[string]ports.ACPBridge{"profile": bridge},
	}

	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected shared bridge to close once, got %d", count)
	}
}
