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
	"strings"
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
	// driftMemory holds in-memory cached drift results.
	driftMemory map[string]*CachedDrift
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
		CacheDir:    cacheDir,
		memory:      make(map[string]*CachedState),
		driftMemory: make(map[string]*CachedDrift),
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

// CachedDrift holds cached source plan drift results.
type CachedDrift struct {
	// SourceDir is the absolute path to the source directory.
	SourceDir string `json:"source_dir"`
	// CachedAt is when the drift was cached.
	CachedAt time.Time `json:"cached_at"`
	// ResourceDiffs are the resource changes detected in the source plan.
	ResourceDiffs []ResourceDiff `json:"resource_diffs"`
	// Output is the raw text output from the terraform plan command.
	Output string `json:"output,omitempty"`
}

// DriftCacheKey generates a unique cache key for source drift results.
func (sc *StateCache) DriftCacheKey(sourceDir string, initCfg *InitConfig) string {
	return "drift-" + sc.CacheKey(sourceDir, initCfg)
}

// GetDrift retrieves cached drift results if they exist.
func (sc *StateCache) GetDrift(sourceDir string, initCfg *InitConfig) (*CachedDrift, error) {
	key := sc.DriftCacheKey(sourceDir, initCfg)

	// Check in-memory cache
	if cached, ok := sc.driftMemory[key]; ok {
		return cached, nil
	}

	// Check disk cache
	cachePath := sc.CachePath(key)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read drift cache: %w", err)
	}

	var cached CachedDrift
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("failed to parse drift cache: %w", err)
	}

	sc.driftMemory[key] = &cached
	return &cached, nil
}

// PutDrift stores drift results in both in-memory and disk cache.
func (sc *StateCache) PutDrift(sourceDir string, initCfg *InitConfig, diffs []ResourceDiff, output string) error {
	key := sc.DriftCacheKey(sourceDir, initCfg)

	cached := &CachedDrift{
		SourceDir:     sourceDir,
		CachedAt:      time.Now(),
		ResourceDiffs: diffs,
		Output:        output,
	}

	sc.driftMemory[key] = cached

	if err := os.MkdirAll(sc.CacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache dir: %w", err)
	}

	data, err := json.Marshal(cached)
	if err != nil {
		return fmt.Errorf("failed to marshal drift cache: %w", err)
	}

	if err := os.WriteFile(sc.CachePath(key), data, 0644); err != nil {
		return fmt.Errorf("failed to write drift cache: %w", err)
	}

	return nil
}

// ClearDrift removes cached drift for a source directory.
func (sc *StateCache) ClearDrift(sourceDir string, initCfg *InitConfig) error {
	key := sc.DriftCacheKey(sourceDir, initCfg)
	delete(sc.driftMemory, key)

	cachePath := sc.CachePath(key)
	if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove drift cache: %w", err)
	}
	return nil
}

// ClearAll removes all cached state from both memory and disk.
func (sc *StateCache) ClearAll() error {
	// Clear memory
	sc.memory = make(map[string]*CachedState)
	sc.driftMemory = make(map[string]*CachedDrift)

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
// env is optional environment variables (KEY=VALUE format) for the terraform CLI.
func (c *Copier) PullSourceState(ctx context.Context, sourceDir string, initCfg *InitConfig, forceRefresh bool, env ...string) (*PullResult, error) {
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

	// Initialize source to connect to its backend.
	// When force-refreshing, pass -upgrade to ensure providers are re-downloaded
	// in case the lock file was updated externally.
	effectiveInitCfg := initCfg
	if forceRefresh {
		if effectiveInitCfg == nil {
			effectiveInitCfg = &InitConfig{}
		} else {
			copied := *effectiveInitCfg
			effectiveInitCfg = &copied
		}
		effectiveInitCfg.ExtraArgs = append(effectiveInitCfg.ExtraArgs, "-upgrade")
	}
	sourceCLI := NewCLI(c.binary, sourceDir)
	sourceCLI.Debug = c.debug
	sourceCLI.Env = append(sourceCLI.Env, env...)
	if err := sourceCLI.InitWithConfig(ctx, effectiveInitCfg); err != nil {
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
// stateFileName overrides the default "terraform.tfstate" name (pass "" for default).
func (c *Copier) WriteStateToTarget(cached *CachedState, targetDir string, stateFileName ...string) error {
	sfName := "terraform.tfstate"
	if len(stateFileName) > 0 && stateFileName[0] != "" {
		sfName = stateFileName[0]
	}
	localStatePath := filepath.Join(targetDir, sfName)

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

// RemoteStateOverride configures a terraform_remote_state data source override
// to point at a local state file during validation.
type RemoteStateOverride struct {
	DataSourceName string // name in data "terraform_remote_state" "<name>"
	StatePath      string // relative path from the workspace dir to the dependency's local state file
}

// InitTargetOpts holds options for InitTargetWithLocalBackendOpts.
type InitTargetOpts struct {
	Env                      []string // Additional environment variables (KEY=VALUE format)
	StateFileName            string   // Override state file name (default: "terraform.tfstate")
	RemoteStateDataSourceNames []string // Data source names for remote_state overrides (generates tfsync_vars.tf + remote_state_override.tf)
}

// InitTargetWithLocalBackendEx initializes the target with status callbacks.
// If the target is already initialized with a local backend, init is skipped.
// env is optional additional environment variables (KEY=VALUE format) for the terraform CLI.
func (c *Copier) InitTargetWithLocalBackendEx(ctx context.Context, targetDir string, onStatus StatusCallback, env ...string) (*InitResult, error) {
	return c.InitTargetWithLocalBackendOpts(ctx, targetDir, onStatus, InitTargetOpts{Env: env})
}

// InitTargetWithLocalBackendOpts initializes the target with full options.
//
// The override strategy is:
//  1. backend_override.tf forces `backend "local" {}` (overrides whatever the module declares)
//  2. `-backend-config=path=<stateFileName>` sets the workspace-specific state path
//     (overrides the empty path in the override file)
//
// This allows multiple workspaces sharing the same directory to each have their
// own state file while sharing a single backend_override.tf.
func (c *Copier) InitTargetWithLocalBackendOpts(ctx context.Context, targetDir string, onStatus StatusCallback, opts InitTargetOpts) (*InitResult, error) {
	notify := func(s string) {
		if onStatus != nil {
			onStatus(s)
		}
	}

	stateFileName := opts.StateFileName
	if stateFileName == "" {
		stateFileName = "terraform.tfstate"
	}

	overridePath := filepath.Join(targetDir, "backend_override.tf")
	overrideContent := `# Auto-generated by tfsync - DO NOT COMMIT
terraform {
  backend "local" {}
}
`

	// Determine the terraform data directory (where .terraform lives)
	tfDataDir := targetDir
	for _, e := range opts.Env {
		if strings.HasPrefix(e, "TF_DATA_DIR=") {
			tfDataDir = e[len("TF_DATA_DIR="):]
		}
	}

	// Write remote_state variable declarations and override files.
	// These must be written before init (and even when init is skipped)
	// because terraform needs to see them during both init and plan.
	// The actual state paths are passed via -var at plan time, so multiple
	// workspaces sharing the same directory can use different paths without
	// clobbering each other's override files.
	if len(opts.RemoteStateDataSourceNames) > 0 {
		varsContent := GenerateRemoteStateVars(opts.RemoteStateDataSourceNames)
		varsPath := filepath.Join(targetDir, "tfsync_vars.tf")
		notify("writing remote state vars")
		if err := os.WriteFile(varsPath, []byte(varsContent), 0644); err != nil {
			return nil, fmt.Errorf("failed to write tfsync vars: %w", err)
		}

		rsContent := GenerateRemoteStateOverride(opts.RemoteStateDataSourceNames)
		rsPath := filepath.Join(targetDir, "remote_state_override.tf")
		notify("writing remote state override")
		if err := os.WriteFile(rsPath, []byte(rsContent), 0644); err != nil {
			return nil, fmt.Errorf("failed to write remote state override: %w", err)
		}
	}

	// Check if already initialized with this backend config
	if c.isAlreadyInitialized(targetDir, tfDataDir, overrideContent, stateFileName) {
		notify("already initialized")
		return &InitResult{Skipped: true}, nil
	}

	// Write backend override (forces local backend, shared across workspaces)
	notify("writing backend override")
	if err := os.WriteFile(overridePath, []byte(overrideContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write backend override: %w", err)
	}

	// Initialize target with local backend.
	// Use -reconfigure to force switching to the local backend without
	// attempting to migrate state from the target's original backend.
	// Use -backend-config=path=<stateFileName> to set the workspace-specific state path.
	notify("running tofu init")
	targetCLI := NewCLI(c.binary, targetDir)
	targetCLI.Debug = c.debug
	targetCLI.Env = append(targetCLI.Env, opts.Env...)
	initArgs := []string{"-reconfigure", fmt.Sprintf("-backend-config=path=%s", stateFileName)}
	if err := targetCLI.InitWithConfig(ctx, &InitConfig{ExtraArgs: initArgs}); err != nil {
		return nil, fmt.Errorf("failed to init target: %w", err)
	}

	return &InitResult{Initialized: true}, nil
}

// RemoteStateVarName returns the tfsync variable name for a remote state data source.
func RemoteStateVarName(dataSourceName string) string {
	// Replace non-alphanumeric characters with underscores for valid HCL variable names
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, dataSourceName)
	return fmt.Sprintf("tfsync_rs_%s_path", safe)
}

// GenerateRemoteStateVars generates tfsync_vars.tf content declaring variables
// for each remote state data source. These variables are passed via -var at plan time,
// allowing multiple workspaces sharing the same directory to use different state paths.
func GenerateRemoteStateVars(dataSourceNames []string) string {
	sorted := make([]string, len(dataSourceNames))
	copy(sorted, dataSourceNames)
	sort.Strings(sorted)

	var buf strings.Builder
	buf.WriteString("# Auto-generated by tfsync - DO NOT COMMIT\n")
	for _, dsName := range sorted {
		fmt.Fprintf(&buf, "\nvariable %q {\n", RemoteStateVarName(dsName))
		buf.WriteString("  type    = string\n")
		buf.WriteString("  default = \"\"\n")
		buf.WriteString("}\n")
	}
	return buf.String()
}

// GenerateRemoteStateOverride generates remote_state_override.tf content that overrides
// terraform_remote_state data sources to use local backend with variable-based paths.
// The actual paths are passed via -var flags at plan time.
func GenerateRemoteStateOverride(dataSourceNames []string) string {
	sorted := make([]string, len(dataSourceNames))
	copy(sorted, dataSourceNames)
	sort.Strings(sorted)

	var buf strings.Builder
	buf.WriteString("# Auto-generated by tfsync - DO NOT COMMIT\n")
	for _, dsName := range sorted {
		fmt.Fprintf(&buf, "\ndata \"terraform_remote_state\" %q {\n", dsName)
		buf.WriteString("  backend = \"local\"\n")
		buf.WriteString("  config = {\n")
		fmt.Fprintf(&buf, "    path = var.%s\n", RemoteStateVarName(dsName))
		buf.WriteString("  }\n")
		buf.WriteString("}\n")
	}
	return buf.String()
}

// isAlreadyInitialized checks if the target is already initialized with our local backend
// and the correct state file path.
// tfDataDir is where .terraform lives (usually targetDir unless TF_DATA_DIR is set).
func (c *Copier) isAlreadyInitialized(targetDir, tfDataDir, expectedOverride, stateFileName string) bool {
	// Check for .terraform directory (indicates init has been run)
	terraformDir := filepath.Join(tfDataDir, ".terraform")
	if _, err := os.Stat(terraformDir); os.IsNotExist(err) {
		return false
	}

	// Check if backend_override.tf exists with expected content
	overridePath := filepath.Join(targetDir, "backend_override.tf")
	data, err := os.ReadFile(overridePath)
	if err != nil {
		return false
	}
	if string(data) != expectedOverride {
		return false
	}

	// Check that the state file path in .terraform/terraform.tfstate matches
	// what we expect. This is how terraform stores the backend config after init.
	backendStatePath := filepath.Join(tfDataDir, ".terraform", "terraform.tfstate")
	backendState, err := os.ReadFile(backendStatePath)
	if err != nil {
		return false
	}
	// Quick check: the backend state should contain our state file name
	return strings.Contains(string(backendState), stateFileName)
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
func (c *Copier) BackupState(targetDir string, stateFileName ...string) (string, error) {
	sfName := "terraform.tfstate"
	if len(stateFileName) > 0 && stateFileName[0] != "" {
		sfName = stateFileName[0]
	}
	statePath := filepath.Join(targetDir, sfName)
	backupPath := filepath.Join(targetDir, sfName+".tfsync-backup")

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
func (c *Copier) RestoreState(targetDir, backupPath string, stateFileName ...string) error {
	if backupPath == "" {
		return nil // No backup to restore
	}

	data, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("failed to read backup: %w", err)
	}

	sfName := "terraform.tfstate"
	if len(stateFileName) > 0 && stateFileName[0] != "" {
		sfName = stateFileName[0]
	}
	statePath := filepath.Join(targetDir, sfName)
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		return fmt.Errorf("failed to restore state: %w", err)
	}

	return nil
}
