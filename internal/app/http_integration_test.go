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

	"nexus/internal/adapters/acp"
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
	startRun     string
	startStatus  string
	startEvents  []domain.RunEvent
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

func (appIntegrationACP) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}

func (appIntegrationACP) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
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

type strictACPIntegrationServer struct {
	server       *httptest.Server
	sessionCount int
	runCount     int
}

func newStrictACPIntegrationServer(t *testing.T) *strictACPIntegrationServer {
	t.Helper()

	state := &strictACPIntegrationServer{}
	state.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agents":
			_, _ = w.Write([]byte(`[{
				"name":"strict-agent",
				"description":"Integration strict ACP agent",
				"supports_await_resume":true,
				"supports_structured_await":true,
				"supports_streaming":true,
				"supports_artifacts":true,
				"healthy":true
			}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			state.sessionCount++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode strict session body: %v", err)
			}
			if body["gateway_session_id"] == "" {
				t.Fatalf("expected gateway_session_id in strict session body, got %+v", body)
			}
			_, _ = w.Write([]byte(`{"id":"ses_strict_integration_1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/runs":
			state.runCount++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode strict run body: %v", err)
			}
			if body["session_id"] != "ses_strict_integration_1" {
				t.Fatalf("expected persisted strict session id in run body, got %+v", body)
			}
			if body["agent_name"] != "strict-agent" {
				t.Fatalf("expected strict agent name in run body, got %+v", body)
			}
			message, _ := body["message"].(map[string]any)
			if message["text"] != "<@BOT> strict integration" {
				t.Fatalf("unexpected strict message body: %+v", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"strict_run_1",
				"session_id":"ses_strict_integration_1",
				"status":"completed",
				"output":"strict integration complete"
			}`))
		default:
			t.Fatalf("unexpected strict ACP request: %s %s", r.Method, r.URL.Path)
		}
	}))
	return state
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
		TenantID:    "tenant_default",
		ChannelType: "telegram",
		CursorPage:  domain.CursorPage{Limit: 10},
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

func TestWebChatPhoneIdentityIntegration(t *testing.T) {
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
	appMustExec(t, ctx, pool, `truncate table outbox_events, outbound_deliveries, audit_events, await_responses, awaits, runs, session_queue_items, artifacts, messages, channel_surface_state, session_aliases, linked_identities, step_up_challenges, users, webchat_auth_sessions, webchat_auth_challenges, sessions restart identity cascade`)

	authSession := domain.WebAuthSession{
		ID:         "websess_phone_1",
		TenantID:   "tenant_default",
		Email:      "user@example.com",
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
		LastSeenAt: time.Now().UTC(),
		CreatedAt:  time.Now().UTC(),
	}
	if err := repo.CreateWebAuthSession(ctx, authSession); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateWebAuthSessionCSRFHash(ctx, authSession.ID, sha256Hex("csrf-phone"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	app := &App{
		Config: config.Config{
			DefaultTenantID:       "tenant_default",
			DefaultAgentProfileID: "agent_profile_default",
			WebChatCookieName:     "nexus_webchat_session",
			StepUpWindowMinutes:   15,
		},
		Repo:     repo,
		DB:       repo,
		Identity: repo,
		WebAuth:  repo,
	}

	updateReq := httptest.NewRequest(http.MethodPost, "/webchat/identity/phone", strings.NewReader(`{"phone":"+62 812-3456-789"}`))
	updateReq.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: authSession.ID})
	updateReq.Header.Set("X-CSRF-Token", "csrf-phone")
	updateRec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("expected phone update ok, got status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}

	profileReq := httptest.NewRequest(http.MethodGet, "/webchat/identity/profile", nil)
	profileReq.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: authSession.ID})
	profileRec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(profileRec, profileReq)
	if profileRec.Code != http.StatusOK {
		t.Fatalf("expected profile ok, got status=%d body=%s", profileRec.Code, profileRec.Body.String())
	}
	var profilePayload struct {
		Data struct {
			PrimaryPhone string         `json:"primary_phone"`
			LinkHints    map[string]any `json:"link_hints"`
		} `json:"data"`
	}
	if err := json.NewDecoder(profileRec.Body).Decode(&profilePayload); err != nil {
		t.Fatal(err)
	}
	if profilePayload.Data.PrimaryPhone != "+62 812-3456-789" {
		t.Fatalf("unexpected profile payload: %+v", profilePayload)
	}
	if _, ok := profilePayload.Data.LinkHints["telegram"]; !ok {
		t.Fatalf("expected telegram link hint in profile payload: %+v", profilePayload)
	}

	bootstrapReq := httptest.NewRequest(http.MethodGet, "/webchat/bootstrap", nil)
	bootstrapReq.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: authSession.ID})
	bootstrapRec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(bootstrapRec, bootstrapReq)
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("expected bootstrap ok, got status=%d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	var bootstrapPayload struct {
		Data struct {
			PrimaryPhone string `json:"primary_phone"`
			CSRFToken    string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bootstrapRec.Body).Decode(&bootstrapPayload); err != nil {
		t.Fatal(err)
	}
	if bootstrapPayload.Data.PrimaryPhone != "+62 812-3456-789" {
		t.Fatalf("unexpected bootstrap payload: %+v", bootstrapPayload)
	}

	trustReq := httptest.NewRequest(http.MethodGet, "/admin/trust/users", nil)
	trustRec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(trustRec, trustReq)
	if trustRec.Code != http.StatusOK {
		t.Fatalf("expected trust users ok, got status=%d body=%s", trustRec.Code, trustRec.Body.String())
	}
	var trustPayload struct {
		Data struct {
			Items []struct {
				User struct {
					PrimaryPhone string `json:"primary_phone"`
				} `json:"user"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(trustRec.Body).Decode(&trustPayload); err != nil {
		t.Fatal(err)
	}
	if len(trustPayload.Data.Items) != 1 || trustPayload.Data.Items[0].User.PrimaryPhone != "+62 812-3456-789" {
		t.Fatalf("unexpected trust users payload: %+v", trustPayload)
	}

	deleteReq := httptest.NewRequest(http.MethodPost, "/webchat/identity/phone/delete", nil)
	deleteReq.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: authSession.ID})
	deleteReq.Header.Set("X-CSRF-Token", bootstrapPayload.Data.CSRFToken)
	deleteRec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected phone delete ok, got status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}

	user, err := repo.GetUserByEmail(ctx, "tenant_default", "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user.PrimaryPhone != "" || user.PrimaryPhoneNormalized != "" || user.PrimaryPhoneVerified {
		t.Fatalf("expected cleared phone on user, got %+v", user)
	}
}

func TestSlackWebhookStrictACPRoundTripIntegration(t *testing.T) {
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

	strictServer := newStrictACPIntegrationServer(t)
	defer strictServer.server.Close()

	strictBridge := acp.NewStrictClient(strictServer.server.URL, "strict-token")
	strictBridge.HTTP = strictServer.server.Client()

	channel := &appIntegrationSlackChannel{}
	cfg := config.Config{
		DefaultTenantID:       "tenant_default",
		DefaultAgentProfileID: "agent_profile_default",
		DefaultACPAgentName:   "strict-agent",
		SlackSigningSecret:    "integration-secret",
	}
	renderers := map[string]ports.Renderer{
		"slack": services.SlackRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"slack": channel,
	}
	catalog := &services.AgentCatalog{
		Bridge: strictBridge,
		TTL:    time.Minute,
	}
	app := &App{
		Config:  cfg,
		Repo:    repo,
		DB:      repo,
		Catalog: catalog,
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
			ACP:       strictBridge,
			Catalog:   catalog,
			Renderer:  renderers["slack"],
			Channel:   channel,
			Renderers: renderers,
			Channels:  channels,
		},
		Slack:    slack.New(cfg.SlackSigningSecret, ""),
		Channels: channels,
	}

	validateReq := httptest.NewRequest(http.MethodGet, "/admin/acp/validate?agent_name=strict-agent", nil)
	validateRec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(validateRec, validateReq)
	if validateRec.Code != http.StatusOK {
		t.Fatalf("expected ACP validate OK, got status=%d body=%s", validateRec.Code, validateRec.Body.String())
	}
	var validatePayload struct {
		Data struct {
			Compatible     bool   `json:"compatible"`
			ValidationMode string `json:"validation_mode"`
		} `json:"data"`
	}
	if err := json.NewDecoder(validateRec.Body).Decode(&validatePayload); err != nil {
		t.Fatal(err)
	}
	if !validatePayload.Data.Compatible || validatePayload.Data.ValidationMode != "strict_acp" {
		t.Fatalf("unexpected strict ACP validation payload: %+v", validatePayload.Data)
	}

	body := `{
		"type":"event_callback",
		"event_id":"evt_strict_slack_1",
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@BOT> strict integration",
			"channel":"C777",
			"ts":"1712400000.000700"
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

	if strictServer.sessionCount != 1 || strictServer.runCount != 1 {
		t.Fatalf("expected one strict ACP session and run, got sessions=%d runs=%d", strictServer.sessionCount, strictServer.runCount)
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
		t.Fatalf("expected one strict slack session, got %+v", sessions.Items)
	}
	if sessions.Items[0].ACPSessionID != "ses_strict_integration_1" {
		t.Fatalf("expected ACP session id to be persisted, got %+v", sessions.Items[0])
	}
	sessionID := sessions.Items[0].ID

	run, err := repo.GetRun(ctx, "run_strict_run_1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.ACPRunID != "strict_run_1" {
		t.Fatalf("unexpected strict ACP run: %+v", run)
	}

	messages, err := repo.ListMessages(ctx, domain.MessageListQuery{
		TenantID:   "tenant_default",
		SessionID:  sessionID,
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Items) != 2 || messages.Items[0].Text != "strict integration complete" {
		t.Fatalf("unexpected strict ACP session messages: %+v", messages.Items)
	}

	if len(channel.sent) != 1 {
		t.Fatalf("expected one outbound strict slack send, got %+v", channel.sent)
	}
	var payload map[string]any
	if err := json.Unmarshal(channel.sent[0].PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["channel"] != "C777" || payload["thread_ts"] != "1712400000.000700" || payload["text"] != "strict integration complete" {
		t.Fatalf("unexpected outbound strict slack payload: %+v", payload)
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

func TestSlackOpenCodeAwaitBlockedIntegration(t *testing.T) {
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
		DefaultACPAgentName:   "build",
		SlackSigningSecret:    "integration-secret",
		WorkerPollInterval:    time.Second,
		ReconcilerInterval:    time.Second,
	}
	renderers := map[string]ports.Renderer{
		"slack": services.SlackRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"slack": channel,
	}
	catalog := &services.AgentCatalog{
		Bridge: testACPBridge{agents: []domain.AgentManifest{{
			Name:                    "build",
			Protocol:                "opencode",
			Healthy:                 true,
			SupportsAwaitResume:     false,
			SupportsStructuredAwait: false,
			SupportsStreaming:       true,
			SupportsArtifacts:       true,
		}}},
		TTL: time.Minute,
	}
	app := &App{
		Config:  cfg,
		Repo:    repo,
		DB:      repo,
		Catalog: catalog,
		Runtime: &RuntimeState{},
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
				startRun:    "run_opencode_block_1",
				startStatus: "running",
				startEvents: []domain.RunEvent{{
					RunID:       "run_opencode_block_1",
					Status:      "awaiting",
					Text:        "needs approval",
					AwaitSchema: []byte(`{"type":"object"}`),
					AwaitPrompt: []byte(`{"title":"Approve?"}`),
				}},
			},
			Catalog:   catalog,
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
		"event_id":"evt_slack_block_1",
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@BOT> request that would pause",
			"channel":"C345",
			"ts":"1712400000.000300"
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

	awaits, err := repo.ListAwaits(ctx, domain.AwaitListQuery{
		TenantID:   "tenant_default",
		SessionID:  "evt_slack_block_1_session",
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(awaits.Items) != 0 {
		t.Fatalf("expected no persisted awaits for blocked OpenCode flow, got %+v", awaits.Items)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("expected one rendered failure delivery, got %+v", channel.sent)
	}
	var failurePayload map[string]any
	if err := json.Unmarshal(channel.sent[0].PayloadJSON, &failurePayload); err != nil {
		t.Fatal(err)
	}
	text, _ := failurePayload["text"].(string)
	if !strings.Contains(text, "Slack route") || !strings.Contains(text, "native await/resume") {
		t.Fatalf("unexpected rendered failure payload: %+v", failurePayload)
	}

	auditPage, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:   "tenant_default",
		RunID:      "run_opencode_block_1",
		EventType:  "worker.await_blocked_opencode_bridge",
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(auditPage.Items) != 1 {
		t.Fatalf("expected one bridge-await-block audit event, got %+v", auditPage.Items)
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/admin/runtime", nil)
	runtimeRec := httptest.NewRecorder()
	app.handleRuntimeStatus(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK {
		t.Fatalf("expected runtime status 200, got %d body=%s", runtimeRec.Code, runtimeRec.Body.String())
	}
	var runtimePayload struct {
		Data struct {
			Persisted map[string]int `json:"persisted"`
		} `json:"data"`
	}
	if err := json.NewDecoder(runtimeRec.Body).Decode(&runtimePayload); err != nil {
		t.Fatal(err)
	}
	if runtimePayload.Data.Persisted["persisted_bridge_await_blocks"] != 1 {
		t.Fatalf("expected persisted bridge await block count, got %+v", runtimePayload.Data.Persisted)
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

func TestTelegramOpenCodeAwaitBlockedIntegration(t *testing.T) {
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
		DefaultACPAgentName:    "build",
		TelegramAllowedUserIDs: []string{"123"},
		WorkerPollInterval:     time.Second,
		ReconcilerInterval:     time.Second,
	}
	renderers := map[string]ports.Renderer{
		"telegram": services.TelegramRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"telegram": channel,
	}
	catalog := &services.AgentCatalog{
		Bridge: testACPBridge{agents: []domain.AgentManifest{{
			Name:                    "build",
			Protocol:                "opencode",
			Healthy:                 true,
			SupportsAwaitResume:     false,
			SupportsStructuredAwait: false,
			SupportsStreaming:       true,
			SupportsArtifacts:       true,
		}}},
		TTL: time.Minute,
	}
	app := &App{
		Config:  cfg,
		Repo:    repo,
		DB:      repo,
		Catalog: catalog,
		Runtime: &RuntimeState{},
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
				startRun:    "run_tg_opencode_block_1",
				startStatus: "running",
				startEvents: []domain.RunEvent{{
					RunID:       "run_tg_opencode_block_1",
					Status:      "awaiting",
					Text:        "needs approval",
					AwaitSchema: []byte(`{"type":"object"}`),
					AwaitPrompt: []byte(`{"title":"Approve?"}`),
				}},
			},
			Catalog:   catalog,
			Renderer:  renderers["telegram"],
			Channel:   channel,
			Renderers: renderers,
			Channels:  channels,
		},
		Telegram: telegram.New("", ""),
		Channels: channels,
	}

	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(`{
		"update_id": 3001,
		"message": {
			"message_id": 88,
			"date": 1712400000,
			"text": "request that would pause",
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
		TenantID:    "tenant_default",
		ChannelType: "telegram",
		CursorPage:  domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 1 {
		t.Fatalf("expected one telegram session, got %+v", sessions.Items)
	}
	awaits, err := repo.ListAwaits(ctx, domain.AwaitListQuery{
		TenantID:   "tenant_default",
		SessionID:  sessions.Items[0].ID,
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(awaits.Items) != 0 {
		t.Fatalf("expected no persisted awaits for blocked OpenCode telegram flow, got %+v", awaits.Items)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("expected one rendered telegram failure delivery, got %+v", channel.sent)
	}
	var failurePayload map[string]any
	if err := json.Unmarshal(channel.sent[0].PayloadJSON, &failurePayload); err != nil {
		t.Fatal(err)
	}
	text, _ := failurePayload["text"].(string)
	if !strings.Contains(text, "Telegram route") || !strings.Contains(text, "native await/resume") {
		t.Fatalf("unexpected rendered telegram failure payload: %+v", failurePayload)
	}

	auditPage, err := repo.ListAuditEvents(ctx, domain.AuditEventListQuery{
		TenantID:   "tenant_default",
		RunID:      "run_tg_opencode_block_1",
		EventType:  "worker.await_blocked_opencode_bridge",
		CursorPage: domain.CursorPage{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(auditPage.Items) != 1 {
		t.Fatalf("expected one telegram bridge-await-block audit event, got %+v", auditPage.Items)
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/admin/runtime", nil)
	runtimeRec := httptest.NewRecorder()
	app.handleRuntimeStatus(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK {
		t.Fatalf("expected runtime status 200, got %d body=%s", runtimeRec.Code, runtimeRec.Body.String())
	}
	var runtimePayload struct {
		Data struct {
			Persisted map[string]int `json:"persisted"`
		} `json:"data"`
	}
	if err := json.NewDecoder(runtimeRec.Body).Decode(&runtimePayload); err != nil {
		t.Fatal(err)
	}
	if runtimePayload.Data.Persisted["persisted_bridge_await_blocks"] != 1 {
		t.Fatalf("expected persisted bridge await block count, got %+v", runtimePayload.Data.Persisted)
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
