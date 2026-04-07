package services

import (
	"context"
	"errors"
	"slices"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type RetentionBlobStore interface {
	Delete(ctx context.Context, storageURI string) error
}

type RetentionService struct {
	Repo     ports.RetentionRepository
	Store    RetentionBlobStore
	Defaults domain.EffectiveRetentionPolicy
}

func (s RetentionService) ResolvePolicy(ctx context.Context, tenantID string) (domain.RetentionPolicy, domain.EffectiveRetentionPolicy, error) {
	override, err := s.Repo.GetRetentionPolicy(ctx, tenantID)
	if err != nil {
		return domain.RetentionPolicy{}, domain.EffectiveRetentionPolicy{}, err
	}
	effective := s.Defaults
	effective.TenantID = tenantID
	if override.Enabled != nil {
		effective.Enabled = *override.Enabled
	}
	if override.PayloadDays != nil {
		effective.PayloadDays = *override.PayloadDays
	}
	if override.ArtifactDays != nil {
		effective.ArtifactDays = *override.ArtifactDays
	}
	if override.AuditDays != nil {
		effective.AuditDays = *override.AuditDays
	}
	if override.RelationalGraceDays != nil {
		effective.RelationalGraceDays = *override.RelationalGraceDays
	}
	override.TenantID = tenantID
	return override, effective, nil
}

func (s RetentionService) DryRun(ctx context.Context, tenantID string, batchSize int) (domain.RetentionRunSummary, error) {
	return s.run(ctx, tenantID, true, batchSize)
}

func (s RetentionService) RunOnce(ctx context.Context, tenantID string, dryRun bool, batchSize int) (domain.RetentionRunSummary, error) {
	return s.run(ctx, tenantID, dryRun, batchSize)
}

func (s RetentionService) UpsertPolicy(ctx context.Context, policy domain.RetentionPolicy) error {
	return s.Repo.UpsertRetentionPolicy(ctx, policy)
}

func (s RetentionService) DeletePolicy(ctx context.Context, tenantID string) error {
	return s.Repo.DeleteRetentionPolicy(ctx, tenantID)
}

func (s RetentionService) run(ctx context.Context, tenantID string, dryRun bool, batchSize int) (domain.RetentionRunSummary, error) {
	started := time.Now().UTC()
	summary := domain.RetentionRunSummary{DryRun: dryRun, StartedAt: started}
	if s.Repo == nil {
		summary.EndedAt = time.Now().UTC()
		return summary, nil
	}
	err := s.Repo.WithRetentionLock(ctx, func(ctx context.Context, repo ports.RetentionRepository) error {
		tenants, err := s.retentionTenants(ctx, repo, tenantID)
		if err != nil {
			return err
		}
		for _, currentTenantID := range tenants {
			override, effective, err := s.ResolvePolicy(ctx, currentTenantID)
			if err != nil {
				return err
			}
			_ = override
			tenantSummary := domain.RetentionTenantSummary{
				TenantID:  currentTenantID,
				Enabled:   effective.Enabled,
				Effective: effective,
			}
			if !effective.Enabled {
				tenantSummary.SkippedReason = "disabled"
				summary.Tenants = append(summary.Tenants, tenantSummary)
				continue
			}
			cutoffs := retentionCutoffs(effective, started)
			if dryRun {
				tenantSummary.Counts, err = repo.CountRetentionCandidates(ctx, currentTenantID, cutoffs)
			} else {
				tenantSummary.Counts, err = repo.ApplyRetention(ctx, currentTenantID, cutoffs, batchSize, s.deleteBlob)
			}
			if err != nil {
				return err
			}
			summary.Tenants = append(summary.Tenants, tenantSummary)
			summary.Totals = addRetentionCounts(summary.Totals, tenantSummary.Counts)
		}
		return nil
	})
	summary.EndedAt = time.Now().UTC()
	return summary, err
}

func (s RetentionService) retentionTenants(ctx context.Context, repo ports.RetentionRepository, tenantID string) ([]string, error) {
	if tenantID != "" {
		return []string{tenantID}, nil
	}
	tenantIDs, err := repo.ListRetentionTenantIDs(ctx)
	if err != nil {
		return nil, err
	}
	if s.Defaults.TenantID != "" {
		tenantIDs = append(tenantIDs, s.Defaults.TenantID)
	}
	slices.Sort(tenantIDs)
	return slices.Compact(tenantIDs), nil
}

func (s RetentionService) deleteBlob(ctx context.Context, storageURI string) error {
	if s.Store == nil || storageURI == "" {
		return nil
	}
	return s.Store.Delete(ctx, storageURI)
}

func retentionCutoffs(policy domain.EffectiveRetentionPolicy, now time.Time) domain.RetentionCutoffs {
	maxDays := policy.PayloadDays
	if policy.ArtifactDays > maxDays {
		maxDays = policy.ArtifactDays
	}
	return domain.RetentionCutoffs{
		PayloadBefore:    now.AddDate(0, 0, -policy.PayloadDays),
		ArtifactBefore:   now.AddDate(0, 0, -policy.ArtifactDays),
		AuditBefore:      now.AddDate(0, 0, -policy.AuditDays),
		RelationalBefore: now.AddDate(0, 0, -(maxDays + policy.RelationalGraceDays)),
	}
}

func addRetentionCounts(left, right domain.RetentionCounts) domain.RetentionCounts {
	return domain.RetentionCounts{
		MessagePayloads:       left.MessagePayloads + right.MessagePayloads,
		DeliveryPayloads:      left.DeliveryPayloads + right.DeliveryPayloads,
		OutboxPayloads:        left.OutboxPayloads + right.OutboxPayloads,
		AwaitPayloads:         left.AwaitPayloads + right.AwaitPayloads,
		AwaitResponsePayloads: left.AwaitResponsePayloads + right.AwaitResponsePayloads,
		ArtifactBlobs:         left.ArtifactBlobs + right.ArtifactBlobs,
		AuditRows:             left.AuditRows + right.AuditRows,
		Sessions:              left.Sessions + right.Sessions,
		HistoryRows:           left.HistoryRows + right.HistoryRows,
	}
}

func IsRetentionLockBusy(err error) bool {
	return errors.Is(err, domain.ErrRetentionLockBusy)
}
