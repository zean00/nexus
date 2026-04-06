package services

import (
	"testing"

	"nexus/internal/domain"
)

func TestValidateAgentCompatibility(t *testing.T) {
	compat := ValidateAgentCompatibility([]domain.AgentManifest{{
		Name:                    "coder",
		SupportsAwaitResume:     true,
		SupportsStructuredAwait: true,
		SupportsSessionReload:   true,
		SupportsStreaming:       true,
		SupportsArtifacts:       true,
		Healthy:                 true,
	}}, "coder")
	if !compat.Compatible {
		t.Fatalf("expected compatible agent, got reasons=%v", compat.Reasons)
	}
	if compat.ValidationMode != "strict_acp" {
		t.Fatalf("expected strict_acp validation mode, got %+v", compat)
	}

	incompat := ValidateAgentCompatibility([]domain.AgentManifest{{
		Name:                    "weak-agent",
		SupportsAwaitResume:     true,
		SupportsStructuredAwait: true,
		SupportsSessionReload:   false,
		SupportsStreaming:       false,
		SupportsArtifacts:       false,
		Healthy:                 false,
	}}, "weak-agent")
	if incompat.Compatible {
		t.Fatal("expected incompatible agent")
	}
	if len(incompat.Reasons) != 4 {
		t.Fatalf("expected 4 incompatibility reasons, got %d", len(incompat.Reasons))
	}
}

func TestValidateOpenCodeAgentCompatibility(t *testing.T) {
	compat := ValidateAgentCompatibility([]domain.AgentManifest{{
		Name:                    "build",
		Protocol:                "opencode",
		SupportsAwaitResume:     false,
		SupportsStructuredAwait: false,
		SupportsSessionReload:   true,
		SupportsStreaming:       true,
		SupportsArtifacts:       true,
		Healthy:                 true,
	}}, "build")
	if !compat.Compatible {
		t.Fatalf("expected OpenCode bridge agent to be compatible, got %+v", compat)
	}
	if compat.ValidationMode != "opencode_bridge" {
		t.Fatalf("expected opencode_bridge validation mode, got %+v", compat)
	}
	if len(compat.Warnings) == 0 {
		t.Fatalf("expected OpenCode bridge warnings, got %+v", compat)
	}
}

func TestValidateRejectsMissingSessionReload(t *testing.T) {
	compat := ValidateAgentCompatibility([]domain.AgentManifest{{
		Name:                    "stdio-no-reload",
		SupportsAwaitResume:     true,
		SupportsStructuredAwait: true,
		SupportsSessionReload:   false,
		SupportsStreaming:       true,
		SupportsArtifacts:       true,
		Healthy:                 true,
	}}, "stdio-no-reload")
	if compat.Compatible {
		t.Fatalf("expected missing session reload to be incompatible, got %+v", compat)
	}
	if len(compat.Reasons) == 0 || compat.Reasons[0] != "session reload not supported" {
		t.Fatalf("expected session reload reason, got %+v", compat.Reasons)
	}
}
