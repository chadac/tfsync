package internal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// Executor runs state migrations.
type Executor struct {
	binary string
	debug  bool
}

// NewExecutor creates a new migration executor.
func NewExecutor(binary string) *Executor {
	if binary == "" {
		binary = "tofu"
	}
	return &Executor{binary: binary}
}

// SetDebug enables debug logging.
func (e *Executor) SetDebug(debug bool) {
	e.debug = debug
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
// Uses direct JSON manipulation for state changes, which is much faster
// than running individual terraform state mv commands.
func (e *Executor) Execute(ctx context.Context, cfg *Migration, targetDir string) (*Result, error) {
	result := &Result{}

	// Run inline moves first using direct JSON manipulation
	if len(cfg.Moves) > 0 {
		if err := e.executeMovesDirect(cfg.Moves, targetDir); err != nil {
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

// executeMovesDirect performs state moves using direct JSON manipulation.
// This is much faster than running terraform state mv for each move.
// Supports both individual resource moves and module-level moves (renaming all resources under a module).
func (e *Executor) executeMovesDirect(moves []Move, targetDir string) error {
	statePath := targetDir + "/terraform.tfstate"
	sf, err := LoadStateFile(statePath)
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	for i, mv := range moves {
		fromAddr := mv.GetFromResource()
		toAddr := mv.GetToResource()
		if e.debug {
			fmt.Printf("[DEBUG] move %q -> %q\n", fromAddr, toAddr)
		}

		// Check if this is a module-level move (from address is just a module path)
		if IsModuleAddress(fromAddr) {
			// Module-level move: rename all resources under the source module
			// Extract the module paths
			fromModule, _, _, _ := parseStateAddress(fromAddr)
			toModule, _, _, _ := parseStateAddress(toAddr)

			moved := sf.MoveModule(fromModule, toModule)
			if moved == 0 {
				return fmt.Errorf("move[%d] (from: %q -> to: %q) failed: module %q not found in source state (no resources exist under this module path)", i, fromAddr, toAddr, fromAddr)
			}
			if e.debug {
				fmt.Printf("[DEBUG] module move: renamed %d resources from %q to %q\n", moved, fromModule, toModule)
			}
		} else {
			// Individual resource move
			if !sf.MoveResource(fromAddr, toAddr) {
				// Build a helpful error message showing what resources exist
				available := sf.ListResources()
				hint := ""
				if len(available) == 0 {
					hint = " (source state is empty)"
				} else if len(available) <= 10 {
					hint = fmt.Sprintf("\n  Available resources in source state: %v", available)
				} else {
					hint = fmt.Sprintf("\n  Source state contains %d resources. Use --debug to see full list.", len(available))
				}
				return fmt.Errorf("move[%d] (from: %q -> to: %q) failed: resource %q not found in source state%s", i, fromAddr, toAddr, fromAddr, hint)
			}
		}
	}

	if err := sf.Save(); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
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

// SourceWorkspaceResources maps source workspace names to their resource addresses.
// Used for auto-detecting which source workspace contains a resource.
type SourceWorkspaceResources map[string][]string

// ExecuteMultiWorkspace runs migrations for multi-workspace configurations.
// It handles routing moves to the correct target workspace.
// For workspace splits (copying full state then trimming), it:
// 1. Groups moves by target workspace
// 2. For each workspace, removes resources NOT assigned to it
// 3. Performs any actual moves (where from != to address)
//
// Deprecated: Use ExecuteMultiWorkspaceWithSources for source workspace auto-detection.
func (e *Executor) ExecuteMultiWorkspace(
	ctx context.Context,
	cfg *Migration,
	targetWorkspaces map[string]string, // workspace name -> directory
) (*Result, error) {
	return e.ExecuteMultiWorkspaceWithSources(ctx, cfg, nil, targetWorkspaces)
}

// ExecuteMultiWorkspaceWithSources runs migrations for multi-workspace configurations
// with source workspace auto-detection support.
//
// sourceResources maps source workspace name -> list of resource addresses in that workspace.
// When a move's source workspace is not specified, it will be auto-detected by finding
// which source workspace contains the resource. An error is returned if the resource
// exists in multiple source workspaces (ambiguous).
//
// This implementation uses direct JSON manipulation for state changes, which is much
// faster than running individual terraform state rm/mv commands.
func (e *Executor) ExecuteMultiWorkspaceWithSources(
	ctx context.Context,
	cfg *Migration,
	sourceResources SourceWorkspaceResources, // workspace name -> resource addresses
	targetWorkspaces map[string]string,       // workspace name -> directory
) (*Result, error) {
	result := &Result{}

	// Build reverse lookup: resource address -> source workspace(s)
	resourceToSourceWS := make(map[string][]string)
	for wsName, resources := range sourceResources {
		for _, addr := range resources {
			resourceToSourceWS[addr] = append(resourceToSourceWS[addr], wsName)
		}
	}

	// Group moves by target workspace and resolve source workspaces
	movesByWorkspace := make(map[string][]Move)
	allSourceAddrs := make(map[string]string)    // from addr -> target workspace (for individual resources)
	moduleSourceAddrs := make(map[string]string) // module prefix -> target workspace (for module-level moves)

	for i, mv := range cfg.Moves {
		fromResource := mv.GetFromResource()
		fromWorkspace := mv.GetFromWorkspace()

		// Check if this is a module-level move
		isModuleMove := IsModuleAddress(fromResource)

		// Auto-detect source workspace if not specified
		if fromWorkspace == "" && sourceResources != nil && !isModuleMove {
			// For individual resources, look up directly
			sourceWSs := resourceToSourceWS[fromResource]
			switch len(sourceWSs) {
			case 0:
				// Resource not found in any source workspace - this is OK,
				// it might be a resource that only exists in some workspaces
			case 1:
				fromWorkspace = sourceWSs[0]
			default:
				return result, fmt.Errorf("move[%d] %q: ambiguous source workspace, resource exists in multiple workspaces: %v. Please specify 'from: { workspace: \"...\", resource: \"%s\" }'",
					i, fromResource, sourceWSs, fromResource)
			}
		}

		ws := mv.GetToWorkspace()
		if ws == "" {
			ws = "default"
		}
		movesByWorkspace[ws] = append(movesByWorkspace[ws], mv)

		if isModuleMove {
			// For module moves, store the module prefix
			fromModule, _, _, _ := parseStateAddress(fromResource)
			moduleSourceAddrs[fromModule] = ws
		} else {
			allSourceAddrs[fromResource] = ws
		}
	}

	// Create state file operator for direct JSON manipulation
	operator := NewStateFileOperator()
	operator.SetDebug(e.debug)

	// For each workspace:
	// 1. Remove resources that belong to OTHER workspaces
	// 2. Execute moves where from != to
	// All done via direct JSON manipulation - much faster than CLI calls
	for wsName, targetDir := range targetWorkspaces {
		// Load state file once
		statePath := targetDir + "/terraform.tfstate"
		sf, err := LoadStateFile(statePath)
		if err != nil {
			if os.IsNotExist(err) {
				// No state file means no resources to migrate - this is likely an error
				// in the configuration (target should have state copied from source first)
				return result, fmt.Errorf("workspace %q: state file not found at %s (was state copied from source?)", wsName, statePath)
			}
			return result, fmt.Errorf("workspace %q: failed to load state: %w", wsName, err)
		}

		// Get current resources in this workspace's state
		currentResources := sf.ListResources()

		// Build batch operations
		batch := NewBatchStateOperations(sf)

		// Queue removes for resources that belong to other workspaces
		for _, addr := range currentResources {
			// Check individual resource mapping first
			if targetWs, hasMapping := allSourceAddrs[addr]; hasMapping && targetWs != wsName {
				batch.Remove(addr)
				if e.debug {
					fmt.Printf("[DEBUG] workspace %q: queuing remove %q (belongs to %q)\n", wsName, addr, targetWs)
				}
				continue
			}

			// Check if this resource belongs to a module that's being moved to another workspace
			for modulePrefix, targetWs := range moduleSourceAddrs {
				if targetWs != wsName && resourceUnderModule(addr, modulePrefix) {
					batch.Remove(addr)
					if e.debug {
						fmt.Printf("[DEBUG] workspace %q: queuing remove %q (module %q belongs to %q)\n", wsName, addr, modulePrefix, targetWs)
					}
					break
				}
			}
		}

		// Queue moves for this workspace (only where from != to)
		moves := movesByWorkspace[wsName]
		for _, mv := range moves {
			fromAddr := mv.GetFromResource()
			toAddr := mv.GetToResource()
			if fromAddr != toAddr {
				// Check if this is a module-level move
				if IsModuleAddress(fromAddr) {
					fromModule, _, _, _ := parseStateAddress(fromAddr)
					toModule, _, _, _ := parseStateAddress(toAddr)
					batch.MoveModule(fromModule, toModule)
					if e.debug {
						fmt.Printf("[DEBUG] workspace %q: queuing module move %q -> %q\n", wsName, fromModule, toModule)
					}
				} else {
					batch.Move(fromAddr, toAddr)
					if e.debug {
						fmt.Printf("[DEBUG] workspace %q: queuing move %q -> %q\n", wsName, fromAddr, toAddr)
					}
				}
			}
		}

		// Apply all operations in one pass
		moved, removed, failedMoves := batch.Apply()
		if e.debug {
			fmt.Printf("[DEBUG] workspace %q: applied %d moves, %d removes\n", wsName, moved, removed)
		}

		// Fail if any moves couldn't be applied (resource not found in state)
		if len(failedMoves) > 0 {
			return result, fmt.Errorf("workspace %q: %d move(s) failed - resources not found in state: %v", wsName, len(failedMoves), failedMoves)
		}

		result.MovesExecuted += moved

		// Save the modified state
		if err := sf.Save(); err != nil {
			return result, fmt.Errorf("workspace %q: failed to save state: %w", wsName, err)
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

// MigrationStatusCallback is called to report migration progress per workspace.
type MigrationStatusCallback func(workspace, status string)

// ExecuteMultiWorkspaceParallel runs migrations for multi-workspace configurations in parallel.
// It's similar to ExecuteMultiWorkspaceWithSources but processes workspaces concurrently.
// The statusFn callback is called to report progress for each workspace.
func (e *Executor) ExecuteMultiWorkspaceParallel(
	ctx context.Context,
	cfg *Migration,
	sourceResources SourceWorkspaceResources,
	targetWorkspaces map[string]string,
	statusFn MigrationStatusCallback,
) (*Result, error) {
	result := &Result{}

	// Build reverse lookup: resource address -> source workspace(s)
	resourceToSourceWS := make(map[string][]string)
	for wsName, resources := range sourceResources {
		for _, addr := range resources {
			resourceToSourceWS[addr] = append(resourceToSourceWS[addr], wsName)
		}
	}

	// Group moves by target workspace and resolve source workspaces
	movesByWorkspace := make(map[string][]Move)
	allSourceAddrs := make(map[string]string)
	moduleSourceAddrs := make(map[string]string)

	for i, mv := range cfg.Moves {
		fromResource := mv.GetFromResource()
		fromWorkspace := mv.GetFromWorkspace()
		isModuleMove := IsModuleAddress(fromResource)

		if fromWorkspace == "" && sourceResources != nil && !isModuleMove {
			sourceWSs := resourceToSourceWS[fromResource]
			switch len(sourceWSs) {
			case 0:
			case 1:
				fromWorkspace = sourceWSs[0]
			default:
				return result, fmt.Errorf("move[%d] %q: ambiguous source workspace, resource exists in multiple workspaces: %v",
					i, fromResource, sourceWSs)
			}
		}

		ws := mv.GetToWorkspace()
		if ws == "" {
			ws = "default"
		}
		movesByWorkspace[ws] = append(movesByWorkspace[ws], mv)

		if isModuleMove {
			fromModule, _, _, _ := parseStateAddress(fromResource)
			moduleSourceAddrs[fromModule] = ws
		} else {
			allSourceAddrs[fromResource] = ws
		}
	}

	// Process workspaces in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	totalMoves := 0

	for wsName, targetDir := range targetWorkspaces {
		wg.Add(1)
		go func(wsName, targetDir string) {
			defer wg.Done()

			if statusFn != nil {
				statusFn(wsName, "migrating")
			}

			moved, err := e.executeWorkspaceMigration(
				wsName, targetDir,
				movesByWorkspace[wsName],
				allSourceAddrs,
				moduleSourceAddrs,
			)

			mu.Lock()
			defer mu.Unlock()

			if err != nil && firstErr == nil {
				firstErr = err
			}
			totalMoves += moved

			if statusFn != nil {
				if err != nil {
					statusFn(wsName, "failed")
				} else {
					statusFn(wsName, "done")
				}
			}
		}(wsName, targetDir)
	}

	wg.Wait()

	if firstErr != nil {
		return result, firstErr
	}

	result.MovesExecuted = totalMoves

	// Run script once (if configured) - script handles multi-workspace itself
	if cfg.Script != "" {
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

// executeWorkspaceMigration performs migration for a single workspace.
// Returns the number of moves executed.
func (e *Executor) executeWorkspaceMigration(
	wsName, targetDir string,
	moves []Move,
	allSourceAddrs map[string]string,
	moduleSourceAddrs map[string]string,
) (int, error) {
	statePath := targetDir + "/terraform.tfstate"
	sf, err := LoadStateFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("workspace %q: state file not found at %s (was state copied from source?)", wsName, statePath)
		}
		return 0, fmt.Errorf("workspace %q: failed to load state: %w", wsName, err)
	}

	currentResources := sf.ListResources()
	batch := NewBatchStateOperations(sf)

	// Queue removes for resources that belong to other workspaces
	for _, addr := range currentResources {
		if targetWs, hasMapping := allSourceAddrs[addr]; hasMapping && targetWs != wsName {
			batch.Remove(addr)
			if e.debug {
				fmt.Printf("[DEBUG] workspace %q: queuing remove %q (belongs to %q)\n", wsName, addr, targetWs)
			}
			continue
		}

		for modulePrefix, targetWs := range moduleSourceAddrs {
			if targetWs != wsName && resourceUnderModule(addr, modulePrefix) {
				batch.Remove(addr)
				if e.debug {
					fmt.Printf("[DEBUG] workspace %q: queuing remove %q (module %q belongs to %q)\n", wsName, addr, modulePrefix, targetWs)
				}
				break
			}
		}
	}

	// Queue moves for this workspace (only where from != to)
	for _, mv := range moves {
		fromAddr := mv.GetFromResource()
		toAddr := mv.GetToResource()
		if fromAddr != toAddr {
			if IsModuleAddress(fromAddr) {
				fromModule, _, _, _ := parseStateAddress(fromAddr)
				toModule, _, _, _ := parseStateAddress(toAddr)
				batch.MoveModule(fromModule, toModule)
				if e.debug {
					fmt.Printf("[DEBUG] workspace %q: queuing module move %q -> %q\n", wsName, fromModule, toModule)
				}
			} else {
				batch.Move(fromAddr, toAddr)
				if e.debug {
					fmt.Printf("[DEBUG] workspace %q: queuing move %q -> %q\n", wsName, fromAddr, toAddr)
				}
			}
		}
	}

	moved, _, failedMoves := batch.Apply()
	if e.debug {
		fmt.Printf("[DEBUG] workspace %q: applied %d moves\n", wsName, moved)
	}

	if len(failedMoves) > 0 {
		return moved, fmt.Errorf("workspace %q: %d move(s) failed - resources not found in state: %v", wsName, len(failedMoves), failedMoves)
	}

	if err := sf.Save(); err != nil {
		return moved, fmt.Errorf("workspace %q: failed to save state: %w", wsName, err)
	}

	return moved, nil
}

// DryRun shows what migrations would be executed without running them.
func (e *Executor) DryRun(cfg *Migration) []string {
	var actions []string

	for _, mv := range cfg.Moves {
		fromResource := mv.GetFromResource()
		toResource := mv.GetToResource()
		action := fmt.Sprintf("state mv %q -> %q", fromResource, toResource)
		if ws := mv.GetFromWorkspace(); ws != "" {
			action += fmt.Sprintf(" (from workspace: %s)", ws)
		}
		if ws := mv.GetToWorkspace(); ws != "" {
			action += fmt.Sprintf(" (to workspace: %s)", ws)
		}
		actions = append(actions, action)
	}

	if cfg.Script != "" {
		actions = append(actions, fmt.Sprintf("run script: %s", cfg.Script))
	}

	return actions
}
