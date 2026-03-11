// Package main provides the tfsync CLI entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/chadac/tfsync/internal"
)

var (
	version = "dev"

	configFile string
	verbose    bool
	debug      bool
	dryRun     bool
	noCleanup  bool
	refresh    bool
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "tfsync",
	Short: "Validate terraform state migrations",
	Long: `tfsync validates terraform state migrations by:
1. Copying state from source to target (local)
2. Running configured state moves/migrations
3. Running terraform plan to verify no changes

This ensures your state migration is correct before applying to production.`,
	Version: version,
}

var runCmd = &cobra.Command{
	Use:   "run [targets...]",
	Short: "Run state migration validation",
	Long: `Runs the full state migration validation workflow:
1. Pull state from source
2. Copy to target workspace(s)
3. Execute configured migrations
4. Run terraform plan to verify no changes

If targets are specified, only those workspaces are validated.
Otherwise, all workspaces are validated.

Examples:
  tfsync run                     # Run on all workspaces
  tfsync run dev-core            # Run on just dev-core workspace
  tfsync run dev-core staging    # Run on dev-core and staging workspaces`,
	SilenceUsage: true, // Don't print usage on error
	RunE:         runSync,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "tfsync.yaml", "Path to config file")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "Debug mode - show all terraform commands being executed")

	runCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without executing")
	runCmd.Flags().BoolVar(&noCleanup, "no-cleanup", false, "Don't cleanup generated files after run")
	runCmd.Flags().BoolVarP(&refresh, "refresh", "r", false, "Force refresh state from source (ignore cache)")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(autosuggestCmd)
}

var autosuggestCmd = &cobra.Command{
	Use:   "autosuggest",
	Short: "Generate suggested migrations based on resource similarity",
	Long: `Analyzes source state and target configuration to suggest state moves.

Compares resources by type and name similarity to generate migration suggestions.
Output can be copied directly into your tfsync.yaml configuration.`,
	RunE: runAutosuggest,
}

func runAutosuggest(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	rep := internal.NewReporter(verbose)

	// Load config (we need source/target paths, migration section is optional for autosuggest)
	rep.Step("Loading configuration from %s", configFile)
	cfg, err := loadConfigForAutosuggest(configFile)
	if err != nil {
		rep.Error("Failed to load config: %v", err)
		return err
	}

	binary := cfg.TF.GetTool()
	rep.Info("Using tf tool: %s", binary)

	suggester := internal.NewSuggester(binary)

	rep.Step("Analyzing source and target resources...")
	result, err := suggester.Suggest(ctx, cfg)
	if err != nil {
		rep.Error("Failed to generate suggestions: %v", err)
		return err
	}

	// Report results
	rep.Header("Suggested Migrations")

	if len(result.Suggestions) == 0 {
		rep.Warning("No migration suggestions found")
	} else {
		for _, s := range result.Suggestions {
			confidence := "high"
			if s.Confidence < 0.7 {
				confidence = "medium"
			}
			if s.Confidence < 0.4 {
				confidence = "low"
			}
			rep.Detail("[%s] %s -> %s (%s)", confidence, s.From, s.To, s.Reason)
		}
	}

	if len(result.UnmappedSource) > 0 {
		rep.Newline()
		rep.Warning("Unmapped source resources (will be removed from state):")
		for _, addr := range result.UnmappedSource {
			rep.Detail("  - %s", addr)
		}
	}

	if len(result.UnmappedTarget) > 0 {
		rep.Newline()
		rep.Warning("Unmapped target resources (will be created):")
		for _, addr := range result.UnmappedTarget {
			rep.Detail("  - %s", addr)
		}
	}

	// Output YAML
	rep.Header("Generated Configuration")
	fmt.Println(result.GenerateYAML())

	return nil
}

// loadConfigForAutosuggest loads config but doesn't require migration section.
func loadConfigForAutosuggest(path string) (*internal.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg internal.Config
	if err := parseYAML(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Validate only source and target, not migration
	if cfg.Version == "" {
		return nil, fmt.Errorf("version is required")
	}
	if cfg.Version != "1" {
		return nil, fmt.Errorf("unsupported config version: %s", cfg.Version)
	}
	if err := cfg.Source.Validate(); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if err := cfg.Target.Validate(); err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}

	return &cfg, nil
}

// parseYAML is a helper to parse YAML without strict validation.
func parseYAML(data []byte, cfg *internal.Config) error {
	// We need to import yaml - let's use a simple approach
	return internal.ParseYAMLRaw(data, cfg)
}

func runSync(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	rep := internal.NewReporter(verbose)

	// Load config
	rep.Step("Loading configuration from %s", configFile)
	cfg, err := internal.Load(configFile)
	if err != nil {
		rep.Error("Failed to load config: %v", err)
		return err
	}

	binary := cfg.TF.GetTool()
	rep.Info("Using tf tool: %s", binary)

	// Dry run mode
	if dryRun {
		executor := internal.NewExecutor(binary)
		actions := executor.DryRun(&cfg.Migration)
		rep.DryRunActions(actions)
		return nil
	}

	// If targets specified, validate and run targeted workflow
	if len(args) > 0 {
		targetWorkspaces := cfg.Target.GetWorkspaces()
		for _, target := range args {
			if _, ok := targetWorkspaces[target]; !ok {
				rep.Error("Unknown target workspace: %s", target)
				var available []string
				for name := range targetWorkspaces {
					available = append(available, name)
				}
				rep.Detail("Available workspaces: %v", available)
				return fmt.Errorf("unknown target workspace: %s", target)
			}
		}
		return runTargetedSyncWorkflow(ctx, cfg, rep, args)
	}

	// Run on all workspaces
	return runSyncWorkflow(ctx, cfg, rep)
}

func runSyncWorkflow(ctx context.Context, cfg *internal.Config, rep *internal.Reporter) error {
	binary := cfg.TF.GetTool()

	// Create copier with caching enabled
	cache := internal.NewStateCache("")
	copier := internal.NewCopierWithCache(binary, cache)
	copier.SetDebug(debug)
	executor := internal.NewExecutor(binary)
	executor.SetDebug(debug)

	sourceWorkspaces := cfg.Source.GetWorkspaces()
	targetWorkspaces := cfg.Target.GetWorkspaces()

	// Track directories for cleanup
	var targetDirs []string
	defer func() {
		if !noCleanup {
			for _, dir := range targetDirs {
				if err := copier.Cleanup(dir); err != nil {
					rep.Warning("Cleanup failed for %s: %v", dir, err)
				}
			}
		}
	}()

	// Phase 1: Pull source states (each source pulled ONCE, cached)
	rep.Header("Pulling Source State")

	// Track if we used cache for any source
	var usedCache bool

	// Collect source workspace resources for auto-detection
	sourceResources := make(internal.SourceWorkspaceResources)

	// Map of source workspace name -> cached state
	cachedStates := make(map[string]*internal.CachedState)

	// Pull each source workspace state once
	for srcName, srcWs := range sourceWorkspaces {
		absSrcDir, err := filepath.Abs(srcWs.Path)
		if err != nil {
			return fmt.Errorf("failed to resolve source path: %w", err)
		}

		rep.Step("Pulling state from %s", srcWs.Path)

		pullResult, err := copier.PullSourceState(ctx, absSrcDir, srcWs.GetInitConfig(), refresh)
		if err != nil {
			rep.Error("Failed to pull source state: %v", err)
			return err
		}

		if pullResult.FromCache {
			usedCache = true
			rep.Info("Using cached state from %s", pullResult.State.CachedAt.Format("2006-01-02 15:04:05"))
		}

		cachedStates[srcName] = pullResult.State
		if pullResult.State.Resources != nil {
			sourceResources[srcName] = pullResult.State.Resources
		}

		rep.Success("Pulled state for %s (%d resources)", srcName, len(pullResult.State.Resources))
	}

	// Phase 2: Write cached state to each target (in parallel)
	rep.Header("Preparing Targets")

	// Build list of targets with their configurations
	type targetTask struct {
		name    string
		absDir  string
		srcName string
		cached  *internal.CachedState
	}
	var tasks []targetTask

	for tgtName, tgtWs := range targetWorkspaces {
		absTargetDir, err := filepath.Abs(tgtWs.Path)
		if err != nil {
			return fmt.Errorf("failed to resolve target path: %w", err)
		}
		targetDirs = append(targetDirs, absTargetDir)

		// Determine which source workspace this target uses
		srcName, err := resolveSourceWorkspace(tgtName, tgtWs, cfg, cachedStates)
		if err != nil {
			rep.Error("%v", err)
			return err
		}
		cached := cachedStates[srcName]

		tasks = append(tasks, targetTask{
			name:    tgtName,
			absDir:  absTargetDir,
			srcName: srcName,
			cached:  cached,
		})
	}

	// Create progress tracker for parallel init
	taskNames := make([]string, len(tasks))
	for i, t := range tasks {
		taskNames[i] = t.name
	}
	progress := rep.NewParallelProgress(taskNames)

	// Run target preparation in parallel: write source state to each target
	g, _ := errgroup.WithContext(ctx)

	for _, task := range tasks {
		task := task // capture for goroutine
		g.Go(func() error {
			progress.Update(task.name, "writing state")
			if err := copier.WriteStateToTarget(task.cached, task.absDir); err != nil {
				progress.Fail(task.name, err)
				return fmt.Errorf("failed to write state to %s: %w", task.name, err)
			}

			progress.Complete(task.name)
			return nil
		})
	}

	// Wait for all targets to be prepared
	if err := g.Wait(); err != nil {
		progress.Finish()
		rep.Error("Target preparation failed: %v", err)
		return err
	}
	progress.Finish()
	rep.Success("Prepared %d targets", len(tasks))

	// Phase 3: Build and validate migration plan
	rep.Header("Planning Migrations")

	// Build target-to-source mapping from resolved tasks
	targetToSource := make(map[string]string)
	for _, task := range tasks {
		targetToSource[task.name] = task.srcName
	}

	// Build plan options with per-workspace provider remaps and source provider info
	planOpts := &internal.PlanOptions{}

	// Extract per-source-workspace provider remaps from config
	for srcName, srcWs := range sourceWorkspaces {
		if len(srcWs.ProviderRemap) > 0 {
			if planOpts.SourceProviderRemaps == nil {
				planOpts.SourceProviderRemaps = make(map[string]map[string]string)
			}
			planOpts.SourceProviderRemaps[srcName] = srcWs.ProviderRemap
		}
	}

	// Extract per-target-workspace provider remaps from config
	for tgtName, tgtWs := range targetWorkspaces {
		if len(tgtWs.ProviderRemap) > 0 {
			if planOpts.TargetProviderRemaps == nil {
				planOpts.TargetProviderRemaps = make(map[string]map[string]string)
			}
			planOpts.TargetProviderRemaps[tgtName] = tgtWs.ProviderRemap
		}
	}

	// Extract source resource provider info for validation
	planOpts.SourceProviders = make(internal.SourceResourceProviders)
	for srcName, cached := range cachedStates {
		if providers, err := internal.ExtractResourceProviders(cached.StateData); err == nil {
			planOpts.SourceProviders[srcName] = providers
		}
	}

	migrationPlan, err := internal.BuildMigrationPlan(&cfg.Migration, sourceResources, targetToSource, planOpts)
	if err != nil {
		rep.Error("Migration plan validation failed: %v", err)
		return err
	}

	// Report plan warnings
	for _, w := range migrationPlan.Warnings {
		rep.Warning(w)
	}

	// Show plan summary in verbose mode
	if verbose {
		for _, line := range migrationPlan.Summary() {
			rep.Detail(line)
		}
	}

	rep.Success("Migration plan validated (%d workspace(s), %d moves, %d removes)",
		len(migrationPlan.WorkspacePlans), migrationPlan.TotalMoves(), migrationPlan.TotalRemoves())

	// Phase 4: Execute migration plan
	rep.Header("Running Migrations")

	targetDirMap := make(map[string]string)
	var migrationWsNames []string
	for wsName, ws := range targetWorkspaces {
		absDir, _ := filepath.Abs(ws.Path)
		targetDirMap[wsName] = absDir
		migrationWsNames = append(migrationWsNames, wsName)
	}

	migrationProgress := rep.NewParallelProgress(migrationWsNames)

	migrationResult, migrationErr := executor.ExecutePlan(
		ctx, migrationPlan, targetDirMap,
		func(workspace, status string) {
			if status == "done" {
				migrationProgress.Complete(workspace)
			} else if status == "failed" {
				migrationProgress.Fail(workspace, fmt.Errorf("migration failed"))
			} else {
				migrationProgress.Update(workspace, status)
			}
		},
	)
	migrationProgress.Finish()

	if migrationErr != nil {
		rep.Error("Migration failed: %v", migrationErr)
		return migrationErr
	}

	rep.Success("Executed %d state moves", migrationResult.MovesExecuted)
	if migrationResult.ScriptExecuted {
		rep.Success("Migration script completed")
	}

	// Phase 5: Initialize targets with local backend (after migrations so
	// init picks up the migrated state file, not an empty one)
	rep.Header("Initializing Targets")

	var initWsNames []string
	for wsName := range targetWorkspaces {
		initWsNames = append(initWsNames, wsName)
	}
	initProgress := rep.NewParallelProgress(initWsNames)

	gInit, gInitCtx := errgroup.WithContext(ctx)
	var initSkipped, initRan int
	var initMu sync.Mutex

	for wsName, ws := range targetWorkspaces {
		wsName, ws := wsName, ws
		gInit.Go(func() error {
			absDir, _ := filepath.Abs(ws.Path)
			result, err := copier.InitTargetWithLocalBackendEx(gInitCtx, absDir, func(status string) {
				initProgress.Update(wsName, status)
			})
			if err != nil {
				initProgress.Fail(wsName, err)
				return fmt.Errorf("failed to init %s: %w", wsName, err)
			}

			initMu.Lock()
			if result.Skipped {
				initSkipped++
			} else {
				initRan++
			}
			initMu.Unlock()

			initProgress.Complete(wsName)
			return nil
		})
	}

	if err := gInit.Wait(); err != nil {
		initProgress.Finish()
		rep.Error("Target initialization failed: %v", err)
		return err
	}
	initProgress.Finish()

	if initSkipped > 0 && initRan > 0 {
		rep.Success("Initialized %d targets (%d ran, %d skipped)", len(initWsNames), initRan, initSkipped)
	} else if initSkipped > 0 {
		rep.Success("Initialized %d targets (all skipped - already initialized)", len(initWsNames))
	} else {
		rep.Success("Initialized %d targets", len(initWsNames))
	}

	// Phase 6: Run plans to validate (in parallel)
	rep.Header("Validating Plans")

	// Build list of workspaces for progress tracking
	var wsNames []string
	for wsName := range targetWorkspaces {
		wsNames = append(wsNames, wsName)
	}
	planProgress := rep.NewParallelProgress(wsNames)

	// Run plans in parallel
	type planResult struct {
		workspace        string
		hasChanges       bool
		output           string
		changedAddresses []string
		ignoredAddresses []string
		err              error
	}

	planResultsChan := make(chan planResult, len(targetWorkspaces))
	var planWg sync.WaitGroup

	for wsName, targetWs := range targetWorkspaces {
		planWg.Add(1)
		go func(wsName string, targetWs internal.Workspace) {
			defer planWg.Done()

			planProgress.Update(wsName, "planning")

			absTargetDir, _ := filepath.Abs(targetWs.Path)
			cli := internal.NewCLIWithConfig(binary, absTargetDir, targetWs.GetPlanConfig())
			cli.Debug = debug

			if cfg.TF != nil {
				cli.Parallelism = cfg.TF.Parallelism
				cli.VarFiles = cfg.TF.VarFiles
				cli.Vars = cfg.TF.Vars
			}

			// Write any override files (e.g., provider overrides for testing)
			overrideFiles, err := cli.WriteOverrideFiles()
			if err != nil {
				planProgress.Fail(wsName, err)
				planResultsChan <- planResult{workspace: wsName, err: err}
				return
			}
			defer cli.CleanupOverrideFiles(overrideFiles)

			checkResult, err := cli.PlanHasChanges(ctx)

			if err != nil {
				planProgress.Fail(wsName, err)
				planResultsChan <- planResult{workspace: wsName, err: err}
				return
			}

			// Apply ignore_changes filtering
			hasChanges, changedAddrs, ignoredAddrs := filterIgnoredChanges(
				checkResult.HasChanges, checkResult.ChangedAddresses, targetWs.GetPlanConfig(),
			)

			planProgress.Complete(wsName)
			planResultsChan <- planResult{
				workspace:        wsName,
				hasChanges:       hasChanges,
				output:           checkResult.Output,
				changedAddresses: changedAddrs,
				ignoredAddresses: ignoredAddrs,
			}
		}(wsName, targetWs)
	}

	// Wait for all plans to complete
	planWg.Wait()
	close(planResultsChan)
	planProgress.Finish()

	// Collect results and check for errors
	var planResults []internal.PlanResult
	var firstPlanErr error
	var failedWorkspace string

	for res := range planResultsChan {
		if res.err != nil && firstPlanErr == nil {
			firstPlanErr = res.err
			failedWorkspace = res.workspace
		}
		planResults = append(planResults, internal.PlanResult{
			Workspace:        res.workspace,
			HasChanges:       res.hasChanges,
			Output:           res.output,
			ChangedAddresses: res.changedAddresses,
			IgnoredAddresses: res.ignoredAddresses,
			Error:            res.err,
		})
	}

	// If any plan failed with an error, report and exit
	if firstPlanErr != nil {
		rep.Error("Plan failed for %s: %v", failedWorkspace, firstPlanErr)
		return fmt.Errorf("plan failed for workspace %s: %w", failedWorkspace, firstPlanErr)
	}

	// Report results
	rep.Header("Plan Results")
	allPassed := rep.ReportPlanResults(planResults)

	// Summary
	rep.ReportSummary(internal.SummaryResult{
		SourceWorkspaces: len(sourceWorkspaces),
		TargetWorkspaces: len(targetWorkspaces),
		MovesExecuted:    migrationResult.MovesExecuted,
		ScriptRan:        migrationResult.ScriptExecuted,
		Success:          allPassed,
	})

	// Warn if cached state was used
	if usedCache && allPassed {
		rep.Newline()
		rep.Warning("State was loaded from cache. For final confirmation before applying to production,")
		rep.Warning("run with --refresh (-r) to pull fresh state from the source backend.")
	}

	if !allPassed {
		return fmt.Errorf("migration validation failed")
	}

	return nil
}

// runTargetedSyncWorkflow runs the sync workflow for specific target workspaces only.
func runTargetedSyncWorkflow(ctx context.Context, cfg *internal.Config, rep *internal.Reporter, targets []string) error {
	binary := cfg.TF.GetTool()

	// Create copier with caching enabled
	cache := internal.NewStateCache("")
	copier := internal.NewCopierWithCache(binary, cache)
	copier.SetDebug(debug)
	executor := internal.NewExecutor(binary)
	executor.SetDebug(debug)

	// Build set of targeted workspaces
	targetSet := make(map[string]bool)
	for _, t := range targets {
		targetSet[t] = true
	}

	allSourceWorkspaces := cfg.Source.GetWorkspaces()
	allTargetWorkspaces := cfg.Target.GetWorkspaces()

	// Filter to only targeted workspaces
	targetWorkspaces := make(map[string]internal.Workspace)
	for name, ws := range allTargetWorkspaces {
		if targetSet[name] {
			targetWorkspaces[name] = ws
		}
	}

	rep.Step("Running on %d of %d workspaces: %v", len(targets), len(allTargetWorkspaces), targets)

	// Track directories for cleanup
	var targetDirs []string
	defer func() {
		if !noCleanup {
			for _, dir := range targetDirs {
				if err := copier.Cleanup(dir); err != nil {
					rep.Warning("Cleanup failed for %s: %v", dir, err)
				}
			}
		}
	}()

	// Phase 1: Pull ALL source states (needed for full migration plan validation)
	rep.Header("Pulling Source State")

	var usedCache bool
	sourceResources := make(internal.SourceWorkspaceResources)
	cachedStates := make(map[string]*internal.CachedState)

	// Pull ALL source workspace states — we need them all for plan validation,
	// even if we're only executing a subset of targets.
	for srcName, srcWs := range allSourceWorkspaces {
		absSrcDir, err := filepath.Abs(srcWs.Path)
		if err != nil {
			return fmt.Errorf("failed to resolve source path: %w", err)
		}

		rep.Step("Pulling state from %s", srcWs.Path)

		pullResult, err := copier.PullSourceState(ctx, absSrcDir, srcWs.GetInitConfig(), refresh)
		if err != nil {
			rep.Error("Failed to pull source state: %v", err)
			return err
		}

		if pullResult.FromCache {
			usedCache = true
			rep.Info("Using cached state from %s", pullResult.State.CachedAt.Format("2006-01-02 15:04:05"))
		}

		cachedStates[srcName] = pullResult.State
		if pullResult.State.Resources != nil {
			sourceResources[srcName] = pullResult.State.Resources
		}

		rep.Success("Pulled state for %s (%d resources)", srcName, len(pullResult.State.Resources))
	}

	// Phase 2: Write cached state to each target (in parallel)
	rep.Header("Preparing Targets")

	type targetTask struct {
		name    string
		absDir  string
		srcName string
		cached  *internal.CachedState
	}
	var tasks []targetTask

	for tgtName, tgtWs := range targetWorkspaces {
		absTargetDir, err := filepath.Abs(tgtWs.Path)
		if err != nil {
			return fmt.Errorf("failed to resolve target path: %w", err)
		}
		targetDirs = append(targetDirs, absTargetDir)

		srcName, err := resolveSourceWorkspace(tgtName, tgtWs, cfg, cachedStates)
		if err != nil {
			rep.Error("%v", err)
			return err
		}
		cached := cachedStates[srcName]

		tasks = append(tasks, targetTask{
			name:    tgtName,
			absDir:  absTargetDir,
			srcName: srcName,
			cached:  cached,
		})
	}

	taskNames := make([]string, len(tasks))
	for i, t := range tasks {
		taskNames[i] = t.name
	}
	progress := rep.NewParallelProgress(taskNames)

	// Write source state to each target in parallel
	g, _ := errgroup.WithContext(ctx)

	for _, task := range tasks {
		task := task
		g.Go(func() error {
			progress.Update(task.name, "writing state")
			if err := copier.WriteStateToTarget(task.cached, task.absDir); err != nil {
				progress.Fail(task.name, err)
				return fmt.Errorf("failed to write state to %s: %w", task.name, err)
			}

			progress.Complete(task.name)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		progress.Finish()
		rep.Error("Target preparation failed: %v", err)
		return err
	}
	progress.Finish()
	rep.Success("Prepared %d targets", len(tasks))

	// Phase 3: Build and validate migration plan (for ALL workspaces, not just targets)
	rep.Header("Planning Migrations")

	// Build target-to-source mapping for ALL target workspaces.
	// The plan must validate the entire migration, even though we only execute a subset.
	allTargetToSource := make(map[string]string)
	for tgtName, tgtWs := range allTargetWorkspaces {
		srcName, err := resolveSourceWorkspace(tgtName, tgtWs, cfg, cachedStates)
		if err != nil {
			rep.Error("%v", err)
			return err
		}
		allTargetToSource[tgtName] = srcName
	}

	// Build plan options with per-workspace provider remaps and source provider info
	planOpts := &internal.PlanOptions{}

	// Extract per-source-workspace provider remaps from config
	for srcName, srcWs := range allSourceWorkspaces {
		if len(srcWs.ProviderRemap) > 0 {
			if planOpts.SourceProviderRemaps == nil {
				planOpts.SourceProviderRemaps = make(map[string]map[string]string)
			}
			planOpts.SourceProviderRemaps[srcName] = srcWs.ProviderRemap
		}
	}

	// Extract per-target-workspace provider remaps from config
	for tgtName, tgtWs := range allTargetWorkspaces {
		if len(tgtWs.ProviderRemap) > 0 {
			if planOpts.TargetProviderRemaps == nil {
				planOpts.TargetProviderRemaps = make(map[string]map[string]string)
			}
			planOpts.TargetProviderRemaps[tgtName] = tgtWs.ProviderRemap
		}
	}

	// Extract source resource provider info for validation
	planOpts.SourceProviders = make(internal.SourceResourceProviders)
	for srcName, cached := range cachedStates {
		if providers, err := internal.ExtractResourceProviders(cached.StateData); err == nil {
			planOpts.SourceProviders[srcName] = providers
		}
	}

	migrationPlan, err := internal.BuildMigrationPlan(&cfg.Migration, sourceResources, allTargetToSource, planOpts)
	if err != nil {
		rep.Error("Migration plan validation failed: %v", err)
		return err
	}

	// Report plan warnings
	for _, w := range migrationPlan.Warnings {
		rep.Warning(w)
	}

	if verbose {
		for _, line := range migrationPlan.Summary() {
			rep.Detail(line)
		}
	}

	rep.Success("Migration plan validated (%d workspace(s), %d moves, %d removes)",
		len(migrationPlan.WorkspacePlans), migrationPlan.TotalMoves(), migrationPlan.TotalRemoves())

	// Phase 4: Execute migration plan (only targeted workspaces)
	rep.Header("Running Migrations")

	targetDirMap := make(map[string]string)
	var migrationWsNames []string
	for wsName, ws := range targetWorkspaces {
		absDir, _ := filepath.Abs(ws.Path)
		targetDirMap[wsName] = absDir
		migrationWsNames = append(migrationWsNames, wsName)
	}

	migrationProgress := rep.NewParallelProgress(migrationWsNames)

	migrationResult, migrationErr := executor.ExecutePlan(
		ctx, migrationPlan, targetDirMap,
		func(workspace, status string) {
			if status == "done" {
				migrationProgress.Complete(workspace)
			} else if status == "failed" {
				migrationProgress.Fail(workspace, fmt.Errorf("migration failed"))
			} else {
				migrationProgress.Update(workspace, status)
			}
		},
	)
	migrationProgress.Finish()

	if migrationErr != nil {
		rep.Error("Migration failed: %v", migrationErr)
		return migrationErr
	}

	rep.Success("Executed %d state moves", migrationResult.MovesExecuted)
	if migrationResult.ScriptExecuted {
		rep.Success("Migration script completed")
	}

	// Phase 5: Initialize targets with local backend (after migrations so
	// init picks up the migrated state file, not an empty one)
	rep.Header("Initializing Targets")

	var initWsNames []string
	for wsName := range targetWorkspaces {
		initWsNames = append(initWsNames, wsName)
	}
	initProgress := rep.NewParallelProgress(initWsNames)

	gInit, gInitCtx := errgroup.WithContext(ctx)
	var initSkipped, initRan int
	var initMu sync.Mutex

	for wsName, ws := range targetWorkspaces {
		wsName, ws := wsName, ws
		gInit.Go(func() error {
			absDir, _ := filepath.Abs(ws.Path)
			result, err := copier.InitTargetWithLocalBackendEx(gInitCtx, absDir, func(status string) {
				initProgress.Update(wsName, status)
			})
			if err != nil {
				initProgress.Fail(wsName, err)
				return fmt.Errorf("failed to init %s: %w", wsName, err)
			}

			initMu.Lock()
			if result.Skipped {
				initSkipped++
			} else {
				initRan++
			}
			initMu.Unlock()

			initProgress.Complete(wsName)
			return nil
		})
	}

	if err := gInit.Wait(); err != nil {
		initProgress.Finish()
		rep.Error("Target initialization failed: %v", err)
		return err
	}
	initProgress.Finish()

	if initSkipped > 0 && initRan > 0 {
		rep.Success("Initialized %d targets (%d ran, %d skipped)", len(initWsNames), initRan, initSkipped)
	} else if initSkipped > 0 {
		rep.Success("Initialized %d targets (all skipped - already initialized)", len(initWsNames))
	} else {
		rep.Success("Initialized %d targets", len(initWsNames))
	}

	// Phase 6: Run plans to validate (in parallel)
	rep.Header("Validating Plans")

	var wsNames []string
	for wsName := range targetWorkspaces {
		wsNames = append(wsNames, wsName)
	}
	planProgress := rep.NewParallelProgress(wsNames)

	type planResult struct {
		workspace        string
		hasChanges       bool
		output           string
		changedAddresses []string
		ignoredAddresses []string
		err              error
	}

	planResultsChan := make(chan planResult, len(targetWorkspaces))
	var planWg sync.WaitGroup

	for wsName, targetWs := range targetWorkspaces {
		planWg.Add(1)
		go func(wsName string, targetWs internal.Workspace) {
			defer planWg.Done()

			planProgress.Update(wsName, "planning")

			absTargetDir, _ := filepath.Abs(targetWs.Path)
			cli := internal.NewCLIWithConfig(binary, absTargetDir, targetWs.GetPlanConfig())
			cli.Debug = debug

			if cfg.TF != nil {
				cli.Parallelism = cfg.TF.Parallelism
				cli.VarFiles = cfg.TF.VarFiles
				cli.Vars = cfg.TF.Vars
			}

			overrideFiles, err := cli.WriteOverrideFiles()
			if err != nil {
				planProgress.Fail(wsName, err)
				planResultsChan <- planResult{workspace: wsName, err: err}
				return
			}
			defer cli.CleanupOverrideFiles(overrideFiles)

			checkResult, err := cli.PlanHasChanges(ctx)

			if err != nil {
				planProgress.Fail(wsName, err)
				planResultsChan <- planResult{workspace: wsName, err: err}
				return
			}

			// Apply ignore_changes filtering
			hasChanges, changedAddrs, ignoredAddrs := filterIgnoredChanges(
				checkResult.HasChanges, checkResult.ChangedAddresses, targetWs.GetPlanConfig(),
			)

			planProgress.Complete(wsName)
			planResultsChan <- planResult{
				workspace:        wsName,
				hasChanges:       hasChanges,
				output:           checkResult.Output,
				changedAddresses: changedAddrs,
				ignoredAddresses: ignoredAddrs,
			}
		}(wsName, targetWs)
	}

	planWg.Wait()
	close(planResultsChan)
	planProgress.Finish()

	var planResults []internal.PlanResult
	var firstPlanErr error
	var failedWorkspace string

	for res := range planResultsChan {
		if res.err != nil && firstPlanErr == nil {
			firstPlanErr = res.err
			failedWorkspace = res.workspace
		}
		planResults = append(planResults, internal.PlanResult{
			Workspace:        res.workspace,
			HasChanges:       res.hasChanges,
			Output:           res.output,
			ChangedAddresses: res.changedAddresses,
			IgnoredAddresses: res.ignoredAddresses,
			Error:            res.err,
		})
	}

	if firstPlanErr != nil {
		rep.Error("Plan failed for %s: %v", failedWorkspace, firstPlanErr)
		return fmt.Errorf("plan failed for workspace %s: %w", failedWorkspace, firstPlanErr)
	}

	rep.Header("Plan Results")
	allPassed := rep.ReportPlanResults(planResults)

	rep.ReportSummary(internal.SummaryResult{
		SourceWorkspaces: len(allSourceWorkspaces),
		TargetWorkspaces: len(targetWorkspaces),
		MovesExecuted:    migrationResult.MovesExecuted,
		ScriptRan:        migrationResult.ScriptExecuted,
		Success:          allPassed,
	})

	if usedCache && allPassed {
		rep.Newline()
		rep.Warning("State was loaded from cache. For final confirmation before applying to production,")
		rep.Warning("run with --refresh (-r) to pull fresh state from the source backend.")
	}

	if !allPassed {
		return fmt.Errorf("migration validation failed")
	}

	return nil
}

// filterIgnoredChanges filters out changes to addresses in the ignore_changes list.
// Returns: (stillHasChanges, remainingChangedAddresses, ignoredAddresses)
func filterIgnoredChanges(hasChanges bool, changedAddresses []string, planCfg *internal.PlanConfig) (bool, []string, []string) {
	if !hasChanges {
		return false, nil, nil
	}

	// No ignore list configured — all changes are real
	if planCfg == nil || len(planCfg.IgnoreChanges) == 0 {
		return true, changedAddresses, nil
	}

	// If we couldn't get changed addresses (fallback), we can't filter
	if len(changedAddresses) == 0 {
		return true, nil, nil
	}

	// Build ignore set
	ignoreSet := make(map[string]bool)
	for _, addr := range planCfg.IgnoreChanges {
		ignoreSet[addr] = true
	}

	// Partition into ignored and remaining
	var remaining, ignored []string
	for _, addr := range changedAddresses {
		if ignoreSet[addr] {
			ignored = append(ignored, addr)
		} else {
			remaining = append(remaining, addr)
		}
	}

	// If all changes are ignored, plan passes
	if len(remaining) == 0 {
		return false, nil, ignored
	}

	return true, remaining, ignored
}

// resolveSourceWorkspaceName determines which source workspace a target should pull from.
// Returns the source workspace name without validating it exists.
func resolveSourceWorkspaceName(tgtName string, tgtWs internal.Workspace, cfg *internal.Config) string {
	if tgtWs.PreferWorkspace != "" {
		return tgtWs.PreferWorkspace
	}
	if cfg.Source.IsMultiWorkspace() {
		// Try name match
		return tgtName
	}
	return "default"
}

// resolveSourceWorkspace determines and validates which source workspace a target should use.
// It checks that the resolved source workspace exists in availableSources.
func resolveSourceWorkspace(tgtName string, tgtWs internal.Workspace, cfg *internal.Config, availableSources map[string]*internal.CachedState) (string, error) {
	srcName := resolveSourceWorkspaceName(tgtName, tgtWs, cfg)

	if _, ok := availableSources[srcName]; ok {
		return srcName, nil
	}

	// Build a helpful error message
	if tgtWs.PreferWorkspace != "" {
		// Explicit prefer_workspace pointed to a nonexistent source
		return "", fmt.Errorf("target %q: prefer_workspace %q does not match any source workspace", tgtName, srcName)
	}

	if cfg.Source.IsMultiWorkspace() {
		// Name match failed — ambiguous, need prefer_workspace
		var sourceNames []string
		for name := range cfg.Source.GetWorkspaces() {
			sourceNames = append(sourceNames, name)
		}
		return "", fmt.Errorf("target %q: no source workspace matches name %q (available: %v); set prefer_workspace to resolve", tgtName, tgtName, sourceNames)
	}

	return "", fmt.Errorf("target %q: source workspace %q not found", tgtName, srcName)
}
