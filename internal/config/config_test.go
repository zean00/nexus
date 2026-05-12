package config

import (
	"os"
	"path/filepath"
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

func TestLoadYAMLConfigFileWithEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nexus.yaml")
	if err := os.WriteFile(path, []byte(`
default_tenant_id: tenant_file
database_url: postgres://file-db
webchat_history_scope: linked_channels
acp:
  mode: multiple
  connections:
    - id: primary
      implementation: strict
      base_url: http://file-acp
      enabled: true
  agent_profiles:
    - id: support
      connection_id: primary
      agent_name: support-agent
routing:
  default_agent_by_channel:
    webchat: support
  allowed_agents_by_channel:
    webchat: [support]
webchat:
  identities:
    - id: support_chat
      path: support
      agent_profile_id: support
      title: Support
whatsapp_web:
  group_allowlist: ["120363111@g.us"]
  group_blocklist: ["120363222@g.us"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_CONFIG_PATH", path)
	t.Setenv("DEFAULT_TENANT_ID", "tenant_env")
	t.Setenv("DATABASE_URL", "postgres://env-db")
	t.Setenv("WEBCHAT_HISTORY_SCOPE", "session")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultTenantID != "tenant_env" {
		t.Fatalf("expected env override, got %q", cfg.DefaultTenantID)
	}
	if cfg.DatabaseURL != "postgres://env-db" || cfg.WebChatHistoryScope != "session" {
		t.Fatalf("expected env-backed fields to override file, got db=%q scope=%q", cfg.DatabaseURL, cfg.WebChatHistoryScope)
	}
	if cfg.ACPMode != "multiple" || len(cfg.ACPConnections) != 1 || cfg.ACPConnections[0].BaseURL != "http://file-acp" {
		t.Fatalf("unexpected acp config: %+v", cfg)
	}
	if cfg.AgentRouting.DefaultAgentByChannel["webchat"] != "support" {
		t.Fatalf("expected webchat default route, got %+v", cfg.AgentRouting.DefaultAgentByChannel)
	}
	if len(cfg.WebChatIdentities) != 1 || cfg.WebChatIdentities[0].ID != "support_chat" || cfg.WebChatIdentities[0].Path != "support" {
		t.Fatalf("expected webchat identity config, got %+v", cfg.WebChatIdentities)
	}
	if len(cfg.WhatsAppWebGroupAllowlist) != 1 || cfg.WhatsAppWebGroupAllowlist[0] != "120363111@g.us" {
		t.Fatalf("expected whatsapp group allowlist, got %+v", cfg.WhatsAppWebGroupAllowlist)
	}
	if len(cfg.WhatsAppWebGroupBlocklist) != 1 || cfg.WhatsAppWebGroupBlocklist[0] != "120363222@g.us" {
		t.Fatalf("expected whatsapp group blocklist, got %+v", cfg.WhatsAppWebGroupBlocklist)
	}
}

func TestLoadJSONConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nexus.json")
	if err := os.WriteFile(path, []byte(`{
		"acp": {
			"mode": "multiple",
			"connections": [{"id":"primary","implementation":"strict","base_url":"http://acp","enabled":true}],
			"agent_profiles": [{"id":"support","connection_id":"primary","agent_name":"support-agent"}]
		},
		"routing": {"default_agent_by_channel": {"webchat":"support","telegram":"support"}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ACPMode != "multiple" || cfg.AgentRouting.DefaultAgentByChannel["telegram"] != "support" {
		t.Fatalf("unexpected json config: %+v", cfg)
	}
}

func TestLoadRejectsDuplicateWebChatIdentityPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nexus.yaml")
	if err := os.WriteFile(path, []byte(`
acp:
  mode: multiple
  connections:
    - id: primary
      implementation: strict
      base_url: http://acp
      enabled: true
  agent_profiles:
    - id: support
      connection_id: primary
      agent_name: support-agent
routing:
  default_agent_by_channel:
    webchat: support
webchat:
  identities:
    - id: support
      path: help
      agent_profile_id: support
    - id: product
      path: help
      agent_profile_id: support
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_CONFIG_PATH", path)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "duplicate webchat identity path") {
		t.Fatalf("expected duplicate path error, got %v", err)
	}
}
