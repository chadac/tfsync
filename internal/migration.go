package internal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Executor runs state migrations.
type Executor struct {
	binary string
}

// NewExecutor creates a new migration executor.
func NewExecutor(binary string) *Executor {
	if binary == "" {
		binary = "tofu"
	}
	return &Executor{binary: binary}
}

// Result contains the result of a migration execution.
type Result struct {
	// MovesExecuted is the number of state moves executed
	MovesExecuted int
	// ScriptExecuted indicates if a migration script was run
	ScriptExecuted bool
	// Errors contains any non-fatal errors encountered
	Errors []error
}

// Execute runs the configured migrations on the target state.
func (e *Executor) Execute(ctx context.Context, cfg *Migration, targetDir string) (*Result, error) {
	result := &Result{}

	// Run inline moves first
	if len(cfg.Moves) > 0 {
		if err := e.executeMoves(ctx, cfg.Moves, targetDir); err != nil {
			return result, fmt.Errorf("failed to execute moves: %w", err)
		}
		result.MovesExecuted = len(cfg.Moves)
	}

	// Run migration script if configured
	if cfg.Script != "" {
		if err := e.executeScript(ctx, cfg.Script, targetDir); err != nil {
			return result, fmt.Errorf("failed to execute migration script: %w", err)
		}
		result.ScriptExecuted = true
	}

	return result, nil
}

// executeMoves runs terraform state mv for each configured move.
func (e *Executor) executeMoves(ctx context.Context, moves []Move, targetDir string) error {
	cli := NewCLI(e.binary, targetDir)

	for i, mv := range moves {
		toResource := mv.GetToResource()
		if err := cli.StateMv(ctx, mv.From, toResource); err != nil {
			return fmt.Errorf("move[%d] %q -> %q: %w", i, mv.From, toResource, err)
		}
	}

	return nil
}

// executeScript runs an external migration script.
func (e *Executor) executeScript(ctx context.Context, script, targetDir string) error {
	cmd := exec.CommandContext(ctx, script)
	cmd.Dir = targetDir
	cmd.Env = append(os.Environ(),
		"TFSYNC_TARGET_DIR="+targetDir,
		"TFSYNC_TF_BINARY="+e.binary,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("script %q failed: %w", script, err)
	}

	return nil
}

// ExecuteMultiWorkspace runs migrations for multi-workspace configurations.
// It handles routing moves to the correct target workspace.
// For workspace splits (copying full state then trimming), it:
// 1. Groups moves by target workspace
// 2. For each workspace, removes resources NOT assigned to it
// 3. Performs any actual moves (where from != to address)
func (e *Executor) ExecuteMultiWorkspace(
	ctx context.Context,
	cfg *Migration,
	targetWorkspaces map[string]string, // workspace name -> directory
) (*Result, error) {
	result := &Result{}

	// Group moves by target workspace
	movesByWorkspace := make(map[string][]Move)
	allSourceAddrs := make(map[string]string) // from addr -> target workspace

	for _, mv := range cfg.Moves {
		ws := mv.GetToWorkspace()
		if ws == "" {
			ws = "default"
		}
		movesByWorkspace[ws] = append(movesByWorkspace[ws], mv)
		allSourceAddrs[mv.From] = ws
	}

	// For each workspace:
	// 1. Remove resources that belong to OTHER workspaces
	// 2. Execute moves where from != to
	for wsName, targetDir := range targetWorkspaces {
		cli := NewCLI(e.binary, targetDir)

		// Get current resources in this workspace's state
		currentResources, err := cli.StateList(ctx)
		if err != nil {
			return result, fmt.Errorf("workspace %q: failed to list state: %w", wsName, err)
		}

		// Remove resources that belong to other workspaces
		for _, addr := range currentResources {
			targetWs, hasMapping := allSourceAddrs[addr]
			if hasMapping && targetWs != wsName {
				// This resource belongs to a different workspace, remove it
				if err := cli.StateRm(ctx, addr); err != nil {
					return result, fmt.Errorf("workspace %q: failed to remove %q: %w", wsName, addr, err)
				}
			}
		}

		// Execute moves for this workspace (only where from != to)
		moves := movesByWorkspace[wsName]
		for i, mv := range moves {
			toResource := mv.GetToResource()
			if mv.From != toResource {
				if err := cli.StateMv(ctx, mv.From, toResource); err != nil {
					return result, fmt.Errorf("workspace %q: move[%d] %q -> %q: %w", wsName, i, mv.From, toResource, err)
				}
				result.MovesExecuted++
			}
		}
	}

	// Run script once (if configured) - script handles multi-workspace itself
	if cfg.Script != "" {
		// Use first workspace directory as working dir for script
		var firstDir string
		for _, dir := range targetWorkspaces {
			firstDir = dir
			break
		}
		if err := e.executeScript(ctx, cfg.Script, firstDir); err != nil {
			return result, err
		}
		result.ScriptExecuted = true
	}

	return result, nil
}

// DryRun shows what migrations would be executed without running them.
func (e *Executor) DryRun(cfg *Migration) []string {
	var actions []string

	for _, mv := range cfg.Moves {
		toResource := mv.GetToResource()
		action := fmt.Sprintf("state mv %q -> %q", mv.From, toResource)
		if ws := mv.GetToWorkspace(); ws != "" {
			action += fmt.Sprintf(" (workspace: %s)", ws)
		}
		actions = append(actions, action)
	}

	if cfg.Script != "" {
		actions = append(actions, fmt.Sprintf("run script: %s", cfg.Script))
	}

	return actions
}
