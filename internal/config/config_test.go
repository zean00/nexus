package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRetentionDefaults(t *testing.T) {
	t.Setenv("RETENTION_ENABLED", "")
	t.Setenv("RETENTION_INTERVAL_SECONDS", "")
	t.Setenv("RETENTION_BATCH_SIZE", "")
	t.Setenv("RETENTION_DEFAULT_PAYLOAD_DAYS", "")
	t.Setenv("RETENTION_DEFAULT_ARTIFACT_DAYS", "")
	t.Setenv("RETENTION_DEFAULT_AUDIT_DAYS", "")
	t.Setenv("RETENTION_RELATIONAL_GRACE_DAYS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetentionEnabled {
		t.Fatal("expected retention disabled by default")
	}
	if cfg.RetentionBatchSize != 500 {
		t.Fatalf("expected retention batch size 500, got %d", cfg.RetentionBatchSize)
	}
	if cfg.RetentionPayloadDays != 30 || cfg.RetentionArtifactDays != 30 || cfg.RetentionAuditDays != 30 || cfg.RetentionGraceDays != 30 {
		t.Fatalf("unexpected retention day defaults: %+v", cfg)
	}
}

func TestLoadHTTPTimeoutDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPReadHeaderTimeout != 5*time.Second || cfg.HTTPReadTimeout != 30*time.Second || cfg.HTTPWriteTimeout != 120*time.Second || cfg.HTTPIdleTimeout != 120*time.Second {
		t.Fatalf("unexpected HTTP timeout defaults: %+v", cfg)
	}
}

func TestLoadProductionRequiresAdminBearerToken(t *testing.T) {
	t.Setenv("NEXUS_ENV", "production")
	t.Setenv("SLACK_SIGNING_SECRET", "slack-secret")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "whatsapp-secret")
	t.Setenv("EMAIL_WEBHOOK_SECRET", "email-secret")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "telegram-secret")
	t.Setenv("ADMIN_BEARER_TOKEN", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ADMIN_BEARER_TOKEN") {
		t.Fatalf("expected ADMIN_BEARER_TOKEN error, got %v", err)
	}
}

func TestLoadTrimsAdminBearerToken(t *testing.T) {
	t.Setenv("ADMIN_BEARER_TOKEN", "  admin-secret  ")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminBearerToken != "admin-secret" {
		t.Fatalf("expected trimmed admin bearer token, got %q", cfg.AdminBearerToken)
	}
}

func TestLoadWebChatDevAuthFlag(t *testing.T) {
	t.Setenv("WEBCHAT_DEV_AUTH", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WebChatDevAuth {
		t.Fatal("expected webchat dev auth to be enabled")
	}
}

func TestLoadWebChatInteractionVisibilityDefault(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebChatInteractionVisibility != "full" {
		t.Fatalf("expected full visibility by default, got %q", cfg.WebChatInteractionVisibility)
	}
}

func TestLoadWebChatHistoryScopeDefault(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebChatHistoryScope != "linked_channels" {
		t.Fatalf("expected linked_channels history scope by default, got %q", cfg.WebChatHistoryScope)
	}
}

func TestLoadWebChatHistoryScopeLinkedChannels(t *testing.T) {
	t.Setenv("WEBCHAT_HISTORY_SCOPE", "linked_channels")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebChatHistoryScope != "linked_channels" {
		t.Fatalf("expected linked_channels scope, got %q", cfg.WebChatHistoryScope)
	}
}

func TestLoadRejectsInvalidWebChatHistoryScope(t *testing.T) {
	t.Setenv("WEBCHAT_HISTORY_SCOPE", "everything")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "WEBCHAT_HISTORY_SCOPE") {
		t.Fatalf("expected history scope error, got %v", err)
	}
}

func TestLoadRejectsInvalidWebChatInteractionVisibility(t *testing.T) {
	t.Setenv("WEBCHAT_INTERACTION_VISIBILITY", "verbose")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "WEBCHAT_INTERACTION_VISIBILITY") {
		t.Fatalf("expected visibility mode error, got %v", err)
	}
}

func TestLoadProductionRejectsDevWebhookSecrets(t *testing.T) {
	t.Setenv("NEXUS_ENV", "production")
	t.Setenv("ADMIN_BEARER_TOKEN", "admin-secret")
	t.Setenv("SLACK_SIGNING_SECRET", "slack-secret")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "whatsapp-secret")
	t.Setenv("EMAIL_WEBHOOK_SECRET", "email-secret")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "dev-telegram-secret")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_WEBHOOK_SECRET") {
		t.Fatalf("expected TELEGRAM_WEBHOOK_SECRET error, got %v", err)
	}
}
