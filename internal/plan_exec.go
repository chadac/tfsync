package internal

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// ExecutePlan executes a pre-built migration plan against target directories.
// This replaces the older ExecuteMultiWorkspace* methods by working from a
// validated plan rather than computing operations on the fly.
//
// targetDirs maps target workspace name -> absolute directory path.
// statusFn is an optional callback for progress reporting.
func (e *Executor) ExecutePlan(
	ctx context.Context,
	plan *MigrationPlan,
	targetDirs map[string]string,
	statusFn MigrationStatusCallback,
) (*Result, error) {
	result := &Result{}

	// Process workspaces in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	totalMoves := 0

	for wsName, wsPlan := range plan.WorkspacePlans {
		targetDir, ok := targetDirs[wsName]
		if !ok {
			// This workspace is not being executed (e.g., targeted run)
			continue
		}

		wg.Add(1)
		go func(wsName, targetDir string, wsPlan *WorkspacePlan) {
			defer wg.Done()

			if statusFn != nil {
				statusFn(wsName, "migrating")
			}

			moved, err := e.executeWorkspacePlan(wsName, targetDir, wsPlan)

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
		}(wsName, targetDir, wsPlan)
	}

	wg.Wait()

	if firstErr != nil {
		return result, firstErr
	}

	result.MovesExecuted = totalMoves

	// Run script once (if configured)
	if plan.Script != "" {
		// Use the first available target directory as working dir for script
		var firstDir string
		for _, dir := range targetDirs {
			firstDir = dir
			break
		}
		if firstDir != "" {
			if err := e.executeScript(ctx, plan.Script, firstDir); err != nil {
				return result, err
			}
			result.ScriptExecuted = true
		}
	}

	return result, nil
}

// executeWorkspacePlan builds a new state file for a target workspace from the
// source state, containing only the resources assigned to this workspace.
// Instead of copying the full source state and then removing/renaming, this
// builds the target state from scratch by selecting only the needed resources.
// Returns the number of moves executed.
func (e *Executor) executeWorkspacePlan(wsName, targetDir string, wsPlan *WorkspacePlan) (int, error) {
	statePath := targetDir + "/terraform.tfstate"
	sourceSF, err := LoadStateFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("workspace %q: state file not found at %s (was state copied from source?)", wsName, statePath)
		}
		return 0, fmt.Errorf("workspace %q: failed to load state: %w", wsName, err)
	}

	sourceState := sourceSF.GetState()

	// Build index: resource address -> StateResource for fast lookup.
	// Resource addresses in the state JSON are at the resource level (e.g.,
	// "module.foo.aws_instance.bar"), NOT the instance level (e.g.,
	// "module.foo.aws_instance.bar[0]"). The plan's Keep/Moves lists use
	// instance-level addresses from `terraform state list`, so we must
	// strip instance indices when looking up resources.
	resourceIndex := make(map[string]StateResource)
	for _, r := range sourceState.Resources {
		resourceIndex[r.Address()] = r
	}

	// Build new state from scratch with same metadata
	newState := &TerraformState{
		Version:          sourceState.Version,
		TerraformVersion: sourceState.TerraformVersion,
		Serial:           sourceState.Serial,
		Lineage:          sourceState.Lineage,
		Outputs:          nil, // Outputs are workspace-specific; don't copy from source
		Resources:        nil,
	}

	// Track which resources have already been added to avoid duplicates.
	// Multiple instance-level addresses (e.g., foo[0], foo[1]) map to the
	// same resource block in the state JSON, so we only add it once.
	addedResources := make(map[string]bool)

	moved := 0

	// Add kept resources (no rename needed)
	for _, addr := range wsPlan.Keep {
		baseAddr := stripInstanceIndex(addr)
		if addedResources[baseAddr] {
			continue
		}
		if r, ok := resourceIndex[baseAddr]; ok {
			newState.Resources = append(newState.Resources, deduplicateInstances(r))
			addedResources[baseAddr] = true
			if e.debug {
				fmt.Printf("[DEBUG] workspace %q: keeping %q\n", wsName, baseAddr)
			}
		}
	}

	// Add individually moved resources (with rename)
	for fromAddr, toAddr := range wsPlan.Moves {
		baseFromAddr := stripInstanceIndex(fromAddr)
		if addedResources[baseFromAddr] {
			continue
		}
		r, ok := resourceIndex[baseFromAddr]
		if !ok {
			return moved, fmt.Errorf("workspace %q: move source %q not found in state", wsName, baseFromAddr)
		}
		// Parse destination address and update resource fields
		baseToAddr := stripInstanceIndex(toAddr)
		newModule, newType, newName, isData := parseStateAddress(baseToAddr)
		r.Module = newModule
		r.Type = newType
		r.Name = newName
		if isData {
			r.Mode = "data"
		} else {
			r.Mode = "managed"
		}
		newState.Resources = append(newState.Resources, deduplicateInstances(r))
		addedResources[baseFromAddr] = true
		moved++
		if e.debug {
			fmt.Printf("[DEBUG] workspace %q: moving %q -> %q\n", wsName, baseFromAddr, baseToAddr)
		}
	}

	// Add module-moved resources (rename module prefix for all resources under the module)
	for fromModule, toModule := range wsPlan.ModuleMoves {
		srcPrefix := fromModule + "."
		for _, r := range sourceState.Resources {
			if r.Module == fromModule {
				r.Module = toModule
				newState.Resources = append(newState.Resources, deduplicateInstances(r))
				moved++
			} else if len(r.Module) > len(srcPrefix) && r.Module[:len(srcPrefix)] == srcPrefix {
				// Nested module under fromModule
				dstPrefix := ""
				if toModule != "" {
					dstPrefix = toModule + "."
				}
				r.Module = dstPrefix + r.Module[len(srcPrefix):]
				newState.Resources = append(newState.Resources, deduplicateInstances(r))
				moved++
			}
		}
		if e.debug {
			fmt.Printf("[DEBUG] workspace %q: module move %q -> %q\n", wsName, fromModule, toModule)
		}
	}

	// Apply provider remapping
	if len(wsPlan.ProviderRemaps) > 0 {
		for i := range newState.Resources {
			if newProvider, ok := wsPlan.ProviderRemaps[newState.Resources[i].Provider]; ok {
				if e.debug {
					fmt.Printf("[DEBUG] workspace %q: remapping provider %q -> %q for %s\n",
						wsName, newState.Resources[i].Provider, newProvider, newState.Resources[i].Address())
				}
				newState.Resources[i].Provider = newProvider
			}
		}
	}

	// Write the new state
	targetSF := &StateFile{
		state: newState,
		path:  statePath,
	}
	if err := targetSF.Save(); err != nil {
		return moved, fmt.Errorf("workspace %q: failed to save state: %w", wsName, err)
	}

	return moved, nil
}
