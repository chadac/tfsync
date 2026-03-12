package internal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// DefaultCacheDir is the default directory for cached state files.
	DefaultCacheDir = ".tfsync/cache"
)

// StateCache stores cached terraform state to avoid repeated init/refresh.
// It maintains both an in-memory cache (for current session) and a disk cache
// (for persistence across runs).
type StateCache struct {
	// CacheDir is the directory where cached state is stored on disk.
	CacheDir string
	// memory holds in-memory cached states for the current session.
	// Key is the cache key (hash of sourceDir + initCfg).
	memory map[string]*CachedState
}

// CachedState represents cached state data with metadata.
type CachedState struct {
	// SourceDir is the absolute path to the source directory.
	SourceDir string `json:"source_dir"`
	// CachedAt is when the state was cached.
	CachedAt time.Time `json:"cached_at"`
	// StateData is the raw terraform state JSON.
	StateData []byte `json:"state_data"`
	// Resources is the list of resource addresses in the state.
	Resources []string `json:"resources"`
}

// NewStateCache creates a new state cache in the given directory.
// If cacheDir is empty, uses DefaultCacheDir relative to current directory.
func NewStateCache(cacheDir string) *StateCache {
	if cacheDir == "" {
		cacheDir = DefaultCacheDir
	}
	return &StateCache{
		CacheDir: cacheDir,
		memory:   make(map[string]*CachedState),
	}
}

// CacheKey generates a unique cache key for a source directory.
func (sc *StateCache) CacheKey(sourceDir string, initCfg *InitConfig) string {
	h := sha256.New()
	h.Write([]byte(sourceDir))
	if initCfg != nil {
		// Include backend config in cache key so different backends don't collide
		// Sort keys for deterministic hash
		keys := make([]string, 0, len(initCfg.Backend))
		for k := range initCfg.Backend {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			h.Write([]byte(k + "=" + initCfg.Backend[k]))
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// CachePath returns the path to the cache file for a given key.
func (sc *StateCache) CachePath(key string) string {
	return filepath.Join(sc.CacheDir, key+".json")
}

// Get retrieves cached state if it exists.
// Checks in-memory cache first, then disk cache.
func (sc *StateCache) Get(sourceDir string, initCfg *InitConfig) (*CachedState, error) {
	key := sc.CacheKey(sourceDir, initCfg)

	// Check in-memory cache first
	if cached, ok := sc.memory[key]; ok {
		return cached, nil
	}

	// Check disk cache
	cachePath := sc.CachePath(key)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No cache, not an error
		}
		return nil, fmt.Errorf("failed to read cache: %w", err)
	}

	var cached CachedState
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("failed to parse cache: %w", err)
	}

	// Store in memory for faster subsequent access
	sc.memory[key] = &cached

	return &cached, nil
}

// Put stores state in both in-memory and disk cache.
func (sc *StateCache) Put(sourceDir string, initCfg *InitConfig, stateData []byte, resources []string) error {
	key := sc.CacheKey(sourceDir, initCfg)

	cached := &CachedState{
		SourceDir: sourceDir,
		CachedAt:  time.Now(),
		StateData: stateData,
		Resources: resources,
	}

	// Store in memory
	sc.memory[key] = cached

	// Store on disk
	if err := os.MkdirAll(sc.CacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache dir: %w", err)
	}

	cachePath := sc.CachePath(key)
	data, err := json.Marshal(cached)
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %w", err)
	}

	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache: %w", err)
	}

	return nil
}

// Clear removes cached state for a source directory from both memory and disk.
func (sc *StateCache) Clear(sourceDir string, initCfg *InitConfig) error {
	key := sc.CacheKey(sourceDir, initCfg)

	// Clear from memory
	delete(sc.memory, key)

	// Clear from disk
	cachePath := sc.CachePath(key)
	if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove cache: %w", err)
	}
	return nil
}

// ClearAll removes all cached state from both memory and disk.
func (sc *StateCache) ClearAll() error {
	// Clear memory
	sc.memory = make(map[string]*CachedState)

	// Clear disk
	if err := os.RemoveAll(sc.CacheDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove cache dir: %w", err)
	}
	return nil
}

// Copier handles copying state between terraform configurations.
type Copier struct {
	binary string
	cache  *StateCache
	debug  bool
}

// NewCopier creates a new state copier without caching.
func NewCopier(binary string) *Copier {
	if binary == "" {
		binary = "tofu"
	}
	return &Copier{binary: binary}
}

// NewCopierWithCache creates a new state copier with caching enabled.
func NewCopierWithCache(binary string, cache *StateCache) *Copier {
	if binary == "" {
		binary = "tofu"
	}
	return &Copier{binary: binary, cache: cache}
}

// SetCache sets the cache for the copier.
func (c *Copier) SetCache(cache *StateCache) {
	c.cache = cache
}

// GetCache returns the cache for the copier.
func (c *Copier) GetCache() *StateCache {
	return c.cache
}

// SetDebug enables debug logging.
func (c *Copier) SetDebug(debug bool) {
	c.debug = debug
}

// PullResult contains the result of pulling source state.
type PullResult struct {
	// State is the cached state data
	State *CachedState
	// FromCache indicates if the state was loaded from cache (vs fresh pull)
	FromCache bool
}

// PullSourceState pulls state from a source terraform config and caches it.
// If forceRefresh is false and cached state exists, returns the cached state.
// This does NOT write state to any target - use WriteStateToTarget for that.
func (c *Copier) PullSourceState(ctx context.Context, sourceDir string, initCfg *InitConfig, forceRefresh bool) (*PullResult, error) {
	// Try to use cached state if not forcing refresh
	if c.cache != nil && !forceRefresh {
		cached, err := c.cache.Get(sourceDir, initCfg)
		if err != nil {
			// Cache error is non-fatal, continue with fresh pull
		} else if cached != nil {
			return &PullResult{
				State:     cached,
				FromCache: true,
			}, nil
		}
	}

	// Initialize source to connect to its backend
	sourceCLI := NewCLI(c.binary, sourceDir)
	sourceCLI.Debug = c.debug
	if err := sourceCLI.InitWithConfig(ctx, initCfg); err != nil {
		return nil, fmt.Errorf("failed to init source: %w", err)
	}

	// Pull state from source
	stateData, err := sourceCLI.StatePull(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to pull source state: %w", err)
	}

	// Get source resources for reporting
	sourceResources, err := sourceCLI.StateList(ctx)
	if err != nil {
		// Non-fatal, just won't have resource list
		sourceResources = nil
	}

	// Cache the state
	if c.cache != nil {
		if err := c.cache.Put(sourceDir, initCfg, stateData, sourceResources); err != nil {
			// Cache error is non-fatal
		}
	}

	// Get the cached state (which now has the CachedAt timestamp)
	cached, _ := c.cache.Get(sourceDir, initCfg)
	if cached == nil {
		// Fallback if cache didn't work
		cached = &CachedState{
			SourceDir: sourceDir,
			CachedAt:  time.Now(),
			StateData: stateData,
			Resources: sourceResources,
		}
	}

	return &PullResult{
		State:     cached,
		FromCache: false,
	}, nil
}

// WriteStateToTarget writes cached state data to a target directory.
// The state is loaded, deduplicated, and re-serialized to clean up any
// corrupted state files with duplicate resource instances.
func (c *Copier) WriteStateToTarget(cached *CachedState, targetDir string) error {
	localStatePath := filepath.Join(targetDir, "terraform.tfstate")

	// Load through StateFile to get deduplication, then re-serialize
	sf, err := LoadStateFromBytes(cached.StateData, localStatePath)
	if err != nil {
		// Fallback to writing raw bytes if we can't parse
		if writeErr := os.WriteFile(localStatePath, cached.StateData, 0644); writeErr != nil {
			return fmt.Errorf("failed to write local state: %w", writeErr)
		}
		return nil
	}

	// Write directly without incrementing serial (this is the initial write, not a mutation)
	data, err := json.MarshalIndent(sf.GetState(), "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}
	if err := os.WriteFile(localStatePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write local state: %w", err)
	}
	return nil
}

// CopyResult contains the result of a state copy operation.
type CopyResult struct {
	// SourceResources is the list of resources in the source state
	SourceResources []string
	// LocalStatePath is the path to the local state file created
	LocalStatePath string
	// FromCache indicates if the state was loaded from cache
	FromCache bool
	// CachedAt is when the state was cached (only set if FromCache is true)
	CachedAt time.Time
}

// CopyToLocal copies state from a source terraform config to a local state file.
// This pulls from the source's configured backend and writes to a local file.
// Deprecated: use CopyToLocalWithConfig instead.
func (c *Copier) CopyToLocal(ctx context.Context, sourceDir, targetDir string, backendConfig map[string]string) (*CopyResult, error) {
	var initCfg *InitConfig
	if len(backendConfig) > 0 {
		initCfg = &InitConfig{Backend: backendConfig}
	}
	return c.CopyToLocalWithConfig(ctx, sourceDir, targetDir, initCfg)
}

// CopyToLocalWithConfig copies state from a source terraform config to a local state file.
// This pulls from the source's configured backend and writes to a local file.
// If caching is enabled and forceRefresh is false, uses cached state if available.
func (c *Copier) CopyToLocalWithConfig(ctx context.Context, sourceDir, targetDir string, initCfg *InitConfig) (*CopyResult, error) {
	return c.CopyToLocalWithRefresh(ctx, sourceDir, targetDir, initCfg, false)
}

// CopyToLocalWithRefresh copies state with explicit cache control.
// If forceRefresh is true, always pulls fresh state even if cache exists.
func (c *Copier) CopyToLocalWithRefresh(ctx context.Context, sourceDir, targetDir string, initCfg *InitConfig, forceRefresh bool) (*CopyResult, error) {
	// Try to use cached state if caching is enabled and not forcing refresh
	if c.cache != nil && !forceRefresh {
		cached, err := c.cache.Get(sourceDir, initCfg)
		if err != nil {
			// Cache error is non-fatal, continue with fresh pull
		} else if cached != nil {
			// Use cached state
			localStatePath := filepath.Join(targetDir, "terraform.tfstate")
			if err := os.WriteFile(localStatePath, cached.StateData, 0644); err != nil {
				return nil, fmt.Errorf("failed to write local state from cache: %w", err)
			}

			return &CopyResult{
				SourceResources: cached.Resources,
				LocalStatePath:  localStatePath,
				FromCache:       true,
				CachedAt:        cached.CachedAt,
			}, nil
		}
	}

	// Initialize source to connect to its backend
	sourceCLI := NewCLI(c.binary, sourceDir)
	if err := sourceCLI.InitWithConfig(ctx, initCfg); err != nil {
		return nil, fmt.Errorf("failed to init source: %w", err)
	}

	// Pull state from source
	stateData, err := sourceCLI.StatePull(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to pull source state: %w", err)
	}

	// Get source resources for reporting
	sourceResources, err := sourceCLI.StateList(ctx)
	if err != nil {
		// Non-fatal, just won't have resource list
		sourceResources = nil
	}

	// Cache the state if caching is enabled
	if c.cache != nil {
		if err := c.cache.Put(sourceDir, initCfg, stateData, sourceResources); err != nil {
			// Cache error is non-fatal, just log/ignore
		}
	}

	// Write state to local file in target directory
	localStatePath := filepath.Join(targetDir, "terraform.tfstate")
	if err := os.WriteFile(localStatePath, stateData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write local state: %w", err)
	}

	return &CopyResult{
		SourceResources: sourceResources,
		LocalStatePath:  localStatePath,
		FromCache:       false,
	}, nil
}

// InitResult contains the result of target initialization.
type InitResult struct {
	// Skipped is true if init was skipped (already initialized).
	Skipped bool
	// Initialized is true if tofu init was run.
	Initialized bool
}

// InitTargetWithLocalBackend initializes the target directory with a local backend.
// This creates an override file to force local state.
func (c *Copier) InitTargetWithLocalBackend(ctx context.Context, targetDir string) error {
	result, err := c.InitTargetWithLocalBackendEx(ctx, targetDir, nil)
	if err != nil {
		return err
	}
	_ = result
	return nil
}

// StatusCallback is called with status updates during operations.
type StatusCallback func(status string)

// InitTargetWithLocalBackendEx initializes the target with status callbacks.
// If the target is already initialized with a local backend, init is skipped.
func (c *Copier) InitTargetWithLocalBackendEx(ctx context.Context, targetDir string, onStatus StatusCallback) (*InitResult, error) {
	notify := func(s string) {
		if onStatus != nil {
			onStatus(s)
		}
	}

	overridePath := filepath.Join(targetDir, "backend_override.tf")
	overrideContent := `# Auto-generated by tfsync - DO NOT COMMIT
terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}
`

	// Check if already initialized with local backend
	if c.isAlreadyInitialized(targetDir, overrideContent) {
		notify("already initialized")
		return &InitResult{Skipped: true}, nil
	}

	// Write backend override
	notify("writing backend override")
	if err := os.WriteFile(overridePath, []byte(overrideContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write backend override: %w", err)
	}

	// Initialize target with local backend.
	// Use -reconfigure to force switching to the local backend without
	// attempting to migrate state from the target's original backend.
	notify("running tofu init")
	targetCLI := NewCLI(c.binary, targetDir)
	targetCLI.Debug = c.debug
	if err := targetCLI.InitWithConfig(ctx, &InitConfig{ExtraArgs: []string{"-reconfigure"}}); err != nil {
		return nil, fmt.Errorf("failed to init target: %w", err)
	}

	return &InitResult{Initialized: true}, nil
}

// isAlreadyInitialized checks if the target is already initialized with our local backend.
func (c *Copier) isAlreadyInitialized(targetDir, expectedOverride string) bool {
	// Check for .terraform directory (indicates init has been run)
	terraformDir := filepath.Join(targetDir, ".terraform")
	if _, err := os.Stat(terraformDir); os.IsNotExist(err) {
		return false
	}

	// Check if backend_override.tf exists with expected content
	overridePath := filepath.Join(targetDir, "backend_override.tf")
	data, err := os.ReadFile(overridePath)
	if err != nil {
		return false
	}

	// If override content matches, we're already initialized correctly
	return string(data) == expectedOverride
}

// Cleanup removes generated files from the target directory.
// State files and backend_override.tf are preserved so the target
// can be inspected after a run (e.g., terraform state list, terraform plan).
func (c *Copier) Cleanup(targetDir string) error {
	// Nothing to clean up currently — we preserve backend_override.tf
	// so that manual terraform commands work after tfsync runs.
	return nil
}

// BackupState creates a backup of the current state in a target directory.
func (c *Copier) BackupState(targetDir string) (string, error) {
	statePath := filepath.Join(targetDir, "terraform.tfstate")
	backupPath := filepath.Join(targetDir, "terraform.tfstate.tfsync-backup")

	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // No state to backup
		}
		return "", fmt.Errorf("failed to read state: %w", err)
	}

	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write backup: %w", err)
	}

	return backupPath, nil
}

// RestoreState restores state from a backup.
func (c *Copier) RestoreState(targetDir, backupPath string) error {
	if backupPath == "" {
		return nil // No backup to restore
	}

	data, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("failed to read backup: %w", err)
	}

	statePath := filepath.Join(targetDir, "terraform.tfstate")
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		return fmt.Errorf("failed to restore state: %w", err)
	}

	return nil
}
