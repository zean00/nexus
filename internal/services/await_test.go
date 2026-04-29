package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type awaitRepo struct {
	resolved domain.Await
	resume   domain.ResumeRequest
	tenantID string
}

func (r *awaitRepo) InTx(ctx context.Context, fn func(ctx context.Context, repo ports.Repository) error) error {
	return fn(ctx, r)
}
func (r *awaitRepo) RecordInboundReceipt(context.Context, domain.CanonicalInboundEvent) (bool, error) {
	return true, nil
}
func (r *awaitRepo) ResolveSession(context.Context, domain.CanonicalInboundEvent, string) (domain.Session, bool, error) {
	return domain.Session{}, false, nil
}
func (r *awaitRepo) HasActiveRun(context.Context, string) (bool, error) { return false, nil }
func (r *awaitRepo) StoreInboundMessage(context.Context, domain.CanonicalInboundEvent, string) (string, error) {
	return "", nil
}
func (r *awaitRepo) StoreOutboundMessage(context.Context, domain.Session, string, string, string, []byte) (string, error) {
	return "msg_out_1", nil
}
func (r *awaitRepo) StoreArtifacts(context.Context, string, string, []domain.Artifact) error {
	return nil
}
func (r *awaitRepo) EnqueueMessage(context.Context, domain.CanonicalInboundEvent, domain.Session, domain.RouteDecision, string, bool) (domain.QueueItem, *domain.OutboxEvent, error) {
	return domain.QueueItem{}, nil, nil
}
func (r *awaitRepo) CreateRun(context.Context, domain.Run) error                       { return nil }
func (r *awaitRepo) UpdateRunStatus(context.Context, string, string) error             { return nil }
func (r *awaitRepo) UpdateQueueItemStatus(context.Context, string, string) error       { return nil }
func (r *awaitRepo) UpdateActiveQueueItemStatus(context.Context, string, string) error { return nil }
func (r *awaitRepo) EnqueueNextQueueItem(context.Context, string) (*domain.OutboxEvent, error) {
	return nil, nil
}
func (r *awaitRepo) StoreAwait(context.Context, domain.Await) error { return nil }
func (r *awaitRepo) ResolveAwait(_ context.Context, awaitID string, actorID, actorUserID, identityAssurance string, payload []byte) (domain.Await, error) {
	r.resolved = domain.Await{ID: awaitID, RunID: "run_1", SessionID: "session_1", ExpiresAt: time.Now().Add(time.Hour)}
	if actorID == "" || len(payload) == 0 {
		return domain.Await{}, nil
	}
	return r.resolved, nil
}
func (r *awaitRepo) GetAwait(context.Context, string) (domain.Await, error) {
	return domain.Await{}, nil
}
func (r *awaitRepo) EnqueueAwaitResume(_ context.Context, req domain.ResumeRequest, tenantID string) error {
	r.resume = req
	r.tenantID = tenantID
	return nil
}
func (r *awaitRepo) EnqueueDelivery(context.Context, domain.OutboundDelivery) error { return nil }
func (r *awaitRepo) ClaimOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}
func (r *awaitRepo) MarkOutboxDone(context.Context, string) error                     { return nil }
func (r *awaitRepo) MarkOutboxFailed(context.Context, string, error, time.Time) error { return nil }
func (r *awaitRepo) GetQueueItem(context.Context, string) (domain.QueueItem, error) {
	return domain.QueueItem{}, nil
}
func (r *awaitRepo) GetQueueStartIdempotencyKey(context.Context, string) (string, error) {
	return "", nil
}
func (r *awaitRepo) GetSession(context.Context, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *awaitRepo) UpdateSessionACPSessionID(context.Context, string, string) error { return nil }
func (r *awaitRepo) GetRouteDecision(context.Context, string) (domain.RouteDecision, error) {
	return domain.RouteDecision{}, nil
}
func (r *awaitRepo) GetTrustPolicy(context.Context, string, string) (domain.TrustPolicy, error) {
	return domain.TrustPolicy{}, domain.ErrTrustPolicyNotFound
}
func (r *awaitRepo) ListTrustPolicies(context.Context, string, int) ([]domain.TrustPolicy, error) {
	return nil, nil
}
func (r *awaitRepo) UpsertTrustPolicy(context.Context, domain.TrustPolicy) error { return nil }
func (r *awaitRepo) GetInboundMessage(context.Context, string) (domain.Message, error) {
	return domain.Message{}, nil
}
func (r *awaitRepo) GetRun(context.Context, string) (domain.Run, error) { return domain.Run{}, nil }
func (r *awaitRepo) GetRunByACP(context.Context, string) (domain.Run, error) {
	return domain.Run{}, nil
}
func (r *awaitRepo) GetAwaitsForRun(context.Context, string, int) ([]domain.Await, error) {
	return nil, nil
}
func (r *awaitRepo) GetAwaitResponses(context.Context, string, int) ([]domain.AwaitResponse, error) {
	return nil, nil
}
func (r *awaitRepo) ListMessages(context.Context, domain.MessageListQuery) (domain.PagedResult[domain.Message], error) {
	return domain.PagedResult[domain.Message]{}, nil
}
func (r *awaitRepo) ListArtifacts(context.Context, domain.ArtifactListQuery) (domain.PagedResult[domain.Artifact], error) {
	return domain.PagedResult[domain.Artifact]{}, nil
}
func (r *awaitRepo) GetSessionDetail(context.Context, string, int) (domain.SessionDetail, error) {
	return domain.SessionDetail{}, nil
}
func (r *awaitRepo) GetRunDetail(context.Context, string, int) (domain.RunDetail, error) {
	return domain.RunDetail{}, nil
}
func (r *awaitRepo) GetAwaitDetail(context.Context, string, int) (domain.AwaitDetail, error) {
	return domain.AwaitDetail{}, nil
}
func (r *awaitRepo) GetDelivery(context.Context, string) (domain.OutboundDelivery, error) {
	return domain.OutboundDelivery{}, nil
}
func (r *awaitRepo) CountSentDeliveriesSince(context.Context, string, time.Time) (int, error) {
	return 0, nil
}
func (r *awaitRepo) HasRecentInboundMessageSince(context.Context, string, time.Time) (bool, error) {
	return true, nil
}
func (r *awaitRepo) GetLatestDeliveryByLogicalMessage(context.Context, string) (*domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *awaitRepo) MarkDeliverySent(context.Context, string, domain.DeliveryResult) error {
	return nil
}
func (r *awaitRepo) MarkDeliverySending(context.Context, string) error       { return nil }
func (r *awaitRepo) MarkDeliveryFailed(context.Context, string, error) error { return nil }
func (r *awaitRepo) ListDeliveries(context.Context, domain.DeliveryListQuery) (domain.PagedResult[domain.OutboundDelivery], error) {
	return domain.PagedResult[domain.OutboundDelivery]{}, nil
}
func (r *awaitRepo) CountMessages(context.Context, domain.MessageListQuery) (int, error) {
	return 0, nil
}
func (r *awaitRepo) CountArtifacts(context.Context, domain.ArtifactListQuery) (int, error) {
	return 0, nil
}
func (r *awaitRepo) ListUsers(context.Context, string, int) ([]domain.User, error) { return nil, nil }
func (r *awaitRepo) UpdateUserPhone(context.Context, string, string, string, string, bool, time.Time) error {
	return nil
}
func (r *awaitRepo) ClearUserPhone(context.Context, string, string) error { return nil }
func (r *awaitRepo) CountLinkedIdentitiesByChannel(context.Context, string) (map[string]int, error) {
	return map[string]int{}, nil
}
func (r *awaitRepo) ListLinkedIdentitiesForUser(context.Context, string, string) ([]domain.LinkedIdentity, error) {
	return nil, nil
}
func (r *awaitRepo) DeleteLinkedIdentity(context.Context, string, string, string) error { return nil }
func (r *awaitRepo) CountDeliveries(context.Context, domain.DeliveryListQuery) (int, error) {
	return 0, nil
}
func (r *awaitRepo) CountSessions(context.Context, domain.SessionListQuery) (int, error) {
	return 0, nil
}
func (r *awaitRepo) CountRuns(context.Context, domain.RunListQuery) (int, error)     { return 0, nil }
func (r *awaitRepo) CountAwaits(context.Context, domain.AwaitListQuery) (int, error) { return 0, nil }
func (r *awaitRepo) ListSessions(context.Context, domain.SessionListQuery) (domain.PagedResult[domain.Session], error) {
	return domain.PagedResult[domain.Session]{}, nil
}
func (r *awaitRepo) ListRuns(context.Context, domain.RunListQuery) (domain.PagedResult[domain.Run], error) {
	return domain.PagedResult[domain.Run]{}, nil
}
func (r *awaitRepo) ListAwaits(context.Context, domain.AwaitListQuery) (domain.PagedResult[domain.Await], error) {
	return domain.PagedResult[domain.Await]{}, nil
}
func (r *awaitRepo) ListAuditEvents(context.Context, domain.AuditEventListQuery) (domain.PagedResult[domain.AuditEvent], error) {
	return domain.PagedResult[domain.AuditEvent]{}, nil
}
func (r *awaitRepo) ListStaleClaimedOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}
func (r *awaitRepo) RequeueOutbox(context.Context, string) error                   { return nil }
func (r *awaitRepo) RequeueQueueStartOutbox(context.Context, string, string) error { return nil }
func (r *awaitRepo) ListStuckQueueItems(context.Context, time.Time, int) ([]domain.QueueItem, error) {
	return nil, nil
}
func (r *awaitRepo) ListStaleRuns(context.Context, time.Time, int) ([]domain.Run, error) {
	return nil, nil
}
func (r *awaitRepo) ListExpiredAwaits(context.Context, time.Time, int) ([]domain.Await, error) {
	return nil, nil
}
func (r *awaitRepo) ListStaleDeliveries(context.Context, time.Time, int, int) ([]domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *awaitRepo) ExpireAwait(context.Context, string) error { return nil }
func (r *awaitRepo) RepairRunFromSnapshot(context.Context, domain.QueueItem, domain.RunStatusSnapshot) (domain.Run, error) {
	return domain.Run{}, nil
}
func (r *awaitRepo) CreateVirtualSession(context.Context, string, string, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *awaitRepo) EnsureNotificationSession(context.Context, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *awaitRepo) SwitchActiveSession(context.Context, string, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *awaitRepo) ListSurfaceSessions(context.Context, string, string, string, string, int) ([]domain.SurfaceSession, error) {
	return nil, nil
}
func (r *awaitRepo) CloseActiveSession(context.Context, string, string, string, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *awaitRepo) IsTelegramUserAllowed(context.Context, string, string) (bool, error) {
	return false, nil
}
func (r *awaitRepo) CountTelegramUserAccess(context.Context, string, string) (int, error) {
	return 0, nil
}
func (r *awaitRepo) ListTelegramUserAccessPage(context.Context, domain.TelegramUserAccessListQuery) (domain.PagedResult[domain.TelegramUserAccess], error) {
	return domain.PagedResult[domain.TelegramUserAccess]{}, nil
}
func (r *awaitRepo) ListTelegramUserAccess(context.Context, string, int) ([]domain.TelegramUserAccess, error) {
	return nil, nil
}
func (r *awaitRepo) ListTelegramUserAccessByStatus(context.Context, string, string, int) ([]domain.TelegramUserAccess, error) {
	return nil, nil
}
func (r *awaitRepo) GetTelegramUserAccess(context.Context, string, string) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *awaitRepo) UpsertTelegramUserAccess(context.Context, domain.TelegramUserAccess) error {
	return nil
}
func (r *awaitRepo) DeleteTelegramUserAccess(context.Context, string, string) error { return nil }
func (r *awaitRepo) RequestTelegramAccess(context.Context, domain.TelegramUserAccess) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *awaitRepo) ResolveTelegramAccessRequest(context.Context, string, string, string, string) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *awaitRepo) CountAuditEvents(context.Context, domain.AuditEventListQuery) (int, error) {
	return 0, nil
}
func (r *awaitRepo) Audit(context.Context, domain.AuditEvent) error              { return nil }
func (r *awaitRepo) UpdateDeliveryPayload(context.Context, string, []byte) error { return nil }
func (r *awaitRepo) RecordWhatsAppInbound(context.Context, string, string, time.Time, time.Duration) error {
	return nil
}
func (r *awaitRepo) GetWhatsAppContactPolicy(context.Context, string, string) (domain.WhatsAppContactPolicy, error) {
	return domain.WhatsAppContactPolicy{}, domain.ErrWhatsAppContactPolicyNotFound
}
func (r *awaitRepo) SetWhatsAppConsentStatus(context.Context, string, string, string, time.Time) error {
	return nil
}
func (r *awaitRepo) RecordWhatsAppTemplateSent(context.Context, string, string, time.Time) error {
	return nil
}
func (r *awaitRepo) RecordWhatsAppPolicyBlocked(context.Context, string, string, time.Time) error {
	return nil
}
func (r *awaitRepo) CountWhatsAppContacts(context.Context, domain.WhatsAppPolicyListQuery) (int, error) {
	return 0, nil
}
func (r *awaitRepo) ListWhatsAppContacts(context.Context, domain.WhatsAppPolicyListQuery) (domain.PagedResult[domain.WhatsAppContactPolicy], error) {
	return domain.PagedResult[domain.WhatsAppContactPolicy]{}, nil
}
func (r *awaitRepo) ForceCancelRun(context.Context, string) error { return nil }
func (r *awaitRepo) RetryDelivery(context.Context, string) error  { return nil }

func TestAwaitServiceHandleResponse(t *testing.T) {
	repo := &awaitRepo{}
	svc := AwaitService{Repo: repo}
	payload, err := json.Marshal(map[string]string{"choice": "prod"})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.HandleResponse(context.Background(), domain.CanonicalInboundEvent{
		TenantID: "tenant_default",
		Sender:   domain.Sender{ChannelUserID: "U123"},
		Metadata: domain.Metadata{
			AwaitID:       "await_run_1",
			ResumePayload: payload,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.resume.AwaitID != "await_run_1" {
		t.Fatalf("expected await resume for await_run_1, got %s", repo.resume.AwaitID)
	}
	if repo.tenantID != "tenant_default" {
		t.Fatalf("expected tenant_default, got %s", repo.tenantID)
	}
}
