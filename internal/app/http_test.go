package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	acpadapter "nexus/internal/adapters/acp"
	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/services"
)

type testChannelAdapter struct {
	event domain.CanonicalInboundEvent
	sent  []domain.OutboundDelivery
}

func (a testChannelAdapter) Channel() string                                            { return a.event.Channel }
func (a testChannelAdapter) VerifyInbound(context.Context, *http.Request, []byte) error { return nil }
func (a testChannelAdapter) ParseInbound(context.Context, *http.Request, []byte, string) (domain.CanonicalInboundEvent, error) {
	return a.event, nil
}
func (a *testChannelAdapter) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	a.sent = append(a.sent, delivery)
	return domain.DeliveryResult{}, nil
}
func (a *testChannelAdapter) SendAwaitPrompt(context.Context, domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return domain.DeliveryResult{}, nil
}

var _ ports.ChannelAdapter = (*testChannelAdapter)(nil)

type testACPBridge struct {
	agents        []domain.AgentManifest
	discoverErr   error
	runtimeStatus acpadapter.StdioRuntimeStatus
}

func (b testACPBridge) DiscoverAgents(context.Context) ([]domain.AgentManifest, error) {
	if b.discoverErr != nil {
		return nil, b.discoverErr
	}
	return append([]domain.AgentManifest(nil), b.agents...), nil
}
func (b testACPBridge) EnsureSession(context.Context, domain.Session) (string, error) {
	return "", nil
}
func (b testACPBridge) StartRun(context.Context, domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	return domain.Run{}, nil, nil
}
func (b testACPBridge) ResumeRun(context.Context, domain.Await, []byte) ([]domain.RunEvent, error) {
	return nil, nil
}
func (b testACPBridge) GetRun(context.Context, string) (domain.RunStatusSnapshot, error) {
	return domain.RunStatusSnapshot{}, nil
}
func (b testACPBridge) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (b testACPBridge) FindLatestRunForSession(context.Context, domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (b testACPBridge) CancelRun(context.Context, domain.Run) error { return nil }
func (b testACPBridge) RuntimeStatus() acpadapter.StdioRuntimeStatus {
	return b.runtimeStatus
}

var _ ports.ACPBridge = testACPBridge{}

type testRouter struct{}

func (testRouter) Route(context.Context, domain.CanonicalInboundEvent, domain.Session) (domain.RouteDecision, error) {
	return domain.RouteDecision{
		AgentProfileID: "agent_profile_default",
		ACPAgentName:   "agent_default",
	}, nil
}

var _ ports.Router = testRouter{}

type appRepoStub struct {
	surfaceItems          []domain.SurfaceSession
	switchedSession       domain.Session
	closedSession         domain.Session
	notificationSession   domain.Session
	forcedCanceledRunID   string
	retriedDeliveryID     string
	auditEvents           []domain.AuditEvent
	deliveries            []domain.OutboundDelivery
	telegramAllowed       map[string]bool
	telegramUsers         []domain.TelegramUserAccess
	telegramAccessQueries []domain.TelegramUserAccessListQuery
	auditPage             domain.PagedResult[domain.AuditEvent]
	auditPagesByEventType map[string]domain.PagedResult[domain.AuditEvent]
	lastAuditQuery        domain.AuditEventListQuery
	auditQueries          []domain.AuditEventListQuery
	sessionDetail         domain.SessionDetail
	runDetail             domain.RunDetail
	awaitDetail           domain.AwaitDetail
	upsertErr             error
	deleteErr             error
	deliveryCounts        map[string]int
	runCounts             map[string]int
	awaitCounts           map[string]int
	auditCounts           map[string]int
}

func (r *appRepoStub) InTx(ctx context.Context, fn func(context.Context, ports.Repository) error) error {
	return fn(ctx, r)
}
func (r *appRepoStub) RecordInboundReceipt(context.Context, domain.CanonicalInboundEvent) (bool, error) {
	return true, nil
}
func (r *appRepoStub) ResolveSession(context.Context, domain.CanonicalInboundEvent, string) (domain.Session, bool, error) {
	return domain.Session{}, false, nil
}
func (r *appRepoStub) HasActiveRun(context.Context, string) (bool, error) { return false, nil }
func (r *appRepoStub) StoreInboundMessage(context.Context, domain.CanonicalInboundEvent, string) (string, error) {
	return "", nil
}
func (r *appRepoStub) StoreOutboundMessage(context.Context, domain.Session, string, string, []byte) (string, error) {
	return "", nil
}
func (r *appRepoStub) StoreArtifacts(context.Context, string, string, []domain.Artifact) error {
	return nil
}
func (r *appRepoStub) EnqueueMessage(context.Context, domain.CanonicalInboundEvent, domain.Session, domain.RouteDecision, string, bool) (domain.QueueItem, *domain.OutboxEvent, error) {
	return domain.QueueItem{}, nil, nil
}
func (r *appRepoStub) CreateRun(context.Context, domain.Run) error                       { return nil }
func (r *appRepoStub) UpdateRunStatus(context.Context, string, string) error             { return nil }
func (r *appRepoStub) UpdateQueueItemStatus(context.Context, string, string) error       { return nil }
func (r *appRepoStub) UpdateActiveQueueItemStatus(context.Context, string, string) error { return nil }
func (r *appRepoStub) EnqueueNextQueueItem(context.Context, string) (*domain.OutboxEvent, error) {
	return nil, nil
}
func (r *appRepoStub) StoreAwait(context.Context, domain.Await) error { return nil }
func (r *appRepoStub) ResolveAwait(context.Context, string, string, []byte) (domain.Await, error) {
	return domain.Await{}, nil
}
func (r *appRepoStub) GetAwait(context.Context, string) (domain.Await, error) {
	return domain.Await{}, nil
}
func (r *appRepoStub) EnqueueAwaitResume(context.Context, domain.ResumeRequest, string) error {
	return nil
}
func (r *appRepoStub) EnqueueDelivery(_ context.Context, delivery domain.OutboundDelivery) error {
	r.deliveries = append(r.deliveries, delivery)
	return nil
}
func (r *appRepoStub) ClaimOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}
func (r *appRepoStub) MarkOutboxDone(context.Context, string) error                     { return nil }
func (r *appRepoStub) MarkOutboxFailed(context.Context, string, error, time.Time) error { return nil }
func (r *appRepoStub) GetQueueItem(context.Context, string) (domain.QueueItem, error) {
	return domain.QueueItem{}, nil
}
func (r *appRepoStub) GetQueueStartIdempotencyKey(context.Context, string) (string, error) {
	return "", nil
}
func (r *appRepoStub) GetSession(context.Context, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *appRepoStub) UpdateSessionACPSessionID(context.Context, string, string) error {
	return nil
}
func (r *appRepoStub) GetRouteDecision(context.Context, string) (domain.RouteDecision, error) {
	return domain.RouteDecision{}, nil
}
func (r *appRepoStub) GetInboundMessage(context.Context, string) (domain.Message, error) {
	return domain.Message{}, nil
}
func (r *appRepoStub) GetDelivery(context.Context, string) (domain.OutboundDelivery, error) {
	return domain.OutboundDelivery{}, nil
}
func (r *appRepoStub) GetLatestDeliveryByLogicalMessage(context.Context, string) (*domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *appRepoStub) GetRun(context.Context, string) (domain.Run, error) { return domain.Run{}, nil }
func (r *appRepoStub) GetRunByACP(context.Context, string) (domain.Run, error) {
	return domain.Run{}, nil
}
func (r *appRepoStub) GetAwaitsForRun(context.Context, string, int) ([]domain.Await, error) {
	return nil, nil
}
func (r *appRepoStub) GetAwaitResponses(context.Context, string, int) ([]domain.AwaitResponse, error) {
	return nil, nil
}
func (r *appRepoStub) MarkDeliverySent(context.Context, string, domain.DeliveryResult) error {
	return nil
}
func (r *appRepoStub) MarkDeliverySending(context.Context, string) error       { return nil }
func (r *appRepoStub) MarkDeliveryFailed(context.Context, string, error) error { return nil }
func (r *appRepoStub) ListMessages(context.Context, domain.MessageListQuery) (domain.PagedResult[domain.Message], error) {
	return domain.PagedResult[domain.Message]{}, nil
}
func (r *appRepoStub) CountMessages(context.Context, domain.MessageListQuery) (int, error) {
	return 1, nil
}
func (r *appRepoStub) ListArtifacts(context.Context, domain.ArtifactListQuery) (domain.PagedResult[domain.Artifact], error) {
	return domain.PagedResult[domain.Artifact]{}, nil
}
func (r *appRepoStub) CountArtifacts(context.Context, domain.ArtifactListQuery) (int, error) {
	return 1, nil
}
func (r *appRepoStub) ListDeliveries(context.Context, domain.DeliveryListQuery) (domain.PagedResult[domain.OutboundDelivery], error) {
	return domain.PagedResult[domain.OutboundDelivery]{}, nil
}
func (r *appRepoStub) CountDeliveries(_ context.Context, query domain.DeliveryListQuery) (int, error) {
	if r.deliveryCounts != nil {
		return r.deliveryCounts[query.Status], nil
	}
	return 1, nil
}
func (r *appRepoStub) ListSessions(context.Context, domain.SessionListQuery) (domain.PagedResult[domain.Session], error) {
	return domain.PagedResult[domain.Session]{}, nil
}
func (r *appRepoStub) CountSessions(context.Context, domain.SessionListQuery) (int, error) {
	return 1, nil
}
func (r *appRepoStub) ListRuns(context.Context, domain.RunListQuery) (domain.PagedResult[domain.Run], error) {
	return domain.PagedResult[domain.Run]{}, nil
}
func (r *appRepoStub) CountRuns(_ context.Context, query domain.RunListQuery) (int, error) {
	if r.runCounts != nil {
		return r.runCounts[query.Status], nil
	}
	return 1, nil
}
func (r *appRepoStub) CountAwaits(_ context.Context, query domain.AwaitListQuery) (int, error) {
	if r.awaitCounts != nil {
		return r.awaitCounts[query.Status], nil
	}
	return 1, nil
}
func (r *appRepoStub) ListAwaits(context.Context, domain.AwaitListQuery) (domain.PagedResult[domain.Await], error) {
	return domain.PagedResult[domain.Await]{}, nil
}
func (r *appRepoStub) ListAuditEvents(_ context.Context, query domain.AuditEventListQuery) (domain.PagedResult[domain.AuditEvent], error) {
	r.lastAuditQuery = query
	r.auditQueries = append(r.auditQueries, query)
	if r.auditPagesByEventType != nil {
		if page, ok := r.auditPagesByEventType[query.EventType]; ok {
			if query.Limit > 0 && len(page.Items) > query.Limit {
				page.Items = page.Items[:query.Limit]
			}
			return page, nil
		}
	}
	if query.Limit > 0 && len(r.auditPage.Items) > query.Limit {
		page := r.auditPage
		page.Items = page.Items[:query.Limit]
		return page, nil
	}
	return r.auditPage, nil
}
func (r *appRepoStub) GetSessionDetail(context.Context, string, int) (domain.SessionDetail, error) {
	return r.sessionDetail, nil
}
func (r *appRepoStub) GetRunDetail(context.Context, string, int) (domain.RunDetail, error) {
	return r.runDetail, nil
}
func (r *appRepoStub) GetAwaitDetail(context.Context, string, int) (domain.AwaitDetail, error) {
	return r.awaitDetail, nil
}
func (r *appRepoStub) ListStaleClaimedOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}
func (r *appRepoStub) RequeueOutbox(context.Context, string) error                   { return nil }
func (r *appRepoStub) RequeueQueueStartOutbox(context.Context, string, string) error { return nil }
func (r *appRepoStub) ListStuckQueueItems(context.Context, time.Time, int) ([]domain.QueueItem, error) {
	return nil, nil
}
func (r *appRepoStub) ListStaleRuns(context.Context, time.Time, int) ([]domain.Run, error) {
	return nil, nil
}
func (r *appRepoStub) ListExpiredAwaits(context.Context, time.Time, int) ([]domain.Await, error) {
	return nil, nil
}
func (r *appRepoStub) ListStaleDeliveries(context.Context, time.Time, int, int) ([]domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *appRepoStub) ExpireAwait(context.Context, string) error { return nil }
func (r *appRepoStub) RepairRunFromSnapshot(context.Context, domain.QueueItem, domain.RunStatusSnapshot) (domain.Run, error) {
	return domain.Run{}, nil
}
func (r *appRepoStub) CreateVirtualSession(context.Context, string, string, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *appRepoStub) EnsureNotificationSession(_ context.Context, tenantID string, channelType string, surfaceKey string, ownerUserID string) (domain.Session, error) {
	return domain.Session{
		ID:          fmt.Sprintf("session_notice_%s_%s_%s_%s", tenantID, channelType, surfaceKey, ownerUserID),
		ChannelType: channelType,
		State:       "open",
	}, nil
}
func (r *appRepoStub) SwitchActiveSession(context.Context, string, string, string, string, string) (domain.Session, error) {
	return r.switchedSession, nil
}
func (r *appRepoStub) ListSurfaceSessions(context.Context, string, string, string, string, int) ([]domain.SurfaceSession, error) {
	return r.surfaceItems, nil
}
func (r *appRepoStub) CloseActiveSession(context.Context, string, string, string, string) (domain.Session, error) {
	return r.closedSession, nil
}
func (r *appRepoStub) IsTelegramUserAllowed(_ context.Context, _ string, telegramUserID string) (bool, error) {
	return r.telegramAllowed[telegramUserID], nil
}
func (r *appRepoStub) CountTelegramUserAccess(_ context.Context, _ string, status string) (int, error) {
	count := 0
	for _, item := range r.telegramUsers {
		if status == "" || item.Status == status {
			count++
		}
	}
	return count, nil
}
func (r *appRepoStub) ListTelegramUserAccessPage(_ context.Context, query domain.TelegramUserAccessListQuery) (domain.PagedResult[domain.TelegramUserAccess], error) {
	r.telegramAccessQueries = append(r.telegramAccessQueries, query)
	items := make([]domain.TelegramUserAccess, 0, len(r.telegramUsers))
	for _, item := range r.telegramUsers {
		if query.Status == "" || item.Status == query.Status {
			items = append(items, item)
		}
	}
	result := domain.PagedResult[domain.TelegramUserAccess]{Items: items}
	if query.Limit > 0 && len(items) > query.Limit {
		result.Items = items[:query.Limit]
		result.NextCursor = formatLocalCursor(decisionTime(items[query.Limit-1]), items[query.Limit-1].TelegramUserID)
	}
	return result, nil
}
func (r *appRepoStub) ListTelegramUserAccess(context.Context, string, int) ([]domain.TelegramUserAccess, error) {
	return r.telegramUsers, nil
}
func (r *appRepoStub) ListTelegramUserAccessByStatus(_ context.Context, _ string, status string, limit int) ([]domain.TelegramUserAccess, error) {
	out := make([]domain.TelegramUserAccess, 0, len(r.telegramUsers))
	for _, item := range r.telegramUsers {
		if item.Status == status {
			out = append(out, item)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *appRepoStub) GetTelegramUserAccess(_ context.Context, _ string, telegramUserID string) (domain.TelegramUserAccess, error) {
	for _, item := range r.telegramUsers {
		if item.TelegramUserID == telegramUserID {
			return item, nil
		}
	}
	return domain.TelegramUserAccess{}, context.Canceled
}
func (r *appRepoStub) UpsertTelegramUserAccess(_ context.Context, entry domain.TelegramUserAccess) error {
	if r.upsertErr != nil {
		return r.upsertErr
	}
	if r.telegramAllowed == nil {
		r.telegramAllowed = map[string]bool{}
	}
	r.telegramAllowed[entry.TelegramUserID] = entry.Allowed
	r.telegramUsers = append(r.telegramUsers, entry)
	return nil
}
func (r *appRepoStub) DeleteTelegramUserAccess(_ context.Context, _ string, telegramUserID string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.telegramAllowed, telegramUserID)
	filtered := r.telegramUsers[:0]
	for _, item := range r.telegramUsers {
		if item.TelegramUserID != telegramUserID {
			filtered = append(filtered, item)
		}
	}
	r.telegramUsers = filtered
	return nil
}
func (r *appRepoStub) RequestTelegramAccess(_ context.Context, entry domain.TelegramUserAccess) (domain.TelegramUserAccess, error) {
	for _, item := range r.telegramUsers {
		if item.TelegramUserID == entry.TelegramUserID {
			switch item.Status {
			case "pending":
				return item, nil
			case "approved", "denied":
				return domain.TelegramUserAccess{}, domain.ErrTelegramAccessRequestAlreadyFinal
			}
		}
	}
	entry.Status = "pending"
	entry.Allowed = false
	r.telegramUsers = append(r.telegramUsers, entry)
	return entry, nil
}
func (r *appRepoStub) ResolveTelegramAccessRequest(_ context.Context, _ string, telegramUserID, status, addedBy string) (domain.TelegramUserAccess, error) {
	for i := range r.telegramUsers {
		if r.telegramUsers[i].TelegramUserID == telegramUserID {
			if r.telegramUsers[i].Status != "pending" {
				return domain.TelegramUserAccess{}, domain.ErrTelegramAccessRequestNotPending
			}
			r.telegramUsers[i].Status = status
			r.telegramUsers[i].Allowed = status == "approved"
			r.telegramUsers[i].AddedBy = addedBy
			return r.telegramUsers[i], nil
		}
	}
	return domain.TelegramUserAccess{}, domain.ErrTelegramAccessRequestNotFound
}
func (r *appRepoStub) CountAuditEvents(_ context.Context, query domain.AuditEventListQuery) (int, error) {
	if r.auditCounts != nil {
		return r.auditCounts[query.EventType], nil
	}
	if r.auditPagesByEventType != nil {
		if page, ok := r.auditPagesByEventType[query.EventType]; ok {
			return len(page.Items), nil
		}
	}
	return len(r.auditPage.Items), nil
}
func (r *appRepoStub) Audit(_ context.Context, event domain.AuditEvent) error {
	r.auditEvents = append(r.auditEvents, event)
	return nil
}
func (r *appRepoStub) ForceCancelRun(_ context.Context, runID string) error {
	r.forcedCanceledRunID = runID
	return nil
}
func (r *appRepoStub) RetryDelivery(_ context.Context, deliveryID string) error {
	r.retriedDeliveryID = deliveryID
	return nil
}

func TestHandleChannelWebhookRejectsUnauthorizedTelegramUser(t *testing.T) {
	repo := &appRepoStub{telegramAllowed: map[string]bool{}}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", TelegramAllowedUserIDs: []string{"123"}},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	adapter := &testChannelAdapter{event: domain.CanonicalInboundEvent{
		Channel:      "telegram",
		Sender:       domain.Sender{ChannelUserID: "999"},
		Conversation: domain.Conversation{ChannelConversationID: "chat123"},
	}}
	app.handleChannelWebhook(rec, req, adapter)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if len(repo.auditEvents) != 2 || repo.auditEvents[0].EventType != "telegram.access_requested" || repo.auditEvents[1].EventType != "telegram.allowlist_denied" {
		t.Fatalf("expected request and deny audit events, got %+v", repo.auditEvents)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Channel        string `json:"channel"`
			ChannelUserID  string `json:"channel_user_id"`
			ConversationID string `json:"conversation_id"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "pairing_requested" || payload.Meta.Channel != "telegram" || payload.Meta.ChannelUserID != "999" || payload.Meta.ConversationID != "chat123" {
		t.Fatalf("unexpected pairing request payload: %+v", payload)
	}
	if len(repo.telegramUsers) != 1 || repo.telegramUsers[0].Status != "pending" {
		t.Fatalf("expected pending telegram access request, got %+v", repo.telegramUsers)
	}
	if len(repo.deliveries) != 1 {
		t.Fatalf("expected pairing response delivery to be queued, got %d", len(repo.deliveries))
	}
	if repo.deliveries[0].SessionID != "session_notice_tenant_default_telegram_chat123_999" {
		t.Fatalf("expected pairing delivery to use notification session, got %+v", repo.deliveries[0])
	}
}

func TestHandleChannelWebhookDoesNotDuplicatePendingTelegramRequest(t *testing.T) {
	repo := &appRepoStub{
		telegramAllowed: map[string]bool{},
		telegramUsers: []domain.TelegramUserAccess{{
			TenantID:       "tenant_default",
			TelegramUserID: "999",
			Status:         "pending",
			Allowed:        false,
		}},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	adapter := &testChannelAdapter{event: domain.CanonicalInboundEvent{
		Channel:         "telegram",
		ProviderEventID: "evt_repeat_1",
		Sender:          domain.Sender{ChannelUserID: "999"},
		Conversation:    domain.Conversation{ChannelConversationID: "chat123"},
	}}
	app.handleChannelWebhook(rec, req, adapter)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Channel        string `json:"channel"`
			ChannelUserID  string `json:"channel_user_id"`
			ConversationID string `json:"conversation_id"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "pairing_pending" || payload.Meta.Channel != "telegram" || payload.Meta.ChannelUserID != "999" || payload.Meta.ConversationID != "chat123" {
		t.Fatalf("expected pairing_pending status, got %+v", payload)
	}
	if len(repo.telegramUsers) != 1 {
		t.Fatalf("expected no duplicate telegram request rows, got %+v", repo.telegramUsers)
	}
	if len(repo.auditEvents) != 0 {
		t.Fatalf("expected no new audit events for duplicate pending request, got %+v", repo.auditEvents)
	}
	if len(repo.deliveries) != 0 {
		t.Fatalf("expected no duplicate queued pairing notices, got %+v", repo.deliveries)
	}
}

func TestHandleChannelWebhookQueuesStableDeniedTelegramNotice(t *testing.T) {
	repo := &appRepoStub{
		telegramAllowed: map[string]bool{},
		telegramUsers: []domain.TelegramUserAccess{{
			TenantID:       "tenant_default",
			TelegramUserID: "999",
			Status:         "denied",
			Allowed:        false,
		}},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	adapter := &testChannelAdapter{event: domain.CanonicalInboundEvent{
		Channel:         "telegram",
		ProviderEventID: "evt_denied_1",
		Sender:          domain.Sender{ChannelUserID: "999"},
		Conversation:    domain.Conversation{ChannelConversationID: "chat123"},
	}}
	app.handleChannelWebhook(rec, req, adapter)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Channel        string `json:"channel"`
			ChannelUserID  string `json:"channel_user_id"`
			ConversationID string `json:"conversation_id"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "access_denied" || payload.Meta.Channel != "telegram" || payload.Meta.ChannelUserID != "999" || payload.Meta.ConversationID != "chat123" {
		t.Fatalf("expected access_denied status, got %+v", payload)
	}
	if len(repo.deliveries) != 1 {
		t.Fatalf("expected one denied notice delivery, got %+v", repo.deliveries)
	}
	if repo.deliveries[0].SessionID != "session_notice_tenant_default_telegram_chat123_999" {
		t.Fatalf("expected denied notice to use notification session, got %+v", repo.deliveries[0])
	}
	if repo.deliveries[0].DeliveryKind != "replace" {
		t.Fatalf("expected denied notice to use replace delivery, got %+v", repo.deliveries[0])
	}
	if repo.deliveries[0].LogicalMessageID != "logical_access_denied_999" {
		t.Fatalf("expected stable denied logical message id, got %+v", repo.deliveries[0])
	}
	if len(repo.auditEvents) != 0 {
		t.Fatalf("expected no new audit events for denied repeat contact, got %+v", repo.auditEvents)
	}
}

func TestHandleChannelWebhookChallengeEnvelope(t *testing.T) {
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: &appRepoStub{}}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	adapter := &testChannelAdapter{event: domain.CanonicalInboundEvent{
		Channel:     "slack",
		Interaction: "challenge",
		Message:     domain.Message{Text: "challenge-token"},
	}}
	app.handleChannelWebhook(rec, req, adapter)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Challenge string `json:"challenge"`
		} `json:"data"`
		Meta struct {
			Interaction string `json:"interaction"`
			Channel     string `json:"channel"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Challenge != "challenge-token" || payload.Meta.Interaction != "challenge" || payload.Meta.Channel != "slack" {
		t.Fatalf("unexpected challenge payload: %+v", payload)
	}
}

func TestHandleChannelWebhookAwaitResponseEnvelope(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   &appRepoStub{},
		Await:  services.AwaitService{Repo: &appRepoStub{}},
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	adapter := &testChannelAdapter{event: domain.CanonicalInboundEvent{
		TenantID:    "tenant_default",
		Channel:     "slack",
		Interaction: "await_response",
		Sender:      domain.Sender{ChannelUserID: "123"},
		Metadata:    domain.Metadata{AwaitID: "await_123", ResumePayload: []byte(`{"choice":"yes"}`)},
	}}
	app.handleChannelWebhook(rec, req, adapter)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Interaction string `json:"interaction"`
			AwaitID     string `json:"await_id"`
			Channel     string `json:"channel"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "accepted" || payload.Meta.Interaction != "await_response" || payload.Meta.AwaitID != "await_123" || payload.Meta.Channel != "slack" {
		t.Fatalf("unexpected await response payload: %+v", payload)
	}
}

func TestHandleChannelWebhookInboundAcceptedEnvelope(t *testing.T) {
	repo := &appRepoStub{}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
		Inbound: services.InboundService{
			Repo:   repo,
			Router: testRouter{},
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	adapter := &testChannelAdapter{event: domain.CanonicalInboundEvent{
		TenantID:        "tenant_default",
		Channel:         "slack",
		ProviderEventID: "evt_slack_1",
		EventID:         "event_slack_1",
		Sender:          domain.Sender{ChannelUserID: "user_1"},
		Conversation:    domain.Conversation{ChannelConversationID: "C123", ChannelThreadID: "171234.123"},
		Message:         domain.Message{Text: "hello"},
	}}
	app.handleChannelWebhook(rec, req, adapter)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	var payload struct {
		Data services.InboundResult `json:"data"`
		Meta struct {
			Channel         string `json:"channel"`
			ProviderEventID string `json:"provider_event_id"`
			Interaction     string `json:"interaction"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "accepted" || payload.Meta.Channel != "slack" || payload.Meta.ProviderEventID != "evt_slack_1" || payload.Meta.Interaction != "" {
		t.Fatalf("unexpected inbound accepted payload: %+v", payload)
	}
}

func TestGatewayHealthEnvelope(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}}},
			TTL:    time.Minute,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Service string `json:"service"`
			Catalog struct {
				CachedAgentCount int  `json:"cached_agent_count"`
				CacheValid       bool `json:"cache_valid"`
			} `json:"catalog"`
			ACPRuntime struct {
				Implementation string `json:"implementation"`
				Running        bool   `json:"running"`
				Initialized    bool   `json:"initialized"`
				LastError      string `json:"last_error"`
			} `json:"acp_runtime"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "ok" || payload.Meta.Service != "gateway" {
		t.Fatalf("unexpected gateway health payload: %+v", payload)
	}
	if payload.Meta.Catalog.CachedAgentCount != 1 || !payload.Meta.Catalog.CacheValid {
		t.Fatalf("unexpected gateway health catalog meta: %+v", payload.Meta.Catalog)
	}
	if payload.Meta.ACPRuntime.Implementation != "" || payload.Meta.ACPRuntime.Running || payload.Meta.ACPRuntime.Initialized || payload.Meta.ACPRuntime.LastError != "" {
		t.Fatalf("expected empty acp runtime for non-stdio bridge, got %+v", payload.Meta.ACPRuntime)
	}
}

func TestAdminHealthEnvelope(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}}},
			TTL:    time.Minute,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Service string `json:"service"`
			Catalog struct {
				CachedAgentCount int  `json:"cached_agent_count"`
				CacheValid       bool `json:"cache_valid"`
			} `json:"catalog"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "ok" || payload.Meta.Service != "admin" {
		t.Fatalf("unexpected admin health payload: %+v", payload)
	}
	if payload.Meta.Catalog.CachedAgentCount != 1 || !payload.Meta.Catalog.CacheValid {
		t.Fatalf("unexpected admin health catalog meta: %+v", payload.Meta.Catalog)
	}
}

func TestGatewayHealthDegradedWhenCatalogFetchFailed(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{discoverErr: errors.New("acp unavailable")},
			TTL:    time.Minute,
		},
	}
	_, _ = app.Catalog.List(context.Background(), true)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Catalog struct {
				LastFetchError string `json:"last_fetch_error"`
				CacheValid     bool   `json:"cache_valid"`
			} `json:"catalog"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "degraded" || payload.Meta.Catalog.LastFetchError != "acp unavailable" || payload.Meta.Catalog.CacheValid {
		t.Fatalf("unexpected degraded health payload: %+v", payload)
	}
}

func TestGatewayHealthDegradedWhenACPRuntimeErrored(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{
				agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}},
				runtimeStatus: acpadapter.StdioRuntimeStatus{
					Implementation: "stdio",
					Running:        false,
					Initialized:    false,
					StartedAt:      time.Now().UTC(),
					LastError:      "stdio exited",
				},
			},
			TTL: time.Minute,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			ACPRuntime struct {
				LastError string `json:"last_error"`
			} `json:"acp_runtime"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "degraded" || payload.Meta.ACPRuntime.LastError != "stdio exited" {
		t.Fatalf("unexpected acp-runtime health payload: %+v", payload)
	}
}

func TestAdminHealthDegradedWhenCatalogCacheExpired(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}}},
			TTL:    time.Millisecond,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Catalog struct {
				CacheValid bool `json:"cache_valid"`
			} `json:"catalog"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "degraded" || payload.Meta.Catalog.CacheValid {
		t.Fatalf("unexpected expired-cache health payload: %+v", payload)
	}
}

func TestGatewayReadinessEnvelope(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultACPAgentName: "agent_a"},
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true}}},
			TTL:    time.Minute,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Service      string `json:"service"`
			DefaultAgent struct {
				Name       string `json:"name"`
				Ready      bool   `json:"ready"`
				Compatible bool   `json:"compatible"`
			} `json:"default_agent"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "ready" || payload.Meta.Service != "gateway" {
		t.Fatalf("unexpected gateway readiness payload: %+v", payload)
	}
	if payload.Meta.DefaultAgent.Name != "agent_a" || !payload.Meta.DefaultAgent.Ready || !payload.Meta.DefaultAgent.Compatible {
		t.Fatalf("unexpected gateway readiness default agent meta: %+v", payload.Meta.DefaultAgent)
	}
}

func TestGatewayHealthDegradedWhenDefaultAgentIncompatible(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultACPAgentName: "agent_bad"},
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_bad", Healthy: true, SupportsAwaitResume: false, SupportsStreaming: true, SupportsArtifacts: true}}},
			TTL:    time.Minute,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			DefaultAgent struct {
				Name         string `json:"name"`
				Ready        bool   `json:"ready"`
				Compatible   bool   `json:"compatible"`
				ReasonCount  int    `json:"reason_count"`
				WarningCount int    `json:"warning_count"`
			} `json:"default_agent"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "degraded" || payload.Meta.DefaultAgent.Name != "agent_bad" || payload.Meta.DefaultAgent.Ready || payload.Meta.DefaultAgent.Compatible || payload.Meta.DefaultAgent.ReasonCount == 0 {
		t.Fatalf("unexpected incompatible default-agent health payload: %+v", payload)
	}
}

func TestAdminReadinessNotReadyWhenCatalogFetchFailed(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{discoverErr: errors.New("acp unavailable")},
			TTL:    time.Minute,
		},
	}
	_, _ = app.Catalog.List(context.Background(), true)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Service string `json:"service"`
			Catalog struct {
				LastFetchError string `json:"last_fetch_error"`
			} `json:"catalog"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "not_ready" || payload.Meta.Service != "admin" || payload.Meta.Catalog.LastFetchError != "acp unavailable" {
		t.Fatalf("unexpected admin readiness payload: %+v", payload)
	}
}

func TestAdminReadinessNotReadyWhenACPRuntimeStopped(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{
				agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}},
				runtimeStatus: acpadapter.StdioRuntimeStatus{
					Implementation: "stdio",
					Running:        false,
					Initialized:    false,
					StartedAt:      time.Now().UTC(),
				},
			},
			TTL: time.Minute,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			ACPRuntime struct {
				Running     bool `json:"running"`
				Initialized bool `json:"initialized"`
			} `json:"acp_runtime"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "not_ready" || payload.Meta.ACPRuntime.Running || payload.Meta.ACPRuntime.Initialized {
		t.Fatalf("unexpected acp-runtime readiness payload: %+v", payload)
	}
}

func TestGatewayReadinessNotReadyWhenCatalogCacheExpired(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}}},
			TTL:    time.Millisecond,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "not_ready" {
		t.Fatalf("unexpected gateway readiness status: %+v", payload)
	}
}

func TestGatewayReadinessNotReadyWhenDefaultAgentIncompatible(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultACPAgentName: "agent_bad"},
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_bad", Healthy: true, SupportsAwaitResume: false, SupportsStreaming: true, SupportsArtifacts: true}}},
			TTL:    time.Minute,
		},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			DefaultAgent struct {
				Name       string `json:"name"`
				Ready      bool   `json:"ready"`
				Compatible bool   `json:"compatible"`
			} `json:"default_agent"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "not_ready" || payload.Meta.DefaultAgent.Name != "agent_bad" || payload.Meta.DefaultAgent.Ready || payload.Meta.DefaultAgent.Compatible {
		t.Fatalf("unexpected incompatible default-agent readiness payload: %+v", payload)
	}
}

func TestGatewayHealthDegradedWhenWorkerLoopErrored(t *testing.T) {
	app := &App{
		Config:  config.Config{WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
		Runtime: &RuntimeState{},
	}
	app.Runtime.MarkWorkerRun(time.Now().UTC())
	app.Runtime.MarkWorkerError(errors.New("worker failed"))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Runtime struct {
				LastWorkerError string `json:"last_worker_error"`
			} `json:"runtime"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "degraded" || payload.Meta.Runtime.LastWorkerError != "worker failed" {
		t.Fatalf("unexpected worker-error health payload: %+v", payload)
	}
}

func TestAdminReadinessNotReadyWhenWorkerLoopStale(t *testing.T) {
	app := &App{
		Config:  config.Config{WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
		Runtime: &RuntimeState{},
	}
	app.Runtime.MarkWorkerRun(time.Now().UTC().Add(-5 * time.Second))
	app.Runtime.MarkReconcileRun(time.Now().UTC())

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "not_ready" {
		t.Fatalf("unexpected stale-loop readiness payload: %+v", payload)
	}
}

func TestProbeTransitionsAreRecordedInRuntimeMeta(t *testing.T) {
	app := &App{
		Config:  config.Config{WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
		Runtime: &RuntimeState{},
	}
	app.Runtime.MarkWorkerRun(time.Now().UTC())
	app.Runtime.MarkReconcileRun(time.Now().UTC())

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", healthRec.Code)
	}

	app.Runtime.MarkWorkerError(errors.New("worker failed"))
	readyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", readyRec.Code)
	}

	var payload struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Runtime struct {
				LastHealthStatus    string `json:"last_health_status"`
				LastReadinessStatus string `json:"last_readiness_status"`
				RecentTransitions   []struct {
					Probe string `json:"probe"`
					From  string `json:"from"`
					To    string `json:"to"`
				} `json:"recent_transitions"`
			} `json:"runtime"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(readyRec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "not_ready" || payload.Meta.Runtime.LastHealthStatus != "ok" || payload.Meta.Runtime.LastReadinessStatus != "not_ready" {
		t.Fatalf("unexpected runtime status payload: %+v", payload)
	}
	if len(payload.Meta.Runtime.RecentTransitions) < 2 {
		t.Fatalf("expected at least two probe transitions, got %+v", payload.Meta.Runtime.RecentTransitions)
	}
	last := payload.Meta.Runtime.RecentTransitions[len(payload.Meta.Runtime.RecentTransitions)-1]
	if last.Probe != "readiness" || last.To != "not_ready" {
		t.Fatalf("unexpected last transition: %+v", last)
	}
}

func TestHandleRuntimeStatus(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
		Repo: &appRepoStub{auditCounts: map[string]int{
			"reconciler.outbox_requeued":                     2,
			"reconciler.queue_repair_recovered":              6,
			"reconciler.queue_repair_requeued":               7,
			"reconciler.run_refreshed":                       3,
			"reconciler.await_expired":                       4,
			"delivery.retry_requested":                       5,
			"worker.await_blocked_opencode_bridge":           8,
			"admin.run_canceled":                             9,
			"admin.surface_session_switched":                 10,
			"admin.surface_session_closed":                   11,
			"admin.telegram_request_approved":                12,
			"admin.telegram_request_denied":                  13,
			"admin.telegram_request_resolve_not_found":       14,
			"admin.telegram_request_resolve_not_pending":     15,
			"admin.telegram_request_resolve_internal_failed": 16,
		}},
		ACP: testACPBridge{runtimeStatus: acpadapter.StdioRuntimeStatus{
			Implementation: "stdio",
			Command:        "opencode",
			Args:           []string{"acp"},
			Running:        true,
			Initialized:    true,
			StartedAt:      time.Unix(1700000000, 0).UTC(),
			AgentName:      "OpenCode",
			AgentVersion:   "1.0.0",
			CallbackCounts: struct {
				PermissionRequests int `json:"permission_requests"`
				FSReadTextFile     int `json:"fs_read_text_file"`
				FSWriteTextFile    int `json:"fs_write_text_file"`
				TerminalCreate     int `json:"terminal_create"`
				TerminalOutput     int `json:"terminal_output"`
				TerminalWait       int `json:"terminal_wait"`
				TerminalKill       int `json:"terminal_kill"`
				TerminalRelease    int `json:"terminal_release"`
			}{
				PermissionRequests: 2,
				FSReadTextFile:     3,
				FSWriteTextFile:    4,
				TerminalCreate:     5,
				TerminalOutput:     6,
				TerminalWait:       7,
				TerminalKill:       8,
				TerminalRelease:    9,
			},
		}},
		Catalog: &services.AgentCatalog{Bridge: testACPBridge{agents: []domain.AgentManifest{
			{Name: "agent_ok", Healthy: true, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
			{Name: "agent_bridge", Healthy: true, Protocol: "opencode", SupportsAwaitResume: false, SupportsStructuredAwait: false, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
			{Name: "agent_bad", Healthy: false, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
		}}, TTL: time.Minute},
		Runtime: &RuntimeState{},
	}
	app.Config.DefaultACPAgentName = "agent_bridge"
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	app.Runtime.MarkWorkerRun(time.Now().UTC())
	app.Runtime.MarkReconcileRun(time.Now().UTC())
	app.Runtime.RecordProbeStatus("health", "ok", time.Now().UTC())
	app.Runtime.MarkWorkerError(errors.New("worker failed"))
	app.Runtime.RecordProbeStatus("readiness", "not_ready", time.Now().UTC())
	app.Runtime.RecordOutboxRequeue()
	app.Runtime.RecordQueueRepairRecovered()
	app.Runtime.RecordQueueRepairRequeued()
	app.Runtime.RecordRunRefresh()
	app.Runtime.RecordAwaitExpiry()
	app.Runtime.RecordDeliveryRetry()

	req := httptest.NewRequest(http.MethodGet, "/admin/runtime", nil)
	rec := httptest.NewRecorder()
	app.handleRuntimeStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Runtime struct {
				LastWorkerError           string `json:"last_worker_error"`
				LastHealthStatus          string `json:"last_health_status"`
				LastReadinessStatus       string `json:"last_readiness_status"`
				OutboxRequeueCount        int    `json:"outbox_requeue_count"`
				QueueRepairRecoveredCount int    `json:"queue_repair_recovered_count"`
				QueueRepairRequeuedCount  int    `json:"queue_repair_requeued_count"`
				RunRefreshCount           int    `json:"run_refresh_count"`
				AwaitExpiryCount          int    `json:"await_expiry_count"`
				DeliveryRetryCount        int    `json:"delivery_retry_count"`
				RecentTransitions         []struct {
					Probe string `json:"probe"`
					To    string `json:"to"`
				} `json:"recent_transitions"`
			} `json:"runtime"`
			Health     string         `json:"health"`
			Readiness  string         `json:"readiness"`
			Persisted  map[string]int `json:"persisted"`
			ACPRuntime struct {
				Implementation string `json:"implementation"`
				Command        string `json:"command"`
				Running        bool   `json:"running"`
				Initialized    bool   `json:"initialized"`
				AgentName      string `json:"agent_name"`
				CallbackCounts struct {
					PermissionRequests int `json:"permission_requests"`
					FSReadTextFile     int `json:"fs_read_text_file"`
					FSWriteTextFile    int `json:"fs_write_text_file"`
					TerminalCreate     int `json:"terminal_create"`
					TerminalOutput     int `json:"terminal_output"`
					TerminalWait       int `json:"terminal_wait"`
					TerminalKill       int `json:"terminal_kill"`
					TerminalRelease    int `json:"terminal_release"`
				} `json:"callback_counts"`
			} `json:"acp_runtime"`
			ACP struct {
				AgentCount               int  `json:"agent_count"`
				CompatibleCount          int  `json:"compatible_count"`
				IncompatibleCount        int  `json:"incompatible_count"`
				BridgeBlockCount         int  `json:"bridge_block_count"`
				DefaultAgentReady        bool `json:"default_agent_ready"`
				DefaultAgentReasonCount  int  `json:"default_agent_reason_count"`
				DefaultAgentWarningCount int  `json:"default_agent_warning_count"`
				DefaultValidation        struct {
					ValidationMode string `json:"validation_mode"`
					Compatible     bool   `json:"compatible"`
				} `json:"default_validation"`
				Compatibility struct {
					BridgeCompatibleCount int `json:"bridge_compatible_count"`
					DegradedCount         int `json:"degraded_count"`
					WarningCount          int `json:"warning_count"`
				} `json:"compatibility"`
			} `json:"acp"`
		} `json:"data"`
		Meta struct {
			Service string `json:"service"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Meta.Service != "admin" || payload.Data.Health != "degraded" || payload.Data.Readiness != "not_ready" {
		t.Fatalf("unexpected runtime endpoint payload: %+v", payload)
	}
	if payload.Data.Runtime.LastWorkerError != "worker failed" || payload.Data.Runtime.LastHealthStatus != "ok" || payload.Data.Runtime.LastReadinessStatus != "not_ready" {
		t.Fatalf("unexpected runtime status data: %+v", payload.Data.Runtime)
	}
	if payload.Data.Runtime.OutboxRequeueCount != 1 || payload.Data.Runtime.QueueRepairRecoveredCount != 1 || payload.Data.Runtime.QueueRepairRequeuedCount != 1 ||
		payload.Data.Runtime.RunRefreshCount != 1 || payload.Data.Runtime.AwaitExpiryCount != 1 || payload.Data.Runtime.DeliveryRetryCount != 1 {
		t.Fatalf("unexpected runtime lifecycle counters: %+v", payload.Data.Runtime)
	}
	if payload.Data.ACPRuntime.Implementation != "stdio" || payload.Data.ACPRuntime.Command != "opencode" || !payload.Data.ACPRuntime.Running || !payload.Data.ACPRuntime.Initialized || payload.Data.ACPRuntime.AgentName != "OpenCode" {
		t.Fatalf("unexpected acp runtime status: %+v", payload.Data.ACPRuntime)
	}
	if payload.Data.ACPRuntime.CallbackCounts.PermissionRequests != 2 ||
		payload.Data.ACPRuntime.CallbackCounts.FSReadTextFile != 3 ||
		payload.Data.ACPRuntime.CallbackCounts.FSWriteTextFile != 4 ||
		payload.Data.ACPRuntime.CallbackCounts.TerminalCreate != 5 ||
		payload.Data.ACPRuntime.CallbackCounts.TerminalOutput != 6 ||
		payload.Data.ACPRuntime.CallbackCounts.TerminalWait != 7 ||
		payload.Data.ACPRuntime.CallbackCounts.TerminalKill != 8 ||
		payload.Data.ACPRuntime.CallbackCounts.TerminalRelease != 9 {
		t.Fatalf("unexpected acp runtime callback counts: %+v", payload.Data.ACPRuntime.CallbackCounts)
	}
	if len(payload.Data.Runtime.RecentTransitions) == 0 {
		t.Fatalf("expected runtime transitions, got %+v", payload.Data.Runtime)
	}
	if payload.Data.Persisted["persisted_outbox_requeues"] != 2 ||
		payload.Data.Persisted["persisted_queue_repairs_recovered"] != 6 ||
		payload.Data.Persisted["persisted_queue_repairs_requeued"] != 7 ||
		payload.Data.Persisted["persisted_run_refreshes"] != 3 ||
		payload.Data.Persisted["persisted_await_expiries"] != 4 ||
		payload.Data.Persisted["persisted_delivery_retries"] != 5 ||
		payload.Data.Persisted["persisted_bridge_await_blocks"] != 8 ||
		payload.Data.Persisted["persisted_operator_run_cancels"] != 9 ||
		payload.Data.Persisted["persisted_operator_surface_switches"] != 10 ||
		payload.Data.Persisted["persisted_operator_surface_closures"] != 11 ||
		payload.Data.Persisted["persisted_operator_telegram_approvals"] != 12 ||
		payload.Data.Persisted["persisted_operator_telegram_denials"] != 13 ||
		payload.Data.Persisted["persisted_operator_telegram_resolve_not_found"] != 14 ||
		payload.Data.Persisted["persisted_operator_telegram_resolve_not_pending"] != 15 ||
		payload.Data.Persisted["persisted_operator_telegram_resolve_internal"] != 16 {
		t.Fatalf("unexpected persisted counters: %+v", payload.Data.Persisted)
	}
	if payload.Data.ACP.AgentCount != 3 || payload.Data.ACP.CompatibleCount != 2 || payload.Data.ACP.IncompatibleCount != 1 || payload.Data.ACP.BridgeBlockCount != 8 {
		t.Fatalf("unexpected runtime acp summary: %+v", payload.Data.ACP)
	}
	if !payload.Data.ACP.DefaultAgentReady {
		t.Fatalf("expected default_agent_ready=true, got %+v", payload.Data.ACP)
	}
	if payload.Data.ACP.DefaultAgentReasonCount != 0 || payload.Data.ACP.DefaultAgentWarningCount != 2 {
		t.Fatalf("unexpected runtime default-agent counts: %+v", payload.Data.ACP)
	}
	if payload.Data.ACP.DefaultValidation.ValidationMode != "opencode_bridge" || !payload.Data.ACP.DefaultValidation.Compatible {
		t.Fatalf("unexpected runtime default acp validation: %+v", payload.Data.ACP.DefaultValidation)
	}
	if payload.Data.ACP.Compatibility.BridgeCompatibleCount != 1 || payload.Data.ACP.Compatibility.DegradedCount != 1 || payload.Data.ACP.Compatibility.WarningCount != 2 {
		t.Fatalf("unexpected runtime acp compatibility summary: %+v", payload.Data.ACP.Compatibility)
	}
}

func TestGatewayMetricsEndpoint(t *testing.T) {
	repo := &appRepoStub{
		deliveryCounts: map[string]int{"queued": 3, "sending": 1},
		runCounts:      map[string]int{"starting": 1, "running": 2, "awaiting": 4},
		awaitCounts:    map[string]int{"pending": 5},
		auditCounts: map[string]int{
			"reconciler.outbox_requeued":                     2,
			"reconciler.queue_repair_recovered":              6,
			"reconciler.queue_repair_requeued":               7,
			"reconciler.run_refreshed":                       3,
			"reconciler.await_expired":                       4,
			"delivery.retry_requested":                       5,
			"worker.await_blocked_opencode_bridge":           8,
			"admin.run_canceled":                             9,
			"admin.surface_session_switched":                 10,
			"admin.surface_session_closed":                   11,
			"admin.telegram_request_approved":                12,
			"admin.telegram_request_denied":                  13,
			"admin.telegram_request_resolve_not_found":       14,
			"admin.telegram_request_resolve_not_pending":     15,
			"admin.telegram_request_resolve_internal_failed": 16,
		},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", DefaultACPAgentName: "agent_bridge", WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
		Repo:   repo,
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{
				agents: []domain.AgentManifest{
					{Name: "agent_ok", Healthy: true, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
					{Name: "agent_bridge", Healthy: true, Protocol: "opencode", SupportsAwaitResume: false, SupportsStructuredAwait: false, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
					{Name: "agent_bad", Healthy: false, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
				},
				runtimeStatus: acpadapter.StdioRuntimeStatus{
					Implementation: "stdio",
					Running:        true,
					Initialized:    true,
					StartedAt:      time.Unix(1700000000, 0).UTC(),
					CallbackCounts: struct {
						PermissionRequests int `json:"permission_requests"`
						FSReadTextFile     int `json:"fs_read_text_file"`
						FSWriteTextFile    int `json:"fs_write_text_file"`
						TerminalCreate     int `json:"terminal_create"`
						TerminalOutput     int `json:"terminal_output"`
						TerminalWait       int `json:"terminal_wait"`
						TerminalKill       int `json:"terminal_kill"`
						TerminalRelease    int `json:"terminal_release"`
					}{
						PermissionRequests: 2,
						FSReadTextFile:     3,
						FSWriteTextFile:    4,
						TerminalCreate:     5,
						TerminalOutput:     6,
						TerminalWait:       7,
						TerminalKill:       8,
						TerminalRelease:    9,
					},
				},
			},
			TTL: time.Minute,
		},
		Runtime: &RuntimeState{},
	}
	if _, err := app.Catalog.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	app.Runtime.MarkWorkerRun(time.Now().UTC())
	app.Runtime.MarkReconcileRun(time.Now().UTC())
	app.Runtime.RecordOutboxRequeue()
	app.Runtime.RecordAwaitExpiry()
	app.Runtime.RecordDeliveryRetry()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	app.GatewayHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain;") {
		t.Fatalf("unexpected content type %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`nexus_health_status{service="gateway"} 1`,
		`nexus_readiness_status{service="gateway"} 1`,
		`nexus_acp_catalog_cache_valid{service="gateway"} 1`,
		`nexus_acp_default_agent_compatible{service="gateway"} 1`,
		`nexus_acp_default_agent_ready{service="gateway"} 1`,
		`nexus_acp_default_agent_reason_count{service="gateway"} 0`,
		`nexus_acp_default_agent_warning_count{service="gateway"} 2`,
		`nexus_acp_runtime_running{service="gateway"} 1`,
		`nexus_acp_runtime_initialized{service="gateway"} 1`,
		`nexus_acp_runtime_error{service="gateway"} 0`,
		`nexus_acp_runtime_permission_requests{service="gateway"} 2`,
		`nexus_acp_runtime_fs_read_text_file{service="gateway"} 3`,
		`nexus_acp_runtime_fs_write_text_file{service="gateway"} 4`,
		`nexus_acp_runtime_terminal_create{service="gateway"} 5`,
		`nexus_acp_runtime_terminal_output{service="gateway"} 6`,
		`nexus_acp_runtime_terminal_wait{service="gateway"} 7`,
		`nexus_acp_runtime_terminal_kill{service="gateway"} 8`,
		`nexus_acp_runtime_terminal_release{service="gateway"} 9`,
		`nexus_acp_runtime_started_unix{service="gateway"} 1700000000`,
		`nexus_acp_compatible_agents{service="gateway"} 2`,
		`nexus_acp_incompatible_agents{service="gateway"} 1`,
		`nexus_acp_bridge_compatible_agents{service="gateway"} 1`,
		`nexus_acp_degraded_agents{service="gateway"} 1`,
		`nexus_acp_warning_count{service="gateway"} 2`,
		`nexus_worker_error{service="gateway"} 0`,
		`nexus_outbox_requeues_total{service="gateway"} 1`,
		`nexus_await_expiries_total{service="gateway"} 1`,
		`nexus_delivery_retries_total{service="gateway"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics body to contain %q, got:\n%s", want, body)
		}
	}
	for _, want := range []string{
		`nexus_queued_deliveries{service="gateway",tenant="tenant_default"} 3`,
		`nexus_sending_deliveries{service="gateway",tenant="tenant_default"} 1`,
		`nexus_pending_awaits{service="gateway",tenant="tenant_default"} 5`,
		`nexus_active_runs{service="gateway",tenant="tenant_default"} 7`,
		`nexus_acp_bridge_block_count{service="gateway",tenant="tenant_default"} 8`,
		`nexus_persisted_outbox_requeues_total{service="gateway",tenant="tenant_default"} 2`,
		`nexus_persisted_queue_repairs_recovered_total{service="gateway",tenant="tenant_default"} 6`,
		`nexus_persisted_queue_repairs_requeued_total{service="gateway",tenant="tenant_default"} 7`,
		`nexus_persisted_run_refreshes_total{service="gateway",tenant="tenant_default"} 3`,
		`nexus_persisted_await_expiries_total{service="gateway",tenant="tenant_default"} 4`,
		`nexus_persisted_delivery_retries_total{service="gateway",tenant="tenant_default"} 5`,
		`nexus_persisted_bridge_await_blocks_total{service="gateway",tenant="tenant_default"} 8`,
		`nexus_persisted_operator_run_cancels_total{service="gateway",tenant="tenant_default"} 9`,
		`nexus_persisted_operator_surface_switches_total{service="gateway",tenant="tenant_default"} 10`,
		`nexus_persisted_operator_surface_closures_total{service="gateway",tenant="tenant_default"} 11`,
		`nexus_persisted_operator_telegram_approvals_total{service="gateway",tenant="tenant_default"} 12`,
		`nexus_persisted_operator_telegram_denials_total{service="gateway",tenant="tenant_default"} 13`,
		`nexus_persisted_operator_telegram_resolve_not_found_total{service="gateway",tenant="tenant_default"} 14`,
		`nexus_persisted_operator_telegram_resolve_not_pending_total{service="gateway",tenant="tenant_default"} 15`,
		`nexus_persisted_operator_telegram_resolve_internal_total{service="gateway",tenant="tenant_default"} 16`,
		`nexus_metrics_repo_error{service="gateway"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics body to contain %q, got:\n%s", want, body)
		}
	}
}

func TestAdminMetricsEndpointReflectsErrors(t *testing.T) {
	app := &App{
		Config:  config.Config{WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
		Repo:    &appRepoStub{},
		Runtime: &RuntimeState{},
	}
	app.Runtime.MarkWorkerError(errors.New("worker failed"))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`nexus_health_status{service="admin"} 0`,
		`nexus_readiness_status{service="admin"} 0`,
		`nexus_worker_error{service="admin"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics body to contain %q, got:\n%s", want, body)
		}
	}
}

func TestTelegramUserAllowed(t *testing.T) {
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default", TelegramAllowedUserIDs: []string{"123", "456"}}, Repo: &appRepoStub{telegramAllowed: map[string]bool{"123": true}}}
	if !app.telegramUserAllowed(context.Background(), "123") {
		t.Fatal("expected listed telegram user to be allowed")
	}
	if app.telegramUserAllowed(context.Background(), "999") {
		t.Fatal("expected unlisted telegram user to be denied")
	}
}

func TestHandleListTelegramUsers(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			telegramUsers: []domain.TelegramUserAccess{
				{TenantID: "tenant_default", TelegramUserID: "123", Allowed: true},
				{TenantID: "tenant_default", TelegramUserID: "456", Allowed: false},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/users?limit=1", nil)
	rec := httptest.NewRecorder()
	app.handleListTelegramUsers(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.TelegramUserAccess `json:"items"`
		NextCursor string                      `json:"next_cursor"`
		TotalCount int                         `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].TelegramUserID != "123" {
		t.Fatalf("unexpected telegram users payload: %+v", payload.Items)
	}
	if payload.TotalCount != 2 || payload.NextCursor == "" {
		t.Fatalf("unexpected telegram users paging payload: %+v", payload)
	}
}

func TestHandleTelegramUserDetail(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			telegramUsers: []domain.TelegramUserAccess{{TenantID: "tenant_default", TelegramUserID: "123", Allowed: false, Status: "pending"}},
			auditPage: domain.PagedResult[domain.AuditEvent]{
				Items: []domain.AuditEvent{
					{AggregateType: "telegram_user", AggregateID: "123", EventType: "telegram.access_requested"},
				},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/users/detail?telegram_user_id=123", nil)
	rec := httptest.NewRecorder()
	app.handleTelegramUserDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			User  domain.TelegramUserAccess `json:"user"`
			Audit []domain.AuditEvent       `json:"audit"`
		} `json:"data"`
		Meta struct {
			TelegramUserID string `json:"telegram_user_id"`
			AuditCount     int    `json:"audit_count"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.User.TelegramUserID != "123" || len(payload.Data.Audit) != 1 || payload.Data.Audit[0].AggregateID != "123" {
		t.Fatalf("unexpected telegram user detail payload: %+v", payload)
	}
	if payload.Meta.TelegramUserID != "123" || payload.Meta.AuditCount != 1 {
		t.Fatalf("unexpected telegram user detail meta: %+v", payload.Meta)
	}
	repo := app.Repo.(*appRepoStub)
	if repo.lastAuditQuery.AggregateID != "123" {
		t.Fatalf("expected telegram user detail to query aggregate_id=123, got %+v", repo.lastAuditQuery)
	}
}

func TestHandleTelegramUserSummary(t *testing.T) {
	repo := &appRepoStub{
		telegramUsers: []domain.TelegramUserAccess{{TenantID: "tenant_default", TelegramUserID: "123", Allowed: false, Status: "pending"}},
		auditPage: domain.PagedResult[domain.AuditEvent]{
			Items: []domain.AuditEvent{
				{AggregateType: "telegram_user", AggregateID: "123", EventType: "telegram.access_requested", CreatedAt: time.Now().UTC()},
				{AggregateType: "telegram_user", AggregateID: "123", EventType: "telegram.allowlist_denied", CreatedAt: time.Now().UTC().Add(-time.Minute)},
				{AggregateType: "telegram_user", AggregateID: "123", EventType: "admin.telegram_request_resolve_failed", CreatedAt: time.Now().UTC().Add(-2 * time.Minute)},
				{AggregateType: "telegram_user", AggregateID: "123", EventType: "irrelevant.event", CreatedAt: time.Now().UTC().Add(-3 * time.Minute)},
			},
		},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/users/summary?telegram_user_id=123", nil)
	rec := httptest.NewRecorder()
	app.handleTelegramUserSummary(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			User         domain.TelegramUserAccess `json:"user"`
			CurrentState string                    `json:"current_state"`
			LastRequest  *domain.AuditEvent        `json:"last_request"`
			LastDecision *domain.AuditEvent        `json:"last_decision"`
			RecentEvents []domain.AuditEvent       `json:"recent_events"`
		} `json:"data"`
		Meta struct {
			TelegramUserID   string `json:"telegram_user_id"`
			RecentEventCount int    `json:"recent_event_count"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.User.TelegramUserID != "123" || payload.Data.CurrentState != "pending" {
		t.Fatalf("unexpected summary user payload: %+v", payload)
	}
	if payload.Data.LastRequest == nil || payload.Data.LastRequest.EventType != "telegram.access_requested" {
		t.Fatalf("expected last_request to be telegram.access_requested, got %+v", payload.Data.LastRequest)
	}
	if payload.Data.LastDecision == nil || payload.Data.LastDecision.EventType != "telegram.allowlist_denied" {
		t.Fatalf("expected last_decision to be telegram.allowlist_denied, got %+v", payload.Data.LastDecision)
	}
	if len(payload.Data.RecentEvents) != 3 {
		t.Fatalf("expected only trust events in recent_events, got %+v", payload.Data.RecentEvents)
	}
	if payload.Meta.TelegramUserID != "123" || payload.Meta.RecentEventCount != 3 {
		t.Fatalf("unexpected telegram user summary meta: %+v", payload.Meta)
	}
	if repo.lastAuditQuery.AggregateID != "123" {
		t.Fatalf("expected telegram user summary to query aggregate_id=123, got %+v", repo.lastAuditQuery)
	}
}

func TestHandleSessionDetailEnvelope(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			sessionDetail: domain.SessionDetail{Session: domain.Session{ID: "session_1"}},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/sessions/detail?session_id=session_1&limit=5", nil)
	rec := httptest.NewRecorder()
	app.handleSessionDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.SessionDetail `json:"data"`
		Meta struct {
			SessionID string `json:"session_id"`
			Limit     int    `json:"limit"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Session.ID != "session_1" || payload.Meta.SessionID != "session_1" || payload.Meta.Limit != 5 {
		t.Fatalf("unexpected session detail envelope: %+v", payload)
	}
}

func TestHandleRunDetailEnvelope(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			runDetail: domain.RunDetail{Run: domain.Run{ID: "run_1"}},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/runs/detail?run_id=run_1&limit=7", nil)
	rec := httptest.NewRecorder()
	app.handleRunDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.RunDetail `json:"data"`
		Meta struct {
			RunID string `json:"run_id"`
			Limit int    `json:"limit"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Run.ID != "run_1" || payload.Meta.RunID != "run_1" || payload.Meta.Limit != 7 {
		t.Fatalf("unexpected run detail envelope: %+v", payload)
	}
}

func TestHandleAwaitDetailEnvelope(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			awaitDetail: domain.AwaitDetail{Await: domain.Await{ID: "await_1"}},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/awaits/detail?await_id=await_1&limit=9", nil)
	rec := httptest.NewRecorder()
	app.handleAwaitDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.AwaitDetail `json:"data"`
		Meta struct {
			AwaitID string `json:"await_id"`
			Limit   int    `json:"limit"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Await.ID != "await_1" || payload.Meta.AwaitID != "await_1" || payload.Meta.Limit != 9 {
		t.Fatalf("unexpected await detail envelope: %+v", payload)
	}
}

func TestHandleListAuditEvents(t *testing.T) {
	repo := &appRepoStub{
		auditPage: domain.PagedResult[domain.AuditEvent]{
			Items:      []domain.AuditEvent{{EventType: "admin.surface_session_switched", AggregateID: "telegram:123"}},
			NextCursor: "audit-next",
		},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/audit?aggregate_type=channel_surface_state&aggregate_id=telegram:123&event_type=admin.surface_session_switched", nil)
	rec := httptest.NewRecorder()
	app.handleListAuditEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.AuditEvent `json:"items"`
		NextCursor string              `json:"next_cursor"`
		TotalCount int                 `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].EventType != "admin.surface_session_switched" {
		t.Fatalf("unexpected audit payload: %+v", payload.Items)
	}
	if payload.TotalCount != 1 || payload.NextCursor != "audit-next" {
		t.Fatalf("unexpected audit paging payload: %+v", payload)
	}
	if repo.lastAuditQuery.AggregateType != "channel_surface_state" || repo.lastAuditQuery.AggregateID != "telegram:123" || repo.lastAuditQuery.EventType != "admin.surface_session_switched" {
		t.Fatalf("unexpected audit query: %+v", repo.lastAuditQuery)
	}
}

func TestHandleListACPBridgeBlocks(t *testing.T) {
	repo := &appRepoStub{
		auditPage: domain.PagedResult[domain.AuditEvent]{
			Items: []domain.AuditEvent{{
				EventType: "worker.await_blocked_opencode_bridge",
				RunID:     "run_1",
				SessionID: "session_1",
			}},
		},
		auditCounts: map[string]int{
			"worker.await_blocked_opencode_bridge": 1,
		},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/acp/bridge-blocks?run_id=run_1&session_id=session_1&limit=10", nil)
	rec := httptest.NewRecorder()
	app.handleListACPBridgeBlocks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.AuditEvent `json:"items"`
		NextCursor string              `json:"next_cursor"`
		TotalCount int                 `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].EventType != "worker.await_blocked_opencode_bridge" || payload.TotalCount != 1 {
		t.Fatalf("unexpected bridge blocks payload: %+v", payload)
	}
	if repo.lastAuditQuery.EventType != "worker.await_blocked_opencode_bridge" || repo.lastAuditQuery.RunID != "run_1" || repo.lastAuditQuery.SessionID != "session_1" {
		t.Fatalf("unexpected bridge blocks query: %+v", repo.lastAuditQuery)
	}
}

func TestHandleACPAdminSummary(t *testing.T) {
	repo := &appRepoStub{
		auditCounts: map[string]int{
			"worker.await_blocked_opencode_bridge": 2,
		},
		auditPagesByEventType: map[string]domain.PagedResult[domain.AuditEvent]{
			"worker.await_blocked_opencode_bridge": {
				Items: []domain.AuditEvent{{EventType: "worker.await_blocked_opencode_bridge", RunID: "run_1"}},
			},
		},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", DefaultACPAgentName: "agent_bridge"},
		Repo:   repo,
		Catalog: &services.AgentCatalog{Bridge: testACPBridge{agents: []domain.AgentManifest{
			{Name: "agent_ok", Healthy: true, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
			{Name: "agent_bridge", Healthy: true, Protocol: "opencode", SupportsAwaitResume: false, SupportsStructuredAwait: false, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
			{Name: "agent_bad", Healthy: false, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
		}}, TTL: time.Minute},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/acp/summary?refresh=true", nil)
	rec := httptest.NewRecorder()
	app.handleACPAdminSummary(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			AgentCount               int                         `json:"agent_count"`
			CompatibleCount          int                         `json:"compatible_count"`
			IncompatibleCount        int                         `json:"incompatible_count"`
			DefaultAgentReady        bool                        `json:"default_agent_ready"`
			DefaultAgentReasonCount  int                         `json:"default_agent_reason_count"`
			DefaultAgentWarningCount int                         `json:"default_agent_warning_count"`
			DefaultAgent             string                      `json:"default_agent"`
			Agents                   []domain.AgentManifest      `json:"agents"`
			Compatible               []domain.AgentCompatibility `json:"compatible"`
			DefaultValidation        domain.AgentCompatibility   `json:"default_validation"`
			Catalog                  struct {
				CachedAgentCount int  `json:"cached_agent_count"`
				CacheValid       bool `json:"cache_valid"`
			} `json:"catalog"`
			Compatibility struct {
				BridgeCompatibleCount int `json:"bridge_compatible_count"`
				DegradedCount         int `json:"degraded_count"`
				WarningCount          int `json:"warning_count"`
			} `json:"compatibility"`
			BridgeBlocks struct {
				TotalCount   int                 `json:"total_count"`
				RecentEvents []domain.AuditEvent `json:"recent_events"`
			} `json:"bridge_blocks"`
		} `json:"data"`
		Meta struct {
			Refresh bool `json:"refresh"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Meta.Refresh || payload.Data.AgentCount != 3 || payload.Data.CompatibleCount != 2 || payload.Data.IncompatibleCount != 1 {
		t.Fatalf("unexpected acp summary counts: %+v", payload)
	}
	if !payload.Data.DefaultAgentReady {
		t.Fatalf("expected default_agent_ready=true, got %+v", payload.Data)
	}
	if payload.Data.DefaultAgentReasonCount != 0 || payload.Data.DefaultAgentWarningCount != 2 {
		t.Fatalf("unexpected default-agent summary counts: %+v", payload.Data)
	}
	if payload.Data.DefaultAgent != "agent_bridge" || payload.Data.DefaultValidation.ValidationMode != "opencode_bridge" || !payload.Data.DefaultValidation.Compatible {
		t.Fatalf("unexpected default agent validation: %+v", payload.Data.DefaultValidation)
	}
	if len(payload.Data.Agents) != 3 || len(payload.Data.Compatible) != 3 {
		t.Fatalf("unexpected acp summary agent lists: %+v", payload.Data)
	}
	if payload.Data.Catalog.CachedAgentCount != 3 || !payload.Data.Catalog.CacheValid {
		t.Fatalf("unexpected acp summary catalog: %+v", payload.Data.Catalog)
	}
	if payload.Data.Compatibility.BridgeCompatibleCount != 1 || payload.Data.Compatibility.DegradedCount != 1 || payload.Data.Compatibility.WarningCount != 2 {
		t.Fatalf("unexpected acp summary compatibility: %+v", payload.Data.Compatibility)
	}
	if payload.Data.BridgeBlocks.TotalCount != 2 || len(payload.Data.BridgeBlocks.RecentEvents) != 1 || payload.Data.BridgeBlocks.RecentEvents[0].RunID != "run_1" {
		t.Fatalf("unexpected acp summary bridge blocks: %+v", payload.Data.BridgeBlocks)
	}
}

func TestHandleListSessionsIncludesTotalCount(t *testing.T) {
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: &appRepoStub{}}
	req := httptest.NewRequest(http.MethodGet, "/admin/sessions?state=open", nil)
	rec := httptest.NewRecorder()
	app.handleListSessions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.Session `json:"items"`
		NextCursor string           `json:"next_cursor"`
		TotalCount int              `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.TotalCount != 1 {
		t.Fatalf("expected total_count=1, got %+v", payload)
	}
}

func TestHandleListRunsIncludesTotalCount(t *testing.T) {
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: &appRepoStub{}}
	req := httptest.NewRequest(http.MethodGet, "/admin/runs?status=completed", nil)
	rec := httptest.NewRecorder()
	app.handleListRuns(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.Run `json:"items"`
		NextCursor string       `json:"next_cursor"`
		TotalCount int          `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.TotalCount != 1 {
		t.Fatalf("expected total_count=1, got %+v", payload)
	}
}

func TestHandleListDeliveriesIncludesTotalCount(t *testing.T) {
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: &appRepoStub{}}
	req := httptest.NewRequest(http.MethodGet, "/admin/deliveries?status=sent", nil)
	rec := httptest.NewRecorder()
	app.handleListDeliveries(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.OutboundDelivery `json:"items"`
		NextCursor string                    `json:"next_cursor"`
		TotalCount int                       `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.TotalCount != 1 {
		t.Fatalf("expected total_count=1, got %+v", payload)
	}
}

func TestHandleListAwaitsIncludesTotalCount(t *testing.T) {
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: &appRepoStub{}}
	req := httptest.NewRequest(http.MethodGet, "/admin/awaits?status=pending", nil)
	rec := httptest.NewRecorder()
	app.handleListAwaits(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.Await `json:"items"`
		NextCursor string         `json:"next_cursor"`
		TotalCount int            `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.TotalCount != 1 {
		t.Fatalf("expected total_count=1, got %+v", payload)
	}
}

func TestHandleListTelegramDenials(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			auditPage: domain.PagedResult[domain.AuditEvent]{
				Items: []domain.AuditEvent{{EventType: "telegram.allowlist_denied", AggregateID: "999"}},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/denials", nil)
	rec := httptest.NewRecorder()
	app.handleListTelegramDenials(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Items      []domain.AuditEvent `json:"items"`
			NextCursor string              `json:"next_cursor"`
		} `json:"data"`
		Meta struct {
			EventType  string `json:"event_type"`
			Limit      int    `json:"limit"`
			TotalCount int    `json:"total_count"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Items) != 1 || payload.Data.Items[0].EventType != "telegram.allowlist_denied" {
		t.Fatalf("unexpected telegram denial payload: %+v", payload)
	}
	if payload.Meta.EventType != "telegram.allowlist_denied" || payload.Meta.TotalCount != 1 || payload.Meta.Limit != 50 {
		t.Fatalf("unexpected telegram denial meta: %+v", payload.Meta)
	}
}

func TestHandleListTelegramFailures(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			auditPagesByEventType: map[string]domain.PagedResult[domain.AuditEvent]{
				"admin.telegram_request_resolve_not_pending": {
					Items: []domain.AuditEvent{{EventType: "admin.telegram_request_resolve_not_pending", AggregateID: "123"}},
				},
			},
			auditCounts: map[string]int{
				"admin.telegram_request_resolve_not_pending": 1,
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/failures?failure_type=not_pending", nil)
	rec := httptest.NewRecorder()
	app.handleListTelegramFailures(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Items      []domain.AuditEvent `json:"items"`
			NextCursor string              `json:"next_cursor"`
		} `json:"data"`
		Meta struct {
			EventType   string `json:"event_type"`
			FailureType string `json:"failure_type"`
			Limit       int    `json:"limit"`
			TotalCount  int    `json:"total_count"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Items) != 1 || payload.Data.Items[0].EventType != "admin.telegram_request_resolve_not_pending" {
		t.Fatalf("unexpected telegram failure payload: %+v", payload)
	}
	if payload.Meta.EventType != "admin.telegram_request_resolve_not_pending" || payload.Meta.FailureType != "not_pending" || payload.Meta.TotalCount != 1 || payload.Meta.Limit != 50 {
		t.Fatalf("unexpected telegram failure meta: %+v", payload.Meta)
	}
}

func TestHandleListTelegramFailuresRejectsInvalidType(t *testing.T) {
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: &appRepoStub{}}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/failures?failure_type=bogus", nil)
	rec := httptest.NewRecorder()
	app.handleListTelegramFailures(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleListAwaits(t *testing.T) {
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: &appRepoStub{}}
	req := httptest.NewRequest(http.MethodGet, "/admin/awaits?status=pending", nil)
	rec := httptest.NewRecorder()
	app.handleListAwaits(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.Await `json:"items"`
		NextCursor string         `json:"next_cursor"`
		TotalCount int            `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.TotalCount != 1 {
		t.Fatalf("expected total_count=1, got %+v", payload)
	}
}

func TestHandleTelegramTrustSummary(t *testing.T) {
	t1 := time.Now().UTC().Add(-time.Hour)
	t2 := time.Now().UTC().Add(-2 * time.Hour)
	t3 := time.Now().UTC().Add(-3 * time.Hour)
	repo := &appRepoStub{
		telegramUsers: []domain.TelegramUserAccess{
			{TelegramUserID: "pending_1", Status: "pending"},
			{TelegramUserID: "pending_2", Status: "pending"},
			{TelegramUserID: "approved_1", Status: "approved", Allowed: true, DecidedAt: &t1},
			{TelegramUserID: "approved_2", Status: "approved", Allowed: true, DecidedAt: &t3},
			{TelegramUserID: "denied_1", Status: "denied", DecidedAt: &t2},
		},
		auditPagesByEventType: map[string]domain.PagedResult[domain.AuditEvent]{
			"admin.telegram_request_resolve_failed": {
				Items: []domain.AuditEvent{
					{EventType: "admin.telegram_request_resolve_not_found", AggregateID: "denied_1"},
					{EventType: "admin.telegram_request_resolve_not_pending", AggregateID: "pending_2"},
				},
				NextCursor: "more-failures",
			},
			"admin.telegram_request_resolve_not_found": {
				Items: []domain.AuditEvent{
					{EventType: "admin.telegram_request_resolve_not_found", AggregateID: "denied_1"},
				},
				NextCursor: "more-not-found",
			},
			"admin.telegram_request_resolve_not_pending": {
				Items: []domain.AuditEvent{
					{EventType: "admin.telegram_request_resolve_not_pending", AggregateID: "pending_2"},
				},
				NextCursor: "more-not-pending",
			},
			"admin.telegram_request_resolve_internal_failed": {
				Items: []domain.AuditEvent{},
			},
			"admin.telegram_request_resolved": {
				Items: []domain.AuditEvent{
					{EventType: "admin.telegram_request_resolved", AggregateID: "approved_1"},
					{EventType: "admin.telegram_request_resolved", AggregateID: "approved_2"},
				},
				NextCursor: "more-resolutions",
			},
		},
		auditCounts: map[string]int{
			"admin.telegram_request_resolve_failed":          2,
			"admin.telegram_request_resolve_not_found":       1,
			"admin.telegram_request_resolve_not_pending":     1,
			"admin.telegram_request_resolve_internal_failed": 0,
			"admin.telegram_request_resolved":                2,
		},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/trust/summary?limit=10&pending_limit=1&decision_limit=2&failure_limit=1&resolution_limit=1", nil)
	rec := httptest.NewRecorder()
	app.handleTelegramTrustSummary(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Counts                 map[string]int                 `json:"counts"`
			HasMore                map[string]bool                `json:"has_more"`
			NextCursors            map[string]string              `json:"next_cursors"`
			PendingRequests        []domain.TelegramUserAccess    `json:"pending_requests"`
			RecentApproved         []domain.TelegramUserAccess    `json:"recent_approved"`
			RecentDenied           []domain.TelegramUserAccess    `json:"recent_denied"`
			RecentDecisions        []domain.TelegramUserAccess    `json:"recent_decisions"`
			RecentFailures         []domain.AuditEvent            `json:"recent_failures"`
			RecentFailureBreakdown map[string][]domain.AuditEvent `json:"recent_failure_breakdown"`
			RecentResolutions      []domain.AuditEvent            `json:"recent_resolutions"`
		} `json:"data"`
		Meta struct {
			Limit           int `json:"limit"`
			PendingLimit    int `json:"pending_limit"`
			DecisionLimit   int `json:"decision_limit"`
			FailureLimit    int `json:"failure_limit"`
			ResolutionLimit int `json:"resolution_limit"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.PendingRequests) != 1 || payload.Data.PendingRequests[0].TelegramUserID != "pending_1" {
		t.Fatalf("unexpected pending requests payload: %+v", payload.Data.PendingRequests)
	}
	if len(payload.Data.RecentApproved) != 2 || payload.Data.RecentApproved[0].TelegramUserID != "approved_1" || payload.Data.RecentApproved[1].TelegramUserID != "approved_2" {
		t.Fatalf("unexpected recent approved payload: %+v", payload.Data.RecentApproved)
	}
	if len(payload.Data.RecentDenied) != 1 || payload.Data.RecentDenied[0].TelegramUserID != "denied_1" {
		t.Fatalf("unexpected recent denied payload: %+v", payload.Data.RecentDenied)
	}
	if len(payload.Data.RecentDecisions) != 2 || payload.Data.RecentDecisions[0].TelegramUserID != "approved_1" || payload.Data.RecentDecisions[1].TelegramUserID != "denied_1" {
		t.Fatalf("unexpected recent decisions payload: %+v", payload.Data.RecentDecisions)
	}
	if len(payload.Data.RecentFailures) != 1 || payload.Data.RecentFailures[0].EventType != "admin.telegram_request_resolve_not_found" {
		t.Fatalf("unexpected recent failures payload: %+v", payload.Data.RecentFailures)
	}
	if len(payload.Data.RecentFailureBreakdown["not_found"]) != 1 || payload.Data.RecentFailureBreakdown["not_found"][0].EventType != "admin.telegram_request_resolve_not_found" {
		t.Fatalf("unexpected not_found failure breakdown: %+v", payload.Data.RecentFailureBreakdown)
	}
	if len(payload.Data.RecentFailureBreakdown["not_pending"]) != 1 || payload.Data.RecentFailureBreakdown["not_pending"][0].EventType != "admin.telegram_request_resolve_not_pending" || len(payload.Data.RecentFailureBreakdown["internal"]) != 0 {
		t.Fatalf("unexpected failure breakdown payload: %+v", payload.Data.RecentFailureBreakdown)
	}
	if len(payload.Data.RecentResolutions) != 1 || payload.Data.RecentResolutions[0].EventType != "admin.telegram_request_resolved" {
		t.Fatalf("unexpected recent resolutions payload: %+v", payload.Data.RecentResolutions)
	}
	if payload.Data.Counts["pending"] != 2 || payload.Data.Counts["approved"] != 2 || payload.Data.Counts["denied"] != 1 || payload.Data.Counts["failures"] != 2 || payload.Data.Counts["failure_not_found"] != 1 || payload.Data.Counts["failure_not_pending"] != 1 || payload.Data.Counts["failure_internal"] != 0 || payload.Data.Counts["resolutions"] != 2 || payload.Data.Counts["decisions"] != 3 {
		t.Fatalf("unexpected trust summary counts: %+v", payload.Data.Counts)
	}
	if !payload.Data.HasMore["pending_requests"] || !payload.Data.HasMore["recent_failures"] || !payload.Data.HasMore["recent_resolutions"] || !payload.Data.HasMore["recent_decisions"] || !payload.Data.HasMore["failure_not_found"] || !payload.Data.HasMore["failure_not_pending"] {
		t.Fatalf("expected has_more metadata for truncated sections, got %+v", payload.Data.HasMore)
	}
	if payload.Data.HasMore["recent_denied"] || payload.Data.HasMore["recent_approved"] || payload.Data.HasMore["failure_internal"] {
		t.Fatalf("did not expect has_more for fully returned sections, got %+v", payload.Data.HasMore)
	}
	if len(repo.auditQueries) != 5 {
		t.Fatalf("expected five audit queries, got %+v", repo.auditQueries)
	}
	if len(repo.telegramAccessQueries) != 3 {
		t.Fatalf("expected three telegram access queries, got %+v", repo.telegramAccessQueries)
	}
	if repo.telegramAccessQueries[0].Status != "pending" || repo.telegramAccessQueries[0].Limit != 1 || repo.telegramAccessQueries[0].After != "" {
		t.Fatalf("unexpected pending access query: %+v", repo.telegramAccessQueries[0])
	}
	if repo.telegramAccessQueries[1].Status != "approved" || repo.telegramAccessQueries[1].Limit != 3 {
		t.Fatalf("unexpected approved access query: %+v", repo.telegramAccessQueries[1])
	}
	if repo.telegramAccessQueries[2].Status != "denied" || repo.telegramAccessQueries[2].Limit != 3 {
		t.Fatalf("unexpected denied access query: %+v", repo.telegramAccessQueries[2])
	}
	if repo.auditQueries[0].AggregateType != "telegram_user" || repo.auditQueries[0].EventType != "admin.telegram_request_resolve_failed" || repo.auditQueries[0].Limit != 1 {
		t.Fatalf("unexpected trust summary failure audit query: %+v", repo.auditQueries[0])
	}
	if repo.auditQueries[1].AggregateType != "telegram_user" || repo.auditQueries[1].EventType != "admin.telegram_request_resolved" || repo.auditQueries[1].Limit != 1 {
		t.Fatalf("unexpected trust summary resolution audit query: %+v", repo.auditQueries[1])
	}
	breakdownQueries := map[string]bool{}
	for _, query := range repo.auditQueries[2:] {
		if query.Limit != 1 {
			t.Fatalf("unexpected breakdown audit query limit: %+v", query)
		}
		breakdownQueries[query.EventType] = true
	}
	for _, eventType := range []string{
		"admin.telegram_request_resolve_not_found",
		"admin.telegram_request_resolve_not_pending",
		"admin.telegram_request_resolve_internal_failed",
	} {
		if !breakdownQueries[eventType] {
			t.Fatalf("missing breakdown audit query for %q: %+v", eventType, repo.auditQueries)
		}
	}
	if payload.Data.NextCursors["pending_requests"] == "" || payload.Data.NextCursors["recent_decisions"] == "" || payload.Data.NextCursors["recent_failures"] != "more-failures" || payload.Data.NextCursors["recent_resolutions"] != "more-resolutions" || payload.Data.NextCursors["failure_not_found"] != "more-not-found" || payload.Data.NextCursors["failure_not_pending"] != "more-not-pending" {
		t.Fatalf("unexpected next_cursors payload: %+v", payload.Data.NextCursors)
	}
	if payload.Meta.Limit != 10 || payload.Meta.PendingLimit != 1 || payload.Meta.DecisionLimit != 2 || payload.Meta.FailureLimit != 1 || payload.Meta.ResolutionLimit != 1 {
		t.Fatalf("unexpected trust summary meta: %+v", payload.Meta)
	}
}

func TestHandleTelegramTrustDecisions(t *testing.T) {
	t1 := time.Now().UTC().Add(-time.Hour)
	t2 := time.Now().UTC().Add(-2 * time.Hour)
	repo := &appRepoStub{
		telegramUsers: []domain.TelegramUserAccess{
			{TelegramUserID: "approved_1", Status: "approved", Allowed: true, DecidedAt: &t1},
			{TelegramUserID: "denied_1", Status: "denied", DecidedAt: &t2},
		},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/trust/decisions?limit=10", nil)
	rec := httptest.NewRecorder()
	app.handleTelegramTrustDecisions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Items      []domain.TelegramUserAccess `json:"items"`
			HasMore    bool                        `json:"has_more"`
			NextCursor string                      `json:"next_cursor"`
		} `json:"data"`
		Meta struct {
			Limit int `json:"limit"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Items) != 2 || payload.Data.Items[0].TelegramUserID != "approved_1" || payload.Data.Items[1].TelegramUserID != "denied_1" {
		t.Fatalf("unexpected trust decisions payload: %+v", payload.Data.Items)
	}
	if payload.Data.HasMore {
		t.Fatalf("did not expect has_more for two returned decisions, got %+v", payload)
	}
	if payload.Data.NextCursor != "" {
		t.Fatalf("did not expect next_cursor for two returned decisions, got %+v", payload)
	}
	if payload.Meta.Limit != 10 {
		t.Fatalf("unexpected trust decisions meta: %+v", payload.Meta)
	}
}

func TestMergeTelegramDecisionLists(t *testing.T) {
	t1 := time.Now().UTC().Add(-time.Hour)
	t2 := time.Now().UTC().Add(-2 * time.Hour)
	t3 := time.Now().UTC().Add(-3 * time.Hour)
	approved := []domain.TelegramUserAccess{
		{TelegramUserID: "approved_1", Status: "approved", DecidedAt: &t2},
		{TelegramUserID: "approved_2", Status: "approved", DecidedAt: &t3},
	}
	denied := []domain.TelegramUserAccess{
		{TelegramUserID: "denied_1", Status: "denied", DecidedAt: &t1},
	}
	got := mergeTelegramDecisionLists(approved, denied, 2)
	if len(got) != 2 {
		t.Fatalf("expected limited merged decisions, got %+v", got)
	}
	if got[0].TelegramUserID != "denied_1" || got[1].TelegramUserID != "approved_1" {
		t.Fatalf("unexpected merged decision order: %+v", got)
	}
}

func TestHandleUpsertTelegramUser(t *testing.T) {
	repo := &appRepoStub{telegramAllowed: map[string]bool{}}
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: repo}
	req := httptest.NewRequest(http.MethodPost, "/admin/telegram/users/upsert", strings.NewReader(`{"telegram_user_id":"123","display_name":"alice","allowed":true,"added_by":"admin"}`))
	rec := httptest.NewRecorder()
	app.handleUpsertTelegramUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.TelegramUserAccess `json:"data"`
		Meta struct {
			Action string `json:"action"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.TelegramUserID != "123" || !payload.Data.Allowed || payload.Meta.Action != "telegram_user_upserted" {
		t.Fatalf("unexpected upsert payload: %+v", payload)
	}
	if !repo.telegramAllowed["123"] {
		t.Fatal("expected telegram user access to be upserted")
	}
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.telegram_user_upserted" {
		t.Fatalf("expected upsert audit event, got %+v", repo.auditEvents)
	}
}

func TestHandleUpsertTelegramUserFailureAudited(t *testing.T) {
	repo := &appRepoStub{
		telegramAllowed: map[string]bool{},
		upsertErr:       errors.New("db down"),
	}
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: repo}
	req := httptest.NewRequest(http.MethodPost, "/admin/telegram/users/upsert", strings.NewReader(`{"telegram_user_id":"123","display_name":"alice","allowed":true,"added_by":"admin"}`))
	rec := httptest.NewRecorder()
	app.handleUpsertTelegramUser(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.telegram_user_upsert_failed" {
		t.Fatalf("expected failed upsert audit event, got %+v", repo.auditEvents)
	}
}

func TestHandleListTelegramRequests(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			telegramUsers: []domain.TelegramUserAccess{
				{TelegramUserID: "123", Status: "pending"},
				{TelegramUserID: "456", Status: "approved"},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/telegram/requests", nil)
	rec := httptest.NewRecorder()
	app.handleListTelegramRequests(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Items      []domain.TelegramUserAccess `json:"items"`
		NextCursor string                      `json:"next_cursor"`
		TotalCount int                         `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].TelegramUserID != "123" {
		t.Fatalf("unexpected telegram requests payload: %+v", payload.Items)
	}
	if payload.TotalCount != 1 {
		t.Fatalf("expected total_count=1, got %+v", payload)
	}
}

func TestHandleResolveTelegramRequest(t *testing.T) {
	repo := &appRepoStub{
		telegramUsers: []domain.TelegramUserAccess{{TelegramUserID: "123", Status: "pending"}},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/telegram/requests/resolve", strings.NewReader(`{"telegram_user_id":"123","status":"approved","added_by":"admin"}`))
	rec := httptest.NewRecorder()
	app.handleResolveTelegramRequest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.TelegramUserAccess `json:"data"`
		Meta struct {
			Action string `json:"action"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "approved" || !payload.Data.Allowed || payload.Meta.Action != "telegram_request_resolved" {
		t.Fatalf("expected approved telegram request payload, got %+v", payload)
	}
	if len(repo.auditEvents) != 2 || repo.auditEvents[0].EventType != "admin.telegram_request_resolved" || repo.auditEvents[1].EventType != "admin.telegram_request_approved" {
		t.Fatalf("expected generic and approval resolve audit events, got %+v", repo.auditEvents)
	}
	if len(repo.deliveries) != 1 {
		t.Fatalf("expected telegram resolution notification delivery, got %d", len(repo.deliveries))
	}
	if repo.deliveries[0].SessionID != "session_notice_tenant_default_telegram_123_123" {
		t.Fatalf("expected resolution delivery to use notification session, got %+v", repo.deliveries[0])
	}
}

func TestHandleResolveTelegramRequestNotFound(t *testing.T) {
	repo := &appRepoStub{}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/telegram/requests/resolve", strings.NewReader(`{"telegram_user_id":"404","status":"approved","added_by":"admin"}`))
	rec := httptest.NewRecorder()
	app.handleResolveTelegramRequest(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if len(repo.auditEvents) != 2 || repo.auditEvents[0].EventType != "admin.telegram_request_resolve_failed" || repo.auditEvents[1].EventType != "admin.telegram_request_resolve_not_found" {
		t.Fatalf("expected generic and not_found resolve audit events, got %+v", repo.auditEvents)
	}
}

func TestHandleResolveTelegramRequestAlreadyResolved(t *testing.T) {
	repo := &appRepoStub{
		telegramUsers: []domain.TelegramUserAccess{{TelegramUserID: "123", Status: "approved", Allowed: true}},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/telegram/requests/resolve", strings.NewReader(`{"telegram_user_id":"123","status":"denied","added_by":"admin"}`))
	rec := httptest.NewRecorder()
	app.handleResolveTelegramRequest(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	if len(repo.auditEvents) != 2 || repo.auditEvents[0].EventType != "admin.telegram_request_resolve_failed" || repo.auditEvents[1].EventType != "admin.telegram_request_resolve_not_pending" {
		t.Fatalf("expected generic and not_pending resolve audit events on conflict, got %+v", repo.auditEvents)
	}
	if len(repo.deliveries) != 0 {
		t.Fatalf("expected no deliveries on conflict, got %+v", repo.deliveries)
	}
}

func TestHandleDeleteTelegramUser(t *testing.T) {
	repo := &appRepoStub{
		telegramAllowed: map[string]bool{"123": true},
		telegramUsers:   []domain.TelegramUserAccess{{TelegramUserID: "123", Allowed: true}},
	}
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: repo}
	req := httptest.NewRequest(http.MethodPost, "/admin/telegram/users/delete", strings.NewReader(`{"telegram_user_id":"123"}`))
	rec := httptest.NewRecorder()
	app.handleDeleteTelegramUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			TelegramUserID string `json:"telegram_user_id"`
		} `json:"data"`
		Meta struct {
			Action string `json:"action"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.TelegramUserID != "123" || payload.Meta.Action != "telegram_user_deleted" {
		t.Fatalf("unexpected delete payload: %+v", payload)
	}
	if repo.telegramAllowed["123"] {
		t.Fatal("expected telegram user access to be removed")
	}
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.telegram_user_deleted" {
		t.Fatalf("expected delete audit event, got %+v", repo.auditEvents)
	}
}

func TestHandleDeleteTelegramUserFailureAudited(t *testing.T) {
	repo := &appRepoStub{
		telegramAllowed: map[string]bool{"123": true},
		deleteErr:       errors.New("db down"),
	}
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: repo}
	req := httptest.NewRequest(http.MethodPost, "/admin/telegram/users/delete", strings.NewReader(`{"telegram_user_id":"123"}`))
	rec := httptest.NewRecorder()
	app.handleDeleteTelegramUser(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.telegram_user_delete_failed" {
		t.Fatalf("expected failed delete audit event, got %+v", repo.auditEvents)
	}
}

func TestHandleListSurfaceSessions(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			surfaceItems: []domain.SurfaceSession{{Session: domain.Session{ID: "session_1", State: "open"}, Alias: "deploy"}},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/surfaces/sessions?channel_type=telegram&surface_key=123&owner_user_id=user1", nil)
	rec := httptest.NewRecorder()
	app.handleListSurfaceSessions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Items []domain.SurfaceSession `json:"items"`
		} `json:"data"`
		Meta struct {
			ChannelType string `json:"channel_type"`
			SurfaceKey  string `json:"surface_key"`
			OwnerUserID string `json:"owner_user_id"`
			Limit       int    `json:"limit"`
			Count       int    `json:"count"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Items) != 1 || payload.Data.Items[0].Alias != "deploy" {
		t.Fatalf("unexpected surface session payload: %+v", payload)
	}
	if payload.Meta.ChannelType != "telegram" || payload.Meta.SurfaceKey != "123" || payload.Meta.OwnerUserID != "user1" || payload.Meta.Limit != 50 || payload.Meta.Count != 1 {
		t.Fatalf("unexpected surface session meta: %+v", payload.Meta)
	}
}

func TestHandleListACPAgents(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", DefaultACPAgentName: "agent_a"},
		Repo: &appRepoStub{
			auditCounts: map[string]int{"worker.await_blocked_opencode_bridge": 3},
			auditPagesByEventType: map[string]domain.PagedResult[domain.AuditEvent]{
				"worker.await_blocked_opencode_bridge": {Items: []domain.AuditEvent{{EventType: "worker.await_blocked_opencode_bridge", RunID: "run_1"}}},
			},
		},
		Catalog: &services.AgentCatalog{Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}, {Name: "agent_b", Healthy: false}}}, TTL: time.Minute},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/acp/agents?refresh=true", nil)
	rec := httptest.NewRecorder()
	app.handleListACPAgents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data []domain.AgentManifest `json:"data"`
		Meta struct {
			Refresh      bool `json:"refresh"`
			Count        int  `json:"count"`
			BridgeBlocks struct {
				TotalCount   int                 `json:"total_count"`
				RecentEvents []domain.AuditEvent `json:"recent_events"`
			} `json:"bridge_blocks"`
			Catalog struct {
				CachedAgentCount int    `json:"cached_agent_count"`
				LastFetchError   string `json:"last_fetch_error"`
				CacheValid       bool   `json:"cache_valid"`
			} `json:"catalog"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 2 || payload.Data[0].Name != "agent_a" || !payload.Meta.Refresh || payload.Meta.Count != 2 {
		t.Fatalf("unexpected acp agents payload: %+v", payload)
	}
	if payload.Meta.Catalog.CachedAgentCount != 2 || !payload.Meta.Catalog.CacheValid || payload.Meta.Catalog.LastFetchError != "" {
		t.Fatalf("unexpected acp catalog meta: %+v", payload.Meta.Catalog)
	}
	if payload.Meta.BridgeBlocks.TotalCount != 3 {
		t.Fatalf("unexpected acp bridge block meta: %+v", payload.Meta.BridgeBlocks)
	}
	if len(payload.Meta.BridgeBlocks.RecentEvents) != 1 || payload.Meta.BridgeBlocks.RecentEvents[0].RunID != "run_1" {
		t.Fatalf("unexpected recent bridge block events: %+v", payload.Meta.BridgeBlocks.RecentEvents)
	}
}

func TestHandleListCompatibleACPAgents(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo: &appRepoStub{
			auditCounts: map[string]int{"worker.await_blocked_opencode_bridge": 4},
			auditPagesByEventType: map[string]domain.PagedResult[domain.AuditEvent]{
				"worker.await_blocked_opencode_bridge": {Items: []domain.AuditEvent{{EventType: "worker.await_blocked_opencode_bridge", SessionID: "session_1"}}},
			},
		},
		Catalog: &services.AgentCatalog{Bridge: testACPBridge{agents: []domain.AgentManifest{
			{Name: "agent_ok", Healthy: true, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
			{Name: "agent_bridge", Healthy: true, Protocol: "opencode", SupportsAwaitResume: false, SupportsStructuredAwait: false, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true},
			{Name: "agent_bad", Healthy: true, SupportsAwaitResume: false, SupportsStreaming: true, SupportsArtifacts: true},
		}}, TTL: time.Minute},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/acp/compatible", nil)
	rec := httptest.NewRecorder()
	app.handleListCompatibleACPAgents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data []domain.AgentCompatibility `json:"data"`
		Meta struct {
			Refresh      bool `json:"refresh"`
			Count        int  `json:"count"`
			BridgeBlocks struct {
				TotalCount   int                 `json:"total_count"`
				RecentEvents []domain.AuditEvent `json:"recent_events"`
			} `json:"bridge_blocks"`
			Compatibility struct {
				BridgeCompatibleCount int `json:"bridge_compatible_count"`
				DegradedCount         int `json:"degraded_count"`
				WarningCount          int `json:"warning_count"`
			} `json:"compatibility"`
			Catalog struct {
				CachedAgentCount int  `json:"cached_agent_count"`
				CacheValid       bool `json:"cache_valid"`
			} `json:"catalog"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 2 || payload.Meta.Count != 2 {
		t.Fatalf("unexpected compatible acp payload: %+v", payload)
	}
	if payload.Data[0].AgentName != "agent_ok" || payload.Data[1].AgentName != "agent_bridge" {
		t.Fatalf("unexpected compatible agent payload: %+v", payload.Data)
	}
	if payload.Meta.Compatibility.BridgeCompatibleCount != 1 || payload.Meta.Compatibility.DegradedCount != 1 || payload.Meta.Compatibility.WarningCount != 2 {
		t.Fatalf("unexpected compatible summary meta: %+v", payload.Meta.Compatibility)
	}
	if payload.Meta.Catalog.CachedAgentCount != 3 || !payload.Meta.Catalog.CacheValid {
		t.Fatalf("unexpected compatible acp catalog meta: %+v", payload.Meta.Catalog)
	}
	if payload.Meta.BridgeBlocks.TotalCount != 4 {
		t.Fatalf("unexpected compatible acp bridge block meta: %+v", payload.Meta.BridgeBlocks)
	}
	if len(payload.Meta.BridgeBlocks.RecentEvents) != 1 || payload.Meta.BridgeBlocks.RecentEvents[0].SessionID != "session_1" {
		t.Fatalf("unexpected compatible recent bridge block events: %+v", payload.Meta.BridgeBlocks.RecentEvents)
	}
}

func TestHandleValidateACPAgent(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", DefaultACPAgentName: "agent_ok"},
		Repo: &appRepoStub{
			auditCounts: map[string]int{"worker.await_blocked_opencode_bridge": 2},
			auditPagesByEventType: map[string]domain.PagedResult[domain.AuditEvent]{
				"worker.await_blocked_opencode_bridge": {Items: []domain.AuditEvent{{EventType: "worker.await_blocked_opencode_bridge", AggregateID: "agent_ok"}}},
			},
		},
		Catalog: &services.AgentCatalog{Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_ok", Healthy: true, SupportsAwaitResume: true, SupportsStructuredAwait: true, SupportsSessionReload: true, SupportsStreaming: true, SupportsArtifacts: true}}}, TTL: time.Minute},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/acp/validate", nil)
	rec := httptest.NewRecorder()
	app.handleValidateACPAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.AgentCompatibility `json:"data"`
		Meta struct {
			AgentName    string `json:"agent_name"`
			Refresh      bool   `json:"refresh"`
			BridgeBlocks struct {
				TotalCount   int                 `json:"total_count"`
				RecentEvents []domain.AuditEvent `json:"recent_events"`
			} `json:"bridge_blocks"`
			Compatibility struct {
				ValidationMode string `json:"validation_mode"`
				WarningCount   int    `json:"warning_count"`
				Degraded       bool   `json:"degraded"`
				ReasonCount    int    `json:"reason_count"`
			} `json:"compatibility"`
			Catalog struct {
				CachedAgentCount int  `json:"cached_agent_count"`
				CacheValid       bool `json:"cache_valid"`
			} `json:"catalog"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.AgentName != "agent_ok" || !payload.Data.Compatible || payload.Meta.AgentName != "agent_ok" {
		t.Fatalf("unexpected validate acp payload: %+v", payload)
	}
	if payload.Meta.Compatibility.ValidationMode != "strict_acp" || payload.Meta.Compatibility.WarningCount != 0 || payload.Meta.Compatibility.Degraded || payload.Meta.Compatibility.ReasonCount != 0 {
		t.Fatalf("unexpected validate compatibility meta: %+v", payload.Meta.Compatibility)
	}
	if payload.Meta.Catalog.CachedAgentCount != 1 || !payload.Meta.Catalog.CacheValid {
		t.Fatalf("unexpected validate acp catalog meta: %+v", payload.Meta.Catalog)
	}
	if payload.Meta.BridgeBlocks.TotalCount != 2 {
		t.Fatalf("unexpected validate acp bridge block meta: %+v", payload.Meta.BridgeBlocks)
	}
	if len(payload.Meta.BridgeBlocks.RecentEvents) != 1 || payload.Meta.BridgeBlocks.RecentEvents[0].AggregateID != "agent_ok" {
		t.Fatalf("unexpected validate recent bridge block events: %+v", payload.Meta.BridgeBlocks.RecentEvents)
	}
}

func TestHandleValidateOpenCodeBridgeAgent(t *testing.T) {
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", DefaultACPAgentName: "build"},
		Repo: &appRepoStub{
			auditCounts: map[string]int{"worker.await_blocked_opencode_bridge": 5},
			auditPagesByEventType: map[string]domain.PagedResult[domain.AuditEvent]{
				"worker.await_blocked_opencode_bridge": {Items: []domain.AuditEvent{{EventType: "worker.await_blocked_opencode_bridge", SessionID: "session_build"}}},
			},
		},
		Catalog: &services.AgentCatalog{Bridge: testACPBridge{agents: []domain.AgentManifest{{
			Name:                    "build",
			Protocol:                "opencode",
			Healthy:                 true,
			SupportsAwaitResume:     false,
			SupportsStructuredAwait: false,
			SupportsSessionReload:   true,
			SupportsStreaming:       true,
			SupportsArtifacts:       true,
		}}}, TTL: time.Minute},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/acp/validate", nil)
	rec := httptest.NewRecorder()
	app.handleValidateACPAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.AgentCompatibility `json:"data"`
		Meta struct {
			BridgeBlocks struct {
				TotalCount   int                 `json:"total_count"`
				RecentEvents []domain.AuditEvent `json:"recent_events"`
			} `json:"bridge_blocks"`
			Compatibility struct {
				ValidationMode string `json:"validation_mode"`
				WarningCount   int    `json:"warning_count"`
				Degraded       bool   `json:"degraded"`
				ReasonCount    int    `json:"reason_count"`
			} `json:"compatibility"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Data.Compatible || payload.Data.ValidationMode != "opencode_bridge" || len(payload.Data.Warnings) == 0 {
		t.Fatalf("unexpected OpenCode validate payload: %+v", payload)
	}
	if payload.Meta.Compatibility.ValidationMode != "opencode_bridge" || payload.Meta.Compatibility.WarningCount == 0 || !payload.Meta.Compatibility.Degraded || payload.Meta.Compatibility.ReasonCount != 0 {
		t.Fatalf("unexpected OpenCode validate compatibility meta: %+v", payload.Meta.Compatibility)
	}
	if payload.Meta.BridgeBlocks.TotalCount != 5 {
		t.Fatalf("unexpected OpenCode validate bridge block meta: %+v", payload.Meta.BridgeBlocks)
	}
	if len(payload.Meta.BridgeBlocks.RecentEvents) != 1 || payload.Meta.BridgeBlocks.RecentEvents[0].SessionID != "session_build" {
		t.Fatalf("unexpected OpenCode recent bridge block events: %+v", payload.Meta.BridgeBlocks.RecentEvents)
	}
}

func TestHandleSwitchSurfaceSession(t *testing.T) {
	repo := &appRepoStub{
		switchedSession: domain.Session{ID: "session_switched", State: "open"},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/surfaces/sessions/switch", strings.NewReader(`{"channel_type":"telegram","surface_key":"123","owner_user_id":"user1","alias_or_id":"deploy"}`))
	rec := httptest.NewRecorder()
	app.handleSwitchSurfaceSession(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.Session `json:"data"`
		Meta struct {
			Action      string `json:"action"`
			ChannelType string `json:"channel_type"`
			SurfaceKey  string `json:"surface_key"`
			OwnerUserID string `json:"owner_user_id"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.ID != "session_switched" || payload.Meta.Action != "surface_session_switched" || payload.Meta.ChannelType != "telegram" || payload.Meta.SurfaceKey != "123" || payload.Meta.OwnerUserID != "user1" {
		t.Fatalf("unexpected switched session payload: %+v", payload)
	}
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.surface_session_switched" {
		t.Fatalf("expected switch audit event, got %+v", repo.auditEvents)
	}
}

func TestHandleCloseSurfaceSession(t *testing.T) {
	repo := &appRepoStub{
		closedSession: domain.Session{ID: "session_closed", State: "archived"},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default"},
		Repo:   repo,
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/surfaces/sessions/close", strings.NewReader(`{"channel_type":"telegram","surface_key":"123","owner_user_id":"user1"}`))
	rec := httptest.NewRecorder()
	app.handleCloseSurfaceSession(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data domain.Session `json:"data"`
		Meta struct {
			Action      string `json:"action"`
			ChannelType string `json:"channel_type"`
			SurfaceKey  string `json:"surface_key"`
			OwnerUserID string `json:"owner_user_id"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.State != "archived" || payload.Meta.Action != "surface_session_closed" || payload.Meta.ChannelType != "telegram" || payload.Meta.SurfaceKey != "123" || payload.Meta.OwnerUserID != "user1" {
		t.Fatalf("unexpected close session payload: %+v", payload)
	}
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.surface_session_closed" {
		t.Fatalf("expected close audit event, got %+v", repo.auditEvents)
	}
}

func TestHandleCancelRun(t *testing.T) {
	repo := &appRepoStub{}
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: repo}
	req := httptest.NewRequest(http.MethodPost, "/admin/runs/cancel", strings.NewReader(`{"run_id":"run_123"}`))
	rec := httptest.NewRecorder()
	app.handleCancelRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
		} `json:"data"`
		Meta struct {
			Action string `json:"action"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.RunID != "run_123" || payload.Data.Status != "canceled" || payload.Meta.Action != "run_canceled" {
		t.Fatalf("unexpected cancel payload: %+v", payload)
	}
	if repo.forcedCanceledRunID != "run_123" {
		t.Fatalf("expected ForceCancelRun to receive run_123, got %q", repo.forcedCanceledRunID)
	}
}

func TestHandleRetryDelivery(t *testing.T) {
	repo := &appRepoStub{}
	app := &App{Config: config.Config{DefaultTenantID: "tenant_default"}, Repo: repo}
	req := httptest.NewRequest(http.MethodPost, "/admin/deliveries/retry", strings.NewReader(`{"delivery_id":"delivery_123"}`))
	rec := httptest.NewRecorder()
	app.handleRetryDelivery(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data struct {
			DeliveryID string `json:"delivery_id"`
			Status     string `json:"status"`
		} `json:"data"`
		Meta struct {
			Action string `json:"action"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.DeliveryID != "delivery_123" || payload.Data.Status != "queued" || payload.Meta.Action != "delivery_retry_queued" {
		t.Fatalf("unexpected retry payload: %+v", payload)
	}
	if repo.retriedDeliveryID != "delivery_123" {
		t.Fatalf("expected RetryDelivery to receive delivery_123, got %q", repo.retriedDeliveryID)
	}
}
