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
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	InputContentTypes   []string `json:"input_content_types"`
	OutputContentTypes  []string `json:"output_content_types"`
	SupportsAwaitResume bool     `json:"supports_await_resume"`
	SupportsStreaming   bool     `json:"supports_streaming"`
	SupportsArtifacts   bool     `json:"supports_artifacts"`
	Healthy             bool     `json:"healthy"`
}

type AgentCompatibility struct {
	AgentName  string        `json:"agent_name"`
	Compatible bool          `json:"compatible"`
	Reasons    []string      `json:"reasons"`
	Manifest   AgentManifest `json:"manifest"`
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
	TenantID       string
	SessionID      string
	RunID          string
	AwaitID        string
	AggregateType  string
	AggregateID    string
	EventType      string
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

type AwaitDetail struct {
	Await     Await            `json:"await"`
	Responses []AwaitResponse  `json:"responses"`
	Audit     []AuditEvent     `json:"audit"`
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
	ACPRunID  string          `json:"acp_run_id"`
	Status    string          `json:"status"`
	Output    string          `json:"output"`
	Artifacts []Artifact      `json:"artifacts"`
	Await     *AwaitSnapshot  `json:"await,omitempty"`
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
	TenantID        string    `json:"tenant_id"`
	TelegramUserID  string    `json:"telegram_user_id"`
	DisplayName     string    `json:"display_name,omitempty"`
	Allowed         bool      `json:"allowed"`
	Status          string    `json:"status"`
	AddedBy         string    `json:"added_by,omitempty"`
	RequestedAt     time.Time `json:"requested_at"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
