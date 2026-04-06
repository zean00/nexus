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

	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/services"
)

type testChannelAdapter struct {
	event domain.CanonicalInboundEvent
	sent  []domain.OutboundDelivery
}

func (a testChannelAdapter) Channel() string { return a.event.Channel }
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
	agents      []domain.AgentManifest
	discoverErr error
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
func (b testACPBridge) FindRunByIdempotencyKey(context.Context, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}
func (b testACPBridge) CancelRun(context.Context, domain.Run) error { return nil }
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
	surfaceItems    []domain.SurfaceSession
	switchedSession domain.Session
	closedSession   domain.Session
	notificationSession domain.Session
	forcedCanceledRunID string
	retriedDeliveryID   string
	auditEvents     []domain.AuditEvent
	deliveries      []domain.OutboundDelivery
	telegramAllowed map[string]bool
	telegramUsers   []domain.TelegramUserAccess
	telegramAccessQueries []domain.TelegramUserAccessListQuery
	auditPage       domain.PagedResult[domain.AuditEvent]
	auditPagesByEventType map[string]domain.PagedResult[domain.AuditEvent]
	lastAuditQuery  domain.AuditEventListQuery
	auditQueries    []domain.AuditEventListQuery
	sessionDetail   domain.SessionDetail
	runDetail       domain.RunDetail
	awaitDetail     domain.AwaitDetail
	upsertErr       error
	deleteErr       error
	deliveryCounts  map[string]int
	runCounts       map[string]int
	awaitCounts     map[string]int
	auditCounts     map[string]int
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
func (r *appRepoStub) StoreArtifacts(context.Context, string, string, []domain.Artifact) error { return nil }
func (r *appRepoStub) EnqueueMessage(context.Context, domain.CanonicalInboundEvent, domain.Session, domain.RouteDecision, string, bool) (domain.QueueItem, *domain.OutboxEvent, error) {
	return domain.QueueItem{}, nil, nil
}
func (r *appRepoStub) CreateRun(context.Context, domain.Run) error { return nil }
func (r *appRepoStub) UpdateRunStatus(context.Context, string, string) error { return nil }
func (r *appRepoStub) UpdateQueueItemStatus(context.Context, string, string) error { return nil }
func (r *appRepoStub) UpdateActiveQueueItemStatus(context.Context, string, string) error { return nil }
func (r *appRepoStub) EnqueueNextQueueItem(context.Context, string) (*domain.OutboxEvent, error) { return nil, nil }
func (r *appRepoStub) StoreAwait(context.Context, domain.Await) error { return nil }
func (r *appRepoStub) ResolveAwait(context.Context, string, string, []byte) (domain.Await, error) {
	return domain.Await{}, nil
}
func (r *appRepoStub) GetAwait(context.Context, string) (domain.Await, error) { return domain.Await{}, nil }
func (r *appRepoStub) EnqueueAwaitResume(context.Context, domain.ResumeRequest, string) error { return nil }
func (r *appRepoStub) EnqueueDelivery(_ context.Context, delivery domain.OutboundDelivery) error {
	r.deliveries = append(r.deliveries, delivery)
	return nil
}
func (r *appRepoStub) ClaimOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) { return nil, nil }
func (r *appRepoStub) MarkOutboxDone(context.Context, string) error { return nil }
func (r *appRepoStub) MarkOutboxFailed(context.Context, string, error, time.Time) error { return nil }
func (r *appRepoStub) GetQueueItem(context.Context, string) (domain.QueueItem, error) { return domain.QueueItem{}, nil }
func (r *appRepoStub) GetSession(context.Context, string) (domain.Session, error) { return domain.Session{}, nil }
func (r *appRepoStub) GetRouteDecision(context.Context, string) (domain.RouteDecision, error) {
	return domain.RouteDecision{}, nil
}
func (r *appRepoStub) GetInboundMessage(context.Context, string) (domain.Message, error) { return domain.Message{}, nil }
func (r *appRepoStub) GetDelivery(context.Context, string) (domain.OutboundDelivery, error) {
	return domain.OutboundDelivery{}, nil
}
func (r *appRepoStub) GetLatestDeliveryByLogicalMessage(context.Context, string) (*domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *appRepoStub) GetRun(context.Context, string) (domain.Run, error) { return domain.Run{}, nil }
func (r *appRepoStub) GetRunByACP(context.Context, string) (domain.Run, error) { return domain.Run{}, nil }
func (r *appRepoStub) GetAwaitsForRun(context.Context, string, int) ([]domain.Await, error) { return nil, nil }
func (r *appRepoStub) GetAwaitResponses(context.Context, string, int) ([]domain.AwaitResponse, error) { return nil, nil }
func (r *appRepoStub) MarkDeliverySent(context.Context, string, domain.DeliveryResult) error { return nil }
func (r *appRepoStub) MarkDeliverySending(context.Context, string) error { return nil }
func (r *appRepoStub) MarkDeliveryFailed(context.Context, string, error) error { return nil }
func (r *appRepoStub) ListMessages(context.Context, domain.MessageListQuery) (domain.PagedResult[domain.Message], error) {
	return domain.PagedResult[domain.Message]{}, nil
}
func (r *appRepoStub) CountMessages(context.Context, domain.MessageListQuery) (int, error) { return 1, nil }
func (r *appRepoStub) ListArtifacts(context.Context, domain.ArtifactListQuery) (domain.PagedResult[domain.Artifact], error) {
	return domain.PagedResult[domain.Artifact]{}, nil
}
func (r *appRepoStub) CountArtifacts(context.Context, domain.ArtifactListQuery) (int, error) { return 1, nil }
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
func (r *appRepoStub) CountSessions(context.Context, domain.SessionListQuery) (int, error) { return 1, nil }
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
func (r *appRepoStub) ListStaleClaimedOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) { return nil, nil }
func (r *appRepoStub) RequeueOutbox(context.Context, string) error { return nil }
func (r *appRepoStub) RequeueQueueStartOutbox(context.Context, string, string) error { return nil }
func (r *appRepoStub) ListStuckQueueItems(context.Context, time.Time, int) ([]domain.QueueItem, error) {
	return nil, nil
}
func (r *appRepoStub) ListStaleRuns(context.Context, time.Time, int) ([]domain.Run, error) { return nil, nil }
func (r *appRepoStub) ListExpiredAwaits(context.Context, time.Time, int) ([]domain.Await, error) { return nil, nil }
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
		Channel: "telegram",
		Sender:  domain.Sender{ChannelUserID: "999"},
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
		Channel:      "telegram",
		ProviderEventID: "evt_repeat_1",
		Sender:       domain.Sender{ChannelUserID: "999"},
		Conversation: domain.Conversation{ChannelConversationID: "chat123"},
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
		TenantID:     "tenant_default",
		Channel:      "slack",
		Interaction:  "await_response",
		Sender:       domain.Sender{ChannelUserID: "123"},
		Metadata:     domain.Metadata{AwaitID: "await_123", ResumePayload: []byte(`{"choice":"yes"}`)},
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
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}}},
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
			Service string `json:"service"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "ready" || payload.Meta.Service != "gateway" {
		t.Fatalf("unexpected gateway readiness payload: %+v", payload)
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
		Config: config.Config{WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
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
		Config:  config.Config{DefaultTenantID: "tenant_default", WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
		Repo:    &appRepoStub{auditCounts: map[string]int{
			"reconciler.outbox_requeued":        2,
			"reconciler.queue_repair_recovered": 6,
			"reconciler.queue_repair_requeued":  7,
			"reconciler.run_refreshed":          3,
			"reconciler.await_expired":          4,
			"delivery.retry_requested":          5,
		}},
		Runtime: &RuntimeState{},
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
				LastWorkerError    string `json:"last_worker_error"`
				LastHealthStatus   string `json:"last_health_status"`
				LastReadinessStatus string `json:"last_readiness_status"`
				OutboxRequeueCount int `json:"outbox_requeue_count"`
				QueueRepairRecoveredCount int `json:"queue_repair_recovered_count"`
				QueueRepairRequeuedCount int `json:"queue_repair_requeued_count"`
				RunRefreshCount int `json:"run_refresh_count"`
				AwaitExpiryCount int `json:"await_expiry_count"`
				DeliveryRetryCount int `json:"delivery_retry_count"`
				RecentTransitions  []struct {
					Probe string `json:"probe"`
					To    string `json:"to"`
				} `json:"recent_transitions"`
			} `json:"runtime"`
			Health    string `json:"health"`
			Readiness string `json:"readiness"`
			Persisted map[string]int `json:"persisted"`
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
	if len(payload.Data.Runtime.RecentTransitions) == 0 {
		t.Fatalf("expected runtime transitions, got %+v", payload.Data.Runtime)
	}
	if payload.Data.Persisted["persisted_outbox_requeues"] != 2 ||
		payload.Data.Persisted["persisted_queue_repairs_recovered"] != 6 ||
		payload.Data.Persisted["persisted_queue_repairs_requeued"] != 7 ||
		payload.Data.Persisted["persisted_run_refreshes"] != 3 ||
		payload.Data.Persisted["persisted_await_expiries"] != 4 ||
		payload.Data.Persisted["persisted_delivery_retries"] != 5 {
		t.Fatalf("unexpected persisted counters: %+v", payload.Data.Persisted)
	}
}

func TestGatewayMetricsEndpoint(t *testing.T) {
	repo := &appRepoStub{
		deliveryCounts: map[string]int{"queued": 3, "sending": 1},
		runCounts:      map[string]int{"starting": 1, "running": 2, "awaiting": 4},
		awaitCounts:    map[string]int{"pending": 5},
		auditCounts: map[string]int{
			"reconciler.outbox_requeued":        2,
			"reconciler.queue_repair_recovered": 6,
			"reconciler.queue_repair_requeued":  7,
			"reconciler.run_refreshed":          3,
			"reconciler.await_expired":          4,
			"delivery.retry_requested":          5,
		},
	}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", WorkerPollInterval: time.Second, ReconcilerInterval: time.Second},
		Repo:   repo,
		Catalog: &services.AgentCatalog{
			Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_a", Healthy: true}}},
			TTL:    time.Minute,
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
		`nexus_persisted_outbox_requeues_total{service="gateway",tenant="tenant_default"} 2`,
		`nexus_persisted_queue_repairs_recovered_total{service="gateway",tenant="tenant_default"} 6`,
		`nexus_persisted_queue_repairs_requeued_total{service="gateway",tenant="tenant_default"} 7`,
		`nexus_persisted_run_refreshes_total{service="gateway",tenant="tenant_default"} 3`,
		`nexus_persisted_await_expiries_total{service="gateway",tenant="tenant_default"} 4`,
		`nexus_persisted_delivery_retries_total{service="gateway",tenant="tenant_default"} 5`,
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
		NextCursor string                     `json:"next_cursor"`
		TotalCount int                        `json:"total_count"`
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
					{EventType: "admin.telegram_request_resolve_failed", AggregateID: "denied_1"},
					{EventType: "admin.telegram_request_resolve_failed", AggregateID: "pending_2"},
				},
				NextCursor: "more-failures",
			},
			"admin.telegram_request_resolved": {
				Items: []domain.AuditEvent{
					{EventType: "admin.telegram_request_resolved", AggregateID: "approved_1"},
					{EventType: "admin.telegram_request_resolved", AggregateID: "approved_2"},
				},
				NextCursor: "more-resolutions",
			},
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
			Counts            map[string]int              `json:"counts"`
			HasMore           map[string]bool             `json:"has_more"`
			NextCursors       map[string]string           `json:"next_cursors"`
			PendingRequests   []domain.TelegramUserAccess `json:"pending_requests"`
			RecentApproved    []domain.TelegramUserAccess `json:"recent_approved"`
			RecentDenied      []domain.TelegramUserAccess `json:"recent_denied"`
			RecentDecisions   []domain.TelegramUserAccess `json:"recent_decisions"`
			RecentFailures    []domain.AuditEvent         `json:"recent_failures"`
			RecentResolutions []domain.AuditEvent         `json:"recent_resolutions"`
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
	if len(payload.Data.RecentFailures) != 1 || payload.Data.RecentFailures[0].EventType != "admin.telegram_request_resolve_failed" {
		t.Fatalf("unexpected recent failures payload: %+v", payload.Data.RecentFailures)
	}
	if len(payload.Data.RecentResolutions) != 1 || payload.Data.RecentResolutions[0].EventType != "admin.telegram_request_resolved" {
		t.Fatalf("unexpected recent resolutions payload: %+v", payload.Data.RecentResolutions)
	}
	if payload.Data.Counts["pending"] != 2 || payload.Data.Counts["approved"] != 2 || payload.Data.Counts["denied"] != 1 || payload.Data.Counts["failures"] != 2 || payload.Data.Counts["resolutions"] != 2 || payload.Data.Counts["decisions"] != 3 {
		t.Fatalf("unexpected trust summary counts: %+v", payload.Data.Counts)
	}
	if !payload.Data.HasMore["pending_requests"] || !payload.Data.HasMore["recent_failures"] || !payload.Data.HasMore["recent_resolutions"] || !payload.Data.HasMore["recent_decisions"] {
		t.Fatalf("expected has_more metadata for truncated sections, got %+v", payload.Data.HasMore)
	}
	if payload.Data.HasMore["recent_denied"] || payload.Data.HasMore["recent_approved"] {
		t.Fatalf("did not expect has_more for fully returned sections, got %+v", payload.Data.HasMore)
	}
	if len(repo.auditQueries) != 2 {
		t.Fatalf("expected two audit queries, got %+v", repo.auditQueries)
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
	if payload.Data.NextCursors["pending_requests"] == "" || payload.Data.NextCursors["recent_decisions"] == "" || payload.Data.NextCursors["recent_failures"] != "more-failures" || payload.Data.NextCursors["recent_resolutions"] != "more-resolutions" {
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
		NextCursor string                     `json:"next_cursor"`
		TotalCount int                        `json:"total_count"`
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
		Config:   config.Config{DefaultTenantID: "tenant_default"},
		Repo:     repo,
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
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.telegram_request_resolved" {
		t.Fatalf("expected resolve audit event, got %+v", repo.auditEvents)
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
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.telegram_request_resolve_failed" {
		t.Fatalf("expected failed resolve audit event, got %+v", repo.auditEvents)
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
	if len(repo.auditEvents) != 1 || repo.auditEvents[0].EventType != "admin.telegram_request_resolve_failed" {
		t.Fatalf("expected failed resolve audit event on conflict, got %+v", repo.auditEvents)
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
		Config:  config.Config{DefaultACPAgentName: "agent_a"},
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
			Refresh bool `json:"refresh"`
			Count   int  `json:"count"`
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
}

func TestHandleListCompatibleACPAgents(t *testing.T) {
	app := &App{
		Catalog: &services.AgentCatalog{Bridge: testACPBridge{agents: []domain.AgentManifest{
			{Name: "agent_ok", Healthy: true, SupportsAwaitResume: true, SupportsStreaming: true, SupportsArtifacts: true},
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
			Refresh bool `json:"refresh"`
			Count   int  `json:"count"`
			Catalog struct {
				CachedAgentCount int  `json:"cached_agent_count"`
				CacheValid       bool `json:"cache_valid"`
			} `json:"catalog"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].AgentName != "agent_ok" || payload.Meta.Count != 1 {
		t.Fatalf("unexpected compatible acp payload: %+v", payload)
	}
	if payload.Meta.Catalog.CachedAgentCount != 2 || !payload.Meta.Catalog.CacheValid {
		t.Fatalf("unexpected compatible acp catalog meta: %+v", payload.Meta.Catalog)
	}
}

func TestHandleValidateACPAgent(t *testing.T) {
	app := &App{
		Config:  config.Config{DefaultACPAgentName: "agent_ok"},
		Catalog: &services.AgentCatalog{Bridge: testACPBridge{agents: []domain.AgentManifest{{Name: "agent_ok", Healthy: true, SupportsAwaitResume: true, SupportsStreaming: true, SupportsArtifacts: true}}}, TTL: time.Minute},
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
			AgentName string `json:"agent_name"`
			Refresh   bool   `json:"refresh"`
			Catalog   struct {
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
	if payload.Meta.Catalog.CachedAgentCount != 1 || !payload.Meta.Catalog.CacheValid {
		t.Fatalf("unexpected validate acp catalog meta: %+v", payload.Meta.Catalog)
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
