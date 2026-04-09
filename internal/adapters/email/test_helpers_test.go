package email

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/services"
)

type noopArtifactStore struct {
	dir string
}

func (s noopArtifactStore) Save(_ context.Context, objectKey string, content []byte) (string, error) {
	target := filepath.Join(s.dir, objectKey)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return "", err
	}
	return "file://" + target, nil
}

func (s noopArtifactStore) Read(_ context.Context, storageURI string) ([]byte, error) {
	return os.ReadFile(strings.TrimPrefix(storageURI, "file://"))
}

func (s noopArtifactStore) SaveInbound(ctx context.Context, filename, mimeType string, content []byte) (domain.Artifact, error) {
	return services.ArtifactService{Store: s}.SaveInbound(ctx, filename, mimeType, content)
}
