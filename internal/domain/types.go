package domain

import "time"

type CanonicalInboundEvent struct {
	EventID         string
	TenantID        string
	Channel         string
	Interaction     string
	ProviderEventID string
	ReceivedAt      time.Time
	Sender          Sender
	Conversation    Conversation
	Message         Message
	Metadata        Metadata
}

type Sender struct {
	ChannelUserID       string
	DisplayName         string
	IsAuthenticated     bool
	IdentityAssurance   string
	AllowedResponderIDs []string
}

type Conversation struct {
	ChannelConversationID string
	ChannelThreadID       string
	ChannelSurfaceKey     string
}

type Message struct {
	MessageID   string
	SessionID   string
	ChannelType string
	MessageType string
	Text        string
	Role        string
	Direction   string
	Parts       []Part
	Artifacts   []Artifact
	RawPayload  []byte
	CreatedAt   time.Time
}

type Part struct {
	ContentType string
	Content     string
}

type Artifact struct {
	ID         string         `json:"id"`
	Type       string         `json:"type,omitempty"`
	MessageID  string         `json:"message_id,omitempty"`
	Name       string         `json:"name,omitempty"`
	MIMEType   string         `json:"mime_type,omitempty"`
	SizeBytes  int64          `json:"size_bytes,omitempty"`
	SHA256     string         `json:"sha256,omitempty"`
	StorageURI string         `json:"storage_uri,omitempty"`
	SourceURL  string         `json:"source_url,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

type Metadata struct {
	MentionsBot      bool
	Command          string
	ArtifactTrust    string
	ResponderBinding ResponderBinding
	RawPayload       []byte
	AwaitID          string
	ResumePayload    []byte
	ActorUserID      string
}

type ResponderBinding struct {
	Mode                  string
	AllowedChannelUserIDs []string
}

type Session struct {
	ID              string
	TenantID        string
	OwnerUserID     string
	AgentProfileID  string
	ChannelType     string
	ChannelScopeKey string
	State           string
	LastActiveAt    time.Time
	ACPSessionID    string
}

type RouteDecision struct {
	AgentProfileID                    string
	ACPConnectionID                   string
	ACPAgentName                      string
	Mode                              string
	RequiresApproval                  bool
	RequiresLinkedIdentity            bool
	RequiresRecentStepUp              bool
	AllowedApprovalChannels           []string
	RequireLinkedIdentityForExecution bool
	RequireLinkedIdentityForApproval  bool
	RequireRecentStepUpForApproval    bool
}

type QueueItem struct {
	ID               string
	SessionID        string
	InboundMessageID string
	Status           string
	QueuePosition    int
	EnqueuedAt       time.Time
	ExpiresAt        time.Time
}

type Run struct {
	ID              string
	SessionID       string
	ACPConnectionID string
	ACPAgentName    string
	ACPRunID        string
	Status          string
	StartedAt       time.Time
	LastEventAt     time.Time
}

type Await struct {
	ID                  string
	RunID               string
	SessionID           string
	ChannelType         string
	Status              string
	SchemaJSON          []byte
	PromptRenderJSON    []byte
	TrustPolicyJSON     []byte
	AllowedResponderIDs []string
	ExpiresAt           time.Time
}

type ResumeRequest struct {
	AwaitID string `json:"await_id"`
	Payload []byte `json:"payload"`
}

type RenderChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type OutboxEvent struct {
	ID             string
	TenantID       string
	EventType      string
	AggregateType  string
	AggregateID    string
	IdempotencyKey string
	PayloadJSON    []byte
	Status         string
	AvailableAt    time.Time
	AttemptCount   int
}

type OutboundDelivery struct {
	ID                string
	TenantID          string
	SessionID         string
	RunID             string
	AwaitID           string
	LogicalMessageID  string
	ChannelType       string
	DeliveryKind      string
	Status            string
	AttemptCount      int
	LastError         string
	ProviderMessageID string
	ProviderRequestID string
	PayloadJSON       []byte
}

type WebAuthChallenge struct {
	ID            string
	TenantID      string
	Email         string
	OTPHash       string
	LinkTokenHash string
	ExpiresAt     time.Time
	ConsumedAt    time.Time
	AttemptCount  int
	CreatedAt     time.Time
}

type WebAuthSession struct {
	ID            string
	TenantID      string
	Email         string
	CSRFTokenHash string
	ExpiresAt     time.Time
	LastSeenAt    time.Time
	CreatedAt     time.Time
}

type User struct {
	ID                     string    `json:"id"`
	TenantID               string    `json:"tenant_id"`
	PrimaryEmail           string    `json:"primary_email"`
	PrimaryEmailVerified   bool      `json:"primary_email_verified"`
	PrimaryPhone           string    `json:"primary_phone,omitempty"`
	PrimaryPhoneNormalized string    `json:"primary_phone_normalized,omitempty"`
	PrimaryPhoneVerified   bool      `json:"primary_phone_verified"`
	PrimaryPhoneAddedAt    time.Time `json:"primary_phone_added_at,omitempty"`
	LastStepUpAt           time.Time `json:"last_step_up_at,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type LinkedIdentity struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	UserID         string    `json:"user_id"`
	ChannelType    string    `json:"channel_type"`
	ChannelUserID  string    `json:"channel_user_id"`
	Status         string    `json:"status"`
	LinkedAt       time.Time `json:"linked_at"`
	LastVerifiedAt time.Time `json:"last_verified_at,omitempty"`
}

type StepUpChallenge struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	UserID      string    `json:"user_id"`
	Purpose     string    `json:"purpose"`
	ChannelType string    `json:"channel_type,omitempty"`
	CodeHash    string    `json:"code_hash"`
	ExpiresAt   time.Time `json:"expires_at"`
	ConsumedAt  time.Time `json:"consumed_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type TrustPolicy struct {
	TenantID                          string    `json:"tenant_id"`
	AgentProfileID                    string    `json:"agent_profile_id"`
	RequireLinkedIdentityForExecution bool      `json:"require_linked_identity_for_execution"`
	RequireLinkedIdentityForApproval  bool      `json:"require_linked_identity_for_approval"`
	RequireRecentStepUpForApproval    bool      `json:"require_recent_step_up_for_approval"`
	AllowedApprovalChannels           []string  `json:"allowed_approval_channels,omitempty"`
	UpdatedAt                         time.Time `json:"updated_at,omitempty"`
}

type WhatsAppContactPolicy struct {
	TenantID            string    `json:"tenant_id"`
	ChannelUserID       string    `json:"channel_user_id"`
	LastInboundAt       time.Time `json:"last_inbound_at,omitempty"`
	WindowExpiresAt     time.Time `json:"window_expires_at,omitempty"`
	ConsentStatus       string    `json:"consent_status"`
	ConsentUpdatedAt    time.Time `json:"consent_updated_at,omitempty"`
	LastTemplateSentAt  time.Time `json:"last_template_sent_at,omitempty"`
	LastPolicyBlockedAt time.Time `json:"last_policy_blocked_at,omitempty"`
	CreatedAt           time.Time `json:"created_at,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

type WhatsAppPolicyListQuery struct {
	CursorPage
	TenantID      string
	ConsentStatus string
	WindowState   string
	Contains      string
}

type WebChatItem struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Role      string            `json:"role,omitempty"`
	Text      string            `json:"text,omitempty"`
	Status    string            `json:"status,omitempty"`
	Partial   bool              `json:"partial,omitempty"`
	AwaitID   string            `json:"await_id,omitempty"`
	Choices   []RenderChoice    `json:"choices,omitempty"`
	Artifacts []Artifact        `json:"artifacts,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

type WebChatActivity struct {
	Phase     string    `json:"phase"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type DeliveryResult struct {
	ProviderMessageID string
	ProviderRequestID string
}

type RunEventStream struct {
	Events <-chan RunEvent
	Err    <-chan error
}

func StaticRunEventStream(events ...RunEvent) RunEventStream {
	eventCh := make(chan RunEvent, len(events))
	errCh := make(chan error, 1)
	for _, evt := range events {
		eventCh <- evt
	}
	close(eventCh)
	close(errCh)
	return RunEventStream{Events: eventCh, Err: errCh}
}

type StartRunRequest struct {
	TenantID       string
	Session        Session
	RouteDecision  RouteDecision
	Message        Message
	IdempotencyKey string
}

type RunEvent struct {
	RunID       string
	MessageKey  string
	Status      string
	Text        string
	IsPartial   bool
	AwaitSchema []byte
	AwaitPrompt []byte
	Artifacts   []Artifact
}

type SessionDetail struct {
	Session    Session            `json:"session"`
	Messages   []Message          `json:"messages"`
	Artifacts  []Artifact         `json:"artifacts"`
	Runs       []Run              `json:"runs"`
	Awaits     []Await            `json:"awaits"`
	Deliveries []OutboundDelivery `json:"deliveries"`
	Audit      []AuditEvent       `json:"audit"`
}

type AgentManifest struct {
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	Protocol                string   `json:"protocol,omitempty"`
	InputContentTypes       []string `json:"input_content_types"`
	OutputContentTypes      []string `json:"output_content_types"`
	SupportsAwaitResume     bool     `json:"supports_await_resume"`
	SupportsStructuredAwait bool     `json:"supports_structured_await"`
	SupportsSessionReload   bool     `json:"supports_session_reload"`
	SupportsStreaming       bool     `json:"supports_streaming"`
	SupportsArtifacts       bool     `json:"supports_artifacts"`
	Healthy                 bool     `json:"healthy"`
}

type AgentCompatibility struct {
	AgentName      string        `json:"agent_name"`
	Compatible     bool          `json:"compatible"`
	ValidationMode string        `json:"validation_mode"`
	Reasons        []string      `json:"reasons"`
	Warnings       []string      `json:"warnings,omitempty"`
	Manifest       AgentManifest `json:"manifest"`
}

type CursorPage struct {
	After string
	Limit int
}

type SessionListQuery struct {
	CursorPage
	TenantID    string
	State       string
	ChannelType string
	OwnerUserID string
}

type RunListQuery struct {
	CursorPage
	TenantID          string
	Status            string
	SessionID         string
	SessionIDs        []string
	OwnerUserID       string
	ChannelIdentities []ChannelIdentity
}

type MessageListQuery struct {
	CursorPage
	TenantID          string
	Type              string
	Contains          string
	SessionID         string
	SessionIDs        []string
	OwnerUserID       string
	ChannelIdentities []ChannelIdentity
}

type ArtifactListQuery struct {
	CursorPage
	TenantID          string
	MIMEType          string
	NameContains      string
	SessionID         string
	SessionIDs        []string
	OwnerUserID       string
	ChannelIdentities []ChannelIdentity
	Direction         string
	StoragePrefix     string
}

type DeliveryListQuery struct {
	CursorPage
	TenantID      string
	Status        string
	SessionID     string
	RunID         string
	DeliveryKind  string
	LogicalPrefix string
}

type AwaitListQuery struct {
	CursorPage
	TenantID          string
	Status            string
	SessionID         string
	SessionIDs        []string
	OwnerUserID       string
	ChannelIdentities []ChannelIdentity
	RunID             string
}

type ChannelIdentity struct {
	ChannelType   string
	ChannelUserID string
}

type AuditEventListQuery struct {
	CursorPage
	TenantID      string
	SessionID     string
	RunID         string
	AwaitID       string
	AggregateType string
	AggregateID   string
	EventType     string
}

type TelegramUserAccessListQuery struct {
	CursorPage
	TenantID string
	Status   string
}

type PagedResult[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type RetentionPolicy struct {
	TenantID            string    `json:"tenant_id"`
	Enabled             *bool     `json:"enabled,omitempty"`
	PayloadDays         *int      `json:"payload_days,omitempty"`
	ArtifactDays        *int      `json:"artifact_days,omitempty"`
	AuditDays           *int      `json:"audit_days,omitempty"`
	RelationalGraceDays *int      `json:"relational_grace_days,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

type EffectiveRetentionPolicy struct {
	TenantID            string `json:"tenant_id"`
	Enabled             bool   `json:"enabled"`
	PayloadDays         int    `json:"payload_days"`
	ArtifactDays        int    `json:"artifact_days"`
	AuditDays           int    `json:"audit_days"`
	RelationalGraceDays int    `json:"relational_grace_days"`
}

type RetentionCutoffs struct {
	PayloadBefore    time.Time
	ArtifactBefore   time.Time
	AuditBefore      time.Time
	RelationalBefore time.Time
}

type RetentionCounts struct {
	MessagePayloads       int `json:"message_payloads"`
	DeliveryPayloads      int `json:"delivery_payloads"`
	OutboxPayloads        int `json:"outbox_payloads"`
	AwaitPayloads         int `json:"await_payloads"`
	AwaitResponsePayloads int `json:"await_response_payloads"`
	ArtifactBlobs         int `json:"artifact_blobs"`
	AuditRows             int `json:"audit_rows"`
	Sessions              int `json:"sessions"`
	HistoryRows           int `json:"history_rows"`
}

type RetentionTenantSummary struct {
	TenantID      string                   `json:"tenant_id"`
	Enabled       bool                     `json:"enabled"`
	SkippedReason string                   `json:"skipped_reason,omitempty"`
	Effective     EffectiveRetentionPolicy `json:"effective"`
	Counts        RetentionCounts          `json:"counts"`
}

type RetentionRunSummary struct {
	DryRun    bool                     `json:"dry_run"`
	StartedAt time.Time                `json:"started_at,omitempty"`
	EndedAt   time.Time                `json:"ended_at,omitempty"`
	Tenants   []RetentionTenantSummary `json:"tenants,omitempty"`
	Totals    RetentionCounts          `json:"totals"`
}

type AwaitDetail struct {
	Await     Await           `json:"await"`
	Responses []AwaitResponse `json:"responses"`
	Audit     []AuditEvent    `json:"audit"`
}

type RunDetail struct {
	Run        Run                `json:"run"`
	Messages   []Message          `json:"messages"`
	Artifacts  []Artifact         `json:"artifacts"`
	Awaits     []Await            `json:"awaits"`
	Deliveries []OutboundDelivery `json:"deliveries"`
	Audit      []AuditEvent       `json:"audit"`
}

type AwaitResponse struct {
	ID                     string    `json:"id"`
	AwaitID                string    `json:"await_id"`
	ActorChannelUserID     string    `json:"actor_channel_user_id"`
	ActorUserID            string    `json:"actor_user_id,omitempty"`
	ActorIdentityAssurance string    `json:"actor_identity_assurance"`
	ResponsePayloadJSON    []byte    `json:"response_payload_json"`
	IdempotencyKey         string    `json:"idempotency_key"`
	AcceptedAt             time.Time `json:"accepted_at"`
	RejectedReason         string    `json:"rejected_reason"`
}

type AuditEvent struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	SessionID     string    `json:"session_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	AwaitID       string    `json:"await_id,omitempty"`
	AggregateType string    `json:"aggregate_type"`
	AggregateID   string    `json:"aggregate_id"`
	EventType     string    `json:"event_type"`
	PayloadJSON   []byte    `json:"payload_json"`
	CreatedAt     time.Time `json:"created_at"`
}

type RunStatusSnapshot struct {
	ACPRunID  string         `json:"acp_run_id"`
	Status    string         `json:"status"`
	Output    string         `json:"output"`
	Artifacts []Artifact     `json:"artifacts"`
	Await     *AwaitSnapshot `json:"await,omitempty"`
}

type AwaitSnapshot struct {
	Schema []byte `json:"schema"`
	Prompt []byte `json:"prompt"`
}

type SurfaceSession struct {
	Session Session `json:"session"`
	Alias   string  `json:"alias,omitempty"`
}

type TelegramUserAccess struct {
	TenantID       string     `json:"tenant_id"`
	TelegramUserID string     `json:"telegram_user_id"`
	DisplayName    string     `json:"display_name,omitempty"`
	Allowed        bool       `json:"allowed"`
	Status         string     `json:"status"`
	AddedBy        string     `json:"added_by,omitempty"`
	RequestedAt    time.Time  `json:"requested_at"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}
