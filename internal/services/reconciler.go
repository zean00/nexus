package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/tracex"
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
	Repo      ports.Repository
	ACP       ports.ACPBridge
	Renderer  ports.Renderer
	Renderers map[string]ports.Renderer
	TenantID  string
	Config    ReconcilerConfig
	Observer  ReconcilerObserver
}

func (r Reconciler) RunOnce(ctx context.Context, limit int) (err error) {
	ctx, end := tracex.StartSpan(ctx, "reconciler.run_once", "limit", limit)
	defer func() { end(err) }()
	now := time.Now().UTC()
	var errs []error
	if err = r.requeueClaimedOutbox(ctx, now, limit); err != nil {
		tracex.Logger(ctx).Error("reconciler.requeue_claimed_outbox_failed", "error", err.Error())
		errs = append(errs, err)
	}
	if err = r.repairStuckQueueItems(ctx, now, limit); err != nil {
		tracex.Logger(ctx).Error("reconciler.repair_stuck_queue_items_failed", "error", err.Error())
		errs = append(errs, err)
	}
	if err = r.refreshStaleRuns(ctx, now, limit); err != nil {
		tracex.Logger(ctx).Error("reconciler.refresh_stale_runs_failed", "error", err.Error())
		errs = append(errs, err)
	}
	if err = r.reconcileVisibleRuntimeEvents(ctx, now, limit); err != nil {
		tracex.Logger(ctx).Error("reconciler.visible_runtime_events_failed", "error", err.Error())
		errs = append(errs, err)
	}
	if err = r.expireAwaits(ctx, now, limit); err != nil {
		tracex.Logger(ctx).Error("reconciler.expire_awaits_failed", "error", err.Error())
		errs = append(errs, err)
	}
	if err = r.retryStaleDeliveries(ctx, now, limit); err != nil {
		tracex.Logger(ctx).Error("reconciler.retry_stale_deliveries_failed", "error", err.Error())
		errs = append(errs, err)
	}
	tracex.Logger(ctx).Info("reconciler.completed", "limit", limit)
	return errors.Join(errs...)
}

func (r Reconciler) rendererFor(channelType string) ports.Renderer {
	if renderer, ok := r.Renderers[channelType]; ok {
		return renderer
	}
	return r.Renderer
}

func (r Reconciler) requeueClaimedOutbox(ctx context.Context, now time.Time, limit int) error {
	items, err := r.Repo.ListStaleClaimedOutbox(ctx, now.Add(-r.Config.OutboxClaimTimeout), limit)
	if err != nil {
		return err
	}
	var errs []error
	for _, item := range items {
		if err := r.Repo.RequeueOutbox(ctx, item.ID); err != nil {
			tracex.Logger(ctx).Error("reconciler.requeue_outbox_item_failed", "outbox_event_id", item.ID, "error", err.Error())
			errs = append(errs, err)
			continue
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
	return errors.Join(errs...)
}

func (r Reconciler) repairStuckQueueItems(ctx context.Context, now time.Time, limit int) error {
	items, err := r.Repo.ListStuckQueueItems(ctx, now.Add(-r.Config.QueueStartingTimeout), limit)
	if err != nil {
		return err
	}
	var errs []error
	for _, item := range items {
		session, err := r.Repo.GetSession(ctx, item.SessionID)
		if err != nil {
			tracex.Logger(ctx).Error("reconciler.load_stuck_queue_session_failed", "queue_item_id", item.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		idempotencyKey, err := r.Repo.GetQueueStartIdempotencyKey(ctx, item.ID)
		if err != nil {
			tracex.Logger(ctx).Error("reconciler.load_queue_idempotency_key_failed", "queue_item_id", item.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if route, routeErr := r.Repo.GetRouteDecision(ctx, item.ID); routeErr == nil {
			session.ACPConnectionID = route.ACPConnectionID
			session.ACPAgentName = route.ACPAgentName
			session.ACPProfileID = route.AgentProfileID
		}
		snapshot, found, err := r.ACP.FindRunByIdempotencyKey(ctx, session, idempotencyKey)
		if err != nil {
			tracex.Logger(ctx).Error("reconciler.find_run_by_idempotency_failed", "queue_item_id", item.ID, "idempotency_key", idempotencyKey, "error", err.Error())
			errs = append(errs, err)
			continue
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
				tracex.Logger(ctx).Error("reconciler.requeue_stuck_queue_item_failed", "queue_item_id", item.ID, "error", err.Error())
				errs = append(errs, err)
				continue
			}
			if r.Observer != nil {
				r.Observer.RecordQueueRepairRequeued()
			}
			continue
		}
		run, err := r.Repo.RepairRunFromSnapshot(ctx, item, snapshot)
		if err != nil {
			tracex.Logger(ctx).Error("reconciler.repair_run_from_snapshot_failed", "queue_item_id", item.ID, "acp_run_id", snapshot.ACPRunID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if err := r.Repo.UpdateQueueItemStatus(ctx, item.ID, snapshot.Status); err != nil {
			tracex.Logger(ctx).Error("reconciler.update_queue_status_failed", "queue_item_id", item.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if err := r.Repo.UpdateRunStatus(ctx, run.ID, snapshot.Status); err != nil {
			tracex.Logger(ctx).Error("reconciler.update_run_status_failed", "run_id", run.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if r.Observer != nil {
			r.Observer.RecordQueueRepairRecovered()
		}
		repairedSession, err := r.Repo.GetSession(ctx, item.SessionID)
		if err != nil {
			tracex.Logger(ctx).Error("reconciler.reload_repaired_session_failed", "queue_item_id", item.ID, "error", err.Error())
			errs = append(errs, err)
			continue
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
	return errors.Join(errs...)
}

func (r Reconciler) refreshStaleRuns(ctx context.Context, now time.Time, limit int) error {
	runs, err := r.Repo.ListStaleRuns(ctx, now.Add(-r.Config.RunStaleTimeout), limit)
	if err != nil {
		return err
	}
	var errs []error
	for _, run := range runs {
		session, err := r.Repo.GetSession(ctx, run.SessionID)
		if err != nil {
			tracex.Logger(ctx).Error("reconciler.load_stale_run_session_failed", "run_id", run.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		session.ACPConnectionID = run.ACPConnectionID
		session.ACPAgentName = run.ACPAgentName
		session.ACPProfileID = session.AgentProfileID
		if run.ACPConnectionID == "" {
			session.ACPProfileID = ""
		}
		var snapshot domain.RunStatusSnapshot
		if scoped, ok := r.ACP.(interface {
			GetRunForSession(context.Context, domain.Session, string) (domain.RunStatusSnapshot, error)
		}); ok {
			snapshot, err = scoped.GetRunForSession(ctx, session, run.ACPRunID)
		} else {
			snapshot, err = r.ACP.GetRun(ctx, run.ACPRunID)
		}
		if err != nil {
			tracex.Logger(ctx).Error("reconciler.fetch_run_snapshot_failed", "run_id", run.ID, "acp_run_id", run.ACPRunID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if err := r.Repo.UpdateRunStatus(ctx, run.ID, snapshot.Status); err != nil {
			tracex.Logger(ctx).Error("reconciler.persist_run_snapshot_failed", "run_id", run.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if snapshot.Status == "completed" && snapshot.Output != "" {
			runEvent := domain.RunEvent{
				RunID:      run.ID,
				MessageKey: run.ACPRunID,
				Status:     "completed",
				Text:       snapshot.Output,
				Artifacts:  snapshot.Artifacts,
			}
			if err := persistRunEvent(ctx, r.Repo, session, runEvent); err != nil {
				tracex.Logger(ctx).Error("reconciler.persist_completed_run_output_failed", "run_id", run.ID, "error", err.Error())
				errs = append(errs, err)
				continue
			}
			renderer := r.rendererFor(session.ChannelType)
			if renderer == nil {
				err := fmt.Errorf("no renderer for channel %s", session.ChannelType)
				tracex.Logger(ctx).Error("reconciler.render_completed_run_failed", "run_id", run.ID, "error", err.Error())
				errs = append(errs, err)
				continue
			}
			deliveries, renderErr := renderer.RenderRunEvent(ctx, session, runEvent)
			if renderErr != nil {
				tracex.Logger(ctx).Error("reconciler.render_completed_run_failed", "run_id", run.ID, "error", renderErr.Error())
				errs = append(errs, renderErr)
				continue
			}
			for _, delivery := range deliveries {
				if err := r.Repo.EnqueueDelivery(ctx, delivery); err != nil && !isDuplicateDeliveryError(err) {
					tracex.Logger(ctx).Error("reconciler.enqueue_completed_run_delivery_failed", "run_id", run.ID, "delivery_id", delivery.ID, "error", err.Error())
					errs = append(errs, err)
					continue
				}
			}
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
			tracex.Logger(ctx).Error("reconciler.audit_run_refresh_failed", "run_id", run.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if r.Observer != nil {
			r.Observer.RecordRunRefresh()
		}
		if snapshot.Status == "completed" || snapshot.Status == "failed" || snapshot.Status == "canceled" {
			if err := r.Repo.UpdateActiveQueueItemStatus(ctx, run.SessionID, snapshot.Status); err != nil {
				tracex.Logger(ctx).Error("reconciler.update_active_queue_status_failed", "run_id", run.ID, "error", err.Error())
				errs = append(errs, err)
				continue
			}
			if _, err := r.Repo.EnqueueNextQueueItem(ctx, run.SessionID); err != nil {
				tracex.Logger(ctx).Error("reconciler.enqueue_next_queue_item_failed", "run_id", run.ID, "error", err.Error())
				errs = append(errs, err)
				continue
			}
		}
	}
	return errors.Join(errs...)
}

func (r Reconciler) reconcileVisibleRuntimeEvents(ctx context.Context, now time.Time, limit int) error {
	lister, ok := r.ACP.(interface {
		ListVisibleEvents(context.Context, domain.Session, int64) ([]domain.VisibleSessionEvent, error)
	})
	if !ok {
		return nil
	}
	scanLimit := limit * 10
	if scanLimit < 100 {
		scanLimit = 100
	}
	var errs []error
	for _, tenantID := range r.visibleRuntimeTenantIDs() {
		cursor := ""
		for {
			page, err := r.Repo.ListSessions(ctx, domain.SessionListQuery{
				TenantID:   tenantID,
				State:      "open",
				CursorPage: domain.CursorPage{After: cursor, Limit: scanLimit},
			})
			if err != nil {
				errs = append(errs, err)
				break
			}
			for _, session := range page.Items {
				if strings.TrimSpace(session.ACPSessionID) == "" || strings.TrimSpace(session.AgentProfileID) == "" {
					continue
				}
				events, err := lister.ListVisibleEvents(ctx, session, 0)
				if err != nil {
					tracex.Logger(ctx).Error("reconciler.list_visible_runtime_events_failed", "session_id", session.ID, "error", err.Error())
					errs = append(errs, err)
					continue
				}
				for _, event := range events {
					if err := r.enqueueVisibleRuntimeEvent(ctx, now, session, event); err != nil {
						tracex.Logger(ctx).Error("reconciler.enqueue_visible_runtime_event_failed", "session_id", session.ID, "event_id", event.ID, "error", err.Error())
						errs = append(errs, err)
						continue
					}
				}
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	}
	return errors.Join(errs...)
}

func (r Reconciler) visibleRuntimeTenantIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, tenantID := range []string{r.TenantID, "tenant_default", "default"} {
		tenantID = strings.TrimSpace(tenantID)
		if tenantID == "" || seen[tenantID] {
			continue
		}
		seen[tenantID] = true
		out = append(out, tenantID)
	}
	return out
}

func (r Reconciler) enqueueVisibleRuntimeEvent(ctx context.Context, now time.Time, session domain.Session, event domain.VisibleSessionEvent) error {
	eventID := strings.TrimSpace(event.ID)
	if eventID == "" || strings.TrimSpace(event.Text) == "" {
		return nil
	}
	runID := "acp_event_" + eventID
	runEvent := domain.RunEvent{
		RunID:      runID,
		MessageKey: eventID,
		Status:     "completed",
		Text:       event.Text,
		Metadata: map[string]any{
			"source":       event.Source,
			"kind":         event.Kind,
			"offset":       event.Offset,
			"execution_id": event.ExecutionID,
		},
	}
	if err := persistRunEvent(ctx, r.Repo, session, runEvent); err != nil && !isDuplicateMessageError(err) {
		return err
	}
	renderer := r.rendererFor(session.ChannelType)
	if renderer == nil {
		return fmt.Errorf("no renderer for channel %s", session.ChannelType)
	}
	deliveries, err := renderer.RenderRunEvent(ctx, session, runEvent)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if _, err := r.Repo.GetDelivery(ctx, delivery.ID); err == nil {
			continue
		}
		if err := r.Repo.EnqueueDelivery(ctx, delivery); err != nil && !isDuplicateDeliveryError(err) {
			return err
		}
		_ = r.Repo.Audit(ctx, domain.AuditEvent{
			ID:            fmt.Sprintf("audit_visible_runtime_delivery_%s_%d", delivery.ID, now.UnixNano()),
			TenantID:      session.TenantID,
			SessionID:     session.ID,
			AggregateType: "outbound_delivery",
			AggregateID:   delivery.ID,
			EventType:     "reconciler.visible_runtime_event_queued",
			PayloadJSON:   mustJSON(map[string]any{"event_id": eventID, "source": event.Source}),
			CreatedAt:     now,
		})
	}
	return nil
}

func isDuplicateMessageError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "unique")
}

func isDuplicateDeliveryError(err error) bool {
	return isDuplicateMessageError(err)
}

func (r Reconciler) expireAwaits(ctx context.Context, now time.Time, limit int) error {
	awaits, err := r.Repo.ListExpiredAwaits(ctx, now, limit)
	if err != nil {
		return err
	}
	var errs []error
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
			tracex.Logger(ctx).Error("reconciler.expire_await_item_failed", "await_id", item.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if r.Observer != nil {
			r.Observer.RecordAwaitExpiry()
		}
	}
	return errors.Join(errs...)
}

func (r Reconciler) retryStaleDeliveries(ctx context.Context, now time.Time, limit int) error {
	deliveries, err := r.Repo.ListStaleDeliveries(ctx, now.Add(-r.Config.DeliverySendingTimeout), r.Config.DeliveryMaxAttempts, limit)
	if err != nil {
		return err
	}
	var errs []error
	for _, delivery := range deliveries {
		if err := r.Repo.RetryDelivery(ctx, delivery.ID); err != nil {
			tracex.Logger(ctx).Error("reconciler.retry_delivery_failed", "delivery_id", delivery.ID, "error", err.Error())
			errs = append(errs, err)
			continue
		}
		if r.Observer != nil {
			r.Observer.RecordDeliveryRetry()
		}
	}
	return errors.Join(errs...)
}

func mustJSON(v any) []byte {
	out, _ := json.Marshal(v)
	return out
}
