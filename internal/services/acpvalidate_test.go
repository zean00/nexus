package services

import (
	"testing"

	"nexus/internal/domain"
)

func TestValidateAgentCompatibility(t *testing.T) {
	compat := ValidateAgentCompatibility([]domain.AgentManifest{{
		Name:                "coder",
		SupportsAwaitResume: true,
		SupportsStreaming:   true,
		SupportsArtifacts:   true,
		Healthy:             true,
	}}, "coder")
	if !compat.Compatible {
		t.Fatalf("expected compatible agent, got reasons=%v", compat.Reasons)
	}

	incompat := ValidateAgentCompatibility([]domain.AgentManifest{{
		Name:                "weak-agent",
		SupportsAwaitResume: true,
		SupportsStreaming:   false,
		SupportsArtifacts:   false,
		Healthy:             false,
	}}, "weak-agent")
	if incompat.Compatible {
		t.Fatal("expected incompatible agent")
	}
	if len(incompat.Reasons) != 3 {
		t.Fatalf("expected 3 incompatibility reasons, got %d", len(incompat.Reasons))
	}
}
