package config

import "testing"

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
