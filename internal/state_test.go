package internal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateCache_PutAndGet(t *testing.T) {
	// Create a temp directory for the cache
	tmpDir, err := os.MkdirTemp("", "tfsync-cache-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cache := NewStateCache(filepath.Join(tmpDir, "cache"))

	sourceDir := "/path/to/source"
	initCfg := &InitConfig{
		Backend: map[string]string{
			"bucket": "my-bucket",
			"key":    "terraform.tfstate",
		},
	}
	stateData := []byte(`{"version": 4, "resources": []}`)
	resources := []string{"aws_instance.foo", "aws_s3_bucket.bar"}

	// Put state into cache
	if err := cache.Put(sourceDir, initCfg, stateData, resources); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get state from cache
	cached, err := cache.Get(sourceDir, initCfg)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if cached == nil {
		t.Fatal("expected cached state, got nil")
	}

	// Verify cached data
	if cached.SourceDir != sourceDir {
		t.Errorf("expected SourceDir %q, got %q", sourceDir, cached.SourceDir)
	}
	if string(cached.StateData) != string(stateData) {
		t.Errorf("expected StateData %q, got %q", stateData, cached.StateData)
	}
	if len(cached.Resources) != len(resources) {
		t.Errorf("expected %d resources, got %d", len(resources), len(cached.Resources))
	}
	if time.Since(cached.CachedAt) > time.Minute {
		t.Errorf("CachedAt seems wrong: %v", cached.CachedAt)
	}
}

func TestStateCache_GetNonExistent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tfsync-cache-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cache := NewStateCache(filepath.Join(tmpDir, "cache"))

	// Get non-existent state should return nil without error
	cached, err := cache.Get("/nonexistent/path", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if cached != nil {
		t.Fatal("expected nil for non-existent cache entry")
	}
}

func TestStateCache_CacheKeyDiffersByBackend(t *testing.T) {
	cache := NewStateCache("")

	sourceDir := "/path/to/source"

	// Same source dir, different backend configs should produce different keys
	key1 := cache.CacheKey(sourceDir, &InitConfig{
		Backend: map[string]string{"bucket": "bucket1"},
	})
	key2 := cache.CacheKey(sourceDir, &InitConfig{
		Backend: map[string]string{"bucket": "bucket2"},
	})

	if key1 == key2 {
		t.Errorf("expected different cache keys for different backends, got %q for both", key1)
	}
}

func TestStateCache_CacheKeyNilConfig(t *testing.T) {
	cache := NewStateCache("")

	sourceDir := "/path/to/source"

	// Should not panic with nil config
	key := cache.CacheKey(sourceDir, nil)
	if key == "" {
		t.Error("expected non-empty cache key")
	}
}

func TestStateCache_Clear(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tfsync-cache-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cache := NewStateCache(filepath.Join(tmpDir, "cache"))

	sourceDir := "/path/to/source"
	stateData := []byte(`{}`)

	// Put then clear
	if err := cache.Put(sourceDir, nil, stateData, nil); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if err := cache.Clear(sourceDir, nil); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// Should be gone
	cached, err := cache.Get(sourceDir, nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if cached != nil {
		t.Fatal("expected nil after Clear")
	}
}

func TestStateCache_ClearAll(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tfsync-cache-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewStateCache(cacheDir)

	// Put multiple entries
	if err := cache.Put("/path/1", nil, []byte(`{}`), nil); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if err := cache.Put("/path/2", nil, []byte(`{}`), nil); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Clear all
	if err := cache.ClearAll(); err != nil {
		t.Fatalf("ClearAll failed: %v", err)
	}

	// Cache directory should be gone
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("expected cache directory to be removed")
	}
}

func TestNewCopierWithCache(t *testing.T) {
	cache := NewStateCache("")
	copier := NewCopierWithCache("tofu", cache)

	if copier.GetCache() != cache {
		t.Error("expected copier to have the cache set")
	}
}

func TestCopier_SetCache(t *testing.T) {
	copier := NewCopier("tofu")

	if copier.GetCache() != nil {
		t.Error("expected nil cache initially")
	}

	cache := NewStateCache("")
	copier.SetCache(cache)

	if copier.GetCache() != cache {
		t.Error("expected cache to be set")
	}
}
