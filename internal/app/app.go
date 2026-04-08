package app

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"nexus/internal/adapters/acp"
	"nexus/internal/adapters/db"
	"nexus/internal/adapters/email"
	"nexus/internal/adapters/slack"
	"nexus/internal/adapters/storage"
	"nexus/internal/adapters/telegram"
	"nexus/internal/adapters/webchat"
	"nexus/internal/adapters/whatsapp"
	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/httpx"
	"nexus/internal/ports"
	"nexus/internal/resilience"
	"nexus/internal/services"
	"nexus/internal/tracex"
)

type App struct {
	Config     config.Config
	Repo       ports.Repository
	DB         *db.PostgresRepository
	Inbound    services.InboundService
	Await      services.AwaitService
	Artifacts  services.ArtifactService
	Retention  services.RetentionService
	Catalog    *services.AgentCatalog
	Worker     services.WorkerService
	Reconciler services.Reconciler
	ACP        ports.ACPBridge
	WebAuth    ports.WebAuthRepository
	Identity   ports.IdentityRepository
	Slack      slack.Adapter
	WhatsApp   whatsapp.Adapter
	Email      email.Adapter
	WebChat    webchat.Adapter
	Telegram   telegram.Adapter
	Channels   map[string]ports.ChannelAdapter
	Runtime    *RuntimeState
}

type RuntimeState struct {
	mu sync.Mutex

	LastWorkerRunAt           time.Time
	LastWorkerError           string
	LastReconcileRunAt        time.Time
	LastReconcileError        string
	LastRetentionRunAt        time.Time
	LastRetentionError        string
	LastHealthStatus          string
	LastReadinessStatus       string
	RecentTransitions         []ProbeTransition
	OutboxRequeueCount        int
	QueueRepairRecoveredCount int
	QueueRepairRequeuedCount  int
	RunRefreshCount           int
	AwaitExpiryCount          int
	DeliveryRetryCount        int
	RetentionPayloadCount     int
	RetentionArtifactCount    int
	RetentionAuditCount       int
	RetentionSessionCount     int
	RetentionHistoryRowCount  int
}

type ProbeTransition struct {
	Probe string    `json:"probe"`
	From  string    `json:"from"`
	To    string    `json:"to"`
	At    time.Time `json:"at"`
}

type RuntimeStatus struct {
	LastWorkerRunAt           time.Time         `json:"last_worker_run_at,omitempty"`
	LastWorkerError           string            `json:"last_worker_error,omitempty"`
	LastReconcileRunAt        time.Time         `json:"last_reconcile_run_at,omitempty"`
	LastReconcileError        string            `json:"last_reconcile_error,omitempty"`
	LastRetentionRunAt        time.Time         `json:"last_retention_run_at,omitempty"`
	LastRetentionError        string            `json:"last_retention_error,omitempty"`
	LastHealthStatus          string            `json:"last_health_status,omitempty"`
	LastReadinessStatus       string            `json:"last_readiness_status,omitempty"`
	RecentTransitions         []ProbeTransition `json:"recent_transitions,omitempty"`
	OutboxRequeueCount        int               `json:"outbox_requeue_count"`
	QueueRepairRecoveredCount int               `json:"queue_repair_recovered_count"`
	QueueRepairRequeuedCount  int               `json:"queue_repair_requeued_count"`
	RunRefreshCount           int               `json:"run_refresh_count"`
	AwaitExpiryCount          int               `json:"await_expiry_count"`
	DeliveryRetryCount        int               `json:"delivery_retry_count"`
	RetentionPayloadCount     int               `json:"retention_payload_count"`
	RetentionArtifactCount    int               `json:"retention_artifact_count"`
	RetentionAuditCount       int               `json:"retention_audit_count"`
	RetentionSessionCount     int               `json:"retention_session_count"`
	RetentionHistoryRowCount  int               `json:"retention_history_row_count"`
}

func (r *RuntimeState) MarkWorkerRun(at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastWorkerRunAt = at
	r.LastWorkerError = ""
}

func (r *RuntimeState) MarkWorkerError(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastWorkerError = err.Error()
}

func (r *RuntimeState) MarkReconcileRun(at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastReconcileRunAt = at
	r.LastReconcileError = ""
}

func (r *RuntimeState) MarkReconcileError(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastReconcileError = err.Error()
}

func (r *RuntimeState) MarkRetentionRun(at time.Time, counts domain.RetentionCounts) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastRetentionRunAt = at
	r.LastRetentionError = ""
	r.RetentionPayloadCount += counts.MessagePayloads + counts.DeliveryPayloads + counts.OutboxPayloads + counts.AwaitPayloads + counts.AwaitResponsePayloads
	r.RetentionArtifactCount += counts.ArtifactBlobs
	r.RetentionAuditCount += counts.AuditRows
	r.RetentionSessionCount += counts.Sessions
	r.RetentionHistoryRowCount += counts.HistoryRows
}

func (r *RuntimeState) MarkRetentionError(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastRetentionError = err.Error()
}

func (r *RuntimeState) Status() RuntimeStatus {
	if r == nil {
		return RuntimeStatus{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return RuntimeStatus{
		LastWorkerRunAt:           r.LastWorkerRunAt,
		LastWorkerError:           r.LastWorkerError,
		LastReconcileRunAt:        r.LastReconcileRunAt,
		LastReconcileError:        r.LastReconcileError,
		LastRetentionRunAt:        r.LastRetentionRunAt,
		LastRetentionError:        r.LastRetentionError,
		LastHealthStatus:          r.LastHealthStatus,
		LastReadinessStatus:       r.LastReadinessStatus,
		RecentTransitions:         append([]ProbeTransition(nil), r.RecentTransitions...),
		OutboxRequeueCount:        r.OutboxRequeueCount,
		QueueRepairRecoveredCount: r.QueueRepairRecoveredCount,
		QueueRepairRequeuedCount:  r.QueueRepairRequeuedCount,
		RunRefreshCount:           r.RunRefreshCount,
		AwaitExpiryCount:          r.AwaitExpiryCount,
		DeliveryRetryCount:        r.DeliveryRetryCount,
		RetentionPayloadCount:     r.RetentionPayloadCount,
		RetentionArtifactCount:    r.RetentionArtifactCount,
		RetentionAuditCount:       r.RetentionAuditCount,
		RetentionSessionCount:     r.RetentionSessionCount,
		RetentionHistoryRowCount:  r.RetentionHistoryRowCount,
	}
}

func (r *RuntimeState) RecordProbeStatus(probe, status string, at time.Time) {
	if r == nil || probe == "" || status == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var current *string
	switch probe {
	case "health":
		current = &r.LastHealthStatus
	case "readiness":
		current = &r.LastReadinessStatus
	default:
		return
	}
	if *current == status {
		return
	}
	r.RecentTransitions = append(r.RecentTransitions, ProbeTransition{
		Probe: probe,
		From:  *current,
		To:    status,
		At:    at,
	})
	if len(r.RecentTransitions) > 10 {
		r.RecentTransitions = append([]ProbeTransition(nil), r.RecentTransitions[len(r.RecentTransitions)-10:]...)
	}
	*current = status
}

func (r *RuntimeState) RecordOutboxRequeue() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.OutboxRequeueCount++
}

func (r *RuntimeState) RecordQueueRepairRecovered() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.QueueRepairRecoveredCount++
}

func (r *RuntimeState) RecordQueueRepairRequeued() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.QueueRepairRequeuedCount++
}

func (r *RuntimeState) RecordRunRefresh() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.RunRefreshCount++
}

func (r *RuntimeState) RecordAwaitExpiry() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.AwaitExpiryCount++
}

func (r *RuntimeState) RecordDeliveryRetry() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.DeliveryRetryCount++
}

func New(ctx context.Context, cfg config.Config) (*App, error) {
	repo, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	slackAdapter := slack.New(cfg.SlackSigningSecret, cfg.SlackBotToken)
	whatsappAdapter := whatsapp.New(cfg.WhatsAppVerifyToken, cfg.WhatsAppAccessToken, cfg.WhatsAppAppSecret, cfg.WhatsAppPhoneNumberID, cfg.WhatsAppAPIBaseURL)
	emailAdapter := email.New(cfg.EmailWebhookSecret, cfg.EmailSMTPAddr, cfg.EmailSMTPUsername, cfg.EmailSMTPPassword, cfg.EmailFromAddress)
	policy := resilience.NewPolicy(resilience.Config{
		MaxAttempts:      cfg.RetryMaxAttempts,
		BaseDelay:        time.Duration(cfg.RetryBaseDelayMS) * time.Millisecond,
		FailureThreshold: cfg.CircuitBreakerFailures,
		CoolDown:         time.Duration(cfg.CircuitBreakerCoolDownSeconds) * time.Second,
	})
	slackAdapter.HTTP = policy.HTTPClient("slack.api", 10*time.Second)
	whatsappAdapter.HTTP = policy.HTTPClient("whatsapp.api", 10*time.Second)
	emailAdapter.RetryDo = policy.Do
	whatsappAdapter.MaxMediaBytes = cfg.WhatsAppMediaMaxBytes
	emailAdapter.MaxWebhookSkew = time.Duration(cfg.EmailWebhookMaxSkewSeconds) * time.Second
	emailAdapter.MaxAttachmentBytes = cfg.EmailMaxAttachmentBytes
	emailAdapter.MaxAttachments = cfg.EmailMaxAttachments
	webchatAdapter := webchat.New()
	telegramAdapter := telegram.New(cfg.TelegramBotToken, cfg.TelegramWebhookSecret)
	telegramAdapter.HTTP = policy.HTTPClient("telegram.api", 10*time.Second)
	router := services.PolicyRouter{
		Repo:                  repo,
		DefaultAgentProfileID: cfg.DefaultAgentProfileID,
		DefaultACPAgentName:   cfg.DefaultACPAgentName,
		FallbackPolicy: domain.TrustPolicy{
			TenantID:                          cfg.DefaultTenantID,
			AgentProfileID:                    cfg.DefaultAgentProfileID,
			RequireLinkedIdentityForExecution: false,
			RequireLinkedIdentityForApproval:  cfg.RequireLinkedIdentity,
			RequireRecentStepUpForApproval:    cfg.RequireRecentStepUp,
			AllowedApprovalChannels:           append([]string(nil), cfg.AllowedApprovalChannels...),
		},
	}
	renderers := map[string]ports.Renderer{
		"slack":    services.SlackRenderer{},
		"whatsapp": services.WhatsAppRenderer{},
		"email":    services.EmailRenderer{},
		"webchat":  services.WebChatRenderer{},
		"telegram": services.TelegramRenderer{},
	}
	channels := map[string]ports.ChannelAdapter{
		"slack":    slackAdapter,
		"whatsapp": whatsappAdapter,
		"email":    emailAdapter,
		"webchat":  webchatAdapter,
		"telegram": telegramAdapter,
	}
	acpClient := acp.NewBridge(acp.BridgeConfig{
		Implementation:   cfg.ACPImplementation,
		BaseURL:          cfg.ACPBaseURL,
		Token:            cfg.ACPToken,
		Command:          cfg.ACPCommand,
		Args:             cfg.ACPArgs,
		Env:              cfg.ACPEnv,
		Workdir:          cfg.ACPWorkdir,
		DefaultAgentName: cfg.DefaultACPAgentName,
		StartupTimeout:   cfg.ACPStartupTimeout,
		RPCTimeout:       cfg.ACPRPCTimeout,
	})
	switch bridge := acpClient.(type) {
	case acp.Client:
		bridge.HTTP = policy.HTTPClient("acp.opencode_http", 60*time.Second)
		acpClient = bridge
	case *acp.Client:
		bridge.HTTP = policy.HTTPClient("acp.opencode_http", 60*time.Second)
	case acp.StrictClient:
		bridge.HTTP = policy.HTTPClient("acp.strict_http", 60*time.Second)
		acpClient = bridge
	case *acp.StrictClient:
		bridge.HTTP = policy.HTTPClient("acp.strict_http", 60*time.Second)
	}
	objectStore := storage.New(cfg.ObjectStorageBaseURL)
	artifactSvc := services.ArtifactService{Store: objectStore}
	catalog := &services.AgentCatalog{
		Bridge: acpClient,
		TTL:    cfg.ACPManifestCacheTTL,
	}
	runtime := &RuntimeState{}
	if cfg.ValidateACPOnStartup {
		compat, err := catalog.Validate(ctx, cfg.DefaultACPAgentName, true)
		if err != nil {
			return nil, err
		}
		if !compat.Compatible {
			return nil, fmt.Errorf("configured ACP agent %q is incompatible: %v", cfg.DefaultACPAgentName, compat.Reasons)
		}
	}

	return &App{
		Config: cfg,
		Repo:   repo,
		DB:     repo,
		Inbound: services.InboundService{
			Repo:     repo,
			Router:   router,
			Identity: repo,
		},
		Await: services.AwaitService{
			Repo: repo,
		},
		Artifacts: artifactSvc,
		Retention: services.RetentionService{
			Repo:  repo,
			Store: objectStore,
			Defaults: domain.EffectiveRetentionPolicy{
				TenantID:            cfg.DefaultTenantID,
				Enabled:             cfg.RetentionEnabled,
				PayloadDays:         cfg.RetentionPayloadDays,
				ArtifactDays:        cfg.RetentionArtifactDays,
				AuditDays:           cfg.RetentionAuditDays,
				RelationalGraceDays: cfg.RetentionGraceDays,
			},
		},
		Catalog: catalog,
		Worker: services.WorkerService{
			Repo:      repo,
			ACP:       acpClient,
			Catalog:   catalog,
			Renderer:  renderers["slack"],
			Channel:   slackAdapter,
			Renderers: renderers,
			Channels:  channels,
		},
		Reconciler: services.Reconciler{
			Repo: repo,
			ACP:  acpClient,
			Config: services.ReconcilerConfig{
				OutboxClaimTimeout:     cfg.OutboxClaimTimeout,
				QueueStartingTimeout:   cfg.QueueStartingTimeout,
				RunStaleTimeout:        cfg.RunStaleTimeout,
				DeliverySendingTimeout: cfg.DeliverySendingTimeout,
				DeliveryMaxAttempts:    cfg.DeliveryMaxAttempts,
			},
			Observer: runtime,
		},
		ACP:      acpClient,
		WebAuth:  repo,
		Identity: repo,
		Slack:    slackAdapter,
		WhatsApp: whatsappAdapter,
		Email:    emailAdapter,
		WebChat:  webchatAdapter,
		Telegram: telegramAdapter,
		Channels: channels,
		Runtime:  runtime,
	}, nil
}

func (a *App) Close() {
	if closer, ok := a.ACP.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	if a.DB != nil {
		a.DB.Close()
	}
}

func (a *App) GatewayHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		status := healthStatus(a.Catalog, a.Runtime, a.Config.DefaultACPAgentName, a.Config.WorkerPollInterval, a.Config.ReconcilerInterval)
		a.Runtime.RecordProbeStatus("health", status, time.Now().UTC())
		httpx.OK(w, map[string]any{"status": status}, healthMeta("gateway", a.Catalog, a.Runtime, a.Config.DefaultACPAgentName))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		status := readinessStatus(a.Catalog, a.Runtime, a.Config.DefaultACPAgentName, a.Config.WorkerPollInterval, a.Config.ReconcilerInterval)
		a.Runtime.RecordProbeStatus("readiness", status, time.Now().UTC())
		code := http.StatusOK
		if status != "ready" {
			code = http.StatusServiceUnavailable
		}
		httpx.Respond(w, code, map[string]any{"status": status}, healthMeta("gateway", a.Catalog, a.Runtime, a.Config.DefaultACPAgentName))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		writeMetrics(w, context.Background(), "gateway", a.Config.DefaultTenantID, a.Repo, a.Catalog, a.Runtime, a.Config.DefaultACPAgentName, a.Config.WorkerPollInterval, a.Config.ReconcilerInterval)
	})
	mux.HandleFunc("/webhooks/slack", a.handleSlackWebhook)
	mux.HandleFunc("/webhooks/whatsapp", a.handleWhatsAppWebhook)
	mux.HandleFunc("/webhooks/email", a.handleEmailWebhook)
	mux.HandleFunc("/webhooks/telegram", a.handleTelegramWebhook)
	mux.HandleFunc("/webchat", a.handleWebChatIndex)
	mux.HandleFunc("/webchat/app.js", a.handleWebChatJS)
	mux.HandleFunc("/webchat/app.css", a.handleWebChatCSS)
	mux.HandleFunc("/webchat/bootstrap", a.handleWebChatBootstrap)
	mux.HandleFunc("/webchat/history", a.handleWebChatHistory)
	mux.HandleFunc("/webchat/events", a.handleWebChatEvents)
	mux.HandleFunc("/webchat/messages", a.handleWebChatMessage)
	mux.HandleFunc("/webchat/awaits/respond", a.handleWebChatAwaitRespond)
	mux.HandleFunc("/webchat/chats/new", a.handleWebChatNewChat)
	mux.HandleFunc("/webchat/chats/close", a.handleWebChatCloseChat)
	mux.HandleFunc("/webchat/identity/profile", a.handleWebChatIdentityProfile)
	mux.HandleFunc("/webchat/identity/phone", a.handleWebChatIdentityPhone)
	mux.HandleFunc("/webchat/identity/phone/delete", a.handleWebChatIdentityPhoneDelete)
	mux.HandleFunc("/webchat/identity/links", a.handleWebChatIdentityLinks)
	mux.HandleFunc("/webchat/identity/link-code", a.handleWebChatIdentityLinkCode)
	mux.HandleFunc("/webchat/identity/unlink", a.handleWebChatIdentityUnlink)
	mux.HandleFunc("/webchat/step-up/request", a.handleWebChatStepUpRequest)
	mux.HandleFunc("/webchat/step-up/verify", a.handleWebChatStepUpVerify)
	mux.HandleFunc("/webchat/auth/request", a.handleWebChatAuthRequest)
	mux.HandleFunc("/webchat/auth/verify", a.handleWebChatAuthVerify)
	mux.HandleFunc("/webchat/auth/callback", a.handleWebChatAuthCallback)
	mux.HandleFunc("/webchat/auth/logout", a.handleWebChatAuthLogout)
	mux.HandleFunc("/webchat/dev/session", a.handleWebChatDevSession)
	return tracex.Middleware("gateway", mux)
}

func (a *App) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		status := healthStatus(a.Catalog, a.Runtime, a.Config.DefaultACPAgentName, a.Config.WorkerPollInterval, a.Config.ReconcilerInterval)
		a.Runtime.RecordProbeStatus("health", status, time.Now().UTC())
		httpx.OK(w, map[string]any{"status": status}, healthMeta("admin", a.Catalog, a.Runtime, a.Config.DefaultACPAgentName))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		status := readinessStatus(a.Catalog, a.Runtime, a.Config.DefaultACPAgentName, a.Config.WorkerPollInterval, a.Config.ReconcilerInterval)
		a.Runtime.RecordProbeStatus("readiness", status, time.Now().UTC())
		code := http.StatusOK
		if status != "ready" {
			code = http.StatusServiceUnavailable
		}
		httpx.Respond(w, code, map[string]any{"status": status}, healthMeta("admin", a.Catalog, a.Runtime, a.Config.DefaultACPAgentName))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		writeMetrics(w, context.Background(), "admin", a.Config.DefaultTenantID, a.Repo, a.Catalog, a.Runtime, a.Config.DefaultACPAgentName, a.Config.WorkerPollInterval, a.Config.ReconcilerInterval)
	})
	mux.HandleFunc("/admin/sessions", a.handleListSessions)
	mux.HandleFunc("/admin/sessions/detail", a.handleSessionDetail)
	mux.HandleFunc("/admin/acp/agents", a.handleListACPAgents)
	mux.HandleFunc("/admin/acp/compatible", a.handleListCompatibleACPAgents)
	mux.HandleFunc("/admin/acp/validate", a.handleValidateACPAgent)
	mux.HandleFunc("/admin/acp/summary", a.handleACPAdminSummary)
	mux.HandleFunc("/admin/acp/bridge-blocks", a.handleListACPBridgeBlocks)
	mux.HandleFunc("/admin/runs", a.handleListRuns)
	mux.HandleFunc("/admin/runs/detail", a.handleRunDetail)
	mux.HandleFunc("/admin/awaits", a.handleListAwaits)
	mux.HandleFunc("/admin/awaits/detail", a.handleAwaitDetail)
	mux.HandleFunc("/admin/audit", a.handleListAuditEvents)
	mux.HandleFunc("/admin/telegram/denials", a.handleListTelegramDenials)
	mux.HandleFunc("/admin/telegram/failures", a.handleListTelegramFailures)
	mux.HandleFunc("/admin/telegram/trust/summary", a.handleTelegramTrustSummary)
	mux.HandleFunc("/admin/telegram/trust/decisions", a.handleTelegramTrustDecisions)
	mux.HandleFunc("/admin/surfaces/sessions", a.handleListSurfaceSessions)
	mux.HandleFunc("/admin/surfaces/sessions/switch", a.handleSwitchSurfaceSession)
	mux.HandleFunc("/admin/surfaces/sessions/close", a.handleCloseSurfaceSession)
	mux.HandleFunc("/admin/telegram/users", a.handleListTelegramUsers)
	mux.HandleFunc("/admin/telegram/users/detail", a.handleTelegramUserDetail)
	mux.HandleFunc("/admin/telegram/users/summary", a.handleTelegramUserSummary)
	mux.HandleFunc("/admin/telegram/users/upsert", a.handleUpsertTelegramUser)
	mux.HandleFunc("/admin/telegram/users/delete", a.handleDeleteTelegramUser)
	mux.HandleFunc("/admin/telegram/requests", a.handleListTelegramRequests)
	mux.HandleFunc("/admin/telegram/requests/resolve", a.handleResolveTelegramRequest)
	mux.HandleFunc("/admin/runtime", a.handleRuntimeStatus)
	mux.HandleFunc("/admin/trust", a.handleTrustAdminIndex)
	mux.HandleFunc("/admin/trust/app.js", a.handleTrustAdminJS)
	mux.HandleFunc("/admin/trust/app.css", a.handleTrustAdminCSS)
	mux.HandleFunc("/admin/trust/summary", a.handleTrustSummary)
	mux.HandleFunc("/admin/trust/policies", a.handleListTrustPolicies)
	mux.HandleFunc("/admin/trust/policies/upsert", a.handleUpsertTrustPolicy)
	mux.HandleFunc("/admin/trust/users", a.handleListTrustUsers)
	mux.HandleFunc("/admin/trust/users/detail", a.handleTrustUserDetail)
	mux.HandleFunc("/admin/trust/links/revoke", a.handleTrustRevokeLink)
	mux.HandleFunc("/admin/trust/events", a.handleTrustEvents)
	mux.HandleFunc("/admin/retention", a.handleRetentionStatus)
	mux.HandleFunc("/admin/retention/run", a.handleRunRetention)
	mux.HandleFunc("/admin/retention/policy/upsert", a.handleUpsertRetentionPolicy)
	mux.HandleFunc("/admin/retention/policy/delete", a.handleDeleteRetentionPolicy)
	mux.HandleFunc("/admin/messages", a.handleListMessages)
	mux.HandleFunc("/admin/artifacts", a.handleListArtifacts)
	mux.HandleFunc("/admin/deliveries", a.handleListDeliveries)
	mux.HandleFunc("/admin/runs/cancel", a.handleCancelRun)
	mux.HandleFunc("/admin/deliveries/retry", a.handleRetryDelivery)
	return tracex.Middleware("admin", a.adminAuthMiddleware(mux))
}

func (a *App) adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.adminAuthExempt(r.URL.Path) || strings.TrimSpace(a.Config.AdminBearerToken) == "" {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		if subtle.ConstantTimeCompare([]byte(token), []byte(a.Config.AdminBearerToken)) != 1 {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) adminAuthExempt(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/admin/trust", "/admin/trust/app.js", "/admin/trust/app.css":
		return true
	default:
		return false
	}
}

func (a *App) WorkerLoop(ctx context.Context) error {
	ticker := time.NewTicker(a.Config.WorkerPollInterval)
	reconcileTicker := time.NewTicker(a.Config.ReconcilerInterval)
	var retentionTicker *time.Ticker
	var retentionCh <-chan time.Time
	if a.Config.RetentionEnabled {
		retentionTicker = time.NewTicker(a.Config.RetentionInterval)
		retentionCh = retentionTicker.C
	}
	defer ticker.Stop()
	defer reconcileTicker.Stop()
	if retentionTicker != nil {
		defer retentionTicker.Stop()
	}
	for {
		if err := a.Worker.ProcessOnce(ctx, 10); err != nil {
			a.Runtime.MarkWorkerError(err)
			return err
		}
		a.Runtime.MarkWorkerRun(time.Now().UTC())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-reconcileTicker.C:
			if err := a.Reconciler.RunOnce(ctx, 10); err != nil {
				a.Runtime.MarkReconcileError(err)
				return err
			}
			a.Runtime.MarkReconcileRun(time.Now().UTC())
		case <-retentionCh:
			summary, err := a.Retention.RunOnce(ctx, "", false, a.Config.RetentionBatchSize)
			if err != nil {
				a.Runtime.MarkRetentionError(err)
				return err
			}
			a.Runtime.MarkRetentionRun(time.Now().UTC(), summary.Totals)
		}
	}
}

func buildDeliveryPayload(text string) domain.OutboundDelivery {
	return domain.OutboundDelivery{
		PayloadJSON: []byte(text),
	}
}
