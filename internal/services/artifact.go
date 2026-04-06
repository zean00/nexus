package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"nexus/internal/domain"
)

type ArtifactStore interface {
	Save(ctx context.Context, objectKey string, content []byte) (string, error)
}

type ArtifactService struct {
	Store ArtifactStore
}

func (s ArtifactService) SaveInbound(ctx context.Context, filename, mimeType string, content []byte) (domain.Artifact, error) {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	objectKey := filepath.Join(time.Now().UTC().Format("2006/01/02"), hash[:2], hash+"_"+filepath.Base(filename))
	storageURI, err := s.Store.Save(ctx, objectKey, content)
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("save artifact: %w", err)
	}
	return domain.Artifact{
		ID:         "artifact_" + hash[:16],
		Name:       filepath.Base(filename),
		MIMEType:   mimeType,
		SizeBytes:  int64(len(content)),
		SHA256:     hash,
		StorageURI: storageURI,
	}, nil
}
