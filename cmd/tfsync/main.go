// Package main provides the tfsync CLI entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/chadac/tfsync/internal/config"
	"github.com/chadac/tfsync/internal/migration"
	"github.com/chadac/tfsync/internal/reporter"
	"github.com/chadac/tfsync/internal/state"
	"github.com/chadac/tfsync/internal/suggest"
	"github.com/chadac/tfsync/internal/terraform"
)

var (
	version = "dev"

	configFile string
	verbose    bool
	dryRun     bool
	noCleanup  bool
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
	RunE:    runSync,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "tfsync.yaml", "Path to config file")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without executing")
	rootCmd.Flags().BoolVar(&noCleanup, "no-cleanup", false, "Don't cleanup generated files after run")

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

	rep := reporter.New(verbose)

	// Load config (we need source/target paths, migration section is optional for autosuggest)
	rep.Step("Loading configuration from %s", configFile)
	cfg, err := loadConfigForAutosuggest(configFile)
	if err != nil {
		rep.Error("Failed to load config: %v", err)
		return err
	}

	binary := cfg.TF.GetTool()
	rep.Info("Using tf tool: %s", binary)

	suggester := suggest.NewSuggester(binary)

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
func loadConfigForAutosuggest(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg config.Config
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
func parseYAML(data []byte, cfg *config.Config) error {
	// We need to import yaml - let's use a simple approach
	return config.ParseYAMLRaw(data, cfg)
}

func runSync(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	rep := reporter.New(verbose)

	// Load config
	rep.Step("Loading configuration from %s", configFile)
	cfg, err := config.Load(configFile)
	if err != nil {
		rep.Error("Failed to load config: %v", err)
		return err
	}

	binary := cfg.TF.GetTool()
	rep.Info("Using tf tool: %s", binary)

	// Dry run mode
	if dryRun {
		executor := migration.NewExecutor(binary)
		actions := executor.DryRun(&cfg.Migration)
		rep.DryRunActions(actions)
		return nil
	}

	// Run the sync
	return runSyncWorkflow(ctx, cfg, rep)
}

func runSyncWorkflow(ctx context.Context, cfg *config.Config, rep *reporter.Reporter) error {
	binary := cfg.TF.GetTool()
	copier := state.NewCopier(binary)
	executor := migration.NewExecutor(binary)

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

	// For single source -> single/multi target, copy source state to each target
	// For multi source -> multi target, copy each source to corresponding target
	rep.Header("Copying State")

	if cfg.Source.IsMultiWorkspace() {
		// Multi-source: each source workspace maps to a target workspace
		for wsName, srcWs := range sourceWorkspaces {
			targetWs, ok := targetWorkspaces[wsName]
			if !ok {
				rep.Warning("Source workspace %q has no corresponding target, skipping", wsName)
				continue
			}

			absTargetDir, err := filepath.Abs(targetWs.Path)
			if err != nil {
				return fmt.Errorf("failed to resolve target path: %w", err)
			}
			targetDirs = append(targetDirs, absTargetDir)

			rep.Step("Copying state: %s -> %s", srcWs.Path, targetWs.Path)

			absSrcDir, err := filepath.Abs(srcWs.Path)
			if err != nil {
				return fmt.Errorf("failed to resolve source path: %w", err)
			}

			if _, err := copier.CopyToLocal(ctx, absSrcDir, absTargetDir, srcWs.Backend); err != nil {
				rep.Error("Failed to copy state: %v", err)
				return err
			}

			if err := copier.InitTargetWithLocalBackend(ctx, absTargetDir); err != nil {
				rep.Error("Failed to init target: %v", err)
				return err
			}

			rep.Success("State copied for workspace %s", wsName)
		}
	} else {
		// Single source: copy to all targets
		srcWs := sourceWorkspaces["default"]
		absSrcDir, err := filepath.Abs(srcWs.Path)
		if err != nil {
			return fmt.Errorf("failed to resolve source path: %w", err)
		}

		for wsName, targetWs := range targetWorkspaces {
			absTargetDir, err := filepath.Abs(targetWs.Path)
			if err != nil {
				return fmt.Errorf("failed to resolve target path: %w", err)
			}
			targetDirs = append(targetDirs, absTargetDir)

			rep.Step("Copying state: %s -> %s", srcWs.Path, targetWs.Path)

			if _, err := copier.CopyToLocal(ctx, absSrcDir, absTargetDir, srcWs.Backend); err != nil {
				rep.Error("Failed to copy state: %v", err)
				return err
			}

			if err := copier.InitTargetWithLocalBackend(ctx, absTargetDir); err != nil {
				rep.Error("Failed to init target: %v", err)
				return err
			}

			rep.Success("State copied to %s", wsName)
		}
	}

	// Execute migrations
	rep.Header("Running Migrations")

	var migrationResult *migration.Result
	if cfg.Target.IsMultiWorkspace() {
		targetDirMap := make(map[string]string)
		for wsName, ws := range targetWorkspaces {
			absDir, _ := filepath.Abs(ws.Path)
			targetDirMap[wsName] = absDir
		}
		var err error
		migrationResult, err = executor.ExecuteMultiWorkspace(ctx, &cfg.Migration, targetDirMap)
		if err != nil {
			rep.Error("Migration failed: %v", err)
			return err
		}
	} else {
		targetWs := targetWorkspaces["default"]
		absTargetDir, _ := filepath.Abs(targetWs.Path)
		var err error
		migrationResult, err = executor.Execute(ctx, &cfg.Migration, absTargetDir)
		if err != nil {
			rep.Error("Migration failed: %v", err)
			return err
		}
	}

	rep.Success("Executed %d state moves", migrationResult.MovesExecuted)
	if migrationResult.ScriptExecuted {
		rep.Success("Migration script completed")
	}

	// Run plans
	rep.Header("Validating Plans")

	var planResults []reporter.PlanResult
	for wsName, targetWs := range targetWorkspaces {
		absTargetDir, _ := filepath.Abs(targetWs.Path)
		cli := terraform.NewCLI(binary, absTargetDir)

		if cfg.TF != nil {
			cli.Parallelism = cfg.TF.Parallelism
			cli.VarFiles = cfg.TF.VarFiles
			cli.Vars = cfg.TF.Vars
		}

		rep.Step("Running plan for %s", wsName)
		hasChanges, output, err := cli.PlanHasChanges(ctx)

		planResults = append(planResults, reporter.PlanResult{
			Workspace:  wsName,
			HasChanges: hasChanges,
			Output:     output,
			Error:      err,
		})
	}

	// Report results
	rep.Header("Plan Results")
	allPassed := rep.ReportPlanResults(planResults)

	// Summary
	rep.ReportSummary(reporter.SummaryResult{
		SourceWorkspaces: len(sourceWorkspaces),
		TargetWorkspaces: len(targetWorkspaces),
		MovesExecuted:    migrationResult.MovesExecuted,
		ScriptRan:        migrationResult.ScriptExecuted,
		Success:          allPassed,
	})

	if !allPassed {
		return fmt.Errorf("migration validation failed")
	}

	return nil
}
