package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestBuildMigrationPlan_SingleWorkspace(t *testing.T) {
	// Single source, single target, a few moves
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_instance.old"}, To: MoveTo{Resource: "aws_instance.new"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_instance.old", "aws_vpc.main", "aws_subnet.private"},
	}

	targetToSource := map[string]string{
		"default": "default",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	wsPlan := plan.WorkspacePlans["default"]
	if wsPlan == nil {
		t.Fatal("missing default workspace plan")
	}

	// Should have 1 move
	if len(wsPlan.Moves) != 1 {
		t.Errorf("expected 1 move, got %d", len(wsPlan.Moves))
	}
	if wsPlan.Moves["aws_instance.old"] != "aws_instance.new" {
		t.Errorf("unexpected move: %v", wsPlan.Moves)
	}

	// Should keep the other 2 resources
	if len(wsPlan.Keep) != 2 {
		t.Errorf("expected 2 keeps, got %d: %v", len(wsPlan.Keep), wsPlan.Keep)
	}

	// No removes (only one target)
	if len(wsPlan.Removes) != 0 {
		t.Errorf("expected 0 removes, got %d", len(wsPlan.Removes))
	}
}

func TestBuildMigrationPlan_MultiWorkspaceSplit(t *testing.T) {
	// One source, two targets — splitting state
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_vpc.main"}, To: MoveTo{Resource: "aws_vpc.main", Workspace: "networking"}},
			{From: MoveFrom{Resource: "aws_subnet.private"}, To: MoveTo{Resource: "aws_subnet.private", Workspace: "networking"}},
			{From: MoveFrom{Resource: "aws_instance.web"}, To: MoveTo{Resource: "aws_instance.web", Workspace: "compute"}},
			{From: MoveFrom{Resource: "aws_instance.api"}, To: MoveTo{Resource: "aws_instance.api", Workspace: "compute"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_vpc.main", "aws_subnet.private", "aws_instance.web", "aws_instance.api"},
	}

	targetToSource := map[string]string{
		"networking": "default",
		"compute":    "default",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Networking should keep vpc and subnet, remove instances
	net := plan.WorkspacePlans["networking"]
	if len(net.Keep) != 2 {
		t.Errorf("networking: expected 2 keeps, got %d: %v", len(net.Keep), net.Keep)
	}
	if len(net.Removes) != 2 {
		t.Errorf("networking: expected 2 removes, got %d: %v", len(net.Removes), net.Removes)
	}

	// Compute should keep instances, remove vpc/subnet
	comp := plan.WorkspacePlans["compute"]
	if len(comp.Keep) != 2 {
		t.Errorf("compute: expected 2 keeps, got %d: %v", len(comp.Keep), comp.Keep)
	}
	if len(comp.Removes) != 2 {
		t.Errorf("compute: expected 2 removes, got %d: %v", len(comp.Removes), comp.Removes)
	}
}

func TestBuildMigrationPlan_DuplicateAssignment(t *testing.T) {
	// Same resource assigned to two different targets
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_vpc.main"}, To: MoveTo{Resource: "aws_vpc.main", Workspace: "networking"}},
			{From: MoveFrom{Resource: "aws_vpc.main"}, To: MoveTo{Resource: "aws_vpc.main", Workspace: "compute"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_vpc.main", "aws_instance.web"},
	}

	targetToSource := map[string]string{
		"networking": "default",
		"compute":    "default",
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err == nil {
		t.Fatal("expected error for duplicate assignment")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "already assigned") {
		t.Errorf("expected 'already assigned' in error, got: %v", errMsg)
	}
	// Error should show both conflicting moves
	if !strings.Contains(errMsg, "first assigned by") {
		t.Errorf("expected 'first assigned by' in error, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "conflicting") {
		t.Errorf("expected 'conflicting' in error, got: %v", errMsg)
	}
	// Error should show source and target workspaces
	if !strings.Contains(errMsg, "networking") {
		t.Errorf("expected 'networking' workspace in error, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "compute") {
		t.Errorf("expected 'compute' workspace in error, got: %v", errMsg)
	}
	// Error should show source workspace
	if !strings.Contains(errMsg, "default") {
		t.Errorf("expected source workspace 'default' in error, got: %v", errMsg)
	}
}

func TestBuildMigrationPlan_OrphanedResource(t *testing.T) {
	// Multiple targets share same source but a resource isn't in any move
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_vpc.main"}, To: MoveTo{Resource: "aws_vpc.main", Workspace: "networking"}},
			// aws_instance.web is NOT assigned to any target
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_vpc.main", "aws_instance.web"},
	}

	targetToSource := map[string]string{
		"networking": "default",
		"compute":    "default",
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err == nil {
		t.Fatal("expected error for unassigned resource")
	}
	if !strings.Contains(err.Error(), "not included in any move") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildMigrationPlan_ResourceNotFound(t *testing.T) {
	// Move references a resource that doesn't exist in source
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_instance.nonexistent"}, To: MoveTo{Resource: "aws_instance.new"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_vpc.main"},
	}

	targetToSource := map[string]string{
		"default": "default",
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err == nil {
		t.Fatal("expected error for missing resource")
	}
	if !strings.Contains(err.Error(), "not found in any source") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildMigrationPlan_ModuleMove(t *testing.T) {
	// Module-level move assigns all resources under module
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "module.foo"}, To: MoveTo{Resource: "module.bar", Workspace: "target-a"}},
			{From: MoveFrom{Resource: "aws_instance.orphan"}, To: MoveTo{Resource: "aws_instance.orphan", Workspace: "target-b"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {
			"module.foo.aws_instance.a",
			"module.foo.aws_instance.b",
			"module.foo.module.nested.aws_vpc.main",
			"aws_instance.orphan",
		},
	}

	targetToSource := map[string]string{
		"target-a": "default",
		"target-b": "default",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	tgtA := plan.WorkspacePlans["target-a"]
	// All 3 resources under module.foo should be in target-a
	// They are "renamed" (module.foo -> module.bar) so not in Keep
	if len(tgtA.ModuleMoves) != 1 {
		t.Errorf("expected 1 module move, got %d", len(tgtA.ModuleMoves))
	}
	if tgtA.ModuleMoves["module.foo"] != "module.bar" {
		t.Errorf("unexpected module move: %v", tgtA.ModuleMoves)
	}
	// Should remove aws_instance.orphan (belongs to target-b)
	if len(tgtA.Removes) != 1 {
		t.Errorf("expected 1 remove, got %d: %v", len(tgtA.Removes), tgtA.Removes)
	}

	tgtB := plan.WorkspacePlans["target-b"]
	// Should remove all 3 module.foo resources
	if len(tgtB.Removes) != 3 {
		t.Errorf("expected 3 removes, got %d: %v", len(tgtB.Removes), tgtB.Removes)
	}
	// Should keep aws_instance.orphan
	if len(tgtB.Keep) != 1 {
		t.Errorf("expected 1 keep, got %d: %v", len(tgtB.Keep), tgtB.Keep)
	}
}

func TestBuildMigrationPlan_ModuleMoveDuplicate(t *testing.T) {
	// Same module assigned twice
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "module.foo"}, To: MoveTo{Resource: "module.bar", Workspace: "target-a"}},
			{From: MoveFrom{Resource: "module.foo"}, To: MoveTo{Resource: "module.baz", Workspace: "target-b"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"module.foo.aws_instance.a"},
	}

	targetToSource := map[string]string{
		"target-a": "default",
		"target-b": "default",
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err == nil {
		t.Fatal("expected error for duplicate module assignment")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "already assigned") {
		t.Errorf("expected 'already assigned' in error, got: %v", errMsg)
	}
	// Error should show both conflicting moves with context
	if !strings.Contains(errMsg, "first assigned by") {
		t.Errorf("expected 'first assigned by' in error, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "target-a") {
		t.Errorf("expected 'target-a' in error, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "target-b") {
		t.Errorf("expected 'target-b' in error, got: %v", errMsg)
	}
}

func TestBuildMigrationPlan_MultiSourceMultiTarget(t *testing.T) {
	// Two sources, two targets, 1:1 mapping
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_instance.old", Workspace: "src-a"}, To: MoveTo{Resource: "aws_instance.new", Workspace: "tgt-a"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"src-a": {"aws_instance.old", "aws_vpc.main"},
		"src-b": {"aws_instance.web"},
	}

	targetToSource := map[string]string{
		"tgt-a": "src-a",
		"tgt-b": "src-b",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	tgtA := plan.WorkspacePlans["tgt-a"]
	if len(tgtA.Moves) != 1 {
		t.Errorf("tgt-a: expected 1 move, got %d", len(tgtA.Moves))
	}
	if len(tgtA.Keep) != 1 {
		t.Errorf("tgt-a: expected 1 keep, got %d: %v", len(tgtA.Keep), tgtA.Keep)
	}

	tgtB := plan.WorkspacePlans["tgt-b"]
	if len(tgtB.Keep) != 1 {
		t.Errorf("tgt-b: expected 1 keep, got %d: %v", len(tgtB.Keep), tgtB.Keep)
	}
}

func TestBuildMigrationPlan_ScriptOnly(t *testing.T) {
	// Script-only migration (no moves) — should work with single target per source
	cfg := &Migration{
		Script: "./migrate.sh",
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_instance.a", "aws_vpc.main"},
	}

	targetToSource := map[string]string{
		"default": "default",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	if plan.Script != "./migrate.sh" {
		t.Errorf("expected script path, got %q", plan.Script)
	}

	wsPlan := plan.WorkspacePlans["default"]
	// All resources should be kept
	if len(wsPlan.Keep) != 2 {
		t.Errorf("expected 2 keeps, got %d", len(wsPlan.Keep))
	}
}

func TestBuildMigrationPlan_AmbiguousSourceWorkspace(t *testing.T) {
	// Resource exists in multiple source workspaces, no explicit from.workspace
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_instance.shared"}, To: MoveTo{Resource: "aws_instance.shared"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"src-a": {"aws_instance.shared"},
		"src-b": {"aws_instance.shared"},
	}

	targetToSource := map[string]string{
		"default": "src-a",
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err == nil {
		t.Fatal("expected error for ambiguous source workspace")
	}
	if !strings.Contains(err.Error(), "ambiguous source workspace") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildMigrationPlan_IdentityMoveWithWorkspace(t *testing.T) {
	// Move with same from/to address but explicit workspace assignment
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_vpc.main"}, To: MoveTo{Resource: "aws_vpc.main", Workspace: "networking"}},
			{From: MoveFrom{Resource: "aws_instance.web"}, To: MoveTo{Resource: "aws_instance.web", Workspace: "compute"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_vpc.main", "aws_instance.web"},
	}

	targetToSource := map[string]string{
		"networking": "default",
		"compute":    "default",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Both should be in Keep (identity moves, not renames)
	net := plan.WorkspacePlans["networking"]
	if len(net.Keep) != 1 || net.Keep[0] != "aws_vpc.main" {
		t.Errorf("networking: expected [aws_vpc.main] in keep, got %v", net.Keep)
	}
	if len(net.Removes) != 1 || net.Removes[0] != "aws_instance.web" {
		t.Errorf("networking: expected [aws_instance.web] in removes, got %v", net.Removes)
	}

	comp := plan.WorkspacePlans["compute"]
	if len(comp.Keep) != 1 || comp.Keep[0] != "aws_instance.web" {
		t.Errorf("compute: expected [aws_instance.web] in keep, got %v", comp.Keep)
	}
	if len(comp.Removes) != 1 || comp.Removes[0] != "aws_vpc.main" {
		t.Errorf("compute: expected [aws_vpc.main] in removes, got %v", comp.Removes)
	}
}

func TestBuildMigrationPlan_WrongTargetWorkspace(t *testing.T) {
	// Move references a target workspace that doesn't exist
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_instance.a"}, To: MoveTo{Resource: "aws_instance.a", Workspace: "nonexistent"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_instance.a"},
	}

	targetToSource := map[string]string{
		"default": "default",
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent target workspace")
	}
	if !strings.Contains(err.Error(), "target workspace") && !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildMigrationPlan_SourceWorkspaceMismatch(t *testing.T) {
	// Move specifies from.workspace that doesn't match target's source
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_instance.a", Workspace: "src-b"}, To: MoveTo{Resource: "aws_instance.a", Workspace: "tgt-a"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"src-a": {"aws_instance.other"},
		"src-b": {"aws_instance.a"},
	}

	targetToSource := map[string]string{
		"tgt-a": "src-a", // tgt-a pulls from src-a, but move says resource is in src-b
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err == nil {
		t.Fatal("expected error for source workspace mismatch")
	}
	if !strings.Contains(err.Error(), "pulls from") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestExecutePlan_EndToEnd tests the full plan -> execute flow with real state files
func TestExecutePlan_EndToEnd(t *testing.T) {
	// Create temp dirs with state files
	tmpDir := t.TempDir()
	netDir := filepath.Join(tmpDir, "networking")
	compDir := filepath.Join(tmpDir, "compute")
	os.MkdirAll(netDir, 0755)
	os.MkdirAll(compDir, 0755)

	// Create source state with 4 resources
	sourceState := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_vpc", Name: "main"},
			{Mode: "managed", Type: "aws_subnet", Name: "private", Module: "module.networking"},
			{Mode: "managed", Type: "aws_instance", Name: "web"},
			{Mode: "managed", Type: "aws_instance", Name: "api"},
		},
	}
	stateData, _ := json.MarshalIndent(sourceState, "", "  ")

	// Write same state to both targets (simulating state copy)
	os.WriteFile(filepath.Join(netDir, "terraform.tfstate"), stateData, 0644)
	os.WriteFile(filepath.Join(compDir, "terraform.tfstate"), stateData, 0644)

	// Build plan
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_vpc.main"}, To: MoveTo{Resource: "aws_vpc.main", Workspace: "networking"}},
			{From: MoveFrom{Resource: "module.networking.aws_subnet.private"}, To: MoveTo{Resource: "module.networking.aws_subnet.private", Workspace: "networking"}},
			{From: MoveFrom{Resource: "aws_instance.web"}, To: MoveTo{Resource: "aws_instance.web", Workspace: "compute"}},
			{From: MoveFrom{Resource: "aws_instance.api"}, To: MoveTo{Resource: "aws_instance.api", Workspace: "compute"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_vpc.main", "module.networking.aws_subnet.private", "aws_instance.web", "aws_instance.api"},
	}

	targetToSource := map[string]string{
		"networking": "default",
		"compute":    "default",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Execute plan
	executor := NewExecutor("tofu")
	result, err := executor.ExecutePlan(nil, plan, map[string]string{
		"networking": netDir,
		"compute":    compDir,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.MovesExecuted != 0 {
		// No renames in this plan, just keeps and removes
		// MovesExecuted counts actual state moves (renames), not remove operations
	}

	// Verify networking state
	netSF, err := LoadStateFile(filepath.Join(netDir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	netAddrs := netSF.ListResources()
	sort.Strings(netAddrs)
	expectedNet := []string{"aws_vpc.main", "module.networking.aws_subnet.private"}
	sort.Strings(expectedNet)
	if len(netAddrs) != len(expectedNet) {
		t.Errorf("networking: expected %v, got %v", expectedNet, netAddrs)
	}
	for i := range netAddrs {
		if i < len(expectedNet) && netAddrs[i] != expectedNet[i] {
			t.Errorf("networking: expected %q, got %q", expectedNet[i], netAddrs[i])
		}
	}

	// Verify compute state
	compSF, err := LoadStateFile(filepath.Join(compDir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	compAddrs := compSF.ListResources()
	sort.Strings(compAddrs)
	expectedComp := []string{"aws_instance.api", "aws_instance.web"}
	sort.Strings(expectedComp)
	if len(compAddrs) != len(expectedComp) {
		t.Errorf("compute: expected %v, got %v", expectedComp, compAddrs)
	}
	for i := range compAddrs {
		if i < len(expectedComp) && compAddrs[i] != expectedComp[i] {
			t.Errorf("compute: expected %q, got %q", expectedComp[i], compAddrs[i])
		}
	}
}

// TestExecutePlan_WithRenames tests plan execution with actual resource renames
func TestExecutePlan_WithRenames(t *testing.T) {
	tmpDir := t.TempDir()

	sourceState := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "old_name"},
			{Mode: "managed", Type: "aws_vpc", Name: "main"},
		},
	}
	stateData, _ := json.MarshalIndent(sourceState, "", "  ")
	os.WriteFile(filepath.Join(tmpDir, "terraform.tfstate"), stateData, 0644)

	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_instance.old_name"}, To: MoveTo{Resource: "aws_instance.new_name"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_instance.old_name", "aws_vpc.main"},
	}

	targetToSource := map[string]string{
		"default": "default",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor("tofu")
	result, err := executor.ExecutePlan(nil, plan, map[string]string{
		"default": tmpDir,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.MovesExecuted != 1 {
		t.Errorf("expected 1 move, got %d", result.MovesExecuted)
	}

	// Verify state
	sf, err := LoadStateFile(filepath.Join(tmpDir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	addrs := sf.ListResources()
	sort.Strings(addrs)
	expected := []string{"aws_instance.new_name", "aws_vpc.main"}
	if len(addrs) != 2 {
		t.Errorf("expected 2 resources, got %d: %v", len(addrs), addrs)
	}
	for i := range addrs {
		if i < len(expected) && addrs[i] != expected[i] {
			t.Errorf("expected %q, got %q", expected[i], addrs[i])
		}
	}
}

// TestExecutePlan_WithModuleRenames tests plan execution with module-level renames
func TestExecutePlan_WithModuleRenames(t *testing.T) {
	tmpDir := t.TempDir()

	sourceState := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "a", Module: `module.projects["foo"]`},
			{Mode: "managed", Type: "aws_instance", Name: "b", Module: `module.projects["foo"]`},
			{Mode: "managed", Type: "aws_vpc", Name: "main"},
		},
	}
	stateData, _ := json.MarshalIndent(sourceState, "", "  ")
	os.WriteFile(filepath.Join(tmpDir, "terraform.tfstate"), stateData, 0644)

	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: `module.projects["foo"]`}, To: MoveTo{Resource: "module.foo"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {
			`module.projects["foo"].aws_instance.a`,
			`module.projects["foo"].aws_instance.b`,
			"aws_vpc.main",
		},
	}

	targetToSource := map[string]string{
		"default": "default",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor("tofu")
	_, err = executor.ExecutePlan(nil, plan, map[string]string{
		"default": tmpDir,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	sf, err := LoadStateFile(filepath.Join(tmpDir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	addrs := sf.ListResources()
	sort.Strings(addrs)

	expected := []string{"aws_vpc.main", "module.foo.aws_instance.a", "module.foo.aws_instance.b"}
	if len(addrs) != len(expected) {
		t.Errorf("expected %v, got %v", expected, addrs)
	}
	for i := range addrs {
		if i < len(expected) && addrs[i] != expected[i] {
			t.Errorf("expected %q, got %q", expected[i], addrs[i])
		}
	}
}

func TestBuildMigrationPlan_ModuleMoveDataSourceNoDuplicate(t *testing.T) {
	// Regression test: module move with data sources should not produce false
	// duplicate errors. Previously the expansion loop iterated all source
	// workspaces instead of just the resolved one, causing self-conflicts.
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "module.automations"}, To: MoveTo{Resource: "module.automations", Workspace: "dev-core"}},
			{From: MoveFrom{Resource: "aws_instance.other"}, To: MoveTo{Resource: "aws_instance.other", Workspace: "dev-infra"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"development": {
			"module.automations.data.aws_caller_identity.current",
			"module.automations.aws_lambda_function.handler",
			"module.automations.aws_iam_role.lambda",
			"aws_instance.other",
		},
	}

	targetToSource := map[string]string{
		"dev-core":  "development",
		"dev-infra": "development",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	core := plan.WorkspacePlans["dev-core"]
	// All 3 module.automations resources should be assigned to dev-core (identity move, kept)
	if len(core.Keep) != 3 {
		t.Errorf("dev-core: expected 3 keeps, got %d: %v", len(core.Keep), core.Keep)
	}
	// aws_instance.other belongs to dev-infra, should be removed from dev-core
	if len(core.Removes) != 1 {
		t.Errorf("dev-core: expected 1 remove, got %d: %v", len(core.Removes), core.Removes)
	}

	infra := plan.WorkspacePlans["dev-infra"]
	if len(infra.Keep) != 1 {
		t.Errorf("dev-infra: expected 1 keep, got %d: %v", len(infra.Keep), infra.Keep)
	}
	// 3 module.automations resources should be removed from dev-infra
	if len(infra.Removes) != 3 {
		t.Errorf("dev-infra: expected 3 removes, got %d: %v", len(infra.Removes), infra.Removes)
	}
}

func TestBuildMigrationPlan_DuplicateResourceInSourceState(t *testing.T) {
	// Reproduce the real-world bug: ListResources() can return duplicate addresses
	// (e.g., data sources that appear multiple times in state). The module expansion
	// should handle this gracefully instead of reporting a false conflict.
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "module.automations"}, To: MoveTo{Resource: "module.automations", Workspace: "dev-core"}},
			{From: MoveFrom{Resource: "aws_instance.other"}, To: MoveTo{Resource: "aws_instance.other", Workspace: "dev-infra"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"development": {
			"module.automations.data.aws_caller_identity.current",
			"module.automations.data.aws_caller_identity.current", // duplicate from ListResources()
			"module.automations.aws_lambda_function.handler",
			"aws_instance.other",
		},
	}

	targetToSource := map[string]string{
		"dev-core":  "development",
		"dev-infra": "development",
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatalf("unexpected error (duplicate source resource should be tolerated): %v", err)
	}
}

func TestBuildMigrationPlan_SameModuleMultipleSourceWorkspaces(t *testing.T) {
	// Same module name (module.automations) exists in two different source workspaces.
	// Each is moved to a different target workspace — this should NOT conflict.
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "module.automations", Workspace: "development"}, To: MoveTo{Resource: "module.automations", Workspace: "dev-core"}},
			{From: MoveFrom{Resource: "module.automations", Workspace: "production"}, To: MoveTo{Resource: "module.automations", Workspace: "prod-core"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"development": {
			"module.automations.aws_lambda_function.dev_handler",
			"module.automations.aws_iam_role.dev_role",
			"aws_instance.dev_other",
		},
		"production": {
			"module.automations.aws_lambda_function.prod_handler",
			"module.automations.aws_iam_role.prod_role",
			"aws_instance.prod_other",
		},
	}

	targetToSource := map[string]string{
		"dev-core":  "development",
		"prod-core": "production",
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatalf("unexpected error (same module in different source workspaces should not conflict): %v", err)
	}

	// Verify dev-core got the development resources
	devPlan := plan.WorkspacePlans["dev-core"]
	if devPlan == nil {
		t.Fatal("expected dev-core workspace plan")
	}
	if len(devPlan.Keep) != 3 { // 2 module resources + 1 non-module
		t.Errorf("dev-core: expected 3 keep, got %d: %v", len(devPlan.Keep), devPlan.Keep)
	}

	// Verify prod-core got the production resources
	prodPlan := plan.WorkspacePlans["prod-core"]
	if prodPlan == nil {
		t.Fatal("expected prod-core workspace plan")
	}
	if len(prodPlan.Keep) != 3 { // 2 module resources + 1 non-module
		t.Errorf("prod-core: expected 3 keep, got %d: %v", len(prodPlan.Keep), prodPlan.Keep)
	}
}

func TestBuildMigrationPlan_MultipleErrors(t *testing.T) {
	// Multiple errors should all be reported, not just the first one.
	cfg := &Migration{
		Moves: []Move{
			// Error 1: resource not found
			{From: MoveFrom{Resource: "aws_instance.nonexistent"}, To: MoveTo{Resource: "aws_instance.nonexistent"}},
			// Error 2: target workspace not found
			{From: MoveFrom{Resource: "aws_instance.a"}, To: MoveTo{Resource: "aws_instance.a", Workspace: "bogus"}},
			// Error 3: another resource not found
			{From: MoveFrom{Resource: "aws_instance.also_nonexistent"}, To: MoveTo{Resource: "aws_instance.also_nonexistent"}},
		},
	}

	sourceResources := SourceWorkspaceResources{
		"default": {"aws_instance.a", "aws_instance.b"},
	}

	targetToSource := map[string]string{
		"default": "default",
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()

	// Should report the count
	if !strings.Contains(errMsg, "3 error(s)") {
		t.Errorf("expected error count '3 error(s)' in message, got:\n%s", errMsg)
	}

	// Should contain all three errors
	if !strings.Contains(errMsg, "aws_instance.nonexistent") {
		t.Errorf("expected first error about nonexistent resource in message, got:\n%s", errMsg)
	}
	if !strings.Contains(errMsg, `target workspace "bogus" not found`) {
		t.Errorf("expected second error about bogus workspace in message, got:\n%s", errMsg)
	}
	if !strings.Contains(errMsg, "aws_instance.also_nonexistent") {
		t.Errorf("expected third error about also_nonexistent resource in message, got:\n%s", errMsg)
	}
}
