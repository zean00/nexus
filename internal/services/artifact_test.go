package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexus/internal/adapters/storage"
)

func TestArtifactServiceSaveInbound(t *testing.T) {
	root := t.TempDir()
	store := storage.New("file://" + root)
	svc := ArtifactService{Store: store}

	artifact, err := svc.SaveInbound(context.Background(), "../report.txt", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Name != "report.txt" {
		t.Fatalf("expected sanitized filename report.txt, got %s", artifact.Name)
	}
	if !strings.HasPrefix(artifact.StorageURI, "file://") {
		t.Fatalf("expected file uri, got %s", artifact.StorageURI)
	}
	target := strings.TrimPrefix(artifact.StorageURI, "file://")
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(target, filepath.Clean(root)) {
		t.Fatalf("artifact path escaped root: %s", target)
	}
}
