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
	MessageType string
	Text        string
	Role        string
	Direction   string
	Parts       []Part
	Artifacts   []Artifact
}

type Part struct {
	ContentType string
	Content     string
}

type Artifact struct {
	ID         string
	MessageID  string
	Name       string
	MIMEType   string
	SizeBytes  int64
	SHA256     string
	StorageURI string
	SourceURL  string
}

type Metadata struct {
	MentionsBot      bool
	Command          string
	ArtifactTrust    string
	ResponderBinding ResponderBinding
	RawPayload       []byte
	AwaitID          string
	ResumePayload    []byte
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
	AgentProfileID   string
	ACPConnectionID  string
	ACPAgentName     string
	Mode             string
	RequiresApproval bool
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

type WebChatItem struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Role      string            `json:"role,omitempty"`
	Text      string            `json:"text,omitempty"`
	Status    string            `json:"status,omitempty"`
	AwaitID   string            `json:"await_id,omitempty"`
	Choices   []RenderChoice    `json:"choices,omitempty"`
	Artifacts []Artifact        `json:"artifacts,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

type DeliveryResult struct {
	ProviderMessageID string
	ProviderRequestID string
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
	Status      string
	Text        string
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
	TenantID  string
	Status    string
	SessionID string
}

type MessageListQuery struct {
	CursorPage
	TenantID  string
	Type      string
	Contains  string
	SessionID string
}

type ArtifactListQuery struct {
	CursorPage
	TenantID      string
	MIMEType      string
	NameContains  string
	SessionID     string
	Direction     string
	StoragePrefix string
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
	TenantID  string
	Status    string
	SessionID string
	RunID     string
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
