package services

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type workerRepo struct {
	outboxEvents        []domain.OutboxEvent
	queueItem           domain.QueueItem
	session             domain.Session
	message             domain.Message
	route               domain.RouteDecision
	delivery            domain.OutboundDelivery
	storedOutboundID    string
	storedOutboundText  string
	storedArtifacts     []domain.Artifact
	storedArtifactsDir  string
	storedAwaits        []domain.Await
	updatedACPSessionID string
	auditEvents         []domain.AuditEvent
	runStatusUpdated    bool
	queueStatusUpdated  bool
	markedSending       []string
	markedSent          []string
	markedFailed        []string
}

func (r *workerRepo) InTx(ctx context.Context, fn func(context.Context, ports.Repository) error) error {
	return fn(ctx, r)
}
func (r *workerRepo) RecordInboundReceipt(context.Context, domain.CanonicalInboundEvent) (bool, error) {
	return true, nil
}
func (r *workerRepo) ResolveSession(context.Context, domain.CanonicalInboundEvent, string) (domain.Session, bool, error) {
	return domain.Session{}, false, nil
}
func (r *workerRepo) HasActiveRun(context.Context, string) (bool, error) { return false, nil }
func (r *workerRepo) StoreInboundMessage(context.Context, domain.CanonicalInboundEvent, string) (string, error) {
	return "", nil
}
func (r *workerRepo) StoreOutboundMessage(_ context.Context, _ domain.Session, _ string, text string, _ []byte) (string, error) {
	r.storedOutboundID = "msg_out_1"
	r.storedOutboundText = text
	return r.storedOutboundID, nil
}
func (r *workerRepo) StoreArtifacts(_ context.Context, _ string, direction string, artifacts []domain.Artifact) error {
	r.storedArtifactsDir = direction
	r.storedArtifacts = append([]domain.Artifact{}, artifacts...)
	return nil
}
func (r *workerRepo) EnqueueMessage(context.Context, domain.CanonicalInboundEvent, domain.Session, domain.RouteDecision, string, bool) (domain.QueueItem, *domain.OutboxEvent, error) {
	return domain.QueueItem{}, nil, nil
}
func (r *workerRepo) CreateRun(context.Context, domain.Run) error { return nil }
func (r *workerRepo) UpdateRunStatus(context.Context, string, string) error {
	r.runStatusUpdated = true
	return nil
}
func (r *workerRepo) UpdateQueueItemStatus(context.Context, string, string) error {
	r.queueStatusUpdated = true
	return nil
}
func (r *workerRepo) UpdateActiveQueueItemStatus(context.Context, string, string) error { return nil }
func (r *workerRepo) EnqueueNextQueueItem(context.Context, string) (*domain.OutboxEvent, error) {
	return nil, nil
}
func (r *workerRepo) StoreAwait(_ context.Context, await domain.Await) error {
	r.storedAwaits = append(r.storedAwaits, await)
	return nil
}
func (r *workerRepo) ResolveAwait(context.Context, string, string, []byte) (domain.Await, error) {
	return domain.Await{}, nil
}
func (r *workerRepo) GetAwait(context.Context, string) (domain.Await, error) {
	return domain.Await{}, nil
}
func (r *workerRepo) EnqueueAwaitResume(context.Context, domain.ResumeRequest, string) error {
	return nil
}
func (r *workerRepo) EnqueueDelivery(context.Context, domain.OutboundDelivery) error { return nil }
func (r *workerRepo) ClaimOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) {
	return r.outboxEvents, nil
}
func (r *workerRepo) MarkOutboxDone(context.Context, string) error                     { return nil }
func (r *workerRepo) MarkOutboxFailed(context.Context, string, error, time.Time) error { return nil }
func (r *workerRepo) GetQueueItem(context.Context, string) (domain.QueueItem, error) {
	return r.queueItem, nil
}
func (r *workerRepo) GetQueueStartIdempotencyKey(context.Context, string) (string, error) {
	return "", nil
}
func (r *workerRepo) GetSession(context.Context, string) (domain.Session, error) {
	return r.session, nil
}
func (r *workerRepo) UpdateSessionACPSessionID(_ context.Context, sessionID, acpSessionID string) error {
	if r.session.ID == sessionID {
		r.session.ACPSessionID = acpSessionID
	}
	r.updatedACPSessionID = acpSessionID
	return nil
}
func (r *workerRepo) GetRouteDecision(context.Context, string) (domain.RouteDecision, error) {
	return r.route, nil
}
func (r *workerRepo) GetInboundMessage(context.Context, string) (domain.Message, error) {
	return r.message, nil
}
func (r *workerRepo) GetRun(context.Context, string) (domain.Run, error) { return domain.Run{}, nil }
func (r *workerRepo) GetRunByACP(context.Context, string) (domain.Run, error) {
	return domain.Run{}, nil
}
func (r *workerRepo) GetAwaitsForRun(context.Context, string, int) ([]domain.Await, error) {
	return nil, nil
}
func (r *workerRepo) GetAwaitResponses(context.Context, string, int) ([]domain.AwaitResponse, error) {
	return nil, nil
}
func (r *workerRepo) ListMessages(context.Context, domain.MessageListQuery) (domain.PagedResult[domain.Message], error) {
	return domain.PagedResult[domain.Message]{}, nil
}
func (r *workerRepo) ListArtifacts(context.Context, domain.ArtifactListQuery) (domain.PagedResult[domain.Artifact], error) {
	return domain.PagedResult[domain.Artifact]{}, nil
}
func (r *workerRepo) GetSessionDetail(context.Context, string, int) (domain.SessionDetail, error) {
	return domain.SessionDetail{}, nil
}
func (r *workerRepo) GetRunDetail(context.Context, string, int) (domain.RunDetail, error) {
	return domain.RunDetail{}, nil
}
func (r *workerRepo) GetAwaitDetail(context.Context, string, int) (domain.AwaitDetail, error) {
	return domain.AwaitDetail{}, nil
}
func (r *workerRepo) GetDelivery(context.Context, string) (domain.OutboundDelivery, error) {
	return r.delivery, nil
}
func (r *workerRepo) GetLatestDeliveryByLogicalMessage(context.Context, string) (*domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *workerRepo) MarkDeliverySent(_ context.Context, deliveryID string, _ domain.DeliveryResult) error {
	r.markedSent = append(r.markedSent, deliveryID)
	return nil
}
func (r *workerRepo) MarkDeliverySending(_ context.Context, deliveryID string) error {
	r.markedSending = append(r.markedSending, deliveryID)
	return nil
}
func (r *workerRepo) MarkDeliveryFailed(_ context.Context, deliveryID string, _ error) error {
	r.markedFailed = append(r.markedFailed, deliveryID)
	return nil
}
func (r *workerRepo) ListDeliveries(context.Context, domain.DeliveryListQuery) (domain.PagedResult[domain.OutboundDelivery], error) {
	return domain.PagedResult[domain.OutboundDelivery]{}, nil
}
func (r *workerRepo) CountMessages(context.Context, domain.MessageListQuery) (int, error) {
	return 0, nil
}
func (r *workerRepo) CountArtifacts(context.Context, domain.ArtifactListQuery) (int, error) {
	return 0, nil
}
func (r *workerRepo) CountDeliveries(context.Context, domain.DeliveryListQuery) (int, error) {
	return 0, nil
}
func (r *workerRepo) CountSessions(context.Context, domain.SessionListQuery) (int, error) {
	return 0, nil
}
func (r *workerRepo) CountRuns(context.Context, domain.RunListQuery) (int, error)     { return 0, nil }
func (r *workerRepo) CountAwaits(context.Context, domain.AwaitListQuery) (int, error) { return 0, nil }
func (r *workerRepo) ListSessions(context.Context, domain.SessionListQuery) (domain.PagedResult[domain.Session], error) {
	return domain.PagedResult[domain.Session]{}, nil
}
func (r *workerRepo) ListRuns(context.Context, domain.RunListQuery) (domain.PagedResult[domain.Run], error) {
	return domain.PagedResult[domain.Run]{}, nil
}
func (r *workerRepo) ListAwaits(context.Context, domain.AwaitListQuery) (domain.PagedResult[domain.Await], error) {
	return domain.PagedResult[domain.Await]{}, nil
}
func (r *workerRepo) ListAuditEvents(context.Context, domain.AuditEventListQuery) (domain.PagedResult[domain.AuditEvent], error) {
	return domain.PagedResult[domain.AuditEvent]{}, nil
}
func (r *workerRepo) ListStaleClaimedOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}
func (r *workerRepo) RequeueOutbox(context.Context, string) error                   { return nil }
func (r *workerRepo) RequeueQueueStartOutbox(context.Context, string, string) error { return nil }
func (r *workerRepo) ListStuckQueueItems(context.Context, time.Time, int) ([]domain.QueueItem, error) {
	return nil, nil
}
func (r *workerRepo) ListStaleRuns(context.Context, time.Time, int) ([]domain.Run, error) {
	return nil, nil
}
func (r *workerRepo) ListExpiredAwaits(context.Context, time.Time, int) ([]domain.Await, error) {
	return nil, nil
}
func (r *workerRepo) ListStaleDeliveries(context.Context, time.Time, int, int) ([]domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *workerRepo) ExpireAwait(context.Context, string) error { return nil }
func (r *workerRepo) RepairRunFromSnapshot(context.Context, domain.QueueItem, domain.RunStatusSnapshot) (domain.Run, error) {
	return domain.Run{}, nil
}
func (r *workerRepo) CreateVirtualSession(context.Context, string, string, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *workerRepo) EnsureNotificationSession(context.Context, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *workerRepo) SwitchActiveSession(context.Context, string, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *workerRepo) ListSurfaceSessions(context.Context, string, string, string, string, int) ([]domain.SurfaceSession, error) {
	return nil, nil
}
func (r *workerRepo) CloseActiveSession(context.Context, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *workerRepo) IsTelegramUserAllowed(context.Context, string, string) (bool, error) {
	return false, nil
}
func (r *workerRepo) CountTelegramUserAccess(context.Context, string, string) (int, error) {
	return 0, nil
}
func (r *workerRepo) ListTelegramUserAccessPage(context.Context, domain.TelegramUserAccessListQuery) (domain.PagedResult[domain.TelegramUserAccess], error) {
	return domain.PagedResult[domain.TelegramUserAccess]{}, nil
}
func (r *workerRepo) ListTelegramUserAccess(context.Context, string, int) ([]domain.TelegramUserAccess, error) {
	return nil, nil
}
func (r *workerRepo) ListTelegramUserAccessByStatus(context.Context, string, string, int) ([]domain.TelegramUserAccess, error) {
	return nil, nil
}
func (r *workerRepo) GetTelegramUserAccess(context.Context, string, string) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *workerRepo) UpsertTelegramUserAccess(context.Context, domain.TelegramUserAccess) error {
	return nil
}
func (r *workerRepo) DeleteTelegramUserAccess(context.Context, string, string) error { return nil }
func (r *workerRepo) RequestTelegramAccess(context.Context, domain.TelegramUserAccess) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *workerRepo) ResolveTelegramAccessRequest(context.Context, string, string, string, string) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *workerRepo) CountAuditEvents(context.Context, domain.AuditEventListQuery) (int, error) {
	return 0, nil
}
func (r *workerRepo) Audit(_ context.Context, event domain.AuditEvent) error {
	r.auditEvents = append(r.auditEvents, event)
	return nil
}
func (r *workerRepo) ForceCancelRun(context.Context, string) error { return nil }
func (r *workerRepo) RetryDelivery(context.Context, string) error  { return nil }

type workerACP struct{}

func (workerACP) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) { return nil, nil }
func (workerACP) EnsureSession(context.Context, domain.Session) (string, error) {
	return "acp_session_1", nil
}
func (workerACP) StartRun(context.Context, domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	return domain.Run{
			ID:          "run_1",
			SessionID:   "session_1",
			Status:      "completed",
			StartedAt:   time.Now(),
			LastEventAt: time.Now(),
		}, []domain.RunEvent{{
			RunID:     "run_1",
			Status:    "completed",
			Text:      "done",
			Artifacts: []domain.Artifact{{ID: "artifact_1", Name: "report.txt", StorageURI: "file:///tmp/report.txt"}},
		}}, nil
}
func (workerACP) ResumeRun(context.Context, domain.Await, []byte) ([]domain.RunEvent, error) {
	return nil, nil
}
func (workerACP) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return domain.RunStatusSnapshot{}, nil
}
func (workerACP) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (workerACP) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (workerACP) CancelRun(context.Context, domain.Run) error { return nil }

type awaitingWorkerACP struct{}

func (awaitingWorkerACP) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) {
	return nil, nil
}
func (awaitingWorkerACP) EnsureSession(context.Context, domain.Session) (string, error) {
	return "acp_session_1", nil
}
func (awaitingWorkerACP) StartRun(context.Context, domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	return domain.Run{
			ID:          "run_await_1",
			SessionID:   "session_1",
			Status:      "running",
			StartedAt:   time.Now(),
			LastEventAt: time.Now(),
		}, []domain.RunEvent{{
			RunID:       "run_await_1",
			Status:      "awaiting",
			Text:        "need approval",
			AwaitSchema: []byte(`{"type":"object"}`),
			AwaitPrompt: []byte(`{"text":"approve?"}`),
		}}, nil
}
func (awaitingWorkerACP) ResumeRun(context.Context, domain.Await, []byte) ([]domain.RunEvent, error) {
	return nil, nil
}
func (awaitingWorkerACP) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return domain.RunStatusSnapshot{}, nil
}
func (awaitingWorkerACP) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (awaitingWorkerACP) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (awaitingWorkerACP) CancelRun(context.Context, domain.Run) error { return nil }

type workerCatalogBridge struct {
	agents []domain.AgentManifest
}

func (b workerCatalogBridge) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) {
	return append([]domain.AgentManifest(nil), b.agents...), nil
}
func (workerCatalogBridge) EnsureSession(context.Context, domain.Session) (string, error) {
	return "", nil
}
func (workerCatalogBridge) StartRun(context.Context, domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	return domain.Run{}, nil, nil
}
func (workerCatalogBridge) ResumeRun(context.Context, domain.Await, []byte) ([]domain.RunEvent, error) {
	return nil, nil
}
func (workerCatalogBridge) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return domain.RunStatusSnapshot{}, nil
}
func (workerCatalogBridge) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (workerCatalogBridge) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (workerCatalogBridge) CancelRun(context.Context, domain.Run) error { return nil }

type noopRenderer struct{}

func (noopRenderer) RenderRunEvent(context.Context, domain.Session, domain.RunEvent) ([]domain.OutboundDelivery, error) {
	payload, _ := json.Marshal(map[string]string{"text": "done"})
	return []domain.OutboundDelivery{{ID: "delivery_1", PayloadJSON: payload}}, nil
}

type noopChannel struct{}

func (noopChannel) Channel() string                                            { return "slack" }
func (noopChannel) VerifyInbound(context.Context, *http.Request, []byte) error { return nil }
func (noopChannel) ParseInbound(context.Context, *http.Request, []byte, string) (domain.CanonicalInboundEvent, error) {
	return domain.CanonicalInboundEvent{}, nil
}
func (noopChannel) SendMessage(context.Context, domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return domain.DeliveryResult{}, nil
}
func (noopChannel) SendAwaitPrompt(context.Context, domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return domain.DeliveryResult{}, nil
}

type telegramChannel struct {
	sent []domain.OutboundDelivery
}

func (*telegramChannel) Channel() string                                            { return "telegram" }
func (*telegramChannel) VerifyInbound(context.Context, *http.Request, []byte) error { return nil }
func (*telegramChannel) ParseInbound(context.Context, *http.Request, []byte, string) (domain.CanonicalInboundEvent, error) {
	return domain.CanonicalInboundEvent{}, nil
}
func (c *telegramChannel) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	c.sent = append(c.sent, delivery)
	return domain.DeliveryResult{ProviderMessageID: "telegram_msg_1", ProviderRequestID: "telegram_req_1"}, nil
}
func (*telegramChannel) SendAwaitPrompt(context.Context, domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return domain.DeliveryResult{}, nil
}

func TestWorkerPersistsOutboundArtifacts(t *testing.T) {
	repo := &workerRepo{
		outboxEvents: []domain.OutboxEvent{{ID: "outbox_1", EventType: "queue.start", AggregateID: "queue_1"}},
		queueItem:    domain.QueueItem{ID: "queue_1", SessionID: "session_1", InboundMessageID: "msg_1", Status: "queued"},
		session:      domain.Session{ID: "session_1", TenantID: "tenant_default", ChannelType: "slack", ChannelScopeKey: "C1:T1"},
		message:      domain.Message{MessageID: "msg_1", Text: "generate"},
		route:        domain.RouteDecision{ACPAgentName: "default-agent"},
	}
	worker := WorkerService{
		Repo:     repo,
		ACP:      workerACP{},
		Renderer: noopRenderer{},
		Channel:  noopChannel{},
	}
	if err := worker.ProcessOnce(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if repo.storedOutboundID == "" {
		t.Fatal("expected outbound message to be stored")
	}
	if repo.updatedACPSessionID != "acp_session_1" {
		t.Fatalf("expected ACP session id to be persisted, got %q", repo.updatedACPSessionID)
	}
	if repo.storedArtifactsDir != "outbound" {
		t.Fatalf("expected outbound artifacts, got %s", repo.storedArtifactsDir)
	}
	if len(repo.storedArtifacts) != 1 {
		t.Fatalf("expected one stored artifact, got %d", len(repo.storedArtifacts))
	}
}

func TestWorkerBlocksStructuredAwaitForOpenCodeBridge(t *testing.T) {
	repo := &workerRepo{
		outboxEvents: []domain.OutboxEvent{{ID: "outbox_1", EventType: "queue.start", AggregateID: "queue_1"}},
		queueItem:    domain.QueueItem{ID: "queue_1", SessionID: "session_1", InboundMessageID: "msg_1", Status: "queued"},
		session:      domain.Session{ID: "session_1", TenantID: "tenant_default", ChannelType: "slack", ChannelScopeKey: "C1:T1"},
		message:      domain.Message{MessageID: "msg_1", Text: "generate"},
		route:        domain.RouteDecision{ACPAgentName: "build"},
	}
	worker := WorkerService{
		Repo:     repo,
		ACP:      awaitingWorkerACP{},
		Renderer: noopRenderer{},
		Channel:  noopChannel{},
		Catalog: &AgentCatalog{
			Bridge: workerCatalogBridge{agents: []domain.AgentManifest{{
				Name:                    "build",
				Protocol:                "opencode",
				Healthy:                 true,
				SupportsAwaitResume:     false,
				SupportsStructuredAwait: false,
				SupportsSessionReload:   true,
				SupportsStreaming:       true,
				SupportsArtifacts:       true,
			}}},
			TTL: time.Minute,
		},
	}
	if err := worker.ProcessOnce(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(repo.storedAwaits) != 0 {
		t.Fatalf("expected no persisted awaits for OpenCode bridge, got %+v", repo.storedAwaits)
	}
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "worker.await_blocked_opencode_bridge" {
		t.Fatalf("expected await-blocked audit event, got %+v", repo.auditEvents)
	}
	if repo.storedOutboundID == "" {
		t.Fatal("expected synthetic terminal outbound message to be stored")
	}
	if repo.storedOutboundText != openCodeAwaitBlockedReason {
		t.Fatalf("unexpected outbound failure text: %q", repo.storedOutboundText)
	}
	if !repo.runStatusUpdated || !repo.queueStatusUpdated {
		t.Fatalf("expected run and queue status updates, got run=%v queue=%v", repo.runStatusUpdated, repo.queueStatusUpdated)
	}
}

func TestWorkerProcessesTelegramDelivery(t *testing.T) {
	repo := &workerRepo{
		outboxEvents: []domain.OutboxEvent{{
			ID:          "outbox_delivery_1",
			EventType:   "delivery.send",
			AggregateID: "delivery_telegram_1",
		}},
		delivery: domain.OutboundDelivery{
			ID:          "delivery_telegram_1",
			SessionID:   "session_notice_telegram_123",
			ChannelType: "telegram",
			Status:      "queued",
			PayloadJSON: []byte(`{"text":"Your access request was approved."}`),
		},
	}
	telegram := &telegramChannel{}
	worker := WorkerService{
		Repo:     repo,
		ACP:      workerACP{},
		Renderer: noopRenderer{},
		Channel:  noopChannel{},
		Channels: map[string]ports.ChannelAdapter{"telegram": telegram},
	}
	if err := worker.ProcessOnce(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(repo.markedSending) != 1 || repo.markedSending[0] != "delivery_telegram_1" {
		t.Fatalf("expected delivery to be marked sending, got %+v", repo.markedSending)
	}
	if len(repo.markedSent) != 1 || repo.markedSent[0] != "delivery_telegram_1" {
		t.Fatalf("expected delivery to be marked sent, got %+v", repo.markedSent)
	}
	if len(repo.markedFailed) != 0 {
		t.Fatalf("expected no failed deliveries, got %+v", repo.markedFailed)
	}
	if len(telegram.sent) != 1 {
		t.Fatalf("expected telegram delivery to be sent once, got %d", len(telegram.sent))
	}
	if telegram.sent[0].SessionID != "session_notice_telegram_123" {
		t.Fatalf("expected telegram delivery session to be preserved, got %+v", telegram.sent[0])
	}
}
