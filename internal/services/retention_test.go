package services

import (
	"context"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type retentionRepoStub struct {
	policies map[string]domain.RetentionPolicy
	tenants  []string
	counts   map[string]domain.RetentionCounts
}

func (r *retentionRepoStub) WithRetentionLock(ctx context.Context, fn func(ctx context.Context, repo ports.RetentionRepository) error) error {
	return fn(ctx, r)
}

func (r *retentionRepoStub) ListRetentionTenantIDs(ctx context.Context) ([]string, error) {
	return append([]string(nil), r.tenants...), nil
}

func (r *retentionRepoStub) GetRetentionPolicy(ctx context.Context, tenantID string) (domain.RetentionPolicy, error) {
	if policy, ok := r.policies[tenantID]; ok {
		return policy, nil
	}
	return domain.RetentionPolicy{TenantID: tenantID}, nil
}

func (r *retentionRepoStub) UpsertRetentionPolicy(ctx context.Context, policy domain.RetentionPolicy) error {
	if r.policies == nil {
		r.policies = map[string]domain.RetentionPolicy{}
	}
	r.policies[policy.TenantID] = policy
	return nil
}

func (r *retentionRepoStub) DeleteRetentionPolicy(ctx context.Context, tenantID string) error {
	delete(r.policies, tenantID)
	return nil
}

func (r *retentionRepoStub) CountRetentionCandidates(ctx context.Context, tenantID string, cutoffs domain.RetentionCutoffs) (domain.RetentionCounts, error) {
	return r.counts[tenantID], nil
}

func (r *retentionRepoStub) ApplyRetention(ctx context.Context, tenantID string, cutoffs domain.RetentionCutoffs, batchSize int, deleteBlob func(context.Context, string) error) (domain.RetentionCounts, error) {
	return r.counts[tenantID], nil
}

func (r *retentionRepoStub) Audit(ctx context.Context, event domain.AuditEvent) error {
	return nil
}

func TestRetentionServiceResolvePolicy(t *testing.T) {
	payloadDays := 14
	enabled := false
	repo := &retentionRepoStub{
		policies: map[string]domain.RetentionPolicy{
			"tenant_a": {
				TenantID:    "tenant_a",
				Enabled:     &enabled,
				PayloadDays: &payloadDays,
				UpdatedAt:   time.Now().UTC(),
			},
		},
	}
	svc := RetentionService{
		Repo: repo,
		Defaults: domain.EffectiveRetentionPolicy{
			TenantID:            "tenant_default",
			Enabled:             true,
			PayloadDays:         30,
			ArtifactDays:        30,
			AuditDays:           30,
			RelationalGraceDays: 30,
		},
	}

	override, effective, err := svc.ResolvePolicy(context.Background(), "tenant_a")
	if err != nil {
		t.Fatal(err)
	}
	if override.PayloadDays == nil || *override.PayloadDays != 14 {
		t.Fatalf("expected override payload days 14, got %+v", override)
	}
	if effective.Enabled {
		t.Fatal("expected effective policy to pick disabled override")
	}
	if effective.PayloadDays != 14 || effective.ArtifactDays != 30 {
		t.Fatalf("unexpected effective policy: %+v", effective)
	}
}

func TestRetentionServiceDryRunAggregatesTenantCounts(t *testing.T) {
	repo := &retentionRepoStub{
		tenants: []string{"tenant_b", "tenant_a"},
		counts: map[string]domain.RetentionCounts{
			"tenant_a": {MessagePayloads: 2, AuditRows: 1},
			"tenant_b": {ArtifactBlobs: 3, Sessions: 1, HistoryRows: 4},
		},
	}
	svc := RetentionService{
		Repo: repo,
		Defaults: domain.EffectiveRetentionPolicy{
			TenantID:            "tenant_default",
			Enabled:             true,
			PayloadDays:         30,
			ArtifactDays:        30,
			AuditDays:           30,
			RelationalGraceDays: 30,
		},
	}

	summary, err := svc.DryRun(context.Background(), "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tenants) != 3 {
		t.Fatalf("expected 3 tenants including default tenant, got %d", len(summary.Tenants))
	}
	if summary.Totals.MessagePayloads != 2 || summary.Totals.ArtifactBlobs != 3 || summary.Totals.Sessions != 1 || summary.Totals.HistoryRows != 4 || summary.Totals.AuditRows != 1 {
		t.Fatalf("unexpected totals: %+v", summary.Totals)
	}
}
