package services

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/adapters/db"
	"nexus/internal/domain"
)

func TestReconcilerIntegrationWithPostgres(t *testing.T) {
	if os.Getenv("NEXUS_INTEGRATION_DB") != "1" {
		t.Skip("set NEXUS_INTEGRATION_DB=1 to run Postgres integration tests")
	}

	ctx := context.Background()
	dbURL := startServicesPostgresContainer(t)
	repo, err := db.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repo.Close)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	applyServicesMigrations(t, ctx, pool)

	t.Run("requeues stale outbox", func(t *testing.T) {
		seedReconcilerStaleOutboxFixture(t, ctx, pool)
		reconciler := Reconciler{
			Repo: repo,
			ACP:  workerIntegrationACP{},
			Config: ReconcilerConfig{
				OutboxClaimTimeout:     30 * time.Second,
				QueueStartingTimeout:   time.Hour,
				RunStaleTimeout:        time.Hour,
				DeliverySendingTimeout: time.Hour,
				DeliveryMaxAttempts:    5,
			},
		}
		if err := reconciler.RunOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := pool.QueryRow(ctx, `select status from outbox_events where id='outbox_stale_1'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "queued" {
			t.Fatalf("expected stale outbox to be requeued, got %s", status)
		}
		page, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:      "tenant_default",
			AggregateType: "outbox_event",
			AggregateID:   "delivery_stale_1",
			EventType:     "reconciler.outbox_requeued",
			CursorPage:    domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("expected one outbox requeue audit event, got %+v", page.Items)
		}
	})

	t.Run("refreshes stale run", func(t *testing.T) {
		seedReconcilerStaleRunFixture(t, ctx, pool)
		reconciler := Reconciler{
			Repo: repo,
			ACP: workerIntegrationACP{
				getRun: domain.RunStatusSnapshot{
					ACPRunID: "acp_run_stale_1",
					Status:   "completed",
					Output:   "done",
				},
			},
			Config: ReconcilerConfig{
				OutboxClaimTimeout:     time.Hour,
				QueueStartingTimeout:   time.Hour,
				RunStaleTimeout:        30 * time.Second,
				DeliverySendingTimeout: time.Hour,
				DeliveryMaxAttempts:    5,
			},
		}
		if err := reconciler.RunOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		run, err := repo.GetRun(ctx, "run_stale_1")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "completed" {
			t.Fatalf("expected stale run to be refreshed to completed, got %+v", run)
		}
		queueItem, err := repo.GetQueueItem(ctx, "queue_stale_run_1")
		if err != nil {
			t.Fatal(err)
		}
		if queueItem.Status != "completed" {
			t.Fatalf("expected active queue item to be completed, got %+v", queueItem)
		}
		page, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:   "tenant_default",
			RunID:      "run_stale_1",
			EventType:  "reconciler.run_refreshed",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("expected one run refresh audit event, got %+v", page.Items)
		}
	})

	t.Run("repairs stuck queue item when acp run exists", func(t *testing.T) {
		seedReconcilerStuckQueueFixture(t, ctx, pool, "queue_found_1", false)
		var requestedKey string
		reconciler := Reconciler{
			Repo: repo,
			ACP: workerIntegrationACP{
				findRun: domain.RunStatusSnapshot{
					ACPRunID: "acp_run_found_1",
					Status:   "running",
				},
				findRunFound: true,
				requestedKey: &requestedKey,
			},
			Config: ReconcilerConfig{
				OutboxClaimTimeout:     time.Hour,
				QueueStartingTimeout:   30 * time.Second,
				RunStaleTimeout:        time.Hour,
				DeliverySendingTimeout: time.Hour,
				DeliveryMaxAttempts:    5,
			},
		}
		if err := reconciler.RunOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		queueItem, err := repo.GetQueueItem(ctx, "queue_found_1")
		if err != nil {
			t.Fatal(err)
		}
		if queueItem.Status != "running" {
			t.Fatalf("expected stuck queue item to move to running, got %+v", queueItem)
		}
		run, err := repo.GetRunByACP(ctx, "acp_run_found_1")
		if err != nil {
			t.Fatal(err)
		}
		if run.SessionID != "session_stuck_1" || run.Status != "running" {
			t.Fatalf("unexpected repaired run: %+v", run)
		}
		if requestedKey != "queue_found_1" {
			t.Fatalf("expected queue repair lookup to use queue item idempotency key, got %q", requestedKey)
		}
		page, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:      "tenant_default",
			AggregateType: "session_queue_item",
			AggregateID:   "queue_found_1",
			EventType:     "reconciler.queue_repair_recovered",
			CursorPage:    domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("expected one queue repair recovered audit event, got %+v", page.Items)
		}
	})

	t.Run("requeues stuck queue item when acp run is missing", func(t *testing.T) {
		seedReconcilerStuckQueueFixture(t, ctx, pool, "queue_missing_1", true)
		var requestedKey string
		reconciler := Reconciler{
			Repo: repo,
			ACP: workerIntegrationACP{
				findRunFound: false,
				requestedKey: &requestedKey,
			},
			Config: ReconcilerConfig{
				OutboxClaimTimeout:     time.Hour,
				QueueStartingTimeout:   30 * time.Second,
				RunStaleTimeout:        time.Hour,
				DeliverySendingTimeout: time.Hour,
				DeliveryMaxAttempts:    5,
			},
		}
		if err := reconciler.RunOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		queueItem, err := repo.GetQueueItem(ctx, "queue_missing_1")
		if err != nil {
			t.Fatal(err)
		}
		if queueItem.Status != "queued" {
			t.Fatalf("expected missing ACP run queue item to be requeued, got %+v", queueItem)
		}
		if requestedKey != "queue_missing_1:next" {
			t.Fatalf("expected resumed queue repair lookup to use next-item idempotency key, got %q", requestedKey)
		}
		var outboxStatus string
		if err := pool.QueryRow(ctx, `select status from outbox_events where aggregate_id='queue_missing_1' and event_type='queue.start' order by available_at desc, id desc limit 1`).Scan(&outboxStatus); err != nil {
			t.Fatal(err)
		}
		if outboxStatus != "queued" {
			t.Fatalf("expected queue-start outbox to be requeued, got %s", outboxStatus)
		}
		page, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:      "tenant_default",
			AggregateType: "session_queue_item",
			AggregateID:   "queue_missing_1",
			EventType:     "reconciler.queue_repair_requeued",
			CursorPage:    domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("expected one queue repair requeued audit event, got %+v", page.Items)
		}
	})

	t.Run("expires awaits and retries stale deliveries", func(t *testing.T) {
		seedReconcilerAwaitAndDeliveryFixture(t, ctx, pool)
		reconciler := Reconciler{
			Repo: repo,
			ACP:  workerIntegrationACP{},
			Config: ReconcilerConfig{
				OutboxClaimTimeout:     time.Hour,
				QueueStartingTimeout:   time.Hour,
				RunStaleTimeout:        time.Hour,
				DeliverySendingTimeout: 30 * time.Second,
				DeliveryMaxAttempts:    5,
			},
		}
		if err := reconciler.RunOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		await, err := repo.GetAwait(ctx, "await_expire_1")
		if err != nil {
			t.Fatal(err)
		}
		if await.Status != "expired" {
			t.Fatalf("expected await to be expired, got %+v", await)
		}
		run, err := repo.GetRun(ctx, "run_reconcile_1")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "expired" {
			t.Fatalf("expected awaiting run to be expired, got %+v", run)
		}
		queueItem, err := repo.GetQueueItem(ctx, "queue_reconcile_active_1")
		if err != nil {
			t.Fatal(err)
		}
		if queueItem.Status != "expired" {
			t.Fatalf("expected active queue item to be expired, got %+v", queueItem)
		}
		var nextOutboxCount int
		if err := pool.QueryRow(ctx, `select count(*) from outbox_events where aggregate_id='queue_reconcile_next_1' and event_type='queue.start' and status='queued'`).Scan(&nextOutboxCount); err != nil {
			t.Fatal(err)
		}
		if nextOutboxCount != 1 {
			t.Fatalf("expected one queue.start outbox for next queued item, got %d", nextOutboxCount)
		}
		delivery, err := repo.GetDelivery(ctx, "delivery_retry_1")
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Status != "queued" {
			t.Fatalf("expected stale delivery to be retried back to queued, got %+v", delivery)
		}
		var retryOutboxCount int
		if err := pool.QueryRow(ctx, `select count(*) from outbox_events where aggregate_id='delivery_retry_1' and event_type='delivery.send' and status='queued'`).Scan(&retryOutboxCount); err != nil {
			t.Fatal(err)
		}
		if retryOutboxCount < 1 {
			t.Fatalf("expected retry outbox event for stale delivery, got count=%d", retryOutboxCount)
		}
		awaitAudit, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:   "tenant_default",
			AwaitID:    "await_expire_1",
			EventType:  "reconciler.await_expired",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(awaitAudit.Items) != 1 {
			t.Fatalf("expected one await expiry audit event, got %+v", awaitAudit.Items)
		}
		retryAudit, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:      "tenant_default",
			AggregateType: "outbound_delivery",
			AggregateID:   "delivery_retry_1",
			EventType:     "delivery.retry_requested",
			CursorPage:    domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(retryAudit.Items) != 1 {
			t.Fatalf("expected one delivery retry audit event, got %+v", retryAudit.Items)
		}
	})
}

func seedReconcilerStaleOutboxFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	workerMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)
	ts := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	workerMustExec(t, ctx, pool, `
		insert into sessions (id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key, acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at)
		values ('session_stale_1','tenant_default','user_1','agent_profile_1','slack','slack:C4:T4','acp_default','','agent_a','','per-thread','open',$1,$1,$1)
	`, ts)
	workerMustExec(t, ctx, pool, `
		insert into outbound_deliveries (id, tenant_id, session_id, run_id, await_id, logical_message_id, channel_type, delivery_kind, provider_message_id, provider_request_id, status, attempt_count, last_error, payload_json, created_at, updated_at)
		values ('delivery_stale_1','tenant_default','session_stale_1','run_unused',null,'logical_stale_1','slack','send','','','queued',0,'',$1,$2,$2)
	`, mustJSONBytes(tMap{"channel": "C4", "text": "stale send"}), ts)
	workerMustExec(t, ctx, pool, `
		insert into outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count, claimed_at)
		values ('outbox_stale_1','tenant_default','delivery.send','outbox_event','delivery_stale_1','delivery_stale_1',$1,'processing',$2,1,$2)
	`, []byte(`{}`), ts)
}

func seedReconcilerStaleRunFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	workerMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)
	t1 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	workerMustExec(t, ctx, pool, `
		insert into sessions (id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key, acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at)
		values ('session_stale_run_1','tenant_default','user_1','agent_profile_1','slack','slack:C5:T5','acp_default','','agent_a','acp_session_stale','per-thread','open',$1,$1,$1)
	`, t1)
	workerMustExec(t, ctx, pool, `
		insert into messages (id, tenant_id, session_id, direction, channel_type, channel_message_id, role, text_preview, raw_payload_json, created_at)
		values ('message_stale_run_1','tenant_default','session_stale_run_1','inbound','slack','cm_stale_run_1','user','still running?',$1,$2)
	`, []byte(`{"text":"still running?"}`), t1)
	workerMustExec(t, ctx, pool, `
		insert into session_queue_items (id, tenant_id, session_id, inbound_message_id, queue_position, status, route_decision_json, enqueued_at, started_at, completed_at, expires_at)
		values ('queue_stale_run_1','tenant_default','session_stale_run_1','message_stale_run_1',1,'running',$1,$2,$2,null,$3)
	`, []byte(`{"acp_agent_name":"agent_a","mode":"per-thread"}`), t1, t1.Add(24*time.Hour))
	workerMustExec(t, ctx, pool, `
		insert into runs (id, session_id, acp_run_id, agent_name, mode, status, started_at, completed_at, last_event_at)
		values ('run_stale_1','session_stale_run_1','acp_run_stale_1','agent_a','per-thread','running',$1,null,$1)
	`, t1)
}

func seedReconcilerAwaitAndDeliveryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	workerMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)
	t1 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	t2 := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	workerMustExec(t, ctx, pool, `
		insert into sessions (id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key, acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at)
		values ('session_reconcile_1','tenant_default','user_1','agent_profile_1','slack','slack:C6:T6','acp_default','','agent_a','acp_session_reconcile','per-thread','open',$1,$1,$1)
	`, t1)
	workerMustExec(t, ctx, pool, `
		insert into runs (id, session_id, acp_run_id, agent_name, mode, status, started_at, completed_at, last_event_at)
		values ('run_reconcile_1','session_reconcile_1','acp_run_reconcile_1','agent_a','per-thread','awaiting',$1,null,$1)
	`, t1)
	workerMustExec(t, ctx, pool, `
		insert into messages (id, tenant_id, session_id, direction, channel_type, channel_message_id, role, text_preview, raw_payload_json, created_at)
		values ('message_reconcile_active_1','tenant_default','session_reconcile_1','inbound','slack','cm_reconcile_active_1','user','approve?',$1,$2),
		       ('message_reconcile_next_1','tenant_default','session_reconcile_1','inbound','slack','cm_reconcile_next_1','user','next please',$3,$2)
	`, []byte(`{"text":"approve?"}`), t1, []byte(`{"text":"next please"}`))
	workerMustExec(t, ctx, pool, `
		insert into session_queue_items (id, tenant_id, session_id, inbound_message_id, queue_position, status, route_decision_json, enqueued_at, started_at, completed_at, expires_at)
		values ('queue_reconcile_active_1','tenant_default','session_reconcile_1','message_reconcile_active_1',1,'awaiting',$1,$2,$2,null,$3),
		       ('queue_reconcile_next_1','tenant_default','session_reconcile_1','message_reconcile_next_1',2,'queued',$1,$2,null,null,$3)
	`, []byte(`{"acp_agent_name":"agent_a","mode":"per-thread"}`), t1, t1.Add(24*time.Hour))
	workerMustExec(t, ctx, pool, `
		insert into awaits (id, run_id, session_id, channel_type, status, schema_json, prompt_render_model_json, allowed_responder_ids_json, expires_at, resolved_at)
		values ('await_expire_1','run_reconcile_1','session_reconcile_1','slack','pending',$1,$2,$3,$4,null)
	`, []byte(`{"type":"object"}`), []byte(`{"text":"approve?"}`), []byte(`["user_1"]`), t2)
	workerMustExec(t, ctx, pool, `
		insert into outbound_deliveries (id, tenant_id, session_id, run_id, await_id, logical_message_id, channel_type, delivery_kind, provider_message_id, provider_request_id, status, attempt_count, last_error, payload_json, created_at, updated_at)
		values ('delivery_retry_1','tenant_default','session_reconcile_1','run_reconcile_1',null,'logical_retry_1','slack','send','','','sending',0,'',$1,$2,$3)
	`, mustJSONBytes(tMap{"channel": "C6", "text": "retry me"}), t1, t2)
}

func seedReconcilerStuckQueueFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, queueID string, resumed bool) {
	t.Helper()
	workerMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)
	t1 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	workerMustExec(t, ctx, pool, `
		insert into sessions (id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key, acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at)
		values ('session_stuck_1','tenant_default','user_1','agent_profile_1','slack','slack:C7:T7','acp_default','','agent_a','acp_session_stuck','per-thread','open',$1,$1,$1)
	`, t1)
	workerMustExec(t, ctx, pool, `
		insert into messages (id, tenant_id, session_id, direction, channel_type, channel_message_id, role, text_preview, raw_payload_json, created_at)
		values ('message_stuck_1','tenant_default','session_stuck_1','inbound','slack','cm_stuck_1','user','stuck start',$1,$2)
	`, []byte(`{"text":"stuck start"}`), t1)
	workerMustExec(t, ctx, pool, `
		insert into session_queue_items (id, tenant_id, session_id, inbound_message_id, queue_position, status, route_decision_json, enqueued_at, started_at, completed_at, expires_at)
		values ($1,'tenant_default','session_stuck_1','message_stuck_1',1,'starting',$2,$3,$3,null,$4)
	`, queueID, []byte(`{"acp_agent_name":"agent_a","mode":"per-thread"}`), t1, t1.Add(24*time.Hour))
	outboxID := "outbox_" + queueID
	idempotencyKey := queueID
	if resumed {
		outboxID = "outbox_resume_" + queueID + "_" + t1.Format("20060102150405")
		idempotencyKey = queueID + ":next"
	}
	workerMustExec(t, ctx, pool, `
		insert into outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count, claimed_at)
		values ($1,'tenant_default','queue.start','session_queue_item',$2,$3,$4,'processing',$5,1,$5)
	`, outboxID, queueID, idempotencyKey, []byte(`{}`), t1)
}
