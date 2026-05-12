package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ServiceName                           string
	Environment                           string
	HTTPAddr                              string
	AdminAddr                             string
	AdminBearerToken                      string
	HTTPReadHeaderTimeout                 time.Duration
	HTTPReadTimeout                       time.Duration
	HTTPWriteTimeout                      time.Duration
	HTTPIdleTimeout                       time.Duration
	DatabaseURL                           string
	ACPImplementation                     string
	ACPBaseURL                            string
	ACPToken                              string
	ACPCommand                            string
	ACPArgs                               []string
	ACPEnv                                []string
	ACPWorkdir                            string
	ACPStartupTimeout                     time.Duration
	ACPRPCTimeout                         time.Duration
	DefaultACPAgentName                   string
	ValidateACPOnStartup                  bool
	ACPManifestCacheTTL                   time.Duration
	SlackSigningSecret                    string
	SlackBotToken                         string
	WhatsAppVerifyToken                   string
	WhatsAppAccessToken                   string
	WhatsAppAppSecret                     string
	WhatsAppPhoneNumberID                 string
	WhatsAppAPIBaseURL                    string
	WhatsAppEnforce24HWindow              bool
	WhatsAppCustomerServiceWindowHours    int
	WhatsAppClosedWindowTemplateJSON      []byte
	WhatsAppWebEnabled                    bool
	WhatsAppWebBaseURL                    string
	WhatsAppWebAPIKey                     string
	WhatsAppWebSession                    string
	WhatsAppWebEngine                     string
	WhatsAppWebWebhookSecret              string
	WhatsAppWebEnableAntiBlock            bool
	WhatsAppWebEnableSeen                 bool
	WhatsAppWebEnableTyping               bool
	WhatsAppWebSetOfflineAfterSend        bool
	WhatsAppWebRequireRecentInbound       bool
	WhatsAppWebGroupMode                  string
	WhatsAppWebGroupBotIDs                []string
	WhatsAppWebGroupAllowlist             []string
	WhatsAppWebGroupBlocklist             []string
	WhatsAppWebGroupContextLimit          int
	WhatsAppWebGroupContextMaxChars       int
	WhatsAppWebMinDelayMS                 int
	WhatsAppWebMaxDelayMS                 int
	WhatsAppWebHourlyMessageCap           int
	WhatsAppWebRecentInboundWindowMinutes int
	WhatsAppWebBurstWindowMinutes         int
	WhatsAppWebBurstMessageCap            int
	NexusPublicBaseURL                    string
	LajuBaseURL                           string
	LajuBearerToken                       string
	EmailLajuForwardOnly                  bool
	EmailWebhookSecret                    string
	EmailSMTPAddr                         string
	EmailSMTPUsername                     string
	EmailSMTPPassword                     string
	EmailFromAddress                      string
	WebChatCookieName                     string
	WebChatDevAuth                        bool
	WebChatInteractionVisibility          string
	WebChatHistoryScope                   string
	WebChatIdentities                     []WebChatIdentityConfig
	WebChatSessionHours                   int
	WebChatOTPMinutes                     int
	WebPushEnabled                        bool
	WebPushVAPIDPublicKey                 string
	WebPushVAPIDPrivateKey                string
	WebPushVAPIDSubject                   string
	WebPushTTLSeconds                     int
	WebPushPreferForOutbound              bool
	IdentityLinkMinutes                   int
	StepUpOTPMinutes                      int
	StepUpWindowMinutes                   int
	RequireLinkedIdentity                 bool
	RequireRecentStepUp                   bool
	AllowedApprovalChannels               []string
	OTLPEndpoint                          string
	OTELSampleRatio                       float64
	RetryMaxAttempts                      int
	RetryBaseDelayMS                      int
	CircuitBreakerFailures                int
	CircuitBreakerCoolDownSeconds         int
	WhatsAppMediaMaxBytes                 int64
	EmailWebhookMaxSkewSeconds            int
	EmailMaxAttachmentBytes               int64
	EmailMaxAttachments                   int
	ObjectStorageBaseURL                  string
	WorkerPollInterval                    time.Duration
	ReconcilerInterval                    time.Duration
	OutboxClaimTimeout                    time.Duration
	QueueStartingTimeout                  time.Duration
	RunStaleTimeout                       time.Duration
	DeliverySendingTimeout                time.Duration
	DeliveryMaxAttempts                   int
	RetentionEnabled                      bool
	RetentionInterval                     time.Duration
	RetentionBatchSize                    int
	RetentionPayloadDays                  int
	RetentionArtifactDays                 int
	RetentionAuditDays                    int
	RetentionGraceDays                    int
	TelegramBotToken                      string
	TelegramWebhookSecret                 string
	TelegramAllowedUserIDs                []string
	DefaultTenantID                       string
	DefaultAgentProfileID                 string
	ConfigPath                            string
	ACPMode                               string
	ACPConnections                        []ACPConnectionConfig
	ACPAgentProfiles                      []ACPAgentProfileConfig
	AgentRouting                          AgentRoutingConfig
}

type ACPConnectionConfig struct {
	ID             string            `json:"id" yaml:"id"`
	Implementation string            `json:"implementation" yaml:"implementation"`
	BaseURL        string            `json:"base_url" yaml:"base_url"`
	Token          string            `json:"token" yaml:"token"`
	Command        string            `json:"command" yaml:"command"`
	Args           []string          `json:"args" yaml:"args"`
	Env            []string          `json:"env" yaml:"env"`
	Workdir        string            `json:"workdir" yaml:"workdir"`
	Headers        map[string]string `json:"headers" yaml:"headers"`
	PathPrefix     string            `json:"path_prefix" yaml:"path_prefix"`
	Enabled        *bool             `json:"enabled" yaml:"enabled"`
}

type ACPAgentProfileConfig struct {
	ID           string            `json:"id" yaml:"id"`
	ConnectionID string            `json:"connection_id" yaml:"connection_id"`
	AgentName    string            `json:"agent_name" yaml:"agent_name"`
	Description  string            `json:"description" yaml:"description"`
	Headers      map[string]string `json:"headers" yaml:"headers"`
	PathPrefix   string            `json:"path_prefix" yaml:"path_prefix"`
}

type AgentRoutingConfig struct {
	DefaultAgentByChannel  map[string]string        `json:"default_agent_by_channel" yaml:"default_agent_by_channel"`
	AllowedAgentsByChannel map[string][]string      `json:"allowed_agents_by_channel" yaml:"allowed_agents_by_channel"`
	Rules                  []AgentRoutingRuleConfig `json:"rules" yaml:"rules"`
}

type AgentRoutingRuleConfig struct {
	ID             string         `json:"id" yaml:"id"`
	Priority       int            `json:"priority" yaml:"priority"`
	Enabled        *bool          `json:"enabled" yaml:"enabled"`
	Match          map[string]any `json:"match" yaml:"match"`
	AgentProfileID string         `json:"agent_profile_id" yaml:"agent_profile_id"`
}

type WebChatIdentityConfig struct {
	ID               string         `json:"id" yaml:"id"`
	Path             string         `json:"path" yaml:"path"`
	AgentProfileID   string         `json:"agent_profile_id" yaml:"agent_profile_id"`
	Title            string         `json:"title" yaml:"title"`
	Subtitle         string         `json:"subtitle" yaml:"subtitle"`
	AllowAgentSwitch *bool          `json:"allow_agent_switch" yaml:"allow_agent_switch"`
	Labels           map[string]any `json:"labels" yaml:"labels"`
	Theme            map[string]any `json:"theme" yaml:"theme"`
	Features         map[string]any `json:"features" yaml:"features"`
}

func Load() (Config, error) {
	var err error
	cfg := Config{
		ServiceName:                           env("SERVICE_NAME", "nexus-gateway"),
		Environment:                           env("NEXUS_ENV", "development"),
		HTTPAddr:                              env("HTTP_ADDR", ":8080"),
		AdminAddr:                             env("ADMIN_ADDR", ":8081"),
		AdminBearerToken:                      strings.TrimSpace(os.Getenv("ADMIN_BEARER_TOKEN")),
		DatabaseURL:                           env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable"),
		ACPImplementation:                     env("ACP_IMPLEMENTATION", "strict"),
		ACPBaseURL:                            env("ACP_BASE_URL", "http://localhost:8090"),
		ACPToken:                              os.Getenv("ACP_TOKEN"),
		ACPCommand:                            env("ACP_COMMAND", "opencode"),
		ACPArgs:                               csvEnv("ACP_ARGS"),
		ACPEnv:                                prefixedEnv("ACP_ENV_"),
		ACPWorkdir:                            env("ACP_WORKDIR", mustGetwd()),
		DefaultACPAgentName:                   env("DEFAULT_ACP_AGENT_NAME", "default-agent"),
		SlackSigningSecret:                    env("SLACK_SIGNING_SECRET", "dev-secret"),
		SlackBotToken:                         os.Getenv("SLACK_BOT_TOKEN"),
		WhatsAppVerifyToken:                   env("WHATSAPP_VERIFY_TOKEN", "dev-whatsapp-verify"),
		WhatsAppAccessToken:                   os.Getenv("WHATSAPP_ACCESS_TOKEN"),
		WhatsAppAppSecret:                     os.Getenv("WHATSAPP_APP_SECRET"),
		WhatsAppPhoneNumberID:                 os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		WhatsAppAPIBaseURL:                    env("WHATSAPP_API_BASE_URL", "https://graph.facebook.com/v20.0"),
		WhatsAppEnforce24HWindow:              envBool("WHATSAPP_ENFORCE_24H_WINDOW", true),
		WhatsAppCustomerServiceWindowHours:    mustEnvIntDefault("WHATSAPP_CUSTOMER_SERVICE_WINDOW_HOURS", 24),
		WhatsAppClosedWindowTemplateJSON:      []byte(strings.TrimSpace(os.Getenv("WHATSAPP_CLOSED_WINDOW_TEMPLATE_JSON"))),
		WhatsAppWebEnabled:                    envBool("WHATSAPP_WEB_ENABLED", false),
		WhatsAppWebBaseURL:                    env("WHATSAPP_WEB_BASE_URL", "http://localhost:3000"),
		WhatsAppWebAPIKey:                     strings.TrimSpace(os.Getenv("WHATSAPP_WEB_API_KEY")),
		WhatsAppWebSession:                    env("WHATSAPP_WEB_SESSION", "default"),
		WhatsAppWebEngine:                     strings.TrimSpace(os.Getenv("WHATSAPP_WEB_ENGINE")),
		WhatsAppWebWebhookSecret:              strings.TrimSpace(os.Getenv("WHATSAPP_WEB_WEBHOOK_SECRET")),
		WhatsAppWebEnableAntiBlock:            envBool("WHATSAPP_WEB_ENABLE_ANTI_BLOCK", true),
		WhatsAppWebEnableSeen:                 envBool("WHATSAPP_WEB_ENABLE_SEEN", true),
		WhatsAppWebEnableTyping:               envBool("WHATSAPP_WEB_ENABLE_TYPING", true),
		WhatsAppWebSetOfflineAfterSend:        envBool("WHATSAPP_WEB_SET_OFFLINE_AFTER_SEND", true),
		WhatsAppWebRequireRecentInbound:       envBool("WHATSAPP_WEB_REQUIRE_RECENT_INBOUND", true),
		WhatsAppWebGroupMode:                  env("WHATSAPP_WEB_GROUP_MODE", "ignore"),
		WhatsAppWebGroupBotIDs:                csvEnv("WHATSAPP_WEB_GROUP_BOT_IDS"),
		WhatsAppWebGroupAllowlist:             csvEnv("WHATSAPP_WEB_GROUP_ALLOWLIST"),
		WhatsAppWebGroupBlocklist:             csvEnv("WHATSAPP_WEB_GROUP_BLOCKLIST"),
		WhatsAppWebGroupContextLimit:          mustEnvIntDefault("WHATSAPP_WEB_GROUP_CONTEXT_LIMIT", 30),
		WhatsAppWebGroupContextMaxChars:       mustEnvIntDefault("WHATSAPP_WEB_GROUP_CONTEXT_MAX_CHARS", 6000),
		WhatsAppWebMinDelayMS:                 mustEnvIntDefault("WHATSAPP_WEB_MIN_DELAY_MS", 800),
		WhatsAppWebMaxDelayMS:                 mustEnvIntDefault("WHATSAPP_WEB_MAX_DELAY_MS", 2500),
		WhatsAppWebHourlyMessageCap:           mustEnvIntDefault("WHATSAPP_WEB_HOURLY_MESSAGE_CAP", 120),
		WhatsAppWebRecentInboundWindowMinutes: mustEnvIntDefault("WHATSAPP_WEB_RECENT_INBOUND_WINDOW_MINUTES", 30),
		WhatsAppWebBurstWindowMinutes:         mustEnvIntDefault("WHATSAPP_WEB_BURST_WINDOW_MINUTES", 2),
		WhatsAppWebBurstMessageCap:            mustEnvIntDefault("WHATSAPP_WEB_BURST_MESSAGE_CAP", 4),
		NexusPublicBaseURL:                    strings.TrimRight(strings.TrimSpace(os.Getenv("NEXUS_PUBLIC_BASE_URL")), "/"),
		LajuBaseURL:                           strings.TrimRight(strings.TrimSpace(os.Getenv("LAJU_URL")), "/"),
		LajuBearerToken:                       strings.TrimSpace(os.Getenv("LAJU_TOKEN")),
		EmailLajuForwardOnly:                  envBool("EMAIL_LAJU_FORWARD_ONLY", false),
		EmailWebhookSecret:                    env("EMAIL_WEBHOOK_SECRET", "dev-email-secret"),
		EmailSMTPAddr:                         os.Getenv("EMAIL_SMTP_ADDR"),
		EmailSMTPUsername:                     os.Getenv("EMAIL_SMTP_USERNAME"),
		EmailSMTPPassword:                     os.Getenv("EMAIL_SMTP_PASSWORD"),
		EmailFromAddress:                      env("EMAIL_FROM_ADDRESS", "nexus@example.com"),
		WebChatCookieName:                     env("WEBCHAT_COOKIE_NAME", "nexus_webchat_session"),
		WebChatDevAuth:                        envBool("WEBCHAT_DEV_AUTH", false),
		WebChatInteractionVisibility:          env("WEBCHAT_INTERACTION_VISIBILITY", "full"),
		WebChatHistoryScope:                   env("WEBCHAT_HISTORY_SCOPE", "linked_channels"),
		WebPushEnabled:                        envBool("WEB_PUSH_ENABLED", false),
		WebPushVAPIDPublicKey:                 strings.TrimSpace(os.Getenv("WEB_PUSH_VAPID_PUBLIC_KEY")),
		WebPushVAPIDPrivateKey:                strings.TrimSpace(os.Getenv("WEB_PUSH_VAPID_PRIVATE_KEY")),
		WebPushVAPIDSubject:                   env("WEB_PUSH_VAPID_SUBJECT", "mailto:admin@example.com"),
		WebPushTTLSeconds:                     mustEnvIntDefault("WEB_PUSH_TTL_SECONDS", 86400),
		WebPushPreferForOutbound:              envBool("WEB_PUSH_PREFER_FOR_OUTBOUND", true),
		IdentityLinkMinutes:                   mustEnvIntDefault("IDENTITY_LINK_MINUTES", 10),
		StepUpOTPMinutes:                      mustEnvIntDefault("STEP_UP_OTP_MINUTES", 10),
		StepUpWindowMinutes:                   mustEnvIntDefault("STEP_UP_WINDOW_MINUTES", 15),
		RequireLinkedIdentity:                 envBool("REQUIRE_LINKED_IDENTITY", false),
		RequireRecentStepUp:                   envBool("REQUIRE_RECENT_STEP_UP", false),
		AllowedApprovalChannels:               csvEnv("ALLOWED_APPROVAL_CHANNELS"),
		OTLPEndpoint:                          os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		RetryMaxAttempts:                      mustEnvIntDefault("RETRY_MAX_ATTEMPTS", 3),
		RetryBaseDelayMS:                      mustEnvIntDefault("RETRY_BASE_DELAY_MS", 200),
		CircuitBreakerFailures:                mustEnvIntDefault("CIRCUIT_BREAKER_FAILURES", 5),
		CircuitBreakerCoolDownSeconds:         mustEnvIntDefault("CIRCUIT_BREAKER_COOLDOWN_SECONDS", 30),
		WhatsAppMediaMaxBytes:                 mustEnvInt64Default("WHATSAPP_MEDIA_MAX_BYTES", 10<<20),
		EmailWebhookMaxSkewSeconds:            mustEnvIntDefault("EMAIL_WEBHOOK_MAX_SKEW_SECONDS", 300),
		EmailMaxAttachmentBytes:               mustEnvInt64Default("EMAIL_MAX_ATTACHMENT_BYTES", 10<<20),
		EmailMaxAttachments:                   mustEnvIntDefault("EMAIL_MAX_ATTACHMENTS", 10),
		TelegramBotToken:                      os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramWebhookSecret:                 env("TELEGRAM_WEBHOOK_SECRET", "dev-telegram-secret"),
		TelegramAllowedUserIDs:                csvEnv("TELEGRAM_ALLOWED_USER_IDS"),
		ObjectStorageBaseURL:                  env("OBJECT_STORAGE_BASE_URL", "file:///tmp/nexus-objects"),
		DefaultTenantID:                       env("DEFAULT_TENANT_ID", "tenant_default"),
		DefaultAgentProfileID:                 env("DEFAULT_AGENT_PROFILE_ID", "agent_profile_default"),
		ConfigPath:                            strings.TrimSpace(os.Getenv("NEXUS_CONFIG_PATH")),
		ACPMode:                               env("ACP_MODE", "single"),
	}
	if err := loadConfigFile(&cfg); err != nil {
		return Config{}, err
	}
	applyEnvOverrides(&cfg)

	if cfg.WhatsAppWebMinDelayMS, err = envInt("WHATSAPP_WEB_MIN_DELAY_MS", cfg.WhatsAppWebMinDelayMS); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_WEB_MIN_DELAY_MS: %w", err)
	}
	if cfg.WhatsAppWebMaxDelayMS, err = envInt("WHATSAPP_WEB_MAX_DELAY_MS", cfg.WhatsAppWebMaxDelayMS); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_WEB_MAX_DELAY_MS: %w", err)
	}
	if cfg.WhatsAppWebHourlyMessageCap, err = envInt("WHATSAPP_WEB_HOURLY_MESSAGE_CAP", cfg.WhatsAppWebHourlyMessageCap); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_WEB_HOURLY_MESSAGE_CAP: %w", err)
	}
	if cfg.WhatsAppWebRecentInboundWindowMinutes, err = envInt("WHATSAPP_WEB_RECENT_INBOUND_WINDOW_MINUTES", cfg.WhatsAppWebRecentInboundWindowMinutes); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_WEB_RECENT_INBOUND_WINDOW_MINUTES: %w", err)
	}
	if cfg.WhatsAppWebBurstWindowMinutes, err = envInt("WHATSAPP_WEB_BURST_WINDOW_MINUTES", cfg.WhatsAppWebBurstWindowMinutes); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_WEB_BURST_WINDOW_MINUTES: %w", err)
	}
	if cfg.WhatsAppWebBurstMessageCap, err = envInt("WHATSAPP_WEB_BURST_MESSAGE_CAP", cfg.WhatsAppWebBurstMessageCap); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_WEB_BURST_MESSAGE_CAP: %w", err)
	}
	if cfg.WhatsAppWebGroupContextLimit, err = envInt("WHATSAPP_WEB_GROUP_CONTEXT_LIMIT", cfg.WhatsAppWebGroupContextLimit); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_WEB_GROUP_CONTEXT_LIMIT: %w", err)
	}
	if cfg.WhatsAppWebGroupContextMaxChars, err = envInt("WHATSAPP_WEB_GROUP_CONTEXT_MAX_CHARS", cfg.WhatsAppWebGroupContextMaxChars); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_WEB_GROUP_CONTEXT_MAX_CHARS: %w", err)
	}
	if cfg.IdentityLinkMinutes, err = envInt("IDENTITY_LINK_MINUTES", cfg.IdentityLinkMinutes); err != nil {
		return Config{}, fmt.Errorf("parse IDENTITY_LINK_MINUTES: %w", err)
	}
	if cfg.StepUpOTPMinutes, err = envInt("STEP_UP_OTP_MINUTES", cfg.StepUpOTPMinutes); err != nil {
		return Config{}, fmt.Errorf("parse STEP_UP_OTP_MINUTES: %w", err)
	}
	if cfg.StepUpWindowMinutes, err = envInt("STEP_UP_WINDOW_MINUTES", cfg.StepUpWindowMinutes); err != nil {
		return Config{}, fmt.Errorf("parse STEP_UP_WINDOW_MINUTES: %w", err)
	}
	if cfg.RetryMaxAttempts, err = envInt("RETRY_MAX_ATTEMPTS", cfg.RetryMaxAttempts); err != nil {
		return Config{}, fmt.Errorf("parse RETRY_MAX_ATTEMPTS: %w", err)
	}
	if cfg.RetryBaseDelayMS, err = envInt("RETRY_BASE_DELAY_MS", cfg.RetryBaseDelayMS); err != nil {
		return Config{}, fmt.Errorf("parse RETRY_BASE_DELAY_MS: %w", err)
	}
	if cfg.CircuitBreakerFailures, err = envInt("CIRCUIT_BREAKER_FAILURES", cfg.CircuitBreakerFailures); err != nil {
		return Config{}, fmt.Errorf("parse CIRCUIT_BREAKER_FAILURES: %w", err)
	}
	if cfg.CircuitBreakerCoolDownSeconds, err = envInt("CIRCUIT_BREAKER_COOLDOWN_SECONDS", cfg.CircuitBreakerCoolDownSeconds); err != nil {
		return Config{}, fmt.Errorf("parse CIRCUIT_BREAKER_COOLDOWN_SECONDS: %w", err)
	}
	if cfg.WhatsAppMediaMaxBytes, err = envInt64("WHATSAPP_MEDIA_MAX_BYTES", cfg.WhatsAppMediaMaxBytes); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_MEDIA_MAX_BYTES: %w", err)
	}
	if cfg.WhatsAppCustomerServiceWindowHours, err = envInt("WHATSAPP_CUSTOMER_SERVICE_WINDOW_HOURS", cfg.WhatsAppCustomerServiceWindowHours); err != nil {
		return Config{}, fmt.Errorf("parse WHATSAPP_CUSTOMER_SERVICE_WINDOW_HOURS: %w", err)
	}
	if len(cfg.WhatsAppClosedWindowTemplateJSON) > 0 && !json.Valid(cfg.WhatsAppClosedWindowTemplateJSON) {
		return Config{}, fmt.Errorf("parse WHATSAPP_CLOSED_WINDOW_TEMPLATE_JSON: invalid JSON")
	}
	if cfg.EmailWebhookMaxSkewSeconds, err = envInt("EMAIL_WEBHOOK_MAX_SKEW_SECONDS", cfg.EmailWebhookMaxSkewSeconds); err != nil {
		return Config{}, fmt.Errorf("parse EMAIL_WEBHOOK_MAX_SKEW_SECONDS: %w", err)
	}
	if cfg.EmailMaxAttachmentBytes, err = envInt64("EMAIL_MAX_ATTACHMENT_BYTES", cfg.EmailMaxAttachmentBytes); err != nil {
		return Config{}, fmt.Errorf("parse EMAIL_MAX_ATTACHMENT_BYTES: %w", err)
	}
	if cfg.EmailMaxAttachments, err = envInt("EMAIL_MAX_ATTACHMENTS", cfg.EmailMaxAttachments); err != nil {
		return Config{}, fmt.Errorf("parse EMAIL_MAX_ATTACHMENTS: %w", err)
	}

	seconds, err := envInt("WORKER_POLL_SECONDS", 2)
	if err != nil {
		return Config{}, fmt.Errorf("parse WORKER_POLL_SECONDS: %w", err)
	}
	cfg.WorkerPollInterval = time.Duration(seconds) * time.Second
	httpReadHeaderSeconds, err := envInt("HTTP_READ_HEADER_TIMEOUT_SECONDS", 5)
	if err != nil {
		return Config{}, fmt.Errorf("parse HTTP_READ_HEADER_TIMEOUT_SECONDS: %w", err)
	}
	cfg.HTTPReadHeaderTimeout = time.Duration(httpReadHeaderSeconds) * time.Second
	httpReadSeconds, err := envInt("HTTP_READ_TIMEOUT_SECONDS", 30)
	if err != nil {
		return Config{}, fmt.Errorf("parse HTTP_READ_TIMEOUT_SECONDS: %w", err)
	}
	cfg.HTTPReadTimeout = time.Duration(httpReadSeconds) * time.Second
	httpWriteSeconds, err := envInt("HTTP_WRITE_TIMEOUT_SECONDS", 120)
	if err != nil {
		return Config{}, fmt.Errorf("parse HTTP_WRITE_TIMEOUT_SECONDS: %w", err)
	}
	cfg.HTTPWriteTimeout = time.Duration(httpWriteSeconds) * time.Second
	httpIdleSeconds, err := envInt("HTTP_IDLE_TIMEOUT_SECONDS", 120)
	if err != nil {
		return Config{}, fmt.Errorf("parse HTTP_IDLE_TIMEOUT_SECONDS: %w", err)
	}
	cfg.HTTPIdleTimeout = time.Duration(httpIdleSeconds) * time.Second
	reconcilerSeconds, err := envInt("RECONCILER_INTERVAL_SECONDS", 30)
	if err != nil {
		return Config{}, fmt.Errorf("parse RECONCILER_INTERVAL_SECONDS: %w", err)
	}
	cfg.ReconcilerInterval = time.Duration(reconcilerSeconds) * time.Second
	outboxClaimSeconds, err := envInt("OUTBOX_CLAIM_TIMEOUT_SECONDS", 120)
	if err != nil {
		return Config{}, fmt.Errorf("parse OUTBOX_CLAIM_TIMEOUT_SECONDS: %w", err)
	}
	cfg.OutboxClaimTimeout = time.Duration(outboxClaimSeconds) * time.Second
	queueStartingSeconds, err := envInt("QUEUE_STARTING_TIMEOUT_SECONDS", 120)
	if err != nil {
		return Config{}, fmt.Errorf("parse QUEUE_STARTING_TIMEOUT_SECONDS: %w", err)
	}
	cfg.QueueStartingTimeout = time.Duration(queueStartingSeconds) * time.Second
	runStaleSeconds, err := envInt("RUN_STALE_TIMEOUT_SECONDS", 300)
	if err != nil {
		return Config{}, fmt.Errorf("parse RUN_STALE_TIMEOUT_SECONDS: %w", err)
	}
	cfg.RunStaleTimeout = time.Duration(runStaleSeconds) * time.Second
	deliverySendingSeconds, err := envInt("DELIVERY_SENDING_TIMEOUT_SECONDS", 120)
	if err != nil {
		return Config{}, fmt.Errorf("parse DELIVERY_SENDING_TIMEOUT_SECONDS: %w", err)
	}
	cfg.DeliverySendingTimeout = time.Duration(deliverySendingSeconds) * time.Second
	cfg.DeliveryMaxAttempts, err = envInt("DELIVERY_MAX_ATTEMPTS", 5)
	if err != nil {
		return Config{}, fmt.Errorf("parse DELIVERY_MAX_ATTEMPTS: %w", err)
	}
	cfg.RetentionEnabled = envBool("RETENTION_ENABLED", false)
	retentionSeconds, err := envInt("RETENTION_INTERVAL_SECONDS", 3600)
	if err != nil {
		return Config{}, fmt.Errorf("parse RETENTION_INTERVAL_SECONDS: %w", err)
	}
	cfg.RetentionInterval = time.Duration(retentionSeconds) * time.Second
	cfg.RetentionBatchSize, err = envInt("RETENTION_BATCH_SIZE", 500)
	if err != nil {
		return Config{}, fmt.Errorf("parse RETENTION_BATCH_SIZE: %w", err)
	}
	cfg.RetentionPayloadDays, err = envInt("RETENTION_DEFAULT_PAYLOAD_DAYS", 30)
	if err != nil {
		return Config{}, fmt.Errorf("parse RETENTION_DEFAULT_PAYLOAD_DAYS: %w", err)
	}
	cfg.RetentionArtifactDays, err = envInt("RETENTION_DEFAULT_ARTIFACT_DAYS", 30)
	if err != nil {
		return Config{}, fmt.Errorf("parse RETENTION_DEFAULT_ARTIFACT_DAYS: %w", err)
	}
	cfg.RetentionAuditDays, err = envInt("RETENTION_DEFAULT_AUDIT_DAYS", 30)
	if err != nil {
		return Config{}, fmt.Errorf("parse RETENTION_DEFAULT_AUDIT_DAYS: %w", err)
	}
	cfg.RetentionGraceDays, err = envInt("RETENTION_RELATIONAL_GRACE_DAYS", 30)
	if err != nil {
		return Config{}, fmt.Errorf("parse RETENTION_RELATIONAL_GRACE_DAYS: %w", err)
	}
	cfg.WebChatSessionHours, err = envInt("WEBCHAT_SESSION_HOURS", 24)
	if err != nil {
		return Config{}, fmt.Errorf("parse WEBCHAT_SESSION_HOURS: %w", err)
	}
	cfg.WebChatOTPMinutes, err = envInt("WEBCHAT_OTP_MINUTES", 10)
	if err != nil {
		return Config{}, fmt.Errorf("parse WEBCHAT_OTP_MINUTES: %w", err)
	}
	cfg.OTELSampleRatio, err = envFloat("OTEL_SAMPLE_RATIO", 1.0)
	if err != nil {
		return Config{}, fmt.Errorf("parse OTEL_SAMPLE_RATIO: %w", err)
	}
	cfg.ValidateACPOnStartup = envBool("VALIDATE_ACP_ON_STARTUP", false)
	cacheTTLSeconds, err := envInt("ACP_MANIFEST_CACHE_TTL_SECONDS", 60)
	if err != nil {
		return Config{}, fmt.Errorf("parse ACP_MANIFEST_CACHE_TTL_SECONDS: %w", err)
	}
	cfg.ACPManifestCacheTTL = time.Duration(cacheTTLSeconds) * time.Second
	startupSeconds, err := envInt("ACP_STARTUP_TIMEOUT_SECONDS", 15)
	if err != nil {
		return Config{}, fmt.Errorf("parse ACP_STARTUP_TIMEOUT_SECONDS: %w", err)
	}
	cfg.ACPStartupTimeout = time.Duration(startupSeconds) * time.Second
	rpcSeconds, err := envInt("ACP_RPC_TIMEOUT_SECONDS", 120)
	if err != nil {
		return Config{}, fmt.Errorf("parse ACP_RPC_TIMEOUT_SECONDS: %w", err)
	}
	cfg.ACPRPCTimeout = time.Duration(rpcSeconds) * time.Second
	normalizeACPConfig(&cfg)
	if err := validateACPConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateWhatsAppWebGroupConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateWebChatIdentities(cfg); err != nil {
		return Config{}, err
	}
	if err := validateProductionConfig(cfg); err != nil {
		return Config{}, err
	}
	mode, err := NormalizeWebChatInteractionVisibility(cfg.WebChatInteractionVisibility)
	if err != nil {
		return Config{}, err
	}
	cfg.WebChatInteractionVisibility = mode
	scope, err := NormalizeWebChatHistoryScope(cfg.WebChatHistoryScope)
	if err != nil {
		return Config{}, err
	}
	cfg.WebChatHistoryScope = scope
	return cfg, nil
}

type fileConfig struct {
	ServiceName                  string                `json:"service_name" yaml:"service_name"`
	Environment                  string                `json:"environment" yaml:"environment"`
	HTTPAddr                     string                `json:"http_addr" yaml:"http_addr"`
	AdminAddr                    string                `json:"admin_addr" yaml:"admin_addr"`
	DatabaseURL                  string                `json:"database_url" yaml:"database_url"`
	DefaultTenantID              string                `json:"default_tenant_id" yaml:"default_tenant_id"`
	DefaultAgentProfileID        string                `json:"default_agent_profile_id" yaml:"default_agent_profile_id"`
	ACP                          fileACPConfig         `json:"acp" yaml:"acp"`
	Routing                      AgentRoutingConfig    `json:"routing" yaml:"routing"`
	WebChat                      fileWebChatConfig     `json:"webchat" yaml:"webchat"`
	WhatsAppWeb                  fileWhatsAppWebConfig `json:"whatsapp_web" yaml:"whatsapp_web"`
	WebChatHistoryScope          string                `json:"webchat_history_scope" yaml:"webchat_history_scope"`
	WebChatInteractionVisibility string                `json:"webchat_interaction_visibility" yaml:"webchat_interaction_visibility"`
}

type fileWebChatConfig struct {
	HistoryScope          string                  `json:"history_scope" yaml:"history_scope"`
	InteractionVisibility string                  `json:"interaction_visibility" yaml:"interaction_visibility"`
	Identities            []WebChatIdentityConfig `json:"identities" yaml:"identities"`
}

type fileWhatsAppWebConfig struct {
	GroupMode            string   `json:"group_mode" yaml:"group_mode"`
	GroupBotIDs          []string `json:"group_bot_ids" yaml:"group_bot_ids"`
	GroupAllowlist       []string `json:"group_allowlist" yaml:"group_allowlist"`
	GroupBlocklist       []string `json:"group_blocklist" yaml:"group_blocklist"`
	GroupContextLimit    *int     `json:"group_context_limit" yaml:"group_context_limit"`
	GroupContextMaxChars *int     `json:"group_context_max_chars" yaml:"group_context_max_chars"`
}

type fileACPConfig struct {
	Mode             string                  `json:"mode" yaml:"mode"`
	Single           *ACPConnectionConfig    `json:"single" yaml:"single"`
	Connections      []ACPConnectionConfig   `json:"connections" yaml:"connections"`
	AgentProfiles    []ACPAgentProfileConfig `json:"agent_profiles" yaml:"agent_profiles"`
	DefaultAgentName string                  `json:"default_agent_name" yaml:"default_agent_name"`
}

func loadConfigFile(cfg *Config) error {
	path := strings.TrimSpace(cfg.ConfigPath)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read NEXUS_CONFIG_PATH: %w", err)
	}
	var file fileConfig
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("parse NEXUS_CONFIG_PATH yaml: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("parse NEXUS_CONFIG_PATH json: %w", err)
		}
	default:
		return fmt.Errorf("NEXUS_CONFIG_PATH must end with .yaml, .yml, or .json")
	}
	mergeFileConfig(cfg, file)
	return nil
}

func mergeFileConfig(cfg *Config, file fileConfig) {
	if file.ServiceName != "" {
		cfg.ServiceName = file.ServiceName
	}
	if file.Environment != "" {
		cfg.Environment = file.Environment
	}
	if file.HTTPAddr != "" {
		cfg.HTTPAddr = file.HTTPAddr
	}
	if file.AdminAddr != "" {
		cfg.AdminAddr = file.AdminAddr
	}
	if file.DatabaseURL != "" {
		cfg.DatabaseURL = file.DatabaseURL
	}
	if file.DefaultTenantID != "" {
		cfg.DefaultTenantID = file.DefaultTenantID
	}
	if file.DefaultAgentProfileID != "" {
		cfg.DefaultAgentProfileID = file.DefaultAgentProfileID
	}
	if file.WebChatHistoryScope != "" {
		cfg.WebChatHistoryScope = file.WebChatHistoryScope
	}
	if file.WebChatInteractionVisibility != "" {
		cfg.WebChatInteractionVisibility = file.WebChatInteractionVisibility
	}
	if file.WebChat.HistoryScope != "" {
		cfg.WebChatHistoryScope = file.WebChat.HistoryScope
	}
	if file.WebChat.InteractionVisibility != "" {
		cfg.WebChatInteractionVisibility = file.WebChat.InteractionVisibility
	}
	if len(file.WebChat.Identities) > 0 {
		cfg.WebChatIdentities = append([]WebChatIdentityConfig(nil), file.WebChat.Identities...)
	}
	if file.WhatsAppWeb.GroupMode != "" {
		cfg.WhatsAppWebGroupMode = file.WhatsAppWeb.GroupMode
	}
	if len(file.WhatsAppWeb.GroupBotIDs) > 0 {
		cfg.WhatsAppWebGroupBotIDs = append([]string(nil), file.WhatsAppWeb.GroupBotIDs...)
	}
	if len(file.WhatsAppWeb.GroupAllowlist) > 0 {
		cfg.WhatsAppWebGroupAllowlist = append([]string(nil), file.WhatsAppWeb.GroupAllowlist...)
	}
	if len(file.WhatsAppWeb.GroupBlocklist) > 0 {
		cfg.WhatsAppWebGroupBlocklist = append([]string(nil), file.WhatsAppWeb.GroupBlocklist...)
	}
	if file.WhatsAppWeb.GroupContextLimit != nil {
		cfg.WhatsAppWebGroupContextLimit = *file.WhatsAppWeb.GroupContextLimit
	}
	if file.WhatsAppWeb.GroupContextMaxChars != nil {
		cfg.WhatsAppWebGroupContextMaxChars = *file.WhatsAppWeb.GroupContextMaxChars
	}
	if file.ACP.Mode != "" {
		cfg.ACPMode = file.ACP.Mode
	}
	if file.ACP.DefaultAgentName != "" {
		cfg.DefaultACPAgentName = file.ACP.DefaultAgentName
	}
	if file.ACP.Single != nil {
		mergeSingleACPConnection(cfg, *file.ACP.Single)
	}
	if len(file.ACP.Connections) > 0 {
		cfg.ACPConnections = append([]ACPConnectionConfig(nil), file.ACP.Connections...)
	}
	if len(file.ACP.AgentProfiles) > 0 {
		cfg.ACPAgentProfiles = append([]ACPAgentProfileConfig(nil), file.ACP.AgentProfiles...)
	}
	if len(file.Routing.DefaultAgentByChannel) > 0 {
		cfg.AgentRouting.DefaultAgentByChannel = cloneStringMap(file.Routing.DefaultAgentByChannel)
	}
	if len(file.Routing.AllowedAgentsByChannel) > 0 {
		cfg.AgentRouting.AllowedAgentsByChannel = cloneStringSliceMap(file.Routing.AllowedAgentsByChannel)
	}
	if len(file.Routing.Rules) > 0 {
		cfg.AgentRouting.Rules = append([]AgentRoutingRuleConfig(nil), file.Routing.Rules...)
	}
}

func mergeSingleACPConnection(cfg *Config, conn ACPConnectionConfig) {
	if conn.Implementation != "" {
		cfg.ACPImplementation = conn.Implementation
	}
	if conn.BaseURL != "" {
		cfg.ACPBaseURL = conn.BaseURL
	}
	if conn.Token != "" {
		cfg.ACPToken = conn.Token
	}
	if conn.Command != "" {
		cfg.ACPCommand = conn.Command
	}
	if len(conn.Args) > 0 {
		cfg.ACPArgs = append([]string(nil), conn.Args...)
	}
	if len(conn.Env) > 0 {
		cfg.ACPEnv = append([]string(nil), conn.Env...)
	}
	if conn.Workdir != "" {
		cfg.ACPWorkdir = conn.Workdir
	}
	if conn.ID != "" || len(conn.Headers) > 0 || conn.PathPrefix != "" || conn.Enabled != nil {
		if conn.ID == "" {
			conn.ID = "acp_default"
		}
		cfg.ACPConnections = []ACPConnectionConfig{conn}
	}
}

func applyEnvOverrides(cfg *Config) {
	cfg.ConfigPath = strings.TrimSpace(os.Getenv("NEXUS_CONFIG_PATH"))
	cfg.ServiceName = env("SERVICE_NAME", cfg.ServiceName)
	cfg.Environment = env("NEXUS_ENV", cfg.Environment)
	cfg.HTTPAddr = env("HTTP_ADDR", cfg.HTTPAddr)
	cfg.AdminAddr = env("ADMIN_ADDR", cfg.AdminAddr)
	cfg.AdminBearerToken = strings.TrimSpace(env("ADMIN_BEARER_TOKEN", cfg.AdminBearerToken))
	cfg.DatabaseURL = env("DATABASE_URL", cfg.DatabaseURL)
	cfg.ACPMode = env("ACP_MODE", cfg.ACPMode)
	cfg.ACPImplementation = env("ACP_IMPLEMENTATION", cfg.ACPImplementation)
	cfg.ACPBaseURL = env("ACP_BASE_URL", cfg.ACPBaseURL)
	if value := os.Getenv("ACP_TOKEN"); value != "" {
		cfg.ACPToken = value
	}
	cfg.ACPCommand = env("ACP_COMMAND", cfg.ACPCommand)
	if value := csvEnv("ACP_ARGS"); len(value) > 0 {
		cfg.ACPArgs = value
	}
	if value := prefixedEnv("ACP_ENV_"); len(value) > 0 {
		cfg.ACPEnv = value
	}
	cfg.ACPWorkdir = env("ACP_WORKDIR", cfg.ACPWorkdir)
	cfg.DefaultACPAgentName = env("DEFAULT_ACP_AGENT_NAME", cfg.DefaultACPAgentName)
	cfg.DefaultTenantID = env("DEFAULT_TENANT_ID", cfg.DefaultTenantID)
	cfg.DefaultAgentProfileID = env("DEFAULT_AGENT_PROFILE_ID", cfg.DefaultAgentProfileID)
	cfg.WebChatInteractionVisibility = env("WEBCHAT_INTERACTION_VISIBILITY", cfg.WebChatInteractionVisibility)
	cfg.WebChatHistoryScope = env("WEBCHAT_HISTORY_SCOPE", cfg.WebChatHistoryScope)
	cfg.SlackSigningSecret = env("SLACK_SIGNING_SECRET", cfg.SlackSigningSecret)
	if value := os.Getenv("SLACK_BOT_TOKEN"); value != "" {
		cfg.SlackBotToken = value
	}
	cfg.WhatsAppVerifyToken = env("WHATSAPP_VERIFY_TOKEN", cfg.WhatsAppVerifyToken)
	if value := os.Getenv("WHATSAPP_ACCESS_TOKEN"); value != "" {
		cfg.WhatsAppAccessToken = value
	}
	if value := os.Getenv("WHATSAPP_APP_SECRET"); value != "" {
		cfg.WhatsAppAppSecret = value
	}
	if value := os.Getenv("WHATSAPP_PHONE_NUMBER_ID"); value != "" {
		cfg.WhatsAppPhoneNumberID = value
	}
	cfg.WhatsAppAPIBaseURL = env("WHATSAPP_API_BASE_URL", cfg.WhatsAppAPIBaseURL)
	cfg.WhatsAppWebGroupMode = env("WHATSAPP_WEB_GROUP_MODE", cfg.WhatsAppWebGroupMode)
	if value := csvEnv("WHATSAPP_WEB_GROUP_BOT_IDS"); len(value) > 0 {
		cfg.WhatsAppWebGroupBotIDs = value
	}
	if value := csvEnv("WHATSAPP_WEB_GROUP_ALLOWLIST"); len(value) > 0 {
		cfg.WhatsAppWebGroupAllowlist = value
	}
	if value := csvEnv("WHATSAPP_WEB_GROUP_BLOCKLIST"); len(value) > 0 {
		cfg.WhatsAppWebGroupBlocklist = value
	}
	cfg.EmailWebhookSecret = env("EMAIL_WEBHOOK_SECRET", cfg.EmailWebhookSecret)
	if value := os.Getenv("EMAIL_SMTP_ADDR"); value != "" {
		cfg.EmailSMTPAddr = value
	}
	if value := os.Getenv("EMAIL_SMTP_USERNAME"); value != "" {
		cfg.EmailSMTPUsername = value
	}
	if value := os.Getenv("EMAIL_SMTP_PASSWORD"); value != "" {
		cfg.EmailSMTPPassword = value
	}
	cfg.EmailFromAddress = env("EMAIL_FROM_ADDRESS", cfg.EmailFromAddress)
	if value := os.Getenv("TELEGRAM_BOT_TOKEN"); value != "" {
		cfg.TelegramBotToken = value
	}
	cfg.TelegramWebhookSecret = env("TELEGRAM_WEBHOOK_SECRET", cfg.TelegramWebhookSecret)
	if value := csvEnv("TELEGRAM_ALLOWED_USER_IDS"); len(value) > 0 {
		cfg.TelegramAllowedUserIDs = value
	}
	cfg.ObjectStorageBaseURL = env("OBJECT_STORAGE_BASE_URL", cfg.ObjectStorageBaseURL)
}

func normalizeACPConfig(cfg *Config) {
	cfg.ACPMode = strings.ToLower(strings.TrimSpace(cfg.ACPMode))
	if cfg.ACPMode == "" {
		cfg.ACPMode = "single"
	}
	if cfg.ACPMode == "single" {
		cfg.ACPConnections = []ACPConnectionConfig{{
			ID:             "acp_default",
			Implementation: cfg.ACPImplementation,
			BaseURL:        cfg.ACPBaseURL,
			Token:          cfg.ACPToken,
			Command:        cfg.ACPCommand,
			Args:           append([]string(nil), cfg.ACPArgs...),
			Env:            append([]string(nil), cfg.ACPEnv...),
			Workdir:        cfg.ACPWorkdir,
			Enabled:        boolPtr(true),
		}}
		cfg.ACPAgentProfiles = []ACPAgentProfileConfig{{
			ID:           cfg.DefaultAgentProfileID,
			ConnectionID: "acp_default",
			AgentName:    cfg.DefaultACPAgentName,
		}}
		if cfg.AgentRouting.DefaultAgentByChannel == nil {
			cfg.AgentRouting.DefaultAgentByChannel = map[string]string{}
		}
		return
	}
	for i := range cfg.ACPConnections {
		cfg.ACPConnections[i].ID = strings.TrimSpace(cfg.ACPConnections[i].ID)
		cfg.ACPConnections[i].Implementation = strings.TrimSpace(cfg.ACPConnections[i].Implementation)
		cfg.ACPConnections[i].BaseURL = strings.TrimRight(strings.TrimSpace(cfg.ACPConnections[i].BaseURL), "/")
		cfg.ACPConnections[i].PathPrefix = strings.Trim(cfg.ACPConnections[i].PathPrefix, "/")
		if cfg.ACPConnections[i].Enabled == nil {
			cfg.ACPConnections[i].Enabled = boolPtr(true)
		}
	}
	for i := range cfg.ACPAgentProfiles {
		cfg.ACPAgentProfiles[i].ID = strings.TrimSpace(cfg.ACPAgentProfiles[i].ID)
		cfg.ACPAgentProfiles[i].ConnectionID = strings.TrimSpace(cfg.ACPAgentProfiles[i].ConnectionID)
		cfg.ACPAgentProfiles[i].AgentName = strings.TrimSpace(cfg.ACPAgentProfiles[i].AgentName)
		cfg.ACPAgentProfiles[i].PathPrefix = strings.Trim(cfg.ACPAgentProfiles[i].PathPrefix, "/")
	}
	for i := range cfg.WebChatIdentities {
		cfg.WebChatIdentities[i].ID = strings.TrimSpace(cfg.WebChatIdentities[i].ID)
		cfg.WebChatIdentities[i].Path = strings.Trim(strings.TrimSpace(cfg.WebChatIdentities[i].Path), "/")
		cfg.WebChatIdentities[i].AgentProfileID = strings.TrimSpace(cfg.WebChatIdentities[i].AgentProfileID)
	}
}

func validateACPConfig(cfg Config) error {
	switch cfg.ACPMode {
	case "single", "multiple":
	default:
		return fmt.Errorf("invalid ACP_MODE %q", cfg.ACPMode)
	}
	if cfg.ACPMode == "single" {
		return nil
	}
	enabledConnections := map[string]bool{}
	for _, conn := range cfg.ACPConnections {
		if conn.ID == "" {
			return fmt.Errorf("acp.connections[].id is required")
		}
		if conn.Enabled != nil && !*conn.Enabled {
			continue
		}
		enabledConnections[conn.ID] = true
	}
	if len(enabledConnections) == 0 {
		return fmt.Errorf("multiple ACP mode requires at least one enabled connection")
	}
	profiles := map[string]ACPAgentProfileConfig{}
	for _, profile := range cfg.ACPAgentProfiles {
		if profile.ID == "" || profile.ConnectionID == "" || profile.AgentName == "" {
			return fmt.Errorf("acp.agent_profiles require id, connection_id, and agent_name")
		}
		if !enabledConnections[profile.ConnectionID] {
			return fmt.Errorf("agent profile %q references disabled or missing connection %q", profile.ID, profile.ConnectionID)
		}
		profiles[profile.ID] = profile
	}
	if len(profiles) == 0 {
		return fmt.Errorf("multiple ACP mode requires at least one agent profile")
	}
	for channel, profileID := range cfg.AgentRouting.DefaultAgentByChannel {
		if _, ok := profiles[profileID]; !ok {
			return fmt.Errorf("routing.default_agent_by_channel[%s] references missing profile %q", channel, profileID)
		}
	}
	for channel, ids := range cfg.AgentRouting.AllowedAgentsByChannel {
		for _, id := range ids {
			if _, ok := profiles[id]; !ok {
				return fmt.Errorf("routing.allowed_agents_by_channel[%s] references missing profile %q", channel, id)
			}
		}
	}
	for _, rule := range cfg.AgentRouting.Rules {
		if rule.AgentProfileID == "" {
			return fmt.Errorf("routing.rules[].agent_profile_id is required")
		}
		if _, ok := profiles[rule.AgentProfileID]; !ok {
			return fmt.Errorf("routing rule references missing profile %q", rule.AgentProfileID)
		}
	}
	for _, channel := range enabledRoutingChannels(cfg) {
		if cfg.AgentRouting.DefaultAgentByChannel[strings.ToLower(channel)] == "" {
			return fmt.Errorf("multiple ACP mode requires routing.default_agent_by_channel[%s]", channel)
		}
	}
	return nil
}

func validateWebChatIdentities(cfg Config) error {
	if len(cfg.WebChatIdentities) == 0 {
		return nil
	}
	profiles := map[string]bool{}
	for _, profile := range cfg.ACPAgentProfiles {
		profiles[profile.ID] = true
	}
	seenIDs := map[string]bool{}
	seenPaths := map[string]bool{}
	reserved := map[string]bool{
		"app.css": true, "app.js": true, "artifacts": true, "auth": true, "awaits": true,
		"bootstrap": true, "chats": true, "dev": true, "events": true, "history": true,
		"identity": true, "messages": true, "step-up": true,
	}
	for _, identity := range cfg.WebChatIdentities {
		id := strings.TrimSpace(identity.ID)
		path := strings.Trim(strings.TrimSpace(identity.Path), "/")
		if id == "" || path == "" || strings.TrimSpace(identity.AgentProfileID) == "" {
			return fmt.Errorf("webchat.identities require id, path, and agent_profile_id")
		}
		if strings.Contains(path, "/") {
			return fmt.Errorf("webchat identity %q path must be a single path segment", id)
		}
		key := strings.ToLower(path)
		if reserved[key] {
			return fmt.Errorf("webchat identity %q uses reserved path %q", id, path)
		}
		if seenIDs[id] {
			return fmt.Errorf("duplicate webchat identity id %q", id)
		}
		if seenPaths[key] {
			return fmt.Errorf("duplicate webchat identity path %q", path)
		}
		seenIDs[id] = true
		seenPaths[key] = true
		if strings.EqualFold(cfg.ACPMode, "multiple") && !profiles[identity.AgentProfileID] {
			return fmt.Errorf("webchat identity %q references missing profile %q", id, identity.AgentProfileID)
		}
	}
	return nil
}

func validateWhatsAppWebGroupConfig(cfg Config) error {
	switch strings.ToLower(strings.TrimSpace(cfg.WhatsAppWebGroupMode)) {
	case "", "ignore", "reply_when_mentioned":
	default:
		return fmt.Errorf("WHATSAPP_WEB_GROUP_MODE must be ignore or reply_when_mentioned")
	}
	if cfg.WhatsAppWebGroupContextLimit < 0 {
		return fmt.Errorf("WHATSAPP_WEB_GROUP_CONTEXT_LIMIT must be non-negative")
	}
	if cfg.WhatsAppWebGroupContextMaxChars < 0 {
		return fmt.Errorf("WHATSAPP_WEB_GROUP_CONTEXT_MAX_CHARS must be non-negative")
	}
	return nil
}

func enabledRoutingChannels(cfg Config) []string {
	channels := []string{"webchat"}
	if strings.TrimSpace(cfg.TelegramBotToken) != "" {
		channels = append(channels, "telegram")
	}
	if strings.TrimSpace(cfg.SlackBotToken) != "" {
		channels = append(channels, "slack")
	}
	if strings.TrimSpace(cfg.WhatsAppAccessToken) != "" || strings.TrimSpace(cfg.WhatsAppPhoneNumberID) != "" {
		channels = append(channels, "whatsapp")
	}
	if cfg.WhatsAppWebEnabled {
		channels = append(channels, "whatsapp_web")
	}
	return channels
}

func boolPtr(value bool) *bool {
	return &value
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, values := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		out[key] = append([]string(nil), values...)
	}
	return out
}

func NormalizeWebChatInteractionVisibility(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case "", "full":
		return "full", nil
	case "simple", "minimal", "off":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid WEBCHAT_INTERACTION_VISIBILITY %q", value)
	}
}

func NormalizeWebChatHistoryScope(value string) (string, error) {
	scope := strings.ToLower(strings.TrimSpace(value))
	switch scope {
	case "", "session":
		return "session", nil
	case "user", "linked_channels":
		return scope, nil
	default:
		return "", fmt.Errorf("invalid WEBCHAT_HISTORY_SCOPE %q", value)
	}
}

func validateProductionConfig(cfg Config) error {
	if !strings.EqualFold(strings.TrimSpace(cfg.Environment), "production") {
		return nil
	}
	if strings.TrimSpace(cfg.AdminBearerToken) == "" {
		return fmt.Errorf("ADMIN_BEARER_TOKEN is required when NEXUS_ENV=production")
	}
	defaultSecrets := map[string]string{
		"SLACK_SIGNING_SECRET":    "dev-secret",
		"WHATSAPP_VERIFY_TOKEN":   "dev-whatsapp-verify",
		"EMAIL_WEBHOOK_SECRET":    "dev-email-secret",
		"TELEGRAM_WEBHOOK_SECRET": "dev-telegram-secret",
	}
	values := map[string]string{
		"SLACK_SIGNING_SECRET":    cfg.SlackSigningSecret,
		"WHATSAPP_VERIFY_TOKEN":   cfg.WhatsAppVerifyToken,
		"EMAIL_WEBHOOK_SECRET":    cfg.EmailWebhookSecret,
		"TELEGRAM_WEBHOOK_SECRET": cfg.TelegramWebhookSecret,
	}
	for key, defaultValue := range defaultSecrets {
		if values[key] == defaultValue {
			return fmt.Errorf("%s must not use the development default when NEXUS_ENV=production", key)
		}
	}
	return nil
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	if value := os.Getenv(key); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, err
		}
		return n, nil
	}
	return fallback, nil
}

func mustEnvIntDefault(key string, fallback int) int {
	value, err := envInt(key, fallback)
	if err != nil {
		return fallback
	}
	return value
}

func mustEnvInt64Default(key string, fallback int64) int64 {
	value, err := envInt64(key, fallback)
	if err != nil {
		return fallback
	}
	return value
}

func envInt64(key string, fallback int64) (int64, error) {
	if value := os.Getenv(key); value != "" {
		return strconv.ParseInt(value, 10, 64)
	}
	return fallback, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	if value := os.Getenv(key); value != "" {
		return strconv.ParseFloat(value, 64)
	}
	return fallback, nil
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func csvEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func prefixedEnv(prefix string) []string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	out := make([]string, 0)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, prefix) {
			out = append(out, item[len(prefix):])
		}
	}
	return out
}
