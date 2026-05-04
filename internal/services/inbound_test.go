package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type fakeRepo struct {
	receipts   map[string]bool
	sessions   map[string]domain.Session
	active     bool
	queue      []domain.QueueItem
	deliveries []domain.OutboundDelivery
}

type fakeIdentityRepo struct {
	challenges map[string]domain.StepUpChallenge
	identities map[string]domain.LinkedIdentity
}

func (r *fakeIdentityRepo) EnsureUserByEmail(context.Context, string, string) (domain.User, error) {
	return domain.User{}, nil
}
func (r *fakeIdentityRepo) GetUser(context.Context, string, string) (domain.User, error) {
	return domain.User{}, domain.ErrIdentityUserNotFound
}
func (r *fakeIdentityRepo) GetUserByEmail(context.Context, string, string) (domain.User, error) {
	return domain.User{}, domain.ErrIdentityUserNotFound
}
func (r *fakeIdentityRepo) ListUsers(context.Context, string, int) ([]domain.User, error) {
	return nil, nil
}
func (r *fakeIdentityRepo) UpdateUserPhone(context.Context, string, string, string, string, bool, time.Time) error {
	return nil
}
func (r *fakeIdentityRepo) ClearUserPhone(context.Context, string, string) error { return nil }
func (r *fakeIdentityRepo) MarkUserStepUp(context.Context, string, string, time.Time) error {
	return nil
}
func (r *fakeIdentityRepo) HasRecentStepUp(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}
func (r *fakeIdentityRepo) CreateStepUpChallenge(context.Context, domain.StepUpChallenge, time.Duration) error {
	return nil
}
func (r *fakeIdentityRepo) ConsumeStepUpChallenge(_ context.Context, tenantID, userID, purpose, channelType, codeHash, actualChannelUserID string, now time.Time) (domain.StepUpChallenge, error) {
	challenge, ok := r.challenges[tenantID+"|"+userID+"|"+purpose+"|"+channelType+"|"+codeHash]
	if !ok {
		return domain.StepUpChallenge{}, domain.ErrStepUpChallengeNotFound
	}
	if challenge.ExpectedChannelUserID != "" && challenge.ExpectedChannelUserID != actualChannelUserID {
		return domain.StepUpChallenge{}, domain.ErrStepUpChallengeNotFound
	}
	challenge.ConsumedAt = now
	r.challenges[tenantID+"|"+purpose+"|"+channelType+"|"+codeHash] = challenge
	return challenge, nil
}
func (r *fakeIdentityRepo) UpsertLinkedIdentity(_ context.Context, identity domain.LinkedIdentity) error {
	if r.identities == nil {
		r.identities = map[string]domain.LinkedIdentity{}
	}
	r.identities[identity.TenantID+"|"+identity.ChannelType+"|"+identity.ChannelUserID] = identity
	return nil
}
func (r *fakeIdentityRepo) GetLinkedIdentity(context.Context, string, string, string) (domain.LinkedIdentity, error) {
	if r.identities != nil {
		for _, item := range r.identities {
			return item, nil
		}
	}
	return domain.LinkedIdentity{}, domain.ErrLinkedIdentityNotFound
}
func (r *fakeIdentityRepo) ListLinkedIdentitiesForUser(context.Context, string, string) ([]domain.LinkedIdentity, error) {
	return nil, nil
}
func (r *fakeIdentityRepo) DeleteLinkedIdentity(context.Context, string, string, string) error {
	return nil
}
func (r *fakeIdentityRepo) GetTrustPolicy(context.Context, string, string) (domain.TrustPolicy, error) {
	return domain.TrustPolicy{}, domain.ErrTrustPolicyNotFound
}
func (r *fakeIdentityRepo) ListTrustPolicies(context.Context, string, int) ([]domain.TrustPolicy, error) {
	return nil, nil
}
func (r *fakeIdentityRepo) UpsertTrustPolicy(context.Context, domain.TrustPolicy) error { return nil }
func (r *fakeIdentityRepo) CountLinkedIdentitiesByChannel(context.Context, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (r *fakeRepo) InTx(ctx context.Context, fn func(ctx context.Context, repo ports.Repository) error) error {
	return fn(ctx, r)
}
func (r *fakeRepo) RecordInboundReceipt(_ context.Context, evt domain.CanonicalInboundEvent) (bool, error) {
	if r.receipts[evt.ProviderEventID] {
		return false, nil
	}
	r.receipts[evt.ProviderEventID] = true
	return true, nil
}
func (r *fakeRepo) ResolveSession(_ context.Context, evt domain.CanonicalInboundEvent, _ string) (domain.Session, bool, error) {
	if s, ok := r.sessions[evt.Conversation.ChannelSurfaceKey]; ok {
		return s, false, nil
	}
	s := domain.Session{ID: "session_1", TenantID: evt.TenantID, ChannelType: evt.Channel, ChannelScopeKey: evt.Conversation.ChannelSurfaceKey}
	r.sessions[evt.Conversation.ChannelSurfaceKey] = s
	return s, true, nil
}
func (r *fakeRepo) HasActiveRun(context.Context, string) (bool, error) { return r.active, nil }
func (r *fakeRepo) StoreInboundMessage(_ context.Context, evt domain.CanonicalInboundEvent, _ string) (string, error) {
	return evt.Message.MessageID, nil
}
func (r *fakeRepo) StoreOutboundMessage(context.Context, domain.Session, string, string, string, []byte) (string, error) {
	return "msg_out_1", nil
}
func (r *fakeRepo) StoreArtifacts(context.Context, string, string, []domain.Artifact) error {
	return nil
}
func (r *fakeRepo) EnqueueMessage(_ context.Context, evt domain.CanonicalInboundEvent, session domain.Session, _ domain.RouteDecision, inboundMessageID string, _ bool) (domain.QueueItem, *domain.OutboxEvent, error) {
	item := domain.QueueItem{ID: "queue_" + evt.EventID, SessionID: session.ID, InboundMessageID: inboundMessageID}
	r.queue = append(r.queue, item)
	return item, nil, nil
}
func (r *fakeRepo) CreateRun(context.Context, domain.Run) error           { return nil }
func (r *fakeRepo) UpdateRunStatus(context.Context, string, string) error { return nil }
func (r *fakeRepo) UpdateQueueItemStatus(context.Context, string, string) error {
	return nil
}
func (r *fakeRepo) UpdateActiveQueueItemStatus(context.Context, string, string) error { return nil }
func (r *fakeRepo) EnqueueNextQueueItem(context.Context, string) (*domain.OutboxEvent, error) {
	return nil, nil
}
func (r *fakeRepo) StoreAwait(context.Context, domain.Await) error { return nil }
func (r *fakeRepo) ResolveAwait(context.Context, string, string, string, string, []byte) (domain.Await, error) {
	return domain.Await{}, nil
}
func (r *fakeRepo) GetAwait(context.Context, string) (domain.Await, error) {
	return domain.Await{}, nil
}
func (r *fakeRepo) CountSentDeliveriesSince(context.Context, string, time.Time) (int, error) {
	return 0, nil
}
func (r *fakeRepo) HasRecentInboundMessageSince(context.Context, string, time.Time) (bool, error) {
	return true, nil
}
func (r *fakeRepo) EnqueueAwaitResume(context.Context, domain.ResumeRequest, string) error {
	return nil
}
func (r *fakeRepo) EnqueueDelivery(_ context.Context, delivery domain.OutboundDelivery) error {
	r.deliveries = append(r.deliveries, delivery)
	return nil
}
func (r *fakeRepo) ClaimOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}
func (r *fakeRepo) MarkOutboxDone(context.Context, string) error                     { return nil }
func (r *fakeRepo) MarkOutboxFailed(context.Context, string, error, time.Time) error { return nil }
func (r *fakeRepo) GetQueueItem(context.Context, string) (domain.QueueItem, error) {
	return domain.QueueItem{}, nil
}
func (r *fakeRepo) GetQueueStartIdempotencyKey(context.Context, string) (string, error) {
	return "", nil
}
func (r *fakeRepo) GetSession(context.Context, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (r *fakeRepo) UpdateSessionACPSessionID(context.Context, string, string) error { return nil }
func (r *fakeRepo) GetRouteDecision(context.Context, string) (domain.RouteDecision, error) {
	return domain.RouteDecision{}, nil
}
func (r *fakeRepo) GetTrustPolicy(context.Context, string, string) (domain.TrustPolicy, error) {
	return domain.TrustPolicy{}, domain.ErrTrustPolicyNotFound
}
func (r *fakeRepo) ListTrustPolicies(context.Context, string, int) ([]domain.TrustPolicy, error) {
	return nil, nil
}
func (r *fakeRepo) UpsertTrustPolicy(context.Context, domain.TrustPolicy) error { return nil }
func (r *fakeRepo) GetInboundMessage(context.Context, string) (domain.Message, error) {
	return domain.Message{}, nil
}
func (r *fakeRepo) GetRun(context.Context, string) (domain.Run, error) { return domain.Run{}, nil }
func (r *fakeRepo) GetRunByACP(context.Context, string) (domain.Run, error) {
	return domain.Run{}, nil
}
func (r *fakeRepo) GetAwaitsForRun(context.Context, string, int) ([]domain.Await, error) {
	return nil, nil
}
func (r *fakeRepo) GetAwaitResponses(context.Context, string, int) ([]domain.AwaitResponse, error) {
	return nil, nil
}
func (r *fakeRepo) ListMessages(context.Context, domain.MessageListQuery) (domain.PagedResult[domain.Message], error) {
	return domain.PagedResult[domain.Message]{}, nil
}
func (r *fakeRepo) ListArtifacts(context.Context, domain.ArtifactListQuery) (domain.PagedResult[domain.Artifact], error) {
	return domain.PagedResult[domain.Artifact]{}, nil
}
func (r *fakeRepo) GetSessionDetail(context.Context, string, int) (domain.SessionDetail, error) {
	return domain.SessionDetail{}, nil
}
func (r *fakeRepo) GetRunDetail(context.Context, string, int) (domain.RunDetail, error) {
	return domain.RunDetail{}, nil
}
func (r *fakeRepo) GetAwaitDetail(context.Context, string, int) (domain.AwaitDetail, error) {
	return domain.AwaitDetail{}, nil
}
func (r *fakeRepo) GetDelivery(context.Context, string) (domain.OutboundDelivery, error) {
	return domain.OutboundDelivery{}, nil
}
func (r *fakeRepo) GetLatestDeliveryByLogicalMessage(context.Context, string) (*domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *fakeRepo) MarkDeliverySent(context.Context, string, domain.DeliveryResult) error { return nil }
func (r *fakeRepo) MarkDeliverySending(context.Context, string) error                     { return nil }
func (r *fakeRepo) MarkDeliveryFailed(context.Context, string, error) error               { return nil }
func (r *fakeRepo) ListDeliveries(context.Context, domain.DeliveryListQuery) (domain.PagedResult[domain.OutboundDelivery], error) {
	return domain.PagedResult[domain.OutboundDelivery]{}, nil
}
func (r *fakeRepo) CountMessages(context.Context, domain.MessageListQuery) (int, error) {
	return 0, nil
}
func (r *fakeRepo) CountArtifacts(context.Context, domain.ArtifactListQuery) (int, error) {
	return 0, nil
}
func (r *fakeRepo) CountDeliveries(context.Context, domain.DeliveryListQuery) (int, error) {
	return 0, nil
}
func (r *fakeRepo) CountSessions(context.Context, domain.SessionListQuery) (int, error) {
	return 0, nil
}
func (r *fakeRepo) CountRuns(context.Context, domain.RunListQuery) (int, error)     { return 0, nil }
func (r *fakeRepo) CountAwaits(context.Context, domain.AwaitListQuery) (int, error) { return 0, nil }
func (r *fakeRepo) ListSessions(context.Context, domain.SessionListQuery) (domain.PagedResult[domain.Session], error) {
	return domain.PagedResult[domain.Session]{}, nil
}
func (r *fakeRepo) ListRuns(context.Context, domain.RunListQuery) (domain.PagedResult[domain.Run], error) {
	return domain.PagedResult[domain.Run]{}, nil
}
func (r *fakeRepo) ListAwaits(context.Context, domain.AwaitListQuery) (domain.PagedResult[domain.Await], error) {
	return domain.PagedResult[domain.Await]{}, nil
}
func (r *fakeRepo) ListAuditEvents(context.Context, domain.AuditEventListQuery) (domain.PagedResult[domain.AuditEvent], error) {
	return domain.PagedResult[domain.AuditEvent]{}, nil
}
func (r *fakeRepo) ListStaleClaimedOutbox(context.Context, time.Time, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}
func (r *fakeRepo) RequeueOutbox(context.Context, string) error                   { return nil }
func (r *fakeRepo) RequeueQueueStartOutbox(context.Context, string, string) error { return nil }
func (r *fakeRepo) ListStuckQueueItems(context.Context, time.Time, int) ([]domain.QueueItem, error) {
	return nil, nil
}
func (r *fakeRepo) ListStaleRuns(context.Context, time.Time, int) ([]domain.Run, error) {
	return nil, nil
}
func (r *fakeRepo) ListExpiredAwaits(context.Context, time.Time, int) ([]domain.Await, error) {
	return nil, nil
}
func (r *fakeRepo) ListStaleDeliveries(context.Context, time.Time, int, int) ([]domain.OutboundDelivery, error) {
	return nil, nil
}
func (r *fakeRepo) ExpireAwait(context.Context, string) error { return nil }
func (r *fakeRepo) RepairRunFromSnapshot(context.Context, domain.QueueItem, domain.RunStatusSnapshot) (domain.Run, error) {
	return domain.Run{}, nil
}
func (r *fakeRepo) CreateVirtualSession(_ context.Context, tenantID, channelType, surfaceKey, ownerUserID, agentProfileID, alias string) (domain.Session, error) {
	s := domain.Session{ID: "session_virtual", TenantID: tenantID, OwnerUserID: ownerUserID, AgentProfileID: agentProfileID, ChannelType: channelType, ChannelScopeKey: surfaceKey + ":session_virtual", State: "open"}
	r.sessions[s.ChannelScopeKey] = s
	return s, nil
}
func (r *fakeRepo) EnsureNotificationSession(context.Context, string, string, string, string) (domain.Session, error) {
	return domain.Session{ID: "session_notice", State: "open"}, nil
}
func (r *fakeRepo) SwitchActiveSession(context.Context, string, string, string, string, string) (domain.Session, error) {
	return domain.Session{ID: "session_switched", State: "open"}, nil
}
func (r *fakeRepo) ListSurfaceSessions(context.Context, string, string, string, string, int) ([]domain.SurfaceSession, error) {
	return []domain.SurfaceSession{{Session: domain.Session{ID: "session_virtual", State: "open"}, Alias: "deploy"}}, nil
}
func (r *fakeRepo) CloseActiveSession(context.Context, string, string, string, string) (domain.Session, error) {
	return domain.Session{ID: "session_virtual", State: "archived"}, nil
}
func (r *fakeRepo) IsTelegramUserAllowed(context.Context, string, string) (bool, error) {
	return false, nil
}
func (r *fakeRepo) CountTelegramUserAccess(context.Context, string, string) (int, error) {
	return 0, nil
}
func (r *fakeRepo) ListTelegramUserAccessPage(context.Context, domain.TelegramUserAccessListQuery) (domain.PagedResult[domain.TelegramUserAccess], error) {
	return domain.PagedResult[domain.TelegramUserAccess]{}, nil
}
func (r *fakeRepo) ListTelegramUserAccess(context.Context, string, int) ([]domain.TelegramUserAccess, error) {
	return nil, nil
}
func (r *fakeRepo) ListTelegramUserAccessByStatus(context.Context, string, string, int) ([]domain.TelegramUserAccess, error) {
	return nil, nil
}
func (r *fakeRepo) GetTelegramUserAccess(context.Context, string, string) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *fakeRepo) UpsertTelegramUserAccess(context.Context, domain.TelegramUserAccess) error {
	return nil
}
func (r *fakeRepo) DeleteTelegramUserAccess(context.Context, string, string) error { return nil }
func (r *fakeRepo) ListUsers(context.Context, string, int) ([]domain.User, error)  { return nil, nil }
func (r *fakeRepo) UpdateUserPhone(context.Context, string, string, string, string, bool, time.Time) error {
	return nil
}
func (r *fakeRepo) ClearUserPhone(context.Context, string, string) error { return nil }
func (r *fakeRepo) CountLinkedIdentitiesByChannel(context.Context, string) (map[string]int, error) {
	return map[string]int{}, nil
}
func (r *fakeRepo) ListLinkedIdentitiesForUser(context.Context, string, string) ([]domain.LinkedIdentity, error) {
	return nil, nil
}
func (r *fakeRepo) DeleteLinkedIdentity(context.Context, string, string, string) error { return nil }
func (r *fakeRepo) RequestTelegramAccess(context.Context, domain.TelegramUserAccess) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *fakeRepo) ResolveTelegramAccessRequest(context.Context, string, string, string, string) (domain.TelegramUserAccess, error) {
	return domain.TelegramUserAccess{}, nil
}
func (r *fakeRepo) CountAuditEvents(context.Context, domain.AuditEventListQuery) (int, error) {
	return 0, nil
}
func (r *fakeRepo) Audit(context.Context, domain.AuditEvent) error              { return nil }
func (r *fakeRepo) UpdateDeliveryPayload(context.Context, string, []byte) error { return nil }
func (r *fakeRepo) RecordWhatsAppInbound(context.Context, string, string, time.Time, time.Duration) error {
	return nil
}
func (r *fakeRepo) GetWhatsAppContactPolicy(context.Context, string, string) (domain.WhatsAppContactPolicy, error) {
	return domain.WhatsAppContactPolicy{}, domain.ErrWhatsAppContactPolicyNotFound
}
func (r *fakeRepo) SetWhatsAppConsentStatus(context.Context, string, string, string, time.Time) error {
	return nil
}
func (r *fakeRepo) RecordWhatsAppTemplateSent(context.Context, string, string, time.Time) error {
	return nil
}
func (r *fakeRepo) RecordWhatsAppPolicyBlocked(context.Context, string, string, time.Time) error {
	return nil
}
func (r *fakeRepo) CountWhatsAppContacts(context.Context, domain.WhatsAppPolicyListQuery) (int, error) {
	return 0, nil
}
func (r *fakeRepo) ListWhatsAppContacts(context.Context, domain.WhatsAppPolicyListQuery) (domain.PagedResult[domain.WhatsAppContactPolicy], error) {
	return domain.PagedResult[domain.WhatsAppContactPolicy]{}, nil
}
func (r *fakeRepo) ForceCancelRun(context.Context, string) error { return nil }
func (r *fakeRepo) RetryDelivery(context.Context, string) error  { return nil }

func TestInboundServiceQueuesMessage(t *testing.T) {
	repo := &fakeRepo{
		receipts: map[string]bool{},
		sessions: map[string]domain.Session{},
		active:   true,
	}
	svc := InboundService{
		Repo: repo,
		Router: StaticRouter{
			DefaultAgentProfileID: "agent_profile_default",
		},
	}
	raw, _ := json.Marshal(map[string]string{"ok": "true"})
	result, err := svc.Handle(context.Background(), domain.CanonicalInboundEvent{
		EventID:         "evt_1",
		TenantID:        "tenant_default",
		Channel:         "slack",
		ProviderEventID: "provider_1",
		ReceivedAt:      time.Now(),
		Conversation:    domain.Conversation{ChannelSurfaceKey: "C1:T1"},
		Message:         domain.Message{MessageID: "msg_1", Text: "hello"},
		Metadata:        domain.Metadata{RawPayload: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "queued" {
		t.Fatalf("expected queued, got %s", result.Status)
	}
	if len(repo.queue) != 1 {
		t.Fatalf("expected one queue item, got %d", len(repo.queue))
	}
}

func TestInboundServiceHandlesTelegramSessionListCommand(t *testing.T) {
	repo := &fakeRepo{
		receipts: map[string]bool{},
		sessions: map[string]domain.Session{
			"123:session_virtual": {ID: "session_virtual", TenantID: "tenant_default", ChannelType: "telegram", ChannelScopeKey: "123:session_virtual"},
		},
	}
	svc := InboundService{
		Repo: repo,
		Router: StaticRouter{
			DefaultAgentProfileID: "agent_profile_default",
		},
	}
	raw, _ := json.Marshal(map[string]string{"ok": "true"})
	result, err := svc.Handle(context.Background(), domain.CanonicalInboundEvent{
		EventID:         "evt_tg_1",
		TenantID:        "tenant_default",
		Channel:         "telegram",
		ProviderEventID: "provider_tg_1",
		ReceivedAt:      time.Now(),
		Sender:          domain.Sender{ChannelUserID: "user1"},
		Conversation:    domain.Conversation{ChannelConversationID: "123", ChannelThreadID: "123", ChannelSurfaceKey: "123"},
		Message:         domain.Message{MessageID: "msg_tg_1", Text: "/sessions"},
		Metadata:        domain.Metadata{RawPayload: raw, Command: "/sessions"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "accepted" {
		t.Fatalf("expected accepted, got %s", result.Status)
	}
	if len(repo.queue) != 0 {
		t.Fatalf("expected no queue items for command, got %d", len(repo.queue))
	}
	if len(repo.deliveries) != 1 {
		t.Fatalf("expected one control delivery, got %d", len(repo.deliveries))
	}
}

func TestInboundServiceLinksIdentityFromCommand(t *testing.T) {
	repo := &fakeRepo{
		receipts: map[string]bool{},
		sessions: map[string]domain.Session{},
	}
	identity := &fakeIdentityRepo{
		challenges: map[string]domain.StepUpChallenge{
			"tenant_default|user_1|link|telegram|" + sha256Hex("12345678"): {
				ID:          "challenge_1",
				TenantID:    "tenant_default",
				UserID:      "user_1",
				Purpose:     "link",
				ChannelType: "telegram",
				CodeHash:    sha256Hex("12345678"),
				ExpiresAt:   time.Now().UTC().Add(5 * time.Minute),
			},
		},
	}
	svc := InboundService{
		Repo:     repo,
		Identity: identity,
		Router: StaticRouter{
			DefaultAgentProfileID: "agent_profile_default",
		},
	}
	raw, _ := json.Marshal(map[string]string{"ok": "true"})
	result, err := svc.Handle(context.Background(), domain.CanonicalInboundEvent{
		EventID:         "evt_link_1",
		TenantID:        "tenant_default",
		Channel:         "telegram",
		ProviderEventID: "provider_link_1",
		ReceivedAt:      time.Now(),
		Sender:          domain.Sender{ChannelUserID: "tg_user_1"},
		Conversation:    domain.Conversation{ChannelSurfaceKey: "tg_surface_1"},
		Message:         domain.Message{MessageID: "msg_link_1", Text: "link user_1.12345678"},
		Metadata:        domain.Metadata{RawPayload: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "identity_linked" {
		t.Fatalf("expected identity_linked, got %s", result.Status)
	}
	linked, ok := identity.identities["tenant_default|telegram|tg_user_1"]
	if !ok || linked.UserID != "user_1" {
		t.Fatalf("expected linked identity, got %+v", identity.identities)
	}
	if len(repo.queue) != 0 {
		t.Fatalf("expected no queued items, got %d", len(repo.queue))
	}
}

func TestInboundServiceLinksRelatedWhatsAppWebIdentity(t *testing.T) {
	repo := &fakeRepo{
		receipts: map[string]bool{},
		sessions: map[string]domain.Session{},
	}
	identity := &fakeIdentityRepo{
		challenges: map[string]domain.StepUpChallenge{
			"tenant_default|user_1|link|whatsapp_web|" + sha256Hex("12345678"): {
				ID:          "challenge_1",
				TenantID:    "tenant_default",
				UserID:      "user_1",
				Purpose:     "link",
				ChannelType: "whatsapp_web",
				CodeHash:    sha256Hex("12345678"),
				ExpiresAt:   time.Now().UTC().Add(5 * time.Minute),
			},
		},
	}
	svc := InboundService{
		Repo:     repo,
		Identity: identity,
		Router:   StaticRouter{DefaultAgentProfileID: "agent_profile_default"},
	}
	raw, _ := json.Marshal(map[string]string{"ok": "true"})
	result, err := svc.Handle(context.Background(), domain.CanonicalInboundEvent{
		EventID:         "evt_link_wa_1",
		TenantID:        "tenant_default",
		Channel:         "whatsapp_web",
		ProviderEventID: "provider_link_wa_1",
		ReceivedAt:      time.Now(),
		Sender:          domain.Sender{ChannelUserID: "+62 812-3456-7890"},
		Conversation:    domain.Conversation{ChannelSurfaceKey: "wa_surface_1"},
		Message:         domain.Message{MessageID: "msg_link_wa_1", Text: "link user_1.12345678"},
		Metadata:        domain.Metadata{RawPayload: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "identity_linked" {
		t.Fatalf("expected identity_linked, got %s", result.Status)
	}
	for _, channel := range []string{"whatsapp", "whatsapp_web"} {
		linked, ok := identity.identities["tenant_default|"+channel+"|6281234567890"]
		if !ok || linked.UserID != "user_1" {
			t.Fatalf("expected %s linked identity, got %+v", channel, identity.identities)
		}
	}
	if len(repo.queue) != 0 {
		t.Fatalf("expected no queued items, got %d", len(repo.queue))
	}
}

func TestInboundServiceConsumesWhatsAppCodeFromWhatsAppWeb(t *testing.T) {
	repo := &fakeRepo{
		receipts: map[string]bool{},
		sessions: map[string]domain.Session{},
	}
	identity := &fakeIdentityRepo{
		challenges: map[string]domain.StepUpChallenge{
			"tenant_default|user_1|link|whatsapp|" + sha256Hex("12345678"): {
				ID:                    "challenge_1",
				TenantID:              "tenant_default",
				UserID:                "user_1",
				Purpose:               "link",
				ChannelType:           "whatsapp",
				ExpectedChannelUserID: "6281234567890",
				CodeHash:              sha256Hex("12345678"),
				ExpiresAt:             time.Now().UTC().Add(5 * time.Minute),
			},
		},
	}
	svc := InboundService{
		Repo:     repo,
		Identity: identity,
		Router:   StaticRouter{DefaultAgentProfileID: "agent_profile_default"},
	}
	raw, _ := json.Marshal(map[string]string{"ok": "true"})
	result, err := svc.Handle(context.Background(), domain.CanonicalInboundEvent{
		EventID:         "evt_link_wa_cross",
		TenantID:        "tenant_default",
		Channel:         "whatsapp_web",
		ProviderEventID: "provider_link_wa_cross",
		ReceivedAt:      time.Now(),
		Sender:          domain.Sender{ChannelUserID: "+62 812-3456-7890"},
		Conversation:    domain.Conversation{ChannelSurfaceKey: "wa_surface_1"},
		Message:         domain.Message{MessageID: "msg_link_wa_cross", Text: "link user_1.12345678"},
		Metadata:        domain.Metadata{RawPayload: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "identity_linked" {
		t.Fatalf("expected identity_linked, got %s", result.Status)
	}
	for _, channel := range []string{"whatsapp", "whatsapp_web"} {
		linked, ok := identity.identities["tenant_default|"+channel+"|6281234567890"]
		if !ok || linked.UserID != "user_1" {
			t.Fatalf("expected %s linked identity, got %+v", channel, identity.identities)
		}
	}
}

func TestInboundServiceRejectsWhatsAppLinkWhenExpectedPhoneDiffers(t *testing.T) {
	repo := &fakeRepo{
		receipts: map[string]bool{},
		sessions: map[string]domain.Session{},
	}
	identity := &fakeIdentityRepo{
		challenges: map[string]domain.StepUpChallenge{
			"tenant_default|user_1|link|whatsapp_web|" + sha256Hex("12345678"): {
				ID:                    "challenge_1",
				TenantID:              "tenant_default",
				UserID:                "user_1",
				Purpose:               "link",
				ChannelType:           "whatsapp_web",
				ExpectedChannelUserID: "628111111111",
				CodeHash:              sha256Hex("12345678"),
				ExpiresAt:             time.Now().UTC().Add(5 * time.Minute),
			},
		},
	}
	svc := InboundService{
		Repo:     repo,
		Identity: identity,
		Router:   StaticRouter{DefaultAgentProfileID: "agent_profile_default"},
	}
	raw, _ := json.Marshal(map[string]string{"ok": "true"})
	_, err := svc.Handle(context.Background(), domain.CanonicalInboundEvent{
		EventID:         "evt_link_wa_mismatch",
		TenantID:        "tenant_default",
		Channel:         "whatsapp_web",
		ProviderEventID: "provider_link_wa_mismatch",
		ReceivedAt:      time.Now(),
		Sender:          domain.Sender{ChannelUserID: "+62 812-3456-7890"},
		Conversation:    domain.Conversation{ChannelSurfaceKey: "wa_surface_1"},
		Message:         domain.Message{MessageID: "msg_link_wa_mismatch", Text: "link user_1.12345678"},
		Metadata:        domain.Metadata{RawPayload: raw},
	})
	if !errors.Is(err, domain.ErrStepUpChallengeNotFound) {
		t.Fatalf("expected challenge not found, got %v", err)
	}
	if len(identity.identities) != 0 {
		t.Fatalf("expected no linked identity, got %+v", identity.identities)
	}
	if challenge := identity.challenges["tenant_default|user_1|link|whatsapp_web|"+sha256Hex("12345678")]; !challenge.ConsumedAt.IsZero() {
		t.Fatalf("expected mismatch not to consume challenge, got %+v", challenge)
	}
}

func TestInboundServiceRequiresPreLinkedIdentityForExecution(t *testing.T) {
	repo := &fakeRepo{
		receipts: map[string]bool{},
		sessions: map[string]domain.Session{},
	}
	identity := &fakeIdentityRepo{}
	svc := InboundService{
		Repo:     repo,
		Identity: identity,
		Router: StaticRouter{
			DefaultAgentProfileID: "agent_profile_default",
			FallbackPolicy: domain.TrustPolicy{
				TenantID:                          "tenant_default",
				AgentProfileID:                    "agent_profile_default",
				RequireLinkedIdentityForExecution: true,
			},
		},
	}
	raw, _ := json.Marshal(map[string]string{"ok": "true"})
	_, err := svc.Handle(context.Background(), domain.CanonicalInboundEvent{
		EventID:         "evt_exec_link_required",
		TenantID:        "tenant_default",
		Channel:         "webchat",
		ProviderEventID: "provider_exec_link_required",
		ReceivedAt:      time.Now(),
		Sender:          domain.Sender{ChannelUserID: "user@example.com"},
		Conversation:    domain.Conversation{ChannelSurfaceKey: "webchat_surface_1"},
		Message:         domain.Message{MessageID: "msg_exec_link_required", Text: "hello"},
		Metadata:        domain.Metadata{RawPayload: raw},
	})
	if err == nil || !errors.Is(err, domain.ErrLinkedIdentityRequired) {
		t.Fatalf("expected ErrLinkedIdentityRequired, got %v", err)
	}
}
