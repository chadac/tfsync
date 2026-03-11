package internal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestE2E runs end-to-end tests against the testdata scenarios.
// These tests require tofu to be installed and in PATH.
func TestE2E(t *testing.T) {
	// Skip if tofu is not available
	if _, err := os.Stat("/nix/store"); err == nil {
		// Running in nix, tofu should be available
	} else {
		t.Skip("Skipping e2e tests: not running in nix environment")
	}

	tests := []struct {
		name        string
		testdataDir string
		configFile  string
		wantSuccess bool
	}{
		{
			name:        "simple single workspace migration",
			testdataDir: "simple",
			configFile:  "tfsync.yaml",
			wantSuccess: true,
		},
		{
			name:        "multi-workspace split",
			testdataDir: "multi-workspace",
			configFile:  "tfsync.yaml",
			wantSuccess: true,
		},
		{
			name:        "rename resources",
			testdataDir: "rename",
			configFile:  "tfsync.yaml",
			wantSuccess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testdataPath := filepath.Join("..", "testdata", "e2e", tt.testdataDir)

			// Verify testdata exists
			if _, err := os.Stat(testdataPath); os.IsNotExist(err) {
				t.Skipf("testdata not found: %s", testdataPath)
			}

			// Load config
			configPath := filepath.Join(testdataPath, tt.configFile)
			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("failed to load config: %v", err)
			}

			// Run the sync workflow
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			err = runE2EWorkflow(ctx, t, cfg, testdataPath)

			if tt.wantSuccess && err != nil {
				t.Errorf("expected success, got error: %v", err)
			}
			if !tt.wantSuccess && err == nil {
				t.Errorf("expected failure, got success")
			}
		})
	}
}

// TestE2ECache tests that caching works correctly.
func TestE2ECache(t *testing.T) {
	testdataPath := filepath.Join("..", "testdata", "e2e", "simple")

	// Verify testdata exists
	if _, err := os.Stat(testdataPath); os.IsNotExist(err) {
		t.Skipf("testdata not found: %s", testdataPath)
	}

	// Create a temp cache directory
	tmpCacheDir, err := os.MkdirTemp("", "tfsync-cache-e2e")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpCacheDir)

	// Load config
	configPath := filepath.Join(testdataPath, "tfsync.yaml")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	binary := cfg.TF.GetTool()
	cache := NewStateCache(tmpCacheDir)
	copier := NewCopierWithCache(binary, cache)

	sourceWorkspaces := cfg.Source.GetWorkspaces()
	srcWs := sourceWorkspaces["default"]
	absSrcDir, err := filepath.Abs(filepath.Join(testdataPath, srcWs.Path))
	if err != nil {
		t.Fatalf("failed to resolve source path: %v", err)
	}

	// First pull - should not be from cache
	result1, err := copier.PullSourceState(ctx, absSrcDir, srcWs.GetInitConfig(), false)
	if err != nil {
		t.Fatalf("first pull failed: %v", err)
	}
	if result1.FromCache {
		t.Error("first pull should not be from cache")
	}
	if len(result1.State.Resources) == 0 {
		t.Error("expected resources in state")
	}

	// Second pull - should be from cache
	result2, err := copier.PullSourceState(ctx, absSrcDir, srcWs.GetInitConfig(), false)
	if err != nil {
		t.Fatalf("second pull failed: %v", err)
	}
	if !result2.FromCache {
		t.Error("second pull should be from cache")
	}

	// Third pull with refresh - should not be from cache
	result3, err := copier.PullSourceState(ctx, absSrcDir, srcWs.GetInitConfig(), true)
	if err != nil {
		t.Fatalf("refresh pull failed: %v", err)
	}
	if result3.FromCache {
		t.Error("refresh pull should not be from cache")
	}
}

// TestE2ECachePersistence tests that cache persists to disk and can be loaded.
func TestE2ECachePersistence(t *testing.T) {
	testdataPath := filepath.Join("..", "testdata", "e2e", "simple")

	// Verify testdata exists
	if _, err := os.Stat(testdataPath); os.IsNotExist(err) {
		t.Skipf("testdata not found: %s", testdataPath)
	}

	// Create a temp cache directory
	tmpCacheDir, err := os.MkdirTemp("", "tfsync-cache-persist")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpCacheDir)

	// Load config
	configPath := filepath.Join(testdataPath, "tfsync.yaml")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	binary := cfg.TF.GetTool()
	sourceWorkspaces := cfg.Source.GetWorkspaces()
	srcWs := sourceWorkspaces["default"]
	absSrcDir, err := filepath.Abs(filepath.Join(testdataPath, srcWs.Path))
	if err != nil {
		t.Fatalf("failed to resolve source path: %v", err)
	}

	// First copier - pull and cache
	cache1 := NewStateCache(tmpCacheDir)
	copier1 := NewCopierWithCache(binary, cache1)

	result1, err := copier1.PullSourceState(ctx, absSrcDir, srcWs.GetInitConfig(), false)
	if err != nil {
		t.Fatalf("first pull failed: %v", err)
	}
	if result1.FromCache {
		t.Error("first pull should not be from cache")
	}
	cachedAt := result1.State.CachedAt

	// Create a NEW cache instance (simulating new process) with same directory
	cache2 := NewStateCache(tmpCacheDir)
	copier2 := NewCopierWithCache(binary, cache2)

	// Pull should load from disk cache
	result2, err := copier2.PullSourceState(ctx, absSrcDir, srcWs.GetInitConfig(), false)
	if err != nil {
		t.Fatalf("second pull failed: %v", err)
	}
	if !result2.FromCache {
		t.Error("second pull should be from disk cache")
	}
	if !result2.State.CachedAt.Equal(cachedAt) {
		t.Errorf("cached timestamp mismatch: got %v, want %v", result2.State.CachedAt, cachedAt)
	}
}

// runE2EWorkflow runs the full tfsync workflow for e2e testing.
func runE2EWorkflow(ctx context.Context, t *testing.T, cfg *Config, baseDir string) error {
	binary := cfg.TF.GetTool()

	// Create copier with temp cache
	tmpCacheDir, err := os.MkdirTemp("", "tfsync-e2e-cache")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpCacheDir)

	cache := NewStateCache(tmpCacheDir)
	copier := NewCopierWithCache(binary, cache)
	executor := NewExecutor(binary)

	sourceWorkspaces := cfg.Source.GetWorkspaces()
	targetWorkspaces := cfg.Target.GetWorkspaces()

	// Track directories for cleanup
	var targetDirs []string
	defer func() {
		for _, dir := range targetDirs {
			_ = copier.Cleanup(dir)
		}
	}()

	// Phase 1: Pull source states
	sourceResources := make(SourceWorkspaceResources)
	cachedStates := make(map[string]*CachedState)

	for srcName, srcWs := range sourceWorkspaces {
		absSrcDir, err := filepath.Abs(filepath.Join(baseDir, srcWs.Path))
		if err != nil {
			return err
		}

		pullResult, err := copier.PullSourceState(ctx, absSrcDir, srcWs.GetInitConfig(), false)
		if err != nil {
			return err
		}

		cachedStates[srcName] = pullResult.State
		if pullResult.State.Resources != nil {
			sourceResources[srcName] = pullResult.State.Resources
		}

		t.Logf("Pulled state for %s (%d resources)", srcName, len(pullResult.State.Resources))
	}

	// Phase 2: Write cached state to each target
	for tgtName, tgtWs := range targetWorkspaces {
		absTargetDir, err := filepath.Abs(filepath.Join(baseDir, tgtWs.Path))
		if err != nil {
			return err
		}
		targetDirs = append(targetDirs, absTargetDir)

		// Determine which source workspace this target uses
		srcName := tgtWs.PreferWorkspace
		if srcName == "" {
			if cfg.Source.IsMultiWorkspace() {
				srcName = tgtName
			} else {
				srcName = "default"
			}
		}

		cached, ok := cachedStates[srcName]
		if !ok {
			t.Logf("Warning: Target %q references unknown source workspace %q", tgtName, srcName)
			continue
		}

		if err := copier.WriteStateToTarget(cached, absTargetDir); err != nil {
			return err
		}

		if err := copier.InitTargetWithLocalBackend(ctx, absTargetDir); err != nil {
			return err
		}

		t.Logf("State written to %s", tgtName)
	}

	// Phase 3: Execute migrations
	var migrationResult *Result
	if cfg.Target.IsMultiWorkspace() {
		targetDirMap := make(map[string]string)
		for wsName, ws := range targetWorkspaces {
			absDir, _ := filepath.Abs(filepath.Join(baseDir, ws.Path))
			targetDirMap[wsName] = absDir
		}
		migrationResult, err = executor.ExecuteMultiWorkspaceWithSources(ctx, &cfg.Migration, sourceResources, targetDirMap)
	} else {
		targetWs := targetWorkspaces["default"]
		absTargetDir, _ := filepath.Abs(filepath.Join(baseDir, targetWs.Path))
		migrationResult, err = executor.Execute(ctx, &cfg.Migration, absTargetDir)
	}
	if err != nil {
		return err
	}

	t.Logf("Executed %d state moves", migrationResult.MovesExecuted)

	// Phase 4: Run plans to validate
	for wsName, targetWs := range targetWorkspaces {
		absTargetDir, _ := filepath.Abs(filepath.Join(baseDir, targetWs.Path))
		cli := NewCLIWithConfig(binary, absTargetDir, targetWs.GetPlanConfig())

		if cfg.TF != nil {
			cli.Parallelism = cfg.TF.Parallelism
			cli.VarFiles = cfg.TF.VarFiles
			cli.Vars = cfg.TF.Vars
		}

		checkResult, err := cli.PlanHasChanges(ctx)
		if err != nil {
			t.Logf("Plan error for %s: %v", wsName, err)
			return err
		}

		if checkResult.HasChanges {
			t.Logf("Plan shows changes for %s:\n%s", wsName, checkResult.Output)
			return &PlanError{Workspace: wsName, Output: checkResult.Output}
		}

		t.Logf("Plan passed for %s - no changes", wsName)
	}

	return nil
}

// PlanError represents a plan validation failure.
type PlanError struct {
	Workspace string
	Output    string
}

func (e *PlanError) Error() string {
	return "plan has changes for workspace: " + e.Workspace
}
