package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type ReconcilerConfig struct {
	OutboxClaimTimeout     time.Duration
	QueueStartingTimeout   time.Duration
	RunStaleTimeout        time.Duration
	DeliverySendingTimeout time.Duration
	DeliveryMaxAttempts    int
}

type ReconcilerObserver interface {
	RecordOutboxRequeue()
	RecordQueueRepairRecovered()
	RecordQueueRepairRequeued()
	RecordRunRefresh()
	RecordAwaitExpiry()
	RecordDeliveryRetry()
}

type Reconciler struct {
	Repo     ports.Repository
	ACP      ports.ACPBridge
	Config   ReconcilerConfig
	Observer ReconcilerObserver
}

func (r Reconciler) RunOnce(ctx context.Context, limit int) error {
	now := time.Now().UTC()
	if err := r.requeueClaimedOutbox(ctx, now, limit); err != nil {
		return err
	}
	if err := r.repairStuckQueueItems(ctx, now, limit); err != nil {
		return err
	}
	if err := r.refreshStaleRuns(ctx, now, limit); err != nil {
		return err
	}
	if err := r.expireAwaits(ctx, now, limit); err != nil {
		return err
	}
	if err := r.retryStaleDeliveries(ctx, now, limit); err != nil {
		return err
	}
	return nil
}

func (r Reconciler) requeueClaimedOutbox(ctx context.Context, now time.Time, limit int) error {
	items, err := r.Repo.ListStaleClaimedOutbox(ctx, now.Add(-r.Config.OutboxClaimTimeout), limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := r.Repo.RequeueOutbox(ctx, item.ID); err != nil {
			return err
		}
		if r.Observer != nil {
			r.Observer.RecordOutboxRequeue()
		}
		_ = r.Repo.Audit(ctx, domain.AuditEvent{
			ID:            fmt.Sprintf("audit_outbox_requeue_%s_%d", item.ID, now.UnixNano()),
			TenantID:      item.TenantID,
			AggregateType: item.AggregateType,
			AggregateID:   item.AggregateID,
			EventType:     "reconciler.outbox_requeued",
			PayloadJSON:   mustJSON(map[string]any{"outbox_event_id": item.ID}),
			CreatedAt:     now,
		})
	}
	return nil
}

func (r Reconciler) repairStuckQueueItems(ctx context.Context, now time.Time, limit int) error {
	items, err := r.Repo.ListStuckQueueItems(ctx, now.Add(-r.Config.QueueStartingTimeout), limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		session, err := r.Repo.GetSession(ctx, item.SessionID)
		if err != nil {
			return err
		}
		idempotencyKey, err := r.Repo.GetQueueStartIdempotencyKey(ctx, item.ID)
		if err != nil {
			return err
		}
		snapshot, found, err := r.ACP.FindRunByIdempotencyKey(ctx, session, idempotencyKey)
		if err != nil {
			return err
		}
		if !found {
			if err := r.Repo.InTx(ctx, func(ctx context.Context, repo ports.Repository) error {
				if err := repo.UpdateQueueItemStatus(ctx, item.ID, "queued"); err != nil {
					return err
				}
				if err := repo.RequeueQueueStartOutbox(ctx, item.ID, session.TenantID); err != nil {
					return err
				}
				return repo.Audit(ctx, domain.AuditEvent{
					ID:            fmt.Sprintf("audit_queue_repair_requeued_%s_%d", item.ID, now.UnixNano()),
					TenantID:      session.TenantID,
					SessionID:     session.ID,
					AggregateType: "session_queue_item",
					AggregateID:   item.ID,
					EventType:     "reconciler.queue_repair_requeued",
					PayloadJSON:   mustJSON(map[string]any{"queue_item_id": item.ID}),
					CreatedAt:     now,
				})
			}); err != nil {
				return err
			}
			if r.Observer != nil {
				r.Observer.RecordQueueRepairRequeued()
			}
			continue
		}
		run, err := r.Repo.RepairRunFromSnapshot(ctx, item, snapshot)
		if err != nil {
			return err
		}
		if err := r.Repo.UpdateQueueItemStatus(ctx, item.ID, snapshot.Status); err != nil {
			return err
		}
		if err := r.Repo.UpdateRunStatus(ctx, run.ID, snapshot.Status); err != nil {
			return err
		}
		if r.Observer != nil {
			r.Observer.RecordQueueRepairRecovered()
		}
		repairedSession, err := r.Repo.GetSession(ctx, item.SessionID)
		if err != nil {
			return err
		}
		_ = r.Repo.Audit(ctx, domain.AuditEvent{
			ID:            fmt.Sprintf("audit_queue_repair_recovered_%s_%d", item.ID, now.UnixNano()),
			TenantID:      repairedSession.TenantID,
			SessionID:     repairedSession.ID,
			RunID:         run.ID,
			AggregateType: "session_queue_item",
			AggregateID:   item.ID,
			EventType:     "reconciler.queue_repair_recovered",
			PayloadJSON:   mustJSON(map[string]any{"queue_item_id": item.ID, "run_id": run.ID, "acp_run_id": snapshot.ACPRunID}),
			CreatedAt:     now,
		})
	}
	return nil
}

func (r Reconciler) refreshStaleRuns(ctx context.Context, now time.Time, limit int) error {
	runs, err := r.Repo.ListStaleRuns(ctx, now.Add(-r.Config.RunStaleTimeout), limit)
	if err != nil {
		return err
	}
	for _, run := range runs {
		snapshot, err := r.ACP.GetRun(ctx, run.ACPRunID)
		if err != nil {
			return err
		}
		if err := r.Repo.UpdateRunStatus(ctx, run.ID, snapshot.Status); err != nil {
			return err
		}
		session, err := r.Repo.GetSession(ctx, run.SessionID)
		if err != nil {
			return err
		}
		if err := r.Repo.Audit(ctx, domain.AuditEvent{
			ID:            fmt.Sprintf("audit_run_refresh_%s_%d", run.ID, now.UnixNano()),
			TenantID:      session.TenantID,
			SessionID:     session.ID,
			RunID:         run.ID,
			AggregateType: "run",
			AggregateID:   run.ID,
			EventType:     "reconciler.run_refreshed",
			PayloadJSON:   mustJSON(snapshot),
			CreatedAt:     now,
		}); err != nil {
			return err
		}
		if r.Observer != nil {
			r.Observer.RecordRunRefresh()
		}
		if snapshot.Status == "completed" || snapshot.Status == "failed" || snapshot.Status == "canceled" {
			if err := r.Repo.UpdateActiveQueueItemStatus(ctx, run.SessionID, snapshot.Status); err != nil {
				return err
			}
			if _, err := r.Repo.EnqueueNextQueueItem(ctx, run.SessionID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r Reconciler) expireAwaits(ctx context.Context, now time.Time, limit int) error {
	awaits, err := r.Repo.ListExpiredAwaits(ctx, now, limit)
	if err != nil {
		return err
	}
	for _, item := range awaits {
		if err := r.Repo.InTx(ctx, func(ctx context.Context, repo ports.Repository) error {
			if err := repo.ExpireAwait(ctx, item.ID); err != nil {
				return err
			}
			if err := repo.UpdateRunStatus(ctx, item.RunID, "expired"); err != nil {
				return err
			}
			if err := repo.UpdateActiveQueueItemStatus(ctx, item.SessionID, "expired"); err != nil {
				return err
			}
			if _, err := repo.EnqueueNextQueueItem(ctx, item.SessionID); err != nil {
				return err
			}
			session, err := repo.GetSession(ctx, item.SessionID)
			if err != nil {
				return err
			}
			return repo.Audit(ctx, domain.AuditEvent{
				ID:            fmt.Sprintf("audit_await_expire_%s_%d", item.ID, now.UnixNano()),
				TenantID:      session.TenantID,
				SessionID:     session.ID,
				RunID:         item.RunID,
				AwaitID:       item.ID,
				AggregateType: "await",
				AggregateID:   item.ID,
				EventType:     "reconciler.await_expired",
				PayloadJSON:   mustJSON(map[string]any{"expired_at": now}),
				CreatedAt:     now,
			})
		}); err != nil {
			return err
		}
		if r.Observer != nil {
			r.Observer.RecordAwaitExpiry()
		}
	}
	return nil
}

func (r Reconciler) retryStaleDeliveries(ctx context.Context, now time.Time, limit int) error {
	deliveries, err := r.Repo.ListStaleDeliveries(ctx, now.Add(-r.Config.DeliverySendingTimeout), r.Config.DeliveryMaxAttempts, limit)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if err := r.Repo.RetryDelivery(ctx, delivery.ID); err != nil {
			return err
		}
		if r.Observer != nil {
			r.Observer.RecordDeliveryRetry()
		}
	}
	return nil
}

func mustJSON(v any) []byte {
	out, _ := json.Marshal(v)
	return out
}
