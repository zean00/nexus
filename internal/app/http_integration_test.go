package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/adapters/db"
	"nexus/internal/adapters/slack"
	"nexus/internal/adapters/storage"
	"nexus/internal/adapters/telegram"
	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/services"
)

type appIntegrationACP struct {
	startRun    string
	startStatus string
	startEvents []domain.RunEvent
	resumeEvents []domain.RunEvent
}

func (appIntegrationACP) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) {
	return nil, nil
}

func (appIntegrationACP) EnsureSession(context.Context, domain.Session) (string, error) {
	return "acp_session_app_integration", nil
}

func (a appIntegrationACP) StartRun(_ context.Context, req domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	now := time.Now().UTC()
	runID := a.startRun
	if runID == "" {
		runID = "run_app_1"
	}
	status := a.startStatus
	if status == "" {
		status = "running"
	}
	events := a.startEvents
	if len(events) == 0 {
		events = []domain.RunEvent{{
			RunID:  runID,
			Status: "completed",
			Text:   "integration complete",
		}}
	}
	return domain.Run{
			ID:          runID,
			SessionID:   req.Session.ID,
			ACPRunID:    "acp_" + runID,
			Status:      status,
			StartedAt:   now,
			LastEventAt: now,
		}, events, nil
}

func (a appIntegrationACP) ResumeRun(context.Context, domain.Await, []byte) ([]domain.RunEvent, error) {
	return a.resumeEvents, nil
}

func (appIntegrationACP) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return domain.RunStatusSnapshot{}, nil
}

func (appIntegrationACP) FindRunByIdempotencyKey(context.Context, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}

func (appIntegrationACP) CancelRun(context.Context, domain.Run) error { return nil }

type appIntegrationChannel struct {
	sent []domain.OutboundDelivery
}

func (*appIntegrationChannel) Channel() string { return "telegram" }
func (*appIntegrationChannel) VerifyInbound(context.Context, *http.Request, []byte) error {
	return nil
}
func (*appIntegrationChannel) ParseInbound(context.Context, *http.Request, []byte, string) (domain.CanonicalInboundEvent, error) {
	return domain.CanonicalInboundEvent{}, nil
}
func (c *appIntegrationChannel) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	c.sent = append(c.sent, delivery)
	return domain.DeliveryResult{ProviderMessageID: "tg_provider_1", ProviderRequestID: "tg_request_1"}, nil
}
func (c *appIntegrationChannel) SendAwaitPrompt(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return c.SendMessage(ctx, delivery)
}

type appIntegrationSlackChannel struct {
	sent []domain.OutboundDelivery
}

func (*appIntegrationSlackChannel) Channel() string { return "slack" }
func (*appIntegrationSlackChannel) VerifyInbound(context.Context, *http.Request, []byte) error {
	return nil
}
func (*appIntegrationSlackChannel) ParseInbound(context.Context, *http.Request, []byte, string) (domain.CanonicalInboundEvent, error) {
	return domain.CanonicalInboundEvent{}, nil
}
func (c *appIntegrationSlackChannel) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	c.sent = append(c.sent, delivery)
	return domain.DeliveryResult{ProviderMessageID: "slack_provider_1", ProviderRequestID: "slack_request_1"}, nil
}
func (c *appIntegrationSlackChannel) SendAwaitPrompt(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return c.SendMessage(ctx, delivery)
}

func TestTelegramWebhookWorkerRoundTripIntegration(t *testing.T) {
	if os.Getenv("NEXUS_INTEGRATION_DB") != "1" {
		t.Skip("set NEXUS_INTEGRATION_DB=1 to run Postgres integration tests")
	}

	ctx := context.Background()
	dbURL := startAppPostgresContainer(t)
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

	applyAppMigrations(t, ctx, pool)
	appMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)

	channel := &appIntegrationChannel{}
	cfg := config.Config{
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "agent_profile_default",
		DefaultACPAgentName:   "agent_a",
		TelegramAllowedUserIDs: []string{"123"},
	}
	renderers := map[string]ports.Renderer{
		"telegram": services.TelegramRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"telegram": channel,
	}
	app := &App{
		Config: cfg,
		Repo:   repo,
		DB:     repo,
		Inbound: services.InboundService{
			Repo: repo,
			Router: services.StaticRouter{
				DefaultAgentProfileID: cfg.DefaultAgentProfileID,
				DefaultACPAgentName:   cfg.DefaultACPAgentName,
			},
		},
		Await: services.AwaitService{Repo: repo},
		Worker: services.WorkerService{
			Repo:      repo,
			ACP:       appIntegrationACP{},
			Renderer:  renderers["telegram"],
			Channel:   channel,
			Renderers: renderers,
			Channels:  channels,
		},
		Telegram: telegram.New("", ""),
		Channels: channels,
	}

	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(`{
		"update_id": 1001,
		"message": {
			"message_id": 55,
			"date": 1712400000,
			"text": "run integration",
			"chat": {"id": 123, "type": "private"},
			"from": {"id": 123}
		}
	}`))
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected telegram webhook accepted, got status=%d body=%s", rec.Code, rec.Body.String())
	}

	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}

	sessions, err := repo.ListSessions(ctx, domain.SessionListQuery{
		TenantID:   "tenant_default",
		ChannelType: "telegram",
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 1 {
		t.Fatalf("expected one telegram session, got %+v", sessions.Items)
	}
	sessionID := sessions.Items[0].ID

	run, err := repo.GetRun(ctx, "run_app_1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("expected created run to be completed, got %+v", run)
	}

	messages, err := repo.ListMessages(ctx, domain.MessageListQuery{
		TenantID:   "tenant_default",
		SessionID:  sessionID,
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Items) != 2 || messages.Items[0].Text != "integration complete" {
		t.Fatalf("unexpected session messages: %+v", messages.Items)
	}

	deliveries, err := repo.ListDeliveries(ctx, domain.DeliveryListQuery{
		TenantID:   "tenant_default",
		SessionID:  sessionID,
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries.Items) != 1 || deliveries.Items[0].Status != "sent" {
		t.Fatalf("unexpected deliveries: %+v", deliveries.Items)
	}
	if deliveries.Items[0].ProviderMessageID != "tg_provider_1" {
		t.Fatalf("expected provider message id to be persisted, got %+v", deliveries.Items[0])
	}

	if len(channel.sent) != 1 {
		t.Fatalf("expected one outbound telegram send, got %+v", channel.sent)
	}
	var payload map[string]any
	if err := json.Unmarshal(channel.sent[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["chat_id"] != "123" || payload["text"] != "integration complete" {
		t.Fatalf("unexpected outbound payload: %+v", payload)
	}
}

func TestSlackWebhookWorkerRoundTripIntegration(t *testing.T) {
	if os.Getenv("NEXUS_INTEGRATION_DB") != "1" {
		t.Skip("set NEXUS_INTEGRATION_DB=1 to run Postgres integration tests")
	}

	ctx := context.Background()
	dbURL := startAppPostgresContainer(t)
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

	applyAppMigrations(t, ctx, pool)
	appMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)

	channel := &appIntegrationSlackChannel{}
	cfg := config.Config{
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "agent_profile_default",
		DefaultACPAgentName:   "agent_a",
		SlackSigningSecret:    "integration-secret",
	}
	renderers := map[string]ports.Renderer{
		"slack": services.SlackRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"slack": channel,
	}
	app := &App{
		Config: cfg,
		Repo:   repo,
		DB:     repo,
		Inbound: services.InboundService{
			Repo: repo,
			Router: services.StaticRouter{
				DefaultAgentProfileID: cfg.DefaultAgentProfileID,
				DefaultACPAgentName:   cfg.DefaultACPAgentName,
			},
		},
		Await: services.AwaitService{Repo: repo},
		Worker: services.WorkerService{
			Repo:      repo,
			ACP:       appIntegrationACP{},
			Renderer:  renderers["slack"],
			Channel:   channel,
			Renderers: renderers,
			Channels:  channels,
		},
		Slack:    slack.New(cfg.SlackSigningSecret, ""),
		Channels: channels,
	}

	body := `{
		"type":"event_callback",
		"event_id":"evt_slack_1",
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@BOT> run integration",
			"channel":"C123",
			"ts":"1712400000.000100"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(body))
	slackSign(req, []byte(body), cfg.SlackSigningSecret)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected slack webhook accepted, got status=%d body=%s", rec.Code, rec.Body.String())
	}

	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}

	sessions, err := repo.ListSessions(ctx, domain.SessionListQuery{
		TenantID:    "tenant_default",
		ChannelType: "slack",
		CursorPage:  domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 1 {
		t.Fatalf("expected one slack session, got %+v", sessions.Items)
	}
	sessionID := sessions.Items[0].ID

	run, err := repo.GetRun(ctx, "run_app_1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("expected created run to be completed, got %+v", run)
	}

	messages, err := repo.ListMessages(ctx, domain.MessageListQuery{
		TenantID:   "tenant_default",
		SessionID:  sessionID,
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Items) != 2 || messages.Items[0].Text != "integration complete" {
		t.Fatalf("unexpected session messages: %+v", messages.Items)
	}

	deliveries, err := repo.ListDeliveries(ctx, domain.DeliveryListQuery{
		TenantID:   "tenant_default",
		SessionID:  sessionID,
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries.Items) != 1 || deliveries.Items[0].Status != "sent" {
		t.Fatalf("unexpected deliveries: %+v", deliveries.Items)
	}
	if deliveries.Items[0].ProviderMessageID != "slack_provider_1" {
		t.Fatalf("expected provider message id to be persisted, got %+v", deliveries.Items[0])
	}

	if len(channel.sent) != 1 {
		t.Fatalf("expected one outbound slack send, got %+v", channel.sent)
	}
	var payload map[string]any
	if err := json.Unmarshal(channel.sent[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["channel"] != "C123" || payload["thread_ts"] != "1712400000.000100" || payload["text"] != "integration complete" {
		t.Fatalf("unexpected outbound slack payload: %+v", payload)
	}
}

func TestSlackAwaitResumeRoundTripIntegration(t *testing.T) {
	if os.Getenv("NEXUS_INTEGRATION_DB") != "1" {
		t.Skip("set NEXUS_INTEGRATION_DB=1 to run Postgres integration tests")
	}

	ctx := context.Background()
	dbURL := startAppPostgresContainer(t)
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

	applyAppMigrations(t, ctx, pool)
	appMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)

	channel := &appIntegrationSlackChannel{}
	cfg := config.Config{
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "agent_profile_default",
		DefaultACPAgentName:   "agent_a",
		SlackSigningSecret:    "integration-secret",
	}
	renderers := map[string]ports.Renderer{
		"slack": services.SlackRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"slack": channel,
	}
	app := &App{
		Config: cfg,
		Repo:   repo,
		DB:     repo,
		Inbound: services.InboundService{
			Repo: repo,
			Router: services.StaticRouter{
				DefaultAgentProfileID: cfg.DefaultAgentProfileID,
				DefaultACPAgentName:   cfg.DefaultACPAgentName,
			},
		},
		Await: services.AwaitService{Repo: repo},
		Worker: services.WorkerService{
			Repo: repo,
			ACP: appIntegrationACP{
				startRun:    "run_app_await_1",
				startStatus: "running",
				startEvents: []domain.RunEvent{{
					RunID:       "run_app_await_1",
					Status:      "awaiting",
					Text:        "need approval",
					AwaitSchema: []byte(`{"type":"object"}`),
					AwaitPrompt: []byte(`{"title":"Approve?","choices":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}`),
				}},
				resumeEvents: []domain.RunEvent{{
					RunID:  "run_app_await_1",
					Status: "completed",
					Text:   "approved and completed",
				}},
			},
			Renderer:  renderers["slack"],
			Channel:   channel,
			Renderers: renderers,
			Channels:  channels,
		},
		Slack:    slack.New(cfg.SlackSigningSecret, ""),
		Channels: channels,
	}

	eventBody := `{
		"type":"event_callback",
		"event_id":"evt_slack_await_1",
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@BOT> need approval",
			"channel":"C234",
			"ts":"1712400000.000200"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(eventBody))
	slackSign(req, []byte(eventBody), cfg.SlackSigningSecret)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected slack webhook accepted, got status=%d body=%s", rec.Code, rec.Body.String())
	}

	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}

	await, err := repo.GetAwait(ctx, "await_run_app_await_1")
	if err != nil {
		t.Fatal(err)
	}
	if await.Status != "pending" {
		t.Fatalf("expected pending await after queue start, got %+v", await)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("expected await prompt delivery, got %+v", channel.sent)
	}
	var awaitPayload map[string]any
	if err := json.Unmarshal(channel.sent[0].PayloadJSON, &awaitPayload); err != nil {
		t.Fatal(err)
	}
	if awaitPayload["channel"] != "C234" || awaitPayload["thread_ts"] != "1712400000.000200" {
		t.Fatalf("unexpected await payload target: %+v", awaitPayload)
	}

	callbackJSON := `{"type":"block_actions","user":{"id":"U123"},"channel":{"id":"C234"},"message":{"ts":"1712400000.000200"},"container":{"thread_ts":"1712400000.000200"},"actions":[{"value":"{\"await_id\":\"await_run_app_await_1\",\"choice\":\"yes\"}"}]}`
	formBody := "payload=" + url.QueryEscape(callbackJSON)
	callbackReq := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(formBody))
	slackSignWithContentType(callbackReq, []byte(formBody), cfg.SlackSigningSecret, "application/x-www-form-urlencoded")
	callbackRec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(callbackRec, callbackReq)
	if callbackRec.Code != http.StatusOK {
		t.Fatalf("expected slack callback accepted, got status=%d body=%s", callbackRec.Code, callbackRec.Body.String())
	}

	awaitAfter, err := repo.GetAwait(ctx, "await_run_app_await_1")
	if err != nil {
		t.Fatal(err)
	}
	if awaitAfter.Status != "resolved" {
		t.Fatalf("expected await to be resolved after callback, got %+v", awaitAfter)
	}
	responses, err := repo.GetAwaitResponses(ctx, "await_run_app_await_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected one await response, got %+v", responses)
	}

	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}

	run, err := repo.GetRun(ctx, "run_app_await_1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("expected resumed run to be completed, got %+v", run)
	}
	if len(channel.sent) != 2 {
		t.Fatalf("expected final slack send after resume, got %+v", channel.sent)
	}
	var finalPayload map[string]any
	if err := json.Unmarshal(channel.sent[1].PayloadJSON, &finalPayload); err != nil {
		t.Fatal(err)
	}
	if finalPayload["text"] != "approved and completed" {
		t.Fatalf("unexpected final payload: %+v", finalPayload)
	}
}

func TestTelegramAwaitResumeRoundTripIntegration(t *testing.T) {
	if os.Getenv("NEXUS_INTEGRATION_DB") != "1" {
		t.Skip("set NEXUS_INTEGRATION_DB=1 to run Postgres integration tests")
	}

	ctx := context.Background()
	dbURL := startAppPostgresContainer(t)
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

	applyAppMigrations(t, ctx, pool)
	appMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)

	channel := &appIntegrationChannel{}
	cfg := config.Config{
		DefaultTenantID:        "tenant_default",
		DefaultAgentProfileID:  "agent_profile_default",
		DefaultACPAgentName:    "agent_a",
		TelegramAllowedUserIDs: []string{"123"},
	}
	renderers := map[string]ports.Renderer{
		"telegram": services.TelegramRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"telegram": channel,
	}
	app := &App{
		Config: cfg,
		Repo:   repo,
		DB:     repo,
		Inbound: services.InboundService{
			Repo: repo,
			Router: services.StaticRouter{
				DefaultAgentProfileID: cfg.DefaultAgentProfileID,
				DefaultACPAgentName:   cfg.DefaultACPAgentName,
			},
		},
		Await: services.AwaitService{Repo: repo},
		Worker: services.WorkerService{
			Repo: repo,
			ACP: appIntegrationACP{
				startRun:    "run_tg_await_1",
				startStatus: "running",
				startEvents: []domain.RunEvent{{
					RunID:       "run_tg_await_1",
					Status:      "awaiting",
					Text:        "need telegram approval",
					AwaitSchema: []byte(`{"type":"object"}`),
					AwaitPrompt: []byte(`{"title":"Approve?","choices":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}`),
				}},
				resumeEvents: []domain.RunEvent{{
					RunID:  "run_tg_await_1",
					Status: "completed",
					Text:   "telegram approval complete",
				}},
			},
			Renderer:  renderers["telegram"],
			Channel:   channel,
			Renderers: renderers,
			Channels:  channels,
		},
		Telegram: telegram.New("", ""),
		Channels: channels,
	}

	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(`{
		"update_id": 2001,
		"message": {
			"message_id": 77,
			"date": 1712400000,
			"text": "need telegram approval",
			"chat": {"id": 123, "type": "private"},
			"from": {"id": 123}
		}
	}`))
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected telegram webhook accepted, got status=%d body=%s", rec.Code, rec.Body.String())
	}

	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}

	await, err := repo.GetAwait(ctx, "await_run_tg_await_1")
	if err != nil {
		t.Fatal(err)
	}
	if await.Status != "pending" {
		t.Fatalf("expected pending await after queue start, got %+v", await)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("expected telegram await prompt delivery, got %+v", channel.sent)
	}
	var awaitPayload map[string]any
	if err := json.Unmarshal(channel.sent[0].PayloadJSON, &awaitPayload); err != nil {
		t.Fatal(err)
	}
	if awaitPayload["chat_id"] != "123" {
		t.Fatalf("unexpected telegram await payload target: %+v", awaitPayload)
	}

	callbackReq := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(`{
		"update_id": 2002,
		"callback_query": {
			"id": "cb-1",
			"data": "{\"await_id\":\"await_run_tg_await_1\",\"choice\":\"yes\"}",
			"from": {"id": 123},
			"message": {
				"message_id": 77,
				"date": 1712400000,
				"chat": {"id": 123}
			}
		}
	}`))
	callbackRec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(callbackRec, callbackReq)
	if callbackRec.Code != http.StatusOK {
		t.Fatalf("expected telegram callback accepted, got status=%d body=%s", callbackRec.Code, callbackRec.Body.String())
	}

	awaitAfter, err := repo.GetAwait(ctx, "await_run_tg_await_1")
	if err != nil {
		t.Fatal(err)
	}
	if awaitAfter.Status != "resolved" {
		t.Fatalf("expected telegram await to be resolved after callback, got %+v", awaitAfter)
	}
	responses, err := repo.GetAwaitResponses(ctx, "await_run_tg_await_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected one telegram await response, got %+v", responses)
	}

	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}

	run, err := repo.GetRun(ctx, "run_tg_await_1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("expected resumed telegram run to be completed, got %+v", run)
	}
	if len(channel.sent) != 2 {
		t.Fatalf("expected final telegram send after resume, got %+v", channel.sent)
	}
	var finalPayload map[string]any
	if err := json.Unmarshal(channel.sent[1].PayloadJSON, &finalPayload); err != nil {
		t.Fatal(err)
	}
	if finalPayload["chat_id"] != "123" || finalPayload["text"] != "telegram approval complete" {
		t.Fatalf("unexpected final telegram payload: %+v", finalPayload)
	}
}

func TestSlackArtifactRoundTripIntegration(t *testing.T) {
	if os.Getenv("NEXUS_INTEGRATION_DB") != "1" {
		t.Skip("set NEXUS_INTEGRATION_DB=1 to run Postgres integration tests")
	}

	ctx := context.Background()
	dbURL := startAppPostgresContainer(t)
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

	applyAppMigrations(t, ctx, pool)
	appMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, telegram_user_access, sessions restart identity cascade`)

	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("inbound artifact content"))
	}))
	defer downloadServer.Close()

	objectDir := t.TempDir()
	outboundFile := filepath.Join(objectDir, "outbound-report.txt")
	if err := os.WriteFile(outboundFile, []byte("outbound artifact content"), 0o644); err != nil {
		t.Fatal(err)
	}

	channel := &appIntegrationSlackChannel{}
	cfg := config.Config{
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "agent_profile_default",
		DefaultACPAgentName:   "agent_a",
		SlackSigningSecret:    "integration-secret",
		ObjectStorageBaseURL:  "file://" + objectDir,
	}
	renderers := map[string]ports.Renderer{
		"slack": services.SlackRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"slack": channel,
	}
	slackAdapter := slack.New(cfg.SlackSigningSecret, "")
	app := &App{
		Config: cfg,
		Repo:   repo,
		DB:     repo,
		Inbound: services.InboundService{
			Repo: repo,
			Router: services.StaticRouter{
				DefaultAgentProfileID: cfg.DefaultAgentProfileID,
				DefaultACPAgentName:   cfg.DefaultACPAgentName,
			},
		},
		Await:     services.AwaitService{Repo: repo},
		Artifacts: services.ArtifactService{Store: storage.New(cfg.ObjectStorageBaseURL)},
		Worker: services.WorkerService{
			Repo: repo,
			ACP: appIntegrationACP{
				startRun:    "run_app_artifact_1",
				startStatus: "running",
				startEvents: []domain.RunEvent{{
					RunID:     "run_app_artifact_1",
					Status:    "completed",
					Text:      "artifact complete",
					Artifacts: []domain.Artifact{{ID: "artifact_out_1", Name: "outbound-report.txt", StorageURI: "file://" + outboundFile}},
				}},
			},
			Renderer:  renderers["slack"],
			Channel:   channel,
			Renderers: renderers,
			Channels:  channels,
		},
		Slack:    slackAdapter,
		Channels: channels,
	}
	app.Slack.HTTP = downloadServer.Client()

	body := fmt.Sprintf(`{
		"type":"event_callback",
		"event_id":"evt_slack_artifact_1",
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@BOT> process file",
			"channel":"C345",
			"ts":"1712400000.000300",
			"files":[{"id":"F123","name":"input.txt","mimetype":"text/plain","size":22,"url_private_download":"%s/file.txt"}]
		}
	}`, downloadServer.URL)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(body))
	slackSign(req, []byte(body), cfg.SlackSigningSecret)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected slack artifact webhook accepted, got status=%d body=%s", rec.Code, rec.Body.String())
	}

	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := app.Worker.ProcessOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}

	sessions, err := repo.ListSessions(ctx, domain.SessionListQuery{
		TenantID:    "tenant_default",
		ChannelType: "slack",
		CursorPage:  domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 1 {
		t.Fatalf("expected one slack session, got %+v", sessions.Items)
	}
	sessionID := sessions.Items[0].ID

	inboundArtifacts, err := repo.ListArtifacts(ctx, domain.ArtifactListQuery{
		TenantID:   "tenant_default",
		SessionID:  sessionID,
		Direction:  "inbound",
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inboundArtifacts.Items) != 1 {
		t.Fatalf("expected one inbound artifact, got %+v", inboundArtifacts.Items)
	}
	if !strings.HasPrefix(inboundArtifacts.Items[0].StorageURI, "file://") {
		t.Fatalf("expected inbound artifact to be stored locally, got %+v", inboundArtifacts.Items[0])
	}
	content, err := os.ReadFile(strings.TrimPrefix(inboundArtifacts.Items[0].StorageURI, "file://"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "inbound artifact content" {
		t.Fatalf("unexpected persisted inbound artifact content: %q", string(content))
	}

	outboundArtifacts, err := repo.ListArtifacts(ctx, domain.ArtifactListQuery{
		TenantID:   "tenant_default",
		SessionID:  sessionID,
		Direction:  "outbound",
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outboundArtifacts.Items) != 1 || outboundArtifacts.Items[0].Name != "outbound-report.txt" {
		t.Fatalf("expected one outbound artifact, got %+v", outboundArtifacts.Items)
	}

	if len(channel.sent) != 2 {
		t.Fatalf("expected text and artifact outbound sends, got %+v", channel.sent)
	}
	var textPayload map[string]any
	if err := json.Unmarshal(channel.sent[0].PayloadJSON, &textPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textPayload["text"].(string), "Artifacts:") {
		t.Fatalf("expected artifact summary in text payload, got %+v", textPayload)
	}
	var artifactPayload map[string]any
	if err := json.Unmarshal(channel.sent[1].PayloadJSON, &artifactPayload); err != nil {
		t.Fatal(err)
	}
	if artifactPayload["kind"] != "artifact_upload" || artifactPayload["file_name"] != "outbound-report.txt" {
		t.Fatalf("unexpected artifact upload payload: %+v", artifactPayload)
	}
}

func startAppPostgresContainer(t *testing.T) string {
	t.Helper()
	port := appFreePort(t)
	name := fmt.Sprintf("nexus-app-test-pg-%d", time.Now().UnixNano())
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
	waitForAppPostgres(t, dbURL)
	return dbURL
}

func waitForAppPostgres(t *testing.T, dbURL string) {
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

func applyAppMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		sqlBytes, err := os.ReadFile(filepath.Join("..", "..", "migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("failed applying migration %s: %v", entry.Name(), err)
		}
	}
}

func appMustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec failed: %v\nsql: %s", err, sql)
	}
}

func appFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func slackSign(req *http.Request, body []byte, signingSecret string) {
	slackSignWithContentType(req, body, signingSecret, "application/json")
}

func slackSignWithContentType(req *http.Request, body []byte, signingSecret string, contentType string) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", contentType)
}
