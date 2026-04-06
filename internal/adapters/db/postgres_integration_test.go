package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
)

func TestPostgresRepositoryIntegrationAdminQueries(t *testing.T) {
	if os.Getenv("NEXUS_INTEGRATION_DB") != "1" {
		t.Skip("set NEXUS_INTEGRATION_DB=1 to run Postgres integration tests")
	}

	ctx := context.Background()
	dbURL := startPostgresContainer(t)

	repo, err := New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repo.Close)

	applyAllMigrations(t, ctx, repo)
	seedAdminQueryFixture(t, ctx, repo)

	t.Run("sessions", func(t *testing.T) {
		query := domain.SessionListQuery{
			TenantID:    "tenant_default",
			State:       "open",
			OwnerUserID: "user_1",
			CursorPage:  domain.CursorPage{Limit: 1},
		}
		page, err := repo.ListSessions(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "session_1" || page.NextCursor == "" {
			t.Fatalf("unexpected sessions page: %+v", page)
		}
		count, err := repo.CountSessions(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("expected session count=2, got %d", count)
		}
	})

	t.Run("messages", func(t *testing.T) {
		query := domain.MessageListQuery{
			TenantID:   "tenant_default",
			Contains:   "hello",
			CursorPage: domain.CursorPage{Limit: 1},
		}
		page, err := repo.ListMessages(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].MessageID != "message_3" || page.NextCursor == "" {
			t.Fatalf("unexpected messages page: %+v", page)
		}
		count, err := repo.CountMessages(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("expected message count=2, got %d", count)
		}
	})

	t.Run("artifacts", func(t *testing.T) {
		query := domain.ArtifactListQuery{
			TenantID:     "tenant_default",
			NameContains: "report",
			CursorPage:   domain.CursorPage{Limit: 10},
		}
		page, err := repo.ListArtifacts(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "artifact_1" {
			t.Fatalf("unexpected artifacts page: %+v", page)
		}
		count, err := repo.CountArtifacts(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected artifact count=1, got %d", count)
		}
	})

	t.Run("runs", func(t *testing.T) {
		query := domain.RunListQuery{
			TenantID:   "tenant_default",
			Status:     "completed",
			SessionID:  "session_1",
			CursorPage: domain.CursorPage{Limit: 10},
		}
		page, err := repo.ListRuns(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "run_1" {
			t.Fatalf("unexpected runs page: %+v", page)
		}
		count, err := repo.CountRuns(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected run count=1, got %d", count)
		}
	})

	t.Run("deliveries", func(t *testing.T) {
		query := domain.DeliveryListQuery{
			TenantID:     "tenant_default",
			Status:       "sent",
			SessionID:    "session_1",
			DeliveryKind: "send",
			CursorPage:   domain.CursorPage{Limit: 10},
		}
		page, err := repo.ListDeliveries(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "delivery_1" {
			t.Fatalf("unexpected deliveries page: %+v", page)
		}
		count, err := repo.CountDeliveries(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected delivery count=1, got %d", count)
		}
	})

	t.Run("awaits", func(t *testing.T) {
		query := domain.AwaitListQuery{
			TenantID:   "tenant_default",
			Status:     "pending",
			SessionID:  "session_1",
			CursorPage: domain.CursorPage{Limit: 10},
		}
		page, err := repo.ListAwaits(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "await_1" {
			t.Fatalf("unexpected awaits page: %+v", page)
		}
		count, err := repo.CountAwaits(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected await count=1, got %d", count)
		}
	})

	t.Run("audit", func(t *testing.T) {
		query := domain.AuditEventListQuery{
			TenantID:      "tenant_default",
			AggregateType: "telegram_user",
			AggregateID:   "123",
			CursorPage:    domain.CursorPage{Limit: 1},
		}
		page, err := repo.ListAuditEvents(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "audit_2" || page.NextCursor == "" {
			t.Fatalf("unexpected audit page: %+v", page)
		}
		count, err := repo.CountAuditEvents(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("expected audit count=2, got %d", count)
		}
	})

	t.Run("telegram-access", func(t *testing.T) {
		pendingPage, err := repo.ListTelegramUserAccessPage(ctx, domain.TelegramUserAccessListQuery{
			TenantID:   "tenant_default",
			Status:     "pending",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(pendingPage.Items) != 1 || pendingPage.Items[0].TelegramUserID != "456" {
			t.Fatalf("unexpected pending telegram users page: %+v", pendingPage)
		}

		allPage, err := repo.ListTelegramUserAccessPage(ctx, domain.TelegramUserAccessListQuery{
			TenantID:   "tenant_default",
			CursorPage: domain.CursorPage{Limit: 2},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(allPage.Items) != 2 || allPage.NextCursor == "" {
			t.Fatalf("unexpected all telegram users page: %+v", allPage)
		}
		count, err := repo.CountTelegramUserAccess(ctx, "tenant_default", "")
		if err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Fatalf("expected telegram user count=3, got %d", count)
		}
	})

	t.Run("telegram-access-transitions", func(t *testing.T) {
		entry, err := repo.RequestTelegramAccess(ctx, domain.TelegramUserAccess{
			TenantID:       "tenant_default",
			TelegramUserID: "999",
			DisplayName:    "dave",
			AddedBy:        "self",
		})
		if err != nil {
			t.Fatal(err)
		}
		if entry.Status != "pending" || entry.Allowed || entry.RequestedAt.IsZero() {
			t.Fatalf("unexpected requested telegram access entry: %+v", entry)
		}

		resolved, err := repo.ResolveTelegramAccessRequest(ctx, "tenant_default", "999", "approved", "admin")
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Status != "approved" || !resolved.Allowed || resolved.DecidedAt == nil || resolved.DecidedAt.IsZero() {
			t.Fatalf("unexpected resolved telegram access entry: %+v", resolved)
		}

		if _, err := repo.ResolveTelegramAccessRequest(ctx, "tenant_default", "999", "denied", "admin"); !errors.Is(err, domain.ErrTelegramAccessRequestNotPending) {
			t.Fatalf("expected second resolution to fail as not pending, got %v", err)
		}
		if _, err := repo.RequestTelegramAccess(ctx, domain.TelegramUserAccess{
			TenantID:       "tenant_default",
			TelegramUserID: "999",
		}); !errors.Is(err, domain.ErrTelegramAccessRequestAlreadyFinal) {
			t.Fatalf("expected approved telegram user to reject new request, got %v", err)
		}
	})

	t.Run("telegram-upsert-delete", func(t *testing.T) {
		err := repo.UpsertTelegramUserAccess(ctx, domain.TelegramUserAccess{
			TenantID:       "tenant_default",
			TelegramUserID: "321",
			DisplayName:    "eve",
			Allowed:        false,
			Status:         "denied",
			AddedBy:        "admin",
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetTelegramUserAccess(ctx, "tenant_default", "321")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "denied" || got.DecidedAt == nil || got.DecidedAt.IsZero() {
			t.Fatalf("unexpected upserted telegram user: %+v", got)
		}
		if err := repo.DeleteTelegramUserAccess(ctx, "tenant_default", "321"); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.GetTelegramUserAccess(ctx, "tenant_default", "321"); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("expected deleted telegram user lookup to return no rows, got %v", err)
		}
	})

	t.Run("notification-sessions-include-surface-and-tenant", func(t *testing.T) {
		sessionA, err := repo.EnsureNotificationSession(ctx, "tenant_default", "telegram", "surface_a", "123")
		if err != nil {
			t.Fatal(err)
		}
		sessionB, err := repo.EnsureNotificationSession(ctx, "tenant_default", "telegram", "surface_b", "123")
		if err != nil {
			t.Fatal(err)
		}
		sessionC, err := repo.EnsureNotificationSession(ctx, "tenant_other", "telegram", "surface_a", "123")
		if err != nil {
			t.Fatal(err)
		}
		if sessionA.ID == sessionB.ID || sessionA.ID == sessionC.ID || sessionB.ID == sessionC.ID {
			t.Fatalf("expected notification sessions to remain distinct across surface and tenant: %+v %+v %+v", sessionA, sessionB, sessionC)
		}
	})

	t.Run("run-cancel-and-delivery-transitions", func(t *testing.T) {
		if err := repo.ForceCancelRun(ctx, "run_2"); err != nil {
			t.Fatal(err)
		}
		run, err := repo.GetRun(ctx, "run_2")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "canceled" {
			t.Fatalf("expected run_2 to be canceled, got %+v", run)
		}
		auditPage, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:   "tenant_default",
			RunID:      "run_2",
			EventType:  "run.force_canceled",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(auditPage.Items) != 1 {
			t.Fatalf("expected one run cancel audit event, got %+v", auditPage.Items)
		}

		if err := repo.MarkDeliverySending(ctx, "delivery_2"); err != nil {
			t.Fatal(err)
		}
		delivery, err := repo.GetDelivery(ctx, "delivery_2")
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Status != "sending" {
			t.Fatalf("expected delivery_2 sending state, got %+v", delivery)
		}

		if err := repo.MarkDeliverySent(ctx, "delivery_2", domain.DeliveryResult{
			ProviderMessageID: "99",
			ProviderRequestID: "req_sent",
		}); err != nil {
			t.Fatal(err)
		}
		delivery, err = repo.GetDelivery(ctx, "delivery_2")
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Status != "sent" || delivery.ProviderMessageID != "99" || delivery.ProviderRequestID != "req_sent" {
			t.Fatalf("unexpected sent delivery state: %+v", delivery)
		}

		if err := repo.MarkDeliveryFailed(ctx, "delivery_3", errors.New("network")); err != nil {
			t.Fatal(err)
		}
		delivery, err = repo.GetDelivery(ctx, "delivery_3")
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Status != "failed" || delivery.AttemptCount != 1 || delivery.LastError != "network" {
			t.Fatalf("unexpected failed delivery state: %+v", delivery)
		}

		if err := repo.RetryDelivery(ctx, "delivery_3"); err != nil {
			t.Fatal(err)
		}
		if err := repo.RetryDelivery(ctx, "delivery_3"); err != nil {
			t.Fatal(err)
		}
		delivery, err = repo.GetDelivery(ctx, "delivery_3")
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Status != "queued" {
			t.Fatalf("expected delivery_3 to be requeued, got %+v", delivery)
		}
		retryAudit, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
			TenantID:   "tenant_default",
			SessionID:  "session_3",
			EventType:  "delivery.retry_requested",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(retryAudit.Items) != 2 {
			t.Fatalf("expected two delivery retry audit events, got %+v", retryAudit.Items)
		}
		var retryOutboxCount int
		if err := repo.queryRow(ctx, `select count(*) from outbox_events where aggregate_id='delivery_3' and event_type='delivery.send' and id like 'outbox_retry_%'`).Scan(&retryOutboxCount); err != nil {
			t.Fatal(err)
		}
		if retryOutboxCount != 2 {
			t.Fatalf("expected two retry outbox rows, got %d", retryOutboxCount)
		}
	})
}

func startPostgresContainer(t *testing.T) string {
	t.Helper()

	port := freePort(t)
	name := fmt.Sprintf("nexus-test-pg-%d", time.Now().UnixNano())
	cmd := exec.Command(
		"docker", "run", "-d", "--rm",
		"--name", name,
		"-e", "POSTGRES_PASSWORD=postgres",
		"-e", "POSTGRES_DB=nexus_test",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"postgres:16",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to start postgres container: %v\n%s", err, string(out))
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})

	dbURL := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/nexus_test?sslmode=disable", port)
	waitForPostgres(t, dbURL)
	return dbURL
}

func waitForPostgres(t *testing.T, dbURL string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pool, err := pgxpool.New(ctx, dbURL)
		if err == nil {
			err = pool.Ping(ctx)
			pool.Close()
		}
		cancel()
		if err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("postgres container did not become ready in time")
}

func applyAllMigrations(t *testing.T, ctx context.Context, repo *PostgresRepository) {
	t.Helper()

	dir := filepath.Join("..", "..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	slices.Sort(files)
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.exec(ctx, string(raw)); err != nil {
			t.Fatalf("failed applying migration %s: %v", name, err)
		}
	}
}

func seedAdminQueryFixture(t *testing.T, ctx context.Context, repo *PostgresRepository) {
	t.Helper()

	t0 := time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 4, 6, 11, 0, 0, 0, time.UTC)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := repo.exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec failed: %v\nsql: %s", err, sql)
		}
	}

	mustExec(`
		insert into sessions (id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key, acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at)
		values
			('session_1','tenant_default','user_1','agent_profile_1','slack','slack:C1:thread1','acp_default','','agent_a','acp_session_1','per-thread','open',$1,$1,$3),
			('session_2','tenant_default','user_2','agent_profile_1','telegram','telegram:dm:2','acp_default','','agent_a','acp_session_2','per-thread','archived',$2,$2,$2),
			('session_3','tenant_default','user_1','agent_profile_1','telegram','telegram:dm:3','acp_default','','agent_b','acp_session_3','per-thread','open',$3,$3,$2)
	`, t1, t2, t3)

	mustExec(`
		insert into messages (id, tenant_id, session_id, direction, channel_type, channel_message_id, role, text_preview, raw_payload_json, created_at)
		values
			('message_1','tenant_default','session_1','inbound','slack','cm_1','user','hello deploy',$1,$2),
			('message_2','tenant_default','session_1','outbound','slack','cm_2','assistant','done result',$3,$4),
			('message_3','tenant_default','session_3','inbound','telegram','cm_3','user','hello telegram',$5,$6)
	`, []byte(`{"text":"hello deploy"}`), t2, []byte(`{"text":"done result"}`), t2, []byte(`{"text":"hello telegram"}`), t3)

	mustExec(`
		insert into artifacts (id, message_id, direction, name, mime_type, size_bytes, sha256, storage_uri, created_at)
		values ('artifact_1','message_2','outbound','report.txt','text/plain',42,'sha256-report','file:///tmp/report.txt',$1)
	`, t3)

	mustExec(`
		insert into runs (id, session_id, acp_run_id, agent_name, mode, status, started_at, completed_at, last_event_at)
		values
			('run_1','session_1','acp_run_1','agent_a','per-thread','completed',$1,$2,$2),
			('run_2','session_3','acp_run_2','agent_b','per-thread','running',$2,null,$3)
	`, t2, t3, t3)

	mustExec(`
		insert into awaits (id, run_id, session_id, channel_type, status, schema_json, prompt_render_model_json, allowed_responder_ids_json, expires_at, resolved_at)
		values ('await_1','run_1','session_1','slack','pending',$1,$2,$3,$4,null)
	`, []byte(`{"type":"object"}`), []byte(`{"text":"confirm?"}`), []byte(`["user_1"]`), t3)

	mustExec(`
		insert into outbound_deliveries (id, tenant_id, session_id, run_id, await_id, logical_message_id, channel_type, delivery_kind, provider_message_id, provider_request_id, status, attempt_count, last_error, payload_json, created_at, updated_at)
		values
			('delivery_1','tenant_default','session_1','run_1',null,'logical_status_1','slack','send','111.222','req_1','sent',1,'',$1,$2,$3),
			('delivery_2','tenant_default','session_3','run_2',null,'logical_status_2','telegram','send','42','req_2','queued',0,'',$4,$3,$3),
			('delivery_3','tenant_default','session_3','run_2',null,'logical_status_3','telegram','send','','','queued',0,'',$5,$2,$2)
	`, []byte(`{"channel":"C1","text":"done"}`), t2, t3, []byte(`{"chat_id":"3","text":"pending"}`), []byte(`{"chat_id":"3","text":"retry me"}`))

	mustExec(`
		insert into audit_events (id, tenant_id, session_id, run_id, await_id, aggregate_type, aggregate_id, event_type, payload_json, created_at)
		values
			('audit_1','tenant_default','','','','telegram_user','123','telegram.access_requested',$1,$2),
			('audit_2','tenant_default','','','','telegram_user','123','admin.telegram_request_resolved',$3,$4),
			('audit_3','tenant_default','session_1','run_1','','channel_surface_state','telegram:123','admin.surface_session_switched',$5,$6)
	`, []byte(`{"provider_event_id":"evt_1"}`), t1, []byte(`{"status":"approved"}`), t3, []byte(`{"alias_or_id":"deploy"}`), t2)

	mustExec(`
		insert into telegram_user_access (tenant_id, telegram_user_id, display_name, allowed, status, added_by, requested_at, decided_at, created_at, updated_at)
		values
			('tenant_default','123','alice',true,'approved','admin',$1,$3,$1,$3),
			('tenant_default','456','bob',false,'pending','self',$2,null,$2,$2),
			('tenant_default','789','carol',false,'denied','admin',$1,$2,$1,$2)
	`, t0, t1, t3)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
