package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ObjectStore struct {
	BaseURL string
	rootDir string
}

func New(baseURL string) ObjectStore {
	store := ObjectStore{BaseURL: baseURL}
	if strings.HasPrefix(baseURL, "file://") {
		store.rootDir = strings.TrimPrefix(baseURL, "file://")
	}
	return store
}

func (s ObjectStore) Save(_ context.Context, objectKey string, content []byte) (string, error) {
	if s.rootDir == "" {
		return "", fmt.Errorf("only file:// object storage is implemented")
	}
	target := filepath.Join(s.rootDir, objectKey)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return "", err
	}
	return "file://" + target, nil
}

func (s ObjectStore) Delete(_ context.Context, storageURI string) error {
	if s.rootDir == "" {
		return fmt.Errorf("only file:// object storage is implemented")
	}
	target := strings.TrimSpace(storageURI)
	if !strings.HasPrefix(target, "file://") {
		return fmt.Errorf("unsupported storage uri %q", storageURI)
	}
	target = strings.TrimPrefix(target, "file://")
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s ObjectStore) Read(_ context.Context, storageURI string) ([]byte, error) {
	if s.rootDir == "" {
		return nil, fmt.Errorf("only file:// object storage is implemented")
	}
	target := strings.TrimSpace(storageURI)
	if !strings.HasPrefix(target, "file://") {
		return nil, fmt.Errorf("unsupported storage uri %q", storageURI)
	}
	target = strings.TrimPrefix(target, "file://")
	return os.ReadFile(target)
}
