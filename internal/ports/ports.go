package ports

import (
	"context"
	"net/http"
	"time"

	"nexus/internal/domain"
)

type ChannelAdapter interface {
	Channel() string
	VerifyInbound(ctx context.Context, r *http.Request, body []byte) error
	ParseInbound(ctx context.Context, r *http.Request, body []byte, tenantID string) (domain.CanonicalInboundEvent, error)
	SendMessage(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error)
	SendAwaitPrompt(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error)
}

type InboundArtifactStore interface {
	SaveInbound(ctx context.Context, filename, mimeType string, content []byte) (domain.Artifact, error)
}

type InboundArtifactHydrator interface {
	HydrateInboundArtifacts(ctx context.Context, evt *domain.CanonicalInboundEvent, store InboundArtifactStore) error
}

type BatchInboundParser interface {
	ParseInboundBatch(ctx context.Context, r *http.Request, body []byte, tenantID string) ([]domain.CanonicalInboundEvent, error)
}

type WebAuthRepository interface {
	CreateWebAuthChallenge(ctx context.Context, challenge domain.WebAuthChallenge, minInterval time.Duration) error
	ConsumeWebAuthChallengeByOTP(ctx context.Context, tenantID, email, otpHash string, now time.Time) (domain.WebAuthChallenge, error)
	ConsumeWebAuthChallengeByLink(ctx context.Context, tenantID, linkTokenHash string, now time.Time) (domain.WebAuthChallenge, error)
	CreateWebAuthSession(ctx context.Context, session domain.WebAuthSession) error
	GetWebAuthSession(ctx context.Context, sessionID string, now time.Time) (domain.WebAuthSession, error)
	UpdateWebAuthSessionCSRFHash(ctx context.Context, sessionID, csrfHash string, now time.Time) error
	DeleteWebAuthSession(ctx context.Context, sessionID string) error
}

type IdentityRepository interface {
	EnsureUserByEmail(ctx context.Context, tenantID, email string) (domain.User, error)
	GetUser(ctx context.Context, tenantID, userID string) (domain.User, error)
	GetUserByEmail(ctx context.Context, tenantID, email string) (domain.User, error)
	ListUsers(ctx context.Context, tenantID string, limit int) ([]domain.User, error)
	UpdateUserPhone(ctx context.Context, tenantID, userID, rawPhone, normalizedPhone string, verified bool, addedAt time.Time) error
	ClearUserPhone(ctx context.Context, tenantID, userID string) error
	MarkUserStepUp(ctx context.Context, tenantID, userID string, at time.Time) error
	HasRecentStepUp(ctx context.Context, tenantID, userID string, since time.Time) (bool, error)
	CreateStepUpChallenge(ctx context.Context, challenge domain.StepUpChallenge, minInterval time.Duration) error
	ConsumeStepUpChallenge(ctx context.Context, tenantID, userID, purpose, channelType, codeHash string, now time.Time) (domain.StepUpChallenge, error)
	UpsertLinkedIdentity(ctx context.Context, identity domain.LinkedIdentity) error
	GetLinkedIdentity(ctx context.Context, tenantID, channelType, channelUserID string) (domain.LinkedIdentity, error)
	ListLinkedIdentitiesForUser(ctx context.Context, tenantID, userID string) ([]domain.LinkedIdentity, error)
	DeleteLinkedIdentity(ctx context.Context, tenantID, channelType, channelUserID string) error
	GetTrustPolicy(ctx context.Context, tenantID, agentProfileID string) (domain.TrustPolicy, error)
	ListTrustPolicies(ctx context.Context, tenantID string, limit int) ([]domain.TrustPolicy, error)
	UpsertTrustPolicy(ctx context.Context, policy domain.TrustPolicy) error
	CountLinkedIdentitiesByChannel(ctx context.Context, tenantID string) (map[string]int, error)
}

type ACPBridge interface {
	DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error)
	EnsureSession(ctx context.Context, session domain.Session) (string, error)
	StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, domain.RunEventStream, error)
	ResumeRun(ctx context.Context, await domain.Await, payload []byte) (domain.RunEventStream, error)
	GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error)
	FindRunByIdempotencyKey(ctx context.Context, session domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error)
	FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error)
	CancelRun(ctx context.Context, run domain.Run) error
}

type RetentionRepository interface {
	WithRetentionLock(ctx context.Context, fn func(ctx context.Context, repo RetentionRepository) error) error
	ListRetentionTenantIDs(ctx context.Context) ([]string, error)
	GetRetentionPolicy(ctx context.Context, tenantID string) (domain.RetentionPolicy, error)
	UpsertRetentionPolicy(ctx context.Context, policy domain.RetentionPolicy) error
	DeleteRetentionPolicy(ctx context.Context, tenantID string) error
	CountRetentionCandidates(ctx context.Context, tenantID string, cutoffs domain.RetentionCutoffs) (domain.RetentionCounts, error)
	ApplyRetention(ctx context.Context, tenantID string, cutoffs domain.RetentionCutoffs, batchSize int, deleteBlob func(context.Context, string) error) (domain.RetentionCounts, error)
	Audit(ctx context.Context, event domain.AuditEvent) error
}

type Repository interface {
	InTx(ctx context.Context, fn func(ctx context.Context, repo Repository) error) error
	RecordInboundReceipt(ctx context.Context, evt domain.CanonicalInboundEvent) (bool, error)
	ResolveSession(ctx context.Context, evt domain.CanonicalInboundEvent, agentProfileID string) (domain.Session, bool, error)
	HasActiveRun(ctx context.Context, sessionID string) (bool, error)
	StoreInboundMessage(ctx context.Context, evt domain.CanonicalInboundEvent, sessionID string) (string, error)
	StoreOutboundMessage(ctx context.Context, session domain.Session, runID string, messageKey string, text string, rawPayload []byte) (string, error)
	StoreArtifacts(ctx context.Context, messageID string, direction string, artifacts []domain.Artifact) error
	EnqueueMessage(ctx context.Context, evt domain.CanonicalInboundEvent, session domain.Session, route domain.RouteDecision, inboundMessageID string, startNow bool) (domain.QueueItem, *domain.OutboxEvent, error)
	CreateRun(ctx context.Context, run domain.Run) error
	UpdateRunStatus(ctx context.Context, runID, status string) error
	UpdateQueueItemStatus(ctx context.Context, queueItemID, status string) error
	UpdateActiveQueueItemStatus(ctx context.Context, sessionID, status string) error
	EnqueueNextQueueItem(ctx context.Context, sessionID string) (*domain.OutboxEvent, error)
	StoreAwait(ctx context.Context, await domain.Await) error
	ResolveAwait(ctx context.Context, awaitID string, actorChannelID, actorUserID, actorIdentityAssurance string, payload []byte) (domain.Await, error)
	GetAwait(ctx context.Context, awaitID string) (domain.Await, error)
	EnqueueAwaitResume(ctx context.Context, req domain.ResumeRequest, tenantID string) error
	EnqueueDelivery(ctx context.Context, delivery domain.OutboundDelivery) error
	ClaimOutbox(ctx context.Context, now time.Time, limit int) ([]domain.OutboxEvent, error)
	MarkOutboxDone(ctx context.Context, eventID string) error
	MarkOutboxFailed(ctx context.Context, eventID string, err error, next time.Time) error
	GetQueueItem(ctx context.Context, queueItemID string) (domain.QueueItem, error)
	GetQueueStartIdempotencyKey(ctx context.Context, queueItemID string) (string, error)
	GetSession(ctx context.Context, sessionID string) (domain.Session, error)
	UpdateSessionACPSessionID(ctx context.Context, sessionID, acpSessionID string) error
	GetRouteDecision(ctx context.Context, queueItemID string) (domain.RouteDecision, error)
	GetTrustPolicy(ctx context.Context, tenantID, agentProfileID string) (domain.TrustPolicy, error)
	ListTrustPolicies(ctx context.Context, tenantID string, limit int) ([]domain.TrustPolicy, error)
	UpsertTrustPolicy(ctx context.Context, policy domain.TrustPolicy) error
	GetInboundMessage(ctx context.Context, messageID string) (domain.Message, error)
	GetDelivery(ctx context.Context, deliveryID string) (domain.OutboundDelivery, error)
	GetLatestDeliveryByLogicalMessage(ctx context.Context, logicalMessageID string) (*domain.OutboundDelivery, error)
	CountSentDeliveriesSince(ctx context.Context, sessionID string, since time.Time) (int, error)
	HasRecentInboundMessageSince(ctx context.Context, sessionID string, since time.Time) (bool, error)
	GetRun(ctx context.Context, runID string) (domain.Run, error)
	GetRunByACP(ctx context.Context, acpRunID string) (domain.Run, error)
	GetAwaitsForRun(ctx context.Context, runID string, limit int) ([]domain.Await, error)
	GetAwaitResponses(ctx context.Context, awaitID string, limit int) ([]domain.AwaitResponse, error)
	MarkDeliverySent(ctx context.Context, deliveryID string, result domain.DeliveryResult) error
	MarkDeliverySending(ctx context.Context, deliveryID string) error
	MarkDeliveryFailed(ctx context.Context, deliveryID string, err error) error
	CountMessages(ctx context.Context, query domain.MessageListQuery) (int, error)
	ListMessages(ctx context.Context, query domain.MessageListQuery) (domain.PagedResult[domain.Message], error)
	CountArtifacts(ctx context.Context, query domain.ArtifactListQuery) (int, error)
	ListArtifacts(ctx context.Context, query domain.ArtifactListQuery) (domain.PagedResult[domain.Artifact], error)
	CountDeliveries(ctx context.Context, query domain.DeliveryListQuery) (int, error)
	ListDeliveries(ctx context.Context, query domain.DeliveryListQuery) (domain.PagedResult[domain.OutboundDelivery], error)
	CountSessions(ctx context.Context, query domain.SessionListQuery) (int, error)
	ListSessions(ctx context.Context, query domain.SessionListQuery) (domain.PagedResult[domain.Session], error)
	CountRuns(ctx context.Context, query domain.RunListQuery) (int, error)
	ListRuns(ctx context.Context, query domain.RunListQuery) (domain.PagedResult[domain.Run], error)
	CountAwaits(ctx context.Context, query domain.AwaitListQuery) (int, error)
	ListAwaits(ctx context.Context, query domain.AwaitListQuery) (domain.PagedResult[domain.Await], error)
	ListAuditEvents(ctx context.Context, query domain.AuditEventListQuery) (domain.PagedResult[domain.AuditEvent], error)
	GetSessionDetail(ctx context.Context, sessionID string, limit int) (domain.SessionDetail, error)
	GetRunDetail(ctx context.Context, runID string, limit int) (domain.RunDetail, error)
	GetAwaitDetail(ctx context.Context, awaitID string, limit int) (domain.AwaitDetail, error)
	ListStaleClaimedOutbox(ctx context.Context, before time.Time, limit int) ([]domain.OutboxEvent, error)
	RequeueOutbox(ctx context.Context, eventID string) error
	RequeueQueueStartOutbox(ctx context.Context, queueItemID, tenantID string) error
	ListStuckQueueItems(ctx context.Context, before time.Time, limit int) ([]domain.QueueItem, error)
	ListStaleRuns(ctx context.Context, before time.Time, limit int) ([]domain.Run, error)
	ListExpiredAwaits(ctx context.Context, before time.Time, limit int) ([]domain.Await, error)
	ListStaleDeliveries(ctx context.Context, before time.Time, maxAttempts int, limit int) ([]domain.OutboundDelivery, error)
	ExpireAwait(ctx context.Context, awaitID string) error
	RepairRunFromSnapshot(ctx context.Context, queueItem domain.QueueItem, snapshot domain.RunStatusSnapshot) (domain.Run, error)
	CreateVirtualSession(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID, agentProfileID, alias string) (domain.Session, error)
	EnsureNotificationSession(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string) (domain.Session, error)
	SwitchActiveSession(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID, aliasOrID string) (domain.Session, error)
	ListSurfaceSessions(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string, limit int) ([]domain.SurfaceSession, error)
	CloseActiveSession(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string) (domain.Session, error)
	IsTelegramUserAllowed(ctx context.Context, tenantID, telegramUserID string) (bool, error)
	CountTelegramUserAccess(ctx context.Context, tenantID, status string) (int, error)
	ListTelegramUserAccessPage(ctx context.Context, query domain.TelegramUserAccessListQuery) (domain.PagedResult[domain.TelegramUserAccess], error)
	ListTelegramUserAccess(ctx context.Context, tenantID string, limit int) ([]domain.TelegramUserAccess, error)
	ListTelegramUserAccessByStatus(ctx context.Context, tenantID, status string, limit int) ([]domain.TelegramUserAccess, error)
	GetTelegramUserAccess(ctx context.Context, tenantID, telegramUserID string) (domain.TelegramUserAccess, error)
	UpsertTelegramUserAccess(ctx context.Context, entry domain.TelegramUserAccess) error
	DeleteTelegramUserAccess(ctx context.Context, tenantID, telegramUserID string) error
	ListUsers(ctx context.Context, tenantID string, limit int) ([]domain.User, error)
	UpdateUserPhone(ctx context.Context, tenantID, userID, rawPhone, normalizedPhone string, verified bool, addedAt time.Time) error
	ClearUserPhone(ctx context.Context, tenantID, userID string) error
	CountLinkedIdentitiesByChannel(ctx context.Context, tenantID string) (map[string]int, error)
	RequestTelegramAccess(ctx context.Context, entry domain.TelegramUserAccess) (domain.TelegramUserAccess, error)
	ResolveTelegramAccessRequest(ctx context.Context, tenantID, telegramUserID, status, addedBy string) (domain.TelegramUserAccess, error)
	ListLinkedIdentitiesForUser(ctx context.Context, tenantID, userID string) ([]domain.LinkedIdentity, error)
	DeleteLinkedIdentity(ctx context.Context, tenantID, channelType, channelUserID string) error
	CountAuditEvents(ctx context.Context, query domain.AuditEventListQuery) (int, error)
	Audit(ctx context.Context, event domain.AuditEvent) error
	ForceCancelRun(ctx context.Context, runID string) error
	RetryDelivery(ctx context.Context, deliveryID string) error
}

type Router interface {
	Route(ctx context.Context, evt domain.CanonicalInboundEvent, session domain.Session) (domain.RouteDecision, error)
}

type Renderer interface {
	RenderRunEvent(ctx context.Context, session domain.Session, evt domain.RunEvent) ([]domain.OutboundDelivery, error)
}
