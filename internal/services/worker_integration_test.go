package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/adapters/db"
	"nexus/internal/domain"
	"nexus/internal/ports"
)

type workerIntegrationACP struct {
	startRun     domain.Run
	startEvents  []domain.RunEvent
	resumeEvents []domain.RunEvent
	getRun       domain.RunStatusSnapshot
	findRun      domain.RunStatusSnapshot
	findRunFound bool
	requestedKey *string
}

func (a workerIntegrationACP) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) {
	return nil, nil
}

func (a workerIntegrationACP) EnsureSession(context.Context, domain.Session) (string, error) {
	return "acp_session_worker", nil
}

func (a workerIntegrationACP) StartRun(context.Context, domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	return a.startRun, a.startEvents, nil
}

func (a workerIntegrationACP) ResumeRun(context.Context, domain.Await, []byte) ([]domain.RunEvent, error) {
	return a.resumeEvents, nil
}

func (a workerIntegrationACP) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return a.getRun, nil
}

func (a workerIntegrationACP) FindRunByIdempotencyKey(ctx context.Context, _ domain.Session, key string) (domain.RunStatusSnapshot, bool, error) {
	if a.requestedKey != nil {
		*a.requestedKey = key
	}
	return a.findRun, a.findRunFound, nil
}

func (a workerIntegrationACP) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return a.findRun, a.findRunFound, nil
}

func (a workerIntegrationACP) CancelRun(context.Context, domain.Run) error { return nil }

type workerIntegrationRenderer struct{}

func (workerIntegrationRenderer) RenderRunEvent(_ context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error) {
	var payload []byte
	switch session.ChannelType {
	case "telegram":
		payload = mustJSONBytes(tMap{"chat_id": "chat_worker", "text": evt.Text})
	default:
		payload = mustJSONBytes(tMap{"channel": "C_worker", "text": evt.Text})
	}
	return []domain.OutboundDelivery{{
		ID:               "delivery_resume_rendered_1",
		TenantID:         session.TenantID,
		SessionID:        session.ID,
		RunID:            evt.RunID,
		ChannelType:      session.ChannelType,
		DeliveryKind:     "send",
		Status:           "queued",
		LogicalMessageID: "logical_resume_rendered_1",
		PayloadJSON:      payload,
	}}, nil
}

type workerIntegrationChannel struct {
	sent []domain.OutboundDelivery
}

func (*workerIntegrationChannel) Channel() string { return "slack" }
func (*workerIntegrationChannel) VerifyInbound(context.Context, *http.Request, []byte) error {
	return nil
}
func (*workerIntegrationChannel) ParseInbound(context.Context, *http.Request, []byte, string) (domain.CanonicalInboundEvent, error) {
	return domain.CanonicalInboundEvent{}, nil
}
func (c *workerIntegrationChannel) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	c.sent = append(c.sent, delivery)
	return domain.DeliveryResult{ProviderMessageID: "provider_msg_1", ProviderRequestID: "provider_req_1"}, nil
}
func (c *workerIntegrationChannel) SendAwaitPrompt(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return c.SendMessage(ctx, delivery)
}

type tMap map[string]any

func TestWorkerServiceIntegrationWithPostgres(t *testing.T) {
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

	t.Run("delivery-send", func(t *testing.T) {
		seedWorkerDeliveryFixture(t, ctx, pool)
		channel := &workerIntegrationChannel{}
		worker := WorkerService{
			Repo:     repo,
			ACP:      workerIntegrationACP{},
			Renderer: workerIntegrationRenderer{},
			Channel:  channel,
			Channels: map[string]ports.ChannelAdapter{"slack": channel},
		}
		if err := worker.ProcessOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		delivery, err := repo.GetDelivery(ctx, "delivery_worker_1")
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Status != "sent" || delivery.ProviderMessageID != "provider_msg_1" || delivery.ProviderRequestID != "provider_req_1" {
			t.Fatalf("unexpected sent delivery: %+v", delivery)
		}
		if len(channel.sent) != 1 || channel.sent[0].ID != "delivery_worker_1" {
			t.Fatalf("unexpected channel sends: %+v", channel.sent)
		}
		var outboxStatus string
		if err := pool.QueryRow(ctx, `select status from outbox_events where id='outbox_delivery_worker_1'`).Scan(&outboxStatus); err != nil {
			t.Fatal(err)
		}
		if outboxStatus != "processed" {
			t.Fatalf("expected delivery outbox to be processed, got %s", outboxStatus)
		}
	})

	t.Run("queue-start", func(t *testing.T) {
		seedWorkerQueueStartFixture(t, ctx, pool)
		worker := WorkerService{
			Repo: repo,
			ACP: workerIntegrationACP{
				startRun: domain.Run{
					ID:          "run_start_worker_1",
					SessionID:   "session_start_worker_1",
					ACPRunID:    "acp_run_start_1",
					Status:      "running",
					StartedAt:   time.Now().UTC().Add(-30 * time.Second),
					LastEventAt: time.Now().UTC().Add(-30 * time.Second),
				},
				startEvents: []domain.RunEvent{{
					RunID:  "run_start_worker_1",
					Status: "completed",
					Text:   "started and completed",
				}},
			},
			Renderer: workerIntegrationRenderer{},
			Renderers: map[string]ports.Renderer{
				"slack": workerIntegrationRenderer{},
			},
		}
		if err := worker.ProcessOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		run, err := repo.GetRun(ctx, "run_start_worker_1")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "completed" || run.ACPRunID != "acp_run_start_1" {
			t.Fatalf("unexpected created run: %+v", run)
		}
		session, err := repo.GetSession(ctx, "session_start_worker_1")
		if err != nil {
			t.Fatal(err)
		}
		if session.ACPSessionID != "acp_session_worker" {
			t.Fatalf("expected persisted ACP session id, got %+v", session)
		}
		queueItem, err := repo.GetQueueItem(ctx, "queue_start_worker_1")
		if err != nil {
			t.Fatal(err)
		}
		if queueItem.Status != "completed" {
			t.Fatalf("expected queue item to be completed, got %+v", queueItem)
		}
		messages, err := repo.ListMessages(ctx, domain.MessageListQuery{
			TenantID:   "tenant_default",
			SessionID:  "session_start_worker_1",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(messages.Items) != 2 || messages.Items[0].Text != "started and completed" {
			t.Fatalf("unexpected persisted messages after queue start: %+v", messages.Items)
		}
		deliveries, err := repo.ListDeliveries(ctx, domain.DeliveryListQuery{
			TenantID:   "tenant_default",
			SessionID:  "session_start_worker_1",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries.Items) != 1 || deliveries.Items[0].Status != "queued" {
			t.Fatalf("unexpected deliveries after queue start: %+v", deliveries.Items)
		}
		var outboxStatus string
		if err := pool.QueryRow(ctx, `select status from outbox_events where id='outbox_queue_start_worker_1'`).Scan(&outboxStatus); err != nil {
			t.Fatal(err)
		}
		if outboxStatus != "processed" {
			t.Fatalf("expected queue start outbox to be processed, got %s", outboxStatus)
		}
	})

	t.Run("await-resume", func(t *testing.T) {
		seedWorkerAwaitResumeFixture(t, ctx, pool)
		worker := WorkerService{
			Repo: repo,
			ACP: workerIntegrationACP{
				resumeEvents: []domain.RunEvent{{
					RunID:  "run_resume_worker_1",
					Status: "completed",
					Text:   "resume complete",
				}},
			},
			Renderer: workerIntegrationRenderer{},
			Renderers: map[string]ports.Renderer{
				"slack": workerIntegrationRenderer{},
			},
			Channel: &workerIntegrationChannel{},
		}
		if err := worker.ProcessOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		run, err := repo.GetRun(ctx, "run_resume_worker_1")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "completed" {
			t.Fatalf("expected resumed run to be completed, got %+v", run)
		}
		queueItem, err := repo.GetQueueItem(ctx, "queue_resume_worker_1")
		if err != nil {
			t.Fatal(err)
		}
		if queueItem.Status != "completed" {
			t.Fatalf("expected active queue item to be completed, got %+v", queueItem)
		}
		messages, err := repo.ListMessages(ctx, domain.MessageListQuery{
			TenantID:   "tenant_default",
			SessionID:  "session_resume_worker_1",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(messages.Items) != 2 || messages.Items[0].Text != "resume complete" {
			t.Fatalf("unexpected persisted messages after resume: %+v", messages.Items)
		}
		deliveries, err := repo.ListDeliveries(ctx, domain.DeliveryListQuery{
			TenantID:   "tenant_default",
			SessionID:  "session_resume_worker_1",
			CursorPage: domain.CursorPage{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries.Items) != 1 || deliveries.Items[0].Status != "queued" {
			t.Fatalf("unexpected deliveries after resume: %+v", deliveries.Items)
		}
		var outboxStatus string
		if err := pool.QueryRow(ctx, `select status from outbox_events where id='outbox_await_resume_worker_1'`).Scan(&outboxStatus); err != nil {
			t.Fatal(err)
		}
		if outboxStatus != "processed" {
			t.Fatalf("expected await resume outbox to be processed, got %s", outboxStatus)
		}
	})
}

func startServicesPostgresContainer(t *testing.T) string {
	t.Helper()
	port := servicesFreePort(t)
	name := fmt.Sprintf("nexus-worker-test-pg-%d", time.Now().UnixNano())
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
	waitForServicesPostgres(t, dbURL)
	return dbURL
}

func waitForServicesPostgres(t *testing.T, dbURL string) {
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

func applyServicesMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	dir := filepath.Join("..", "..", "migrations")
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
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("failed applying migration %s: %v", name, err)
		}
	}
}

func seedWorkerDeliveryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	workerMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)
	ts := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	workerMustExec(t, ctx, pool, `
		insert into sessions (id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key, acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at)
		values ('session_worker_1','tenant_default','user_1','agent_profile_1','slack','slack:C1:T1','acp_default','','agent_a','acp_session_1','per-thread','open',$1,$1,$1)
	`, ts)
	workerMustExec(t, ctx, pool, `
		insert into outbound_deliveries (id, tenant_id, session_id, run_id, await_id, logical_message_id, channel_type, delivery_kind, provider_message_id, provider_request_id, status, attempt_count, last_error, payload_json, created_at, updated_at)
		values ('delivery_worker_1','tenant_default','session_worker_1','run_unused',null,'logical_worker_1','slack','send','','','queued',0,'',$1,$2,$2)
	`, mustJSONBytes(tMap{"channel": "C1", "text": "hello from worker"}), ts)
	workerMustExec(t, ctx, pool, `
		insert into outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count)
		values ('outbox_delivery_worker_1','tenant_default','delivery.send','outbound_delivery','delivery_worker_1','delivery_worker_1',$1,'queued',$2,0)
	`, []byte(`{}`), ts)
}

func seedWorkerAwaitResumeFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	workerMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)
	t1 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	t2 := time.Now().UTC().Add(-1 * time.Minute).Truncate(time.Second)
	workerMustExec(t, ctx, pool, `
		insert into sessions (id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key, acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at)
		values ('session_resume_worker_1','tenant_default','user_1','agent_profile_1','slack','slack:C2:T2','acp_default','','agent_a','acp_session_resume','per-thread','open',$1,$1,$1)
	`, t1)
	workerMustExec(t, ctx, pool, `
		insert into messages (id, tenant_id, session_id, direction, channel_type, channel_message_id, role, text_preview, raw_payload_json, created_at)
		values ('message_resume_in_1','tenant_default','session_resume_worker_1','inbound','slack','cm_resume_1','user','resume please',$1,$2)
	`, []byte(`{"text":"resume please"}`), t1)
	workerMustExec(t, ctx, pool, `
		insert into session_queue_items (id, tenant_id, session_id, inbound_message_id, queue_position, status, route_decision_json, enqueued_at, started_at, completed_at, expires_at)
		values ('queue_resume_worker_1','tenant_default','session_resume_worker_1','message_resume_in_1',1,'awaiting',$1,$2,$2,null,$3)
	`, []byte(`{"acp_agent_name":"agent_a"}`), t1, t2)
	workerMustExec(t, ctx, pool, `
		insert into runs (id, session_id, acp_run_id, agent_name, mode, status, started_at, completed_at, last_event_at)
		values ('run_resume_worker_1','session_resume_worker_1','acp_run_resume_1','agent_a','per-thread','awaiting',$1,null,$1)
	`, t1)
	workerMustExec(t, ctx, pool, `
		insert into awaits (id, run_id, session_id, channel_type, status, schema_json, prompt_render_model_json, allowed_responder_ids_json, expires_at, resolved_at)
		values ('await_resume_worker_1','run_resume_worker_1','session_resume_worker_1','slack','pending',$1,$2,$3,$4,null)
	`, []byte(`{"type":"object"}`), []byte(`{"text":"confirm?"}`), []byte(`["user_1"]`), t2)
	workerMustExec(t, ctx, pool, `
		insert into outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count)
		values ('outbox_await_resume_worker_1','tenant_default','await.resume','await','await_resume_worker_1','await_resume_worker_1',$1,'queued',$2,0)
	`, mustJSONBytes(domain.ResumeRequest{AwaitID: "await_resume_worker_1", Payload: []byte(`{"choice":"yes"}`)}), t2)
}

func seedWorkerQueueStartFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	workerMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)
	t1 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	t2 := time.Now().UTC().Add(-1 * time.Minute).Truncate(time.Second)
	workerMustExec(t, ctx, pool, `
		insert into sessions (id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key, acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at)
		values ('session_start_worker_1','tenant_default','user_1','agent_profile_1','slack','slack:C3:T3','acp_default','','agent_a','','per-thread','open',$1,$1,$1)
	`, t1)
	workerMustExec(t, ctx, pool, `
		insert into messages (id, tenant_id, session_id, direction, channel_type, channel_message_id, role, text_preview, raw_payload_json, created_at)
		values ('message_start_in_1','tenant_default','session_start_worker_1','inbound','slack','cm_start_1','user','start please',$1,$2)
	`, []byte(`{"text":"start please"}`), t1)
	workerMustExec(t, ctx, pool, `
		insert into session_queue_items (id, tenant_id, session_id, inbound_message_id, queue_position, status, route_decision_json, enqueued_at, started_at, completed_at, expires_at)
		values ('queue_start_worker_1','tenant_default','session_start_worker_1','message_start_in_1',1,'queued',$1,$2,null,null,$3)
	`, []byte(`{"acp_agent_name":"agent_a","mode":"per-thread"}`), t1, t2)
	workerMustExec(t, ctx, pool, `
		insert into outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count)
		values ('outbox_queue_start_worker_1','tenant_default','queue.start','session_queue_item','queue_start_worker_1','queue_start_worker_1',$1,'queued',$2,0)
	`, []byte(`{}`), t2)
}

func workerMustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec failed: %v\nsql: %s", err, sql)
	}
}

func servicesFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func mustJSONBytes(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
