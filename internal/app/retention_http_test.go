package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/services"
)

type retentionHTTPRepoStub struct {
	policy domain.RetentionPolicy
	counts domain.RetentionCounts
}

func (r *retentionHTTPRepoStub) WithRetentionLock(ctx context.Context, fn func(ctx context.Context, repo ports.RetentionRepository) error) error {
	return fn(ctx, r)
}

func (r *retentionHTTPRepoStub) ListRetentionTenantIDs(ctx context.Context) ([]string, error) {
	return []string{"tenant_default"}, nil
}

func (r *retentionHTTPRepoStub) GetRetentionPolicy(ctx context.Context, tenantID string) (domain.RetentionPolicy, error) {
	return r.policy, nil
}

func (r *retentionHTTPRepoStub) UpsertRetentionPolicy(ctx context.Context, policy domain.RetentionPolicy) error {
	return nil
}

func (r *retentionHTTPRepoStub) DeleteRetentionPolicy(ctx context.Context, tenantID string) error {
	return nil
}

func (r *retentionHTTPRepoStub) CountRetentionCandidates(ctx context.Context, tenantID string, cutoffs domain.RetentionCutoffs) (domain.RetentionCounts, error) {
	return r.counts, nil
}

func (r *retentionHTTPRepoStub) ApplyRetention(ctx context.Context, tenantID string, cutoffs domain.RetentionCutoffs, batchSize int, deleteBlob func(context.Context, string) error) (domain.RetentionCounts, error) {
	return r.counts, nil
}

func (r *retentionHTTPRepoStub) Audit(ctx context.Context, event domain.AuditEvent) error {
	return nil
}

func TestHandleRetentionStatus(t *testing.T) {
	payloadDays := 14
	repo := &retentionHTTPRepoStub{
		policy: domain.RetentionPolicy{
			TenantID:    "tenant_default",
			PayloadDays: &payloadDays,
		},
		counts: domain.RetentionCounts{MessagePayloads: 2, AuditRows: 1},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Retention: services.RetentionService{
			Repo: repo,
			Defaults: domain.EffectiveRetentionPolicy{
				TenantID:            "tenant_default",
				Enabled:             true,
				PayloadDays:         30,
				ArtifactDays:        30,
				AuditDays:           30,
				RelationalGraceDays: 30,
			},
		},
		Runtime: &RuntimeState{},
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/retention", nil)
	w := httptest.NewRecorder()
	app.handleRetentionStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"payload_days":14`) || !strings.Contains(body, `"message_payloads":2`) {
		t.Fatalf("unexpected response: %s", body)
	}
}
