package services

import "nexus/internal/domain"

func ValidateAgentCompatibility(manifests []domain.AgentManifest, agentName string) domain.AgentCompatibility {
	result := domain.AgentCompatibility{
		AgentName: agentName,
		Reasons:   []string{},
	}
	for _, manifest := range manifests {
		if manifest.Name != agentName {
			continue
		}
		result.Manifest = manifest
		if !manifest.Healthy {
			result.Reasons = append(result.Reasons, "agent is unhealthy")
		}
		if !manifest.SupportsAwaitResume {
			result.Reasons = append(result.Reasons, "await/resume not supported")
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
