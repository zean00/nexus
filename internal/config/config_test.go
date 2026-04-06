package config

import "testing"

func TestLoadDefaultsToStrictACPImplementation(t *testing.T) {
	t.Setenv("ACP_IMPLEMENTATION", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ACPImplementation != "strict" {
		t.Fatalf("expected default ACP implementation to remain strict, got %q", cfg.ACPImplementation)
	}
}
