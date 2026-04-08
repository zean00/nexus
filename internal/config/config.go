package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ServiceName                   string
	Environment                   string
	HTTPAddr                      string
	AdminAddr                     string
	AdminBearerToken              string
	HTTPReadHeaderTimeout         time.Duration
	HTTPReadTimeout               time.Duration
	HTTPWriteTimeout              time.Duration
	HTTPIdleTimeout               time.Duration
	DatabaseURL                   string
	ACPImplementation             string
	ACPBaseURL                    string
	ACPToken                      string
	ACPCommand                    string
	ACPArgs                       []string
	ACPEnv                        []string
	ACPWorkdir                    string
	ACPStartupTimeout             time.Duration
	ACPRPCTimeout                 time.Duration
	DefaultACPAgentName           string
	ValidateACPOnStartup          bool
	ACPManifestCacheTTL           time.Duration
	SlackSigningSecret            string
	SlackBotToken                 string
	WhatsAppVerifyToken           string
	WhatsAppAccessToken           string
	WhatsAppAppSecret             string
	WhatsAppPhoneNumberID         string
	WhatsAppAPIBaseURL            string
	EmailWebhookSecret            string
	EmailSMTPAddr                 string
	EmailSMTPUsername             string
	EmailSMTPPassword             string
	EmailFromAddress              string
	WebChatCookieName             string
	WebChatDevAuth                bool
	WebChatSessionHours           int
	WebChatOTPMinutes             int
	IdentityLinkMinutes           int
	StepUpOTPMinutes              int
	StepUpWindowMinutes           int
	RequireLinkedIdentity         bool
	RequireRecentStepUp           bool
	AllowedApprovalChannels       []string
	OTLPEndpoint                  string
	OTELSampleRatio               float64
	RetryMaxAttempts              int
	RetryBaseDelayMS              int
	CircuitBreakerFailures        int
	CircuitBreakerCoolDownSeconds int
	WhatsAppMediaMaxBytes         int64
	EmailWebhookMaxSkewSeconds    int
	EmailMaxAttachmentBytes       int64
	EmailMaxAttachments           int
	ObjectStorageBaseURL          string
	WorkerPollInterval            time.Duration
	ReconcilerInterval            time.Duration
	OutboxClaimTimeout            time.Duration
	QueueStartingTimeout          time.Duration
	RunStaleTimeout               time.Duration
	DeliverySendingTimeout        time.Duration
	DeliveryMaxAttempts           int
	RetentionEnabled              bool
	RetentionInterval             time.Duration
	RetentionBatchSize            int
	RetentionPayloadDays          int
	RetentionArtifactDays         int
	RetentionAuditDays            int
	RetentionGraceDays            int
	TelegramBotToken              string
	TelegramWebhookSecret         string
	TelegramAllowedUserIDs        []string
	DefaultTenantID               string
	DefaultAgentProfileID         string
}

func Load() (Config, error) {
	cfg := Config{
		ServiceName:                   env("SERVICE_NAME", "nexus-gateway"),
		Environment:                   env("NEXUS_ENV", "development"),
		HTTPAddr:                      env("HTTP_ADDR", ":8080"),
		AdminAddr:                     env("ADMIN_ADDR", ":8081"),
		AdminBearerToken:              strings.TrimSpace(os.Getenv("ADMIN_BEARER_TOKEN")),
		DatabaseURL:                   env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable"),
		ACPImplementation:             env("ACP_IMPLEMENTATION", "strict"),
		ACPBaseURL:                    env("ACP_BASE_URL", "http://localhost:8090"),
		ACPToken:                      os.Getenv("ACP_TOKEN"),
		ACPCommand:                    env("ACP_COMMAND", "opencode"),
		ACPArgs:                       csvEnv("ACP_ARGS"),
		ACPEnv:                        prefixedEnv("ACP_ENV_"),
		ACPWorkdir:                    env("ACP_WORKDIR", mustGetwd()),
		DefaultACPAgentName:           env("DEFAULT_ACP_AGENT_NAME", "default-agent"),
		SlackSigningSecret:            env("SLACK_SIGNING_SECRET", "dev-secret"),
		SlackBotToken:                 os.Getenv("SLACK_BOT_TOKEN"),
		WhatsAppVerifyToken:           env("WHATSAPP_VERIFY_TOKEN", "dev-whatsapp-verify"),
		WhatsAppAccessToken:           os.Getenv("WHATSAPP_ACCESS_TOKEN"),
		WhatsAppAppSecret:             os.Getenv("WHATSAPP_APP_SECRET"),
		WhatsAppPhoneNumberID:         os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		WhatsAppAPIBaseURL:            env("WHATSAPP_API_BASE_URL", "https://graph.facebook.com/v20.0"),
		EmailWebhookSecret:            env("EMAIL_WEBHOOK_SECRET", "dev-email-secret"),
		EmailSMTPAddr:                 os.Getenv("EMAIL_SMTP_ADDR"),
		EmailSMTPUsername:             os.Getenv("EMAIL_SMTP_USERNAME"),
		EmailSMTPPassword:             os.Getenv("EMAIL_SMTP_PASSWORD"),
		EmailFromAddress:              env("EMAIL_FROM_ADDRESS", "nexus@example.com"),
		WebChatCookieName:             env("WEBCHAT_COOKIE_NAME", "nexus_webchat_session"),
		WebChatDevAuth:                envBool("WEBCHAT_DEV_AUTH", false),
		IdentityLinkMinutes:           mustEnvIntDefault("IDENTITY_LINK_MINUTES", 10),
		StepUpOTPMinutes:              mustEnvIntDefault("STEP_UP_OTP_MINUTES", 10),
		StepUpWindowMinutes:           mustEnvIntDefault("STEP_UP_WINDOW_MINUTES", 15),
		RequireLinkedIdentity:         envBool("REQUIRE_LINKED_IDENTITY", false),
		RequireRecentStepUp:           envBool("REQUIRE_RECENT_STEP_UP", false),
		AllowedApprovalChannels:       csvEnv("ALLOWED_APPROVAL_CHANNELS"),
		OTLPEndpoint:                  os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		RetryMaxAttempts:              mustEnvIntDefault("RETRY_MAX_ATTEMPTS", 3),
		RetryBaseDelayMS:              mustEnvIntDefault("RETRY_BASE_DELAY_MS", 200),
		CircuitBreakerFailures:        mustEnvIntDefault("CIRCUIT_BREAKER_FAILURES", 5),
		CircuitBreakerCoolDownSeconds: mustEnvIntDefault("CIRCUIT_BREAKER_COOLDOWN_SECONDS", 30),
		WhatsAppMediaMaxBytes:         mustEnvInt64Default("WHATSAPP_MEDIA_MAX_BYTES", 10<<20),
		EmailWebhookMaxSkewSeconds:    mustEnvIntDefault("EMAIL_WEBHOOK_MAX_SKEW_SECONDS", 300),
		EmailMaxAttachmentBytes:       mustEnvInt64Default("EMAIL_MAX_ATTACHMENT_BYTES", 10<<20),
		EmailMaxAttachments:           mustEnvIntDefault("EMAIL_MAX_ATTACHMENTS", 10),
		TelegramBotToken:              os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramWebhookSecret:         env("TELEGRAM_WEBHOOK_SECRET", "dev-telegram-secret"),
		TelegramAllowedUserIDs:        csvEnv("TELEGRAM_ALLOWED_USER_IDS"),
		ObjectStorageBaseURL:          env("OBJECT_STORAGE_BASE_URL", "file:///tmp/nexus-objects"),
		DefaultTenantID:               env("DEFAULT_TENANT_ID", "tenant_default"),
		DefaultAgentProfileID:         env("DEFAULT_AGENT_PROFILE_ID", "agent_profile_default"),
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
	if err := validateProductionConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
	if value := os.Getenv(key); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err == nil {
			return n
		}
	}
	return fallback
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
