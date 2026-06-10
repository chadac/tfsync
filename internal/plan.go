package internal

import (
	"fmt"
	"sort"
	"strings"
)

// MigrationPlan describes the complete set of state operations for a migration.
// Built from config + source resource lists BEFORE any state modification.
// This allows validation (e.g., every source resource appears exactly once)
// before any state files are touched.
type MigrationPlan struct {
	// SourceResources maps source workspace name -> resource addresses.
	SourceResources map[string][]string

	// WorkspacePlans maps target workspace name -> plan for that workspace.
	WorkspacePlans map[string]*WorkspacePlan

	// Script is an optional post-migration script path (opaque, not validated).
	Script string

	// Warnings holds non-fatal validation warnings.
	Warnings []string
}

// WorkspacePlan describes the state operations for a single target workspace.
type WorkspacePlan struct {
	// SourceWorkspace is which source workspace this target pulls state from.
	SourceWorkspace string

	// Keep are resource addresses that stay as-is in this target workspace.
	Keep []string

	// Moves maps from-address -> to-address for resources that get renamed.
	Moves map[string]string

	// ModuleMoves maps from-module-prefix -> to-module-prefix for module renames.
	ModuleMoves map[string]string

	// Removes are resource addresses that exist in this target's source state
	// but belong to a different target workspace and should be removed.
	Removes []string

	// ProviderRemaps maps full provider strings from source to target format.
	// Pre-computed from short-name config. Applied during state file generation.
	// Example: {"provider[\"registry.terraform.io/hashicorp/aws\"].ireland": "provider[\"registry.terraform.io/hashicorp/aws\"]"}
	ProviderRemaps map[string]string
}

// TranslateSourceAddress translates a source resource address to the target
// address space using this workspace's Moves and ModuleMoves mappings.
// Returns the translated address and true if a mapping was found, or the
// original address and false if no mapping applies.
func (wp *WorkspacePlan) TranslateSourceAddress(sourceAddr string) (string, bool) {
	// Check exact moves first
	if targetAddr, ok := wp.Moves[sourceAddr]; ok {
		return targetAddr, true
	}

	// Check module prefix moves
	for fromPrefix, toPrefix := range wp.ModuleMoves {
		if strings.HasPrefix(sourceAddr, fromPrefix+".") {
			return toPrefix + sourceAddr[len(fromPrefix):], true
		}
		if sourceAddr == fromPrefix {
			return toPrefix, true
		}
	}

	// No translation needed — address is the same in source and target (Keep resources)
	return sourceAddr, false
}

// TranslateUpstreamDiffs translates a slice of source upstream diffs into the
// target address space so they can be matched against target plan diffs.
func (wp *WorkspacePlan) TranslateUpstreamDiffs(diffs []ResourceDiff) []ResourceDiff {
	translated := make([]ResourceDiff, len(diffs))
	for i, d := range diffs {
		translated[i] = d
		addr, _ := wp.TranslateSourceAddress(d.Address)
		translated[i].Address = addr
	}
	return translated
}

// resourceAssignment tracks which target workspace a source resource is assigned to.
type resourceAssignment struct {
	targetWorkspace string
	// sourceWorkspace is which source workspace the resource came from
	sourceWorkspace string
	// renamed indicates the resource is being moved/renamed (not just kept)
	renamed bool
	// assignedByMove is the index of the move that assigned this resource (-1 if implicit)
	assignedByMove int
	// assignedByFrom is the from address of the move that assigned this resource
	assignedByFrom string
	// assignedByTo is the to address of the move that assigned this resource
	assignedByTo string
	// assignedByLine is the YAML line number of the move (0 means unknown)
	assignedByLine int
}

// moveRef formats a move reference for error messages, including line number if available.
func moveRef(index int, line int, from, to string) string {
	if line > 0 {
		return fmt.Sprintf("move[%d] (line %d, from: %q -> to: %q)", index, line, from, to)
	}
	return fmt.Sprintf("move[%d] (from: %q -> to: %q)", index, from, to)
}

// SourceResourceProviders maps source workspace name -> resource address -> provider string.
// Used for provider remap validation.
type SourceResourceProviders map[string]map[string]string

// PlanOptions holds optional parameters for BuildMigrationPlan.
type PlanOptions struct {
	// SourceProviderRemaps maps source workspace name -> short-name provider remap.
	// Applied as the base layer for all targets pulling from that source workspace.
	// Overridden by Migration.ProviderRemap, target workspace, and per-move remaps.
	SourceProviderRemaps map[string]map[string]string

	// TargetProviderRemaps maps target workspace name -> short-name provider remap.
	// Takes precedence over source workspace and Migration.ProviderRemap,
	// overridden by per-move ProviderRemap.
	TargetProviderRemaps map[string]map[string]string

	// SourceProviders maps source workspace -> resource address -> provider string.
	// When provided, enables validation that aliased providers have remaps configured.
	SourceProviders SourceResourceProviders
}

// BuildMigrationPlan builds a migration plan from config, source resources, and target-to-source mapping.
//
// sourceResources maps source workspace name -> list of resource addresses in that workspace.
// targetToSource maps target workspace name -> source workspace name it pulls from.
// opts is optional (may be nil) and provides per-workspace provider remaps and validation data.
//
// The plan validates:
//   - Every move references a resource that exists in a source workspace
//   - No resource is assigned to multiple target workspaces
//   - Every source resource is assigned to exactly one target workspace
//     (relaxed when only one target pulls from a given source)
func BuildMigrationPlan(
	cfg *Migration,
	sourceResources SourceWorkspaceResources,
	targetToSource map[string]string,
	opts *PlanOptions,
) (*MigrationPlan, error) {
	if opts == nil {
		opts = &PlanOptions{}
	}

	// Normalize source resources to resource-level addresses.
	// terraform state list returns instance-level addresses (e.g., "foo[0]", "foo[1]")
	// but the state JSON stores resources at the block level ("foo") with instances
	// as a nested array. Normalize here so the entire plan operates consistently
	// with what executeWorkspacePlan will actually find in the state file.
	sourceResources = normalizeSourceResources(sourceResources)

	plan := &MigrationPlan{
		SourceResources: sourceResources,
		WorkspacePlans:  make(map[string]*WorkspacePlan),
		Script:          cfg.Script,
	}

	// Initialize workspace plans
	for tgtName, srcName := range targetToSource {
		plan.WorkspacePlans[tgtName] = &WorkspacePlan{
			SourceWorkspace: srcName,
			Moves:           make(map[string]string),
			ModuleMoves:     make(map[string]string),
		}
	}

	// Build reverse lookups
	// resource address -> list of source workspaces containing it
	resourceToSourceWS := make(map[string][]string)
	for wsName, resources := range sourceResources {
		for _, addr := range resources {
			resourceToSourceWS[addr] = append(resourceToSourceWS[addr], wsName)
		}
	}

	// source workspace -> list of target workspaces that pull from it
	srcToTargets := make(map[string][]string)
	for tgtName, srcName := range targetToSource {
		srcToTargets[srcName] = append(srcToTargets[srcName], tgtName)
	}

	// Track assignment: "sourceWorkspace\x00resourceAddr" -> which target workspace owns it
	assignments := make(map[string]*resourceAssignment)
	assignmentKey := func(srcWS, addr string) string { return srcWS + "\x00" + addr }

	// Track module assignments for duplicate detection
	type moduleAssignmentEntry struct {
		targetWorkspace string
		toModule        string
		moveIndex       int
		moveLine        int
		fromAddr        string
		toAddr          string
	}
	moduleAssignments := make(map[string]*moduleAssignmentEntry) // "sourceWorkspace\x00fromModule" -> assignment

	// Track data sources explicitly assigned to multiple workspaces.
	// These are allowed because data sources are read-only and can be duplicated.
	// Key: "srcWorkspace\x00rAddr", Value: set of all target workspaces.
	duplicatedDataSources := make(map[string]map[string]bool)

	// Collect all validation errors instead of failing on the first one
	var errs []string

	// Process each move from config
	for i, mv := range cfg.Moves {
		fromResource := mv.GetFromResource()
		fromWorkspace := mv.GetFromWorkspace()
		toResource := mv.GetToResource()
		toWorkspace := mv.GetToWorkspace()
		mvLine := mv.Line
		mvRef := moveRef(i, mvLine, fromResource, toResource)

		if toWorkspace == "" {
			toWorkspace = "default"
		}

		isModuleMove := IsModuleAddress(fromResource)

		// Validate target workspace exists in plan
		wsPlan, ok := plan.WorkspacePlans[toWorkspace]
		if !ok {
			var available []string
			for name := range plan.WorkspacePlans {
				available = append(available, name)
			}
			sort.Strings(available)
			errs = append(errs, fmt.Sprintf("%s: target workspace %q not found (available: %v)",
				mvRef, toWorkspace, available))
			continue
		}

		// Resolve source workspace for individual resource moves
		if fromWorkspace == "" && !isModuleMove {
			sourceWSs := resourceToSourceWS[fromResource]
			switch len(sourceWSs) {
			case 0:
				errs = append(errs, fmt.Sprintf("%s: resource not found in any source workspace",
					mvRef))
				continue
			case 1:
				fromWorkspace = sourceWSs[0]
			default:
				errs = append(errs, fmt.Sprintf("%s: ambiguous source workspace, resource exists in %v; specify 'from: { workspace: \"...\", resource: %q }'",
					mvRef, sourceWSs, fromResource))
				continue
			}
		}

		// For module moves, resolve source workspace by scanning for resources under the module
		if fromWorkspace == "" && isModuleMove {
			fromModule, _, _, _ := parseStateAddress(fromResource)
			var foundIn []string
			for wsName, resources := range sourceResources {
				for _, rAddr := range resources {
					if resourceUnderModule(rAddr, fromModule) {
						foundIn = append(foundIn, wsName)
						break
					}
				}
			}
			switch len(foundIn) {
			case 0:
				errs = append(errs, fmt.Sprintf("%s: module not found in any source workspace (no resources exist under this module path)",
					mvRef))
				continue
			case 1:
				fromWorkspace = foundIn[0]
			default:
				errs = append(errs, fmt.Sprintf("%s: ambiguous source workspace, module has resources in %v; specify 'from: { workspace: \"...\", resource: %q }'",
					mvRef, foundIn, fromResource))
				continue
			}
		}

		// Validate: the target workspace's source must match the move's source
		if fromWorkspace != "" && fromWorkspace != wsPlan.SourceWorkspace {
			errs = append(errs, fmt.Sprintf("%s: resource is in source workspace %q but target workspace %q pulls from %q",
				mvRef, fromWorkspace, toWorkspace, wsPlan.SourceWorkspace))
			continue
		}

		if isModuleMove {
			fromModule, _, _, _ := parseStateAddress(fromResource)
			toModule, _, _, _ := parseStateAddress(toResource)

			// Check for duplicate module assignment
			moduleKey := fromWorkspace + "\x00" + fromModule
			if existing, ok := moduleAssignments[moduleKey]; ok {
				errs = append(errs, fmt.Sprintf(
					"%s: module %q already assigned to target workspace %q\n"+
						"  first assigned by: %s (target workspace: %q)\n"+
						"  conflicting:       %s (target workspace: %q)",
					mvRef, fromModule, existing.targetWorkspace,
					moveRef(existing.moveIndex, existing.moveLine, existing.fromAddr, existing.toAddr), existing.targetWorkspace,
					mvRef, toWorkspace))
				continue
			}
			moduleAssignments[moduleKey] = &moduleAssignmentEntry{
				targetWorkspace: toWorkspace,
				toModule:        toModule,
				moveIndex:       i,
				moveLine:        mvLine,
				fromAddr:        fromResource,
				toAddr:          toResource,
			}
			// Record module move in workspace plan if it's a rename
			if fromModule != toModule {
				wsPlan.ModuleMoves[fromModule] = toModule
			}

			// Expand: assign all individual resources under this module
			// Only expand from the resolved source workspace to avoid false duplicates
			for _, rAddr := range sourceResources[fromWorkspace] {
				if resourceUnderModule(rAddr, fromModule) {
					aKey := assignmentKey(fromWorkspace, rAddr)
					if existing, ok := assignments[aKey]; ok {
						// Skip duplicates from the same move (source state can have duplicate addresses)
						if existing.assignedByMove == i {
							continue
						}
						errs = append(errs, fmt.Sprintf(
							"%s: resource %q (under module %q) already assigned to target workspace %q\n"+
								"  first assigned by: %s (source workspace: %q, target workspace: %q)\n"+
								"  conflicting:       %s (source workspace: %q, target workspace: %q)",
							mvRef, rAddr, fromModule, existing.targetWorkspace,
							moveRef(existing.assignedByMove, existing.assignedByLine, existing.assignedByFrom, existing.assignedByTo), existing.sourceWorkspace, existing.targetWorkspace,
							mvRef, fromWorkspace, toWorkspace))
						continue
					}
					assignments[aKey] = &resourceAssignment{
						targetWorkspace: toWorkspace,
						sourceWorkspace: fromWorkspace,
						renamed:         fromModule != toModule,
						assignedByMove:  i,
						assignedByFrom:  fromResource,
						assignedByTo:    toResource,
						assignedByLine:  mvLine,
					}
				}
			}
		} else {
			// Individual resource move
			key := assignmentKey(fromWorkspace, fromResource)
			if existing, ok := assignments[key]; ok {
				// Data sources can be explicitly duplicated into multiple workspaces
				if IsDataSourceAddress(fromResource) && existing.targetWorkspace != toWorkspace {
					if duplicatedDataSources[key] == nil {
						duplicatedDataSources[key] = map[string]bool{existing.targetWorkspace: true}
					}
					duplicatedDataSources[key][toWorkspace] = true
					// Add to the new workspace's Keep (or Moves if renamed)
					if fromResource != toResource {
						wsPlan.Moves[fromResource] = toResource
					} else {
						wsPlan.Keep = append(wsPlan.Keep, fromResource)
					}
					continue
				}
				errs = append(errs, fmt.Sprintf(
					"%s: resource %q already assigned to target workspace %q\n"+
						"  first assigned by: %s (source workspace: %q, target workspace: %q)\n"+
						"  conflicting:       %s (source workspace: %q, target workspace: %q)",
					mvRef, fromResource, existing.targetWorkspace,
					moveRef(existing.assignedByMove, existing.assignedByLine, existing.assignedByFrom, existing.assignedByTo), existing.sourceWorkspace, existing.targetWorkspace,
					mvRef, fromWorkspace, toWorkspace))
				continue
			}
			assignments[key] = &resourceAssignment{
				targetWorkspace: toWorkspace,
				sourceWorkspace: fromWorkspace,
				renamed:         fromResource != toResource,
				assignedByMove:  i,
				assignedByFrom:  fromResource,
				assignedByTo:    toResource,
				assignedByLine:  mvLine,
			}

			// Record in workspace plan if it's a rename
			if fromResource != toResource {
				wsPlan.Moves[fromResource] = toResource
			}
		}
	}

	// Assign all unmentioned source resources
	for srcName, resources := range sourceResources {
		targets := srcToTargets[srcName]
		for _, rAddr := range resources {
			if _, assigned := assignments[assignmentKey(srcName, rAddr)]; assigned {
				continue // Already handled by a move
			}

			switch len(targets) {
			case 0:
				// No target references this source workspace — orphaned
				errs = append(errs, fmt.Sprintf("resource %q in source workspace %q is not included in any move and has no target workspace",
					rAddr, srcName))
			case 1:
				// Exactly one target uses this source — implicitly kept
				assignments[assignmentKey(srcName, rAddr)] = &resourceAssignment{
					targetWorkspace: targets[0],
					sourceWorkspace: srcName,
					renamed:         false,
					assignedByMove:  -1, // implicit assignment
				}
			default:
				// Multiple targets share the same source — ambiguous
				sort.Strings(targets)
				errs = append(errs, fmt.Sprintf("resource %q in source workspace %q is not included in any move but must be assigned to a target workspace (candidates: %v); add an explicit move with to.workspace",
					rAddr, srcName, targets))
			}
		}
	}

	// Return all collected errors
	if len(errs) > 0 {
		return nil, fmt.Errorf("migration plan has %d error(s):\n- %s", len(errs), strings.Join(errs, "\n- "))
	}

	// Build Keep lists for each workspace plan
	for key, assignment := range assignments {
		tgtName := assignment.targetWorkspace
		wsPlan := plan.WorkspacePlans[tgtName]
		// Extract bare resource address from composite key "srcWorkspace\x00rAddr"
		rAddr := key[strings.IndexByte(key, '\x00')+1:]

		if !assignment.renamed {
			// Only add to Keep if not already recorded as a move source
			if _, isMove := wsPlan.Moves[rAddr]; !isMove {
				wsPlan.Keep = append(wsPlan.Keep, rAddr)
			}
		}
	}

	// Build Removes for each workspace: resources in source state assigned to other targets
	for tgtName, wsPlan := range plan.WorkspacePlans {
		srcResources := sourceResources[wsPlan.SourceWorkspace]
		for _, rAddr := range srcResources {
			key := assignmentKey(wsPlan.SourceWorkspace, rAddr)
			// Duplicated data sources are present in multiple workspaces — don't remove them
			if dups, ok := duplicatedDataSources[key]; ok && dups[tgtName] {
				continue
			}
			assignment := assignments[key]
			if assignment != nil && assignment.targetWorkspace != tgtName {
				wsPlan.Removes = append(wsPlan.Removes, rAddr)
			}
		}
		// Sort for deterministic output
		sort.Strings(wsPlan.Keep)
		sort.Strings(wsPlan.Removes)
	}

	// Build provider remap tables for each workspace plan.
	// Merge priority (lowest to highest):
	//   1. Source workspace provider_remap
	//   2. Global Migration.provider_remap
	//   3. Target workspace provider_remap
	//   4. Per-move provider_remap
	hasAnyRemap := len(cfg.ProviderRemap) > 0 ||
		anyMoveHasProviderRemap(cfg.Moves) ||
		anyWorkspaceHasProviderRemap(opts.TargetProviderRemaps) ||
		anyWorkspaceHasProviderRemap(opts.SourceProviderRemaps)

	if hasAnyRemap {
		// Collect per-workspace short-name remaps
		wsRemaps := make(map[string]map[string]string) // target workspace -> merged short-name remap

		for tgtName, wsPlan := range plan.WorkspacePlans {
			merged := make(map[string]string)

			// Layer 1: source workspace ProviderRemap (lowest priority)
			if srcRemap, ok := opts.SourceProviderRemaps[wsPlan.SourceWorkspace]; ok {
				for k, v := range srcRemap {
					merged[k] = v
				}
			}

			// Layer 2: global Migration.ProviderRemap
			for k, v := range cfg.ProviderRemap {
				merged[k] = v
			}

			// Layer 3: per-target-workspace ProviderRemap
			if wsRemap, ok := opts.TargetProviderRemaps[tgtName]; ok {
				for k, v := range wsRemap {
					merged[k] = v
				}
			}

			if len(merged) > 0 {
				wsRemaps[tgtName] = merged
			}
		}

		// Layer 3: per-move ProviderRemap (highest priority)
		for _, mv := range cfg.Moves {
			if len(mv.ProviderRemap) == 0 {
				continue
			}
			toWorkspace := mv.GetToWorkspace()
			if toWorkspace == "" {
				toWorkspace = "default"
			}
			if _, ok := wsRemaps[toWorkspace]; !ok {
				wsRemaps[toWorkspace] = make(map[string]string)
			}
			for k, v := range mv.ProviderRemap {
				wsRemaps[toWorkspace][k] = v // per-move wins
			}
		}

		// Convert short-name remaps to full provider string remaps
		for tgtName, shortRemap := range wsRemaps {
			if wsPlan, ok := plan.WorkspacePlans[tgtName]; ok {
				wsPlan.ProviderRemaps = BuildProviderRemapTable(shortRemap)
			}
		}
	}

	// Validate provider remaps: warn when source resources use aliased providers without a remap.
	if opts.SourceProviders != nil {
		for tgtName, wsPlan := range plan.WorkspacePlans {
			srcProviders := opts.SourceProviders[wsPlan.SourceWorkspace]
			if srcProviders == nil {
				continue
			}

			// Collect unique provider strings for resources assigned to this target
			seenProviders := make(map[string]bool)
			for _, addr := range wsPlan.Keep {
				if p, ok := srcProviders[addr]; ok {
					seenProviders[p] = true
				}
			}
			for fromAddr := range wsPlan.Moves {
				if p, ok := srcProviders[fromAddr]; ok {
					seenProviders[p] = true
				}
			}
			for fromModule := range wsPlan.ModuleMoves {
				for addr, p := range srcProviders {
					if resourceUnderModule(addr, fromModule) {
						seenProviders[p] = true
					}
				}
			}

			// Error for aliased providers without a remap
			for providerStr := range seenProviders {
				_, alias, ok := ParseProviderString(providerStr)
				if !ok || alias == "" {
					continue // default provider, no remap needed
				}
				if _, hasRemap := wsPlan.ProviderRemaps[providerStr]; !hasRemap {
					errs = append(errs, fmt.Sprintf(
						"target workspace %q (source %q): source resources use aliased provider %q but no provider_remap is configured for it; "+
							"add provider_remap to your target workspace or migration config",
						tgtName, wsPlan.SourceWorkspace, providerStr))
				}
			}
		}
	}

	// Return provider remap validation errors
	if len(errs) > 0 {
		return nil, fmt.Errorf("migration plan has %d error(s):\n- %s", len(errs), strings.Join(errs, "\n- "))
	}

	return plan, nil
}

// anyMoveHasProviderRemap returns true if any move has a per-move provider remap.
func anyMoveHasProviderRemap(moves []Move) bool {
	for _, mv := range moves {
		if len(mv.ProviderRemap) > 0 {
			return true
		}
	}
	return false
}

// anyWorkspaceHasProviderRemap returns true if any target workspace has a provider remap.
func anyWorkspaceHasProviderRemap(remaps map[string]map[string]string) bool {
	for _, m := range remaps {
		if len(m) > 0 {
			return true
		}
	}
	return false
}

// normalizeSourceResources converts instance-level resource addresses to
// resource-level addresses and deduplicates them.
//
// terraform state list returns instance-level addresses like:
//
//	module.foo.aws_instance.bar[0]
//	module.foo.aws_instance.bar[1]
//
// But the state JSON stores these as a single resource block
// "module.foo.aws_instance.bar" with instances as a nested array.
// Since tfsync operates on whole resource blocks (not individual instances),
// we normalize to resource-level addresses so the plan matches the state
// structure exactly.
//
// Module indices (e.g., module.foo[0]) are preserved since those represent
// distinct resource blocks in the state JSON.
func normalizeSourceResources(src SourceWorkspaceResources) SourceWorkspaceResources {
	normalized := make(SourceWorkspaceResources, len(src))
	for wsName, addrs := range src {
		seen := make(map[string]bool)
		var deduped []string
		for _, addr := range addrs {
			base := stripInstanceIndex(addr)
			if !seen[base] {
				seen[base] = true
				deduped = append(deduped, base)
			}
		}
		normalized[wsName] = deduped
	}
	return normalized
}

// Summary returns a human-readable summary of the migration plan.
func (p *MigrationPlan) Summary() []string {
	var lines []string

	// Sort workspace names for deterministic output
	var wsNames []string
	for name := range p.WorkspacePlans {
		wsNames = append(wsNames, name)
	}
	sort.Strings(wsNames)

	for _, wsName := range wsNames {
		wsPlan := p.WorkspacePlans[wsName]
		lines = append(lines, fmt.Sprintf("Target workspace %q (from source %q):", wsName, wsPlan.SourceWorkspace))

		for from, to := range wsPlan.ModuleMoves {
			lines = append(lines, fmt.Sprintf("  module move: %s -> %s", from, to))
		}

		var froms []string
		for from := range wsPlan.Moves {
			froms = append(froms, from)
		}
		sort.Strings(froms)
		for _, from := range froms {
			lines = append(lines, fmt.Sprintf("  move: %s -> %s", from, wsPlan.Moves[from]))
		}

		lines = append(lines, fmt.Sprintf("  keep: %d resources", len(wsPlan.Keep)))
		lines = append(lines, fmt.Sprintf("  remove: %d resources", len(wsPlan.Removes)))
		if len(wsPlan.ProviderRemaps) > 0 {
			lines = append(lines, fmt.Sprintf("  provider remaps: %d", len(wsPlan.ProviderRemaps)))
			for from, to := range wsPlan.ProviderRemaps {
				lines = append(lines, fmt.Sprintf("    %s -> %s", from, to))
			}
		}
	}

	if p.Script != "" {
		lines = append(lines, fmt.Sprintf("Script: %s", p.Script))
	}

	return lines
}

// TotalMoves returns the total number of move operations across all workspaces.
func (p *MigrationPlan) TotalMoves() int {
	total := 0
	for _, wsPlan := range p.WorkspacePlans {
		total += len(wsPlan.Moves) + len(wsPlan.ModuleMoves)
	}
	return total
}

// TotalRemoves returns the total number of remove operations across all workspaces.
func (p *MigrationPlan) TotalRemoves() int {
	total := 0
	for _, wsPlan := range p.WorkspacePlans {
		total += len(wsPlan.Removes)
	}
	return total
}

// IsDataSourceAddress checks if a resource address refers to a data source.
// Handles both top-level "data.x.y" and module-scoped "module.foo.data.x.y".
func IsDataSourceAddress(addr string) bool {
	parts := strings.Split(addr, ".")
	for i, part := range parts {
		if part == "data" && i+2 < len(parts) {
			return true
		}
	}
	return false
}

// CrossWorkspaceDependency describes a dependency that crosses workspace boundaries.
type CrossWorkspaceDependency struct {
	// Resource is the resource that has the dependency
	Resource string
	// ResourceWorkspace is the target workspace the resource is assigned to
	ResourceWorkspace string
	// Dependency is the resource address being depended on
	Dependency string
	// DependencyWorkspace is the target workspace the dependency is assigned to
	DependencyWorkspace string
	// SourceWorkspace is the source workspace both came from
	SourceWorkspace string
}

// CrossWorkspaceDependencyResult categorizes cross-workspace dependencies.
type CrossWorkspaceDependencyResult struct {
	// Errors are cross-workspace deps that are not covered by depends_on and are not data sources.
	Errors []CrossWorkspaceDependency
	// Warnings are cross-workspace deps that are covered by depends_on (expected/acknowledged).
	Warnings []CrossWorkspaceDependency
}

// CheckCrossWorkspaceDependencies checks whether any resource has dependencies
// on resources assigned to a different target workspace. This detects cases where
// splitting state would break resource references.
//
// sourceDeps maps source workspace name -> resource address -> dependency addresses.
// targetWorkspaces provides the depends_on graph for filtering expected dependencies.
// The plan must already be built (resources assigned to workspaces).
//
// Dependencies on data sources (data.*) are silently skipped — data sources are
// read-only and will be re-evaluated in each workspace independently.
//
// Dependencies where the resource's workspace declares depends_on the dependency's
// workspace are reported as warnings (acknowledged/expected) rather than errors.
func (p *MigrationPlan) CheckCrossWorkspaceDependencies(
	sourceDeps SourceResourceDependencies,
	targetWorkspaces map[string]Workspace,
) CrossWorkspaceDependencyResult {
	// Build depends_on lookup: workspace -> set of workspaces it depends on
	dependsOnGraph := make(map[string]map[string]bool)
	for wsName, ws := range targetWorkspaces {
		if len(ws.DependsOn) > 0 {
			dependsOnGraph[wsName] = make(map[string]bool)
			for _, dep := range ws.DependsOn {
				dependsOnGraph[wsName][dep] = true
			}
		}
	}
	// Build a reverse index: for each source workspace, map resource address -> target workspace.
	// This handles both Keep (same address) and Moves (from-address -> target workspace).
	// Also track module prefixes for module-level dependency resolution.
	type wsAssignment struct {
		targetWorkspace string
		sourceWorkspace string
	}

	resourceToTarget := make(map[string]wsAssignment) // "srcWs:addr" -> assignment
	modulePrefixes := make(map[string]wsAssignment)   // "srcWs:module.foo" -> assignment

	for tgtName, wsPlan := range p.WorkspacePlans {
		srcWs := wsPlan.SourceWorkspace

		// Resources kept as-is
		for _, addr := range wsPlan.Keep {
			key := srcWs + ":" + addr
			resourceToTarget[key] = wsAssignment{targetWorkspace: tgtName, sourceWorkspace: srcWs}
		}

		// Resources being moved (index by from-address)
		for fromAddr := range wsPlan.Moves {
			key := srcWs + ":" + fromAddr
			resourceToTarget[key] = wsAssignment{targetWorkspace: tgtName, sourceWorkspace: srcWs}
		}

		// Module moves — register the module prefix
		for fromModule := range wsPlan.ModuleMoves {
			key := srcWs + ":" + fromModule
			modulePrefixes[key] = wsAssignment{targetWorkspace: tgtName, sourceWorkspace: srcWs}
		}
	}

	// resolveWorkspace finds which target workspace a dependency address belongs to.
	resolveWorkspace := func(srcWs, depAddr string) (string, bool) {
		// Direct resource match
		key := srcWs + ":" + depAddr
		if asgn, ok := resourceToTarget[key]; ok {
			return asgn.targetWorkspace, true
		}

		// Module-level match: check if depAddr starts with any registered module prefix
		for modKey, asgn := range modulePrefixes {
			if asgn.sourceWorkspace != srcWs {
				continue
			}
			modPrefix := modKey[len(srcWs)+1:] // strip "srcWs:" prefix
			if depAddr == modPrefix || strings.HasPrefix(depAddr, modPrefix+".") {
				return asgn.targetWorkspace, true
			}
		}

		// Check if depAddr is a resource under any module that was implicitly assigned
		// by checking all resource assignments for prefix match
		for resKey, asgn := range resourceToTarget {
			if asgn.sourceWorkspace != srcWs {
				continue
			}
			resAddr := resKey[len(srcWs)+1:]
			// If dep is a module and a resource under it is assigned
			if strings.HasPrefix(resAddr, depAddr+".") {
				return asgn.targetWorkspace, true
			}
		}

		return "", false
	}

	var result CrossWorkspaceDependencyResult

	// Check each resource's dependencies
	for srcWs, deps := range sourceDeps {
		for resAddr, depAddrs := range deps {
			resKey := srcWs + ":" + resAddr
			resAsgn, resFound := resourceToTarget[resKey]
			if !resFound {
				// Resource not assigned (maybe it's under a module move)
				// Try to resolve via module prefix
				resTgt, found := resolveWorkspace(srcWs, resAddr)
				if !found {
					continue // resource not in the plan at all
				}
				resAsgn = wsAssignment{targetWorkspace: resTgt, sourceWorkspace: srcWs}
			}

			for _, depAddr := range depAddrs {
				// Skip data sources — they're read-only and each workspace
				// will have its own copy after the split
				if IsDataSourceAddress(depAddr) {
					continue
				}

				depTgt, found := resolveWorkspace(srcWs, depAddr)
				if !found {
					continue // dependency not in the plan (e.g., data source not tracked)
				}

				if depTgt != resAsgn.targetWorkspace {
					cd := CrossWorkspaceDependency{
						Resource:            resAddr,
						ResourceWorkspace:   resAsgn.targetWorkspace,
						Dependency:          depAddr,
						DependencyWorkspace: depTgt,
						SourceWorkspace:     srcWs,
					}

					// If the resource's workspace depends_on the dependency's workspace,
					// this is an acknowledged/expected cross-workspace reference
					if deps, ok := dependsOnGraph[resAsgn.targetWorkspace]; ok && deps[depTgt] {
						result.Warnings = append(result.Warnings, cd)
					} else {
						result.Errors = append(result.Errors, cd)
					}
				}
			}
		}
	}

	// Sort for deterministic output
	sortDeps := func(deps []CrossWorkspaceDependency) {
		sort.Slice(deps, func(i, j int) bool {
			if deps[i].ResourceWorkspace != deps[j].ResourceWorkspace {
				return deps[i].ResourceWorkspace < deps[j].ResourceWorkspace
			}
			if deps[i].Resource != deps[j].Resource {
				return deps[i].Resource < deps[j].Resource
			}
			return deps[i].Dependency < deps[j].Dependency
		})
	}
	sortDeps(result.Errors)
	sortDeps(result.Warnings)

	return result
}
