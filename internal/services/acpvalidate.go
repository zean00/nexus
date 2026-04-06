package services

import "nexus/internal/domain"

func ValidateAgentCompatibility(manifests []domain.AgentManifest, agentName string) domain.AgentCompatibility {
	result := domain.AgentCompatibility{
		AgentName:      agentName,
		ValidationMode: "strict_acp",
		Reasons:        []string{},
		Warnings:       []string{},
	}
	for _, manifest := range manifests {
		if manifest.Name != agentName {
			continue
		}
		result.Manifest = manifest
		if manifest.Protocol == "opencode" {
			result.ValidationMode = "opencode_bridge"
		}
		if !manifest.Healthy {
			result.Reasons = append(result.Reasons, "agent is unhealthy")
		}
		if !manifest.SupportsSessionReload {
			result.Reasons = append(result.Reasons, "session reload not supported")
		}
		if result.ValidationMode == "strict_acp" && !manifest.SupportsAwaitResume {
			result.Reasons = append(result.Reasons, "await/resume not supported")
		}
		if result.ValidationMode == "opencode_bridge" && !manifest.SupportsAwaitResume {
			result.Warnings = append(result.Warnings, "structured await/resume is not natively supported by OpenCode")
		}
		if result.ValidationMode == "opencode_bridge" && !manifest.SupportsStructuredAwait {
			result.Warnings = append(result.Warnings, "structured awaits are bridged through follow-up messages")
		}
		if !manifest.SupportsStreaming {
			result.Reasons = append(result.Reasons, "streaming not supported")
		}
		if !manifest.SupportsArtifacts {
			result.Reasons = append(result.Reasons, "artifacts not supported")
		}
		result.Compatible = len(result.Reasons) == 0
		return result
	}
	result.Reasons = append(result.Reasons, "agent manifest not found")
	return result
}
