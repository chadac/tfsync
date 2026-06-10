// Package internal contains the core tfsync implementation.
package internal

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the root tfsync configuration.
type Config struct {
	Version   string    `yaml:"version"`
	Source    Source    `yaml:"source"`
	Target    Target    `yaml:"target"`
	Migration Migration `yaml:"migration"`
	TF        *TF       `yaml:"tf,omitempty"`
	Cleanup   *Cleanup  `yaml:"cleanup,omitempty"`
}

// Source represents the source terraform configuration (where state comes from).
type Source struct {
	// Single workspace mode
	Path string `yaml:"path,omitempty"`
	// Init configuration (single workspace mode)
	Init *InitConfig `yaml:"init,omitempty"`
	// Plan configuration (single workspace mode)
	Plan *PlanConfig `yaml:"plan,omitempty"`
	// Deprecated: use Init.Backend instead
	Backend map[string]string `yaml:"backend,omitempty"`

	// Multi-workspace mode
	Workspaces map[string]Workspace `yaml:"workspaces,omitempty"`
}

// Target represents the target terraform configuration (where state goes).
type Target struct {
	// Single workspace mode
	Path string `yaml:"path,omitempty"`
	// Init configuration (single workspace mode)
	Init *InitConfig `yaml:"init,omitempty"`
	// Plan configuration (single workspace mode)
	Plan *PlanConfig `yaml:"plan,omitempty"`
	// Deprecated: use Init.Backend instead
	Backend map[string]string `yaml:"backend,omitempty"`

	// Multi-workspace mode
	Workspaces map[string]Workspace `yaml:"workspaces,omitempty"`
}

// InitConfig holds configuration for terraform init.
type InitConfig struct {
	// Backend config overrides passed as -backend-config=key=value
	Backend map[string]string `yaml:"backend,omitempty"`
	// ExtraArgs are additional arguments passed to init
	ExtraArgs []string `yaml:"extra_args,omitempty"`
}

// PlanConfig holds configuration for terraform plan.
type PlanConfig struct {
	// Lock controls state locking (-lock flag)
	Lock *bool `yaml:"lock,omitempty"`
	// Refresh controls state refresh (-refresh flag)
	Refresh *bool `yaml:"refresh,omitempty"`
	// ExtraArgs are additional arguments passed to plan
	ExtraArgs []string `yaml:"extra_args,omitempty"`
	// Vars is a map of variable name -> value, passed as -var flags.
	// Per-workspace vars override/supplement global TF.Vars.
	Vars map[string]string `yaml:"vars,omitempty"`
	// VarFiles is a list of -var-file paths.
	// Per-workspace var files are appended after global TF.VarFiles.
	VarFiles []string `yaml:"var_files,omitempty"`
	// OverrideFiles are terraform override files to write before running plan.
	// Map of filename -> content. Files are written to the target directory.
	// Example: {"provider_override.tf": "provider \"aws\" { ... }"}
	OverrideFiles map[string]string `yaml:"override_files,omitempty"`
	// IgnoreChanges is a list of resource addresses whose plan changes should be ignored.
	// If a plan has changes but all changed resources are in this list, the plan is considered passing.
	IgnoreChanges []string `yaml:"ignore_changes,omitempty"`
	// IgnoreErrors is a list of resource addresses whose plan errors should be ignored.
	// If a plan fails (exit code 1) but all error resource addresses are in this list,
	// the plan is considered passing.
	IgnoreErrors []string `yaml:"ignore_errors,omitempty"`
	// FallbackOutputs provides static output values to inject into the workspace's
	// state file when planned output extraction fails (e.g., due to provider init errors).
	// These values are used by dependent workspaces via terraform_remote_state.
	// Map of output name -> value (any YAML value: string, number, list, map).
	FallbackOutputs map[string]interface{} `yaml:"fallback_outputs,omitempty"`
}

// Workspace represents a single terraform workspace configuration.
type Workspace struct {
	Path string `yaml:"path"`
	// Init configuration
	Init *InitConfig `yaml:"init,omitempty"`
	// Plan configuration
	Plan *PlanConfig `yaml:"plan,omitempty"`
	// Deprecated: use Init.Backend instead
	Backend map[string]string `yaml:"backend,omitempty"`
	// PreferWorkspace specifies which source workspace this target prefers to pull from.
	// Only used for target workspaces. If empty, defaults to matching by name.
	PreferWorkspace string `yaml:"prefer_workspace,omitempty"`
	// ProviderRemap maps provider short names from source to target for this workspace.
	// Takes precedence over Migration-level ProviderRemap.
	// Example: {"aws.ireland": "aws"} remaps provider alias "ireland" to the default provider.
	ProviderRemap map[string]string `yaml:"provider_remap,omitempty"`
	// DependsOn lists target workspace names that must be planned before this one.
	// Only affects plan validation ordering — migrations run independently.
	// When a dependency is declared, tfsync auto-generates a terraform_remote_state
	// override so that the dependency's state is read from the local file.
	DependsOn []string `yaml:"depends_on,omitempty"`
	// RemoteState maps dependency workspace names to their terraform_remote_state
	// data source names. Used to override remote_state data sources to point at
	// local state files during validation.
	// Key is the dependency workspace name (must be in DependsOn).
	// If a dependency has no entry here, the data source name defaults to the
	// dependency workspace name.
	RemoteState map[string]RemoteStateMapping `yaml:"remote_state,omitempty"`
	// Env is a map of environment variables to set when running terraform commands
	// for this workspace. Values support ${VAR} expansion.
	Env map[string]string `yaml:"env,omitempty"`

	// stateFileName is the name of the local state file for this workspace.
	// Auto-set when multiple workspaces share the same path.
	// Defaults to "terraform.tfstate" when not set.
	stateFileName string
}

// RemoteStateMapping configures how a dependency workspace's terraform_remote_state
// data source should be overridden during local validation.
type RemoteStateMapping struct {
	// DataSource is the name used in data "terraform_remote_state" "<name>".
	// Defaults to the dependency workspace name if empty.
	DataSource string `yaml:"data_source,omitempty"`
}

// Migration defines how state should be transformed.
type Migration struct {
	// Script is an external script to run for complex migrations
	Script string `yaml:"script,omitempty"`

	// Moves is a list of state move operations (populated after resolving file references)
	Moves []Move `yaml:"-"`

	// rawMoves holds the raw YAML entries before file resolution
	rawMoves []rawMoveEntry `yaml:"-"`

	// ProviderRemap maps provider short names from source to target.
	// Applied globally to all moves. Per-move ProviderRemap takes precedence.
	// Example: {"aws.ireland": "aws"} remaps provider alias "ireland" to the default provider.
	ProviderRemap map[string]string `yaml:"provider_remap,omitempty"`
}

// rawMoveEntry represents a single entry in the moves list before file resolution.
// It can be either an inline Move or a file reference.
type rawMoveEntry struct {
	File string // non-empty if this is a file reference
	Move *Move  // non-nil if this is an inline move
}

// UnmarshalYAML implements custom unmarshaling for Migration to handle file references in moves.
func (m *Migration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Unmarshal non-moves fields normally
	type migrationAlias struct {
		Script        string            `yaml:"script,omitempty"`
		Moves         []yaml.Node       `yaml:"moves,omitempty"`
		ProviderRemap map[string]string `yaml:"provider_remap,omitempty"`
	}
	var raw migrationAlias
	if err := unmarshal(&raw); err != nil {
		return err
	}

	m.Script = raw.Script
	m.ProviderRemap = raw.ProviderRemap

	// Parse each move entry — could be a file reference or an inline move
	for _, node := range raw.Moves {
		// Check if this node has a "file" key (and no "from"/"to" keys)
		if node.Kind == yaml.MappingNode {
			isFileRef := false
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == "file" {
					filePath := node.Content[i+1].Value
					m.rawMoves = append(m.rawMoves, rawMoveEntry{File: filePath})
					isFileRef = true
					break
				}
			}
			if isFileRef {
				continue
			}
		}

		// Inline move — decode normally
		var mv Move
		if err := node.Decode(&mv); err != nil {
			return fmt.Errorf("invalid move entry: %w", err)
		}
		mv.Line = node.Line
		m.rawMoves = append(m.rawMoves, rawMoveEntry{Move: &mv})
	}

	return nil
}

// ResolveFiles expands file references in the moves list by loading external YAML files.
// baseDir is the directory containing the config file (file paths are resolved relative to it).
func (m *Migration) ResolveFiles(baseDir string) error {
	var moves []Move
	for _, entry := range m.rawMoves {
		if entry.Move != nil {
			moves = append(moves, *entry.Move)
			continue
		}

		// Load moves from external file
		filePath := entry.File
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(baseDir, filePath)
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read moves file %q: %w", entry.File, err)
		}

		var fileMoves []Move
		if err := yaml.Unmarshal(data, &fileMoves); err != nil {
			return fmt.Errorf("failed to parse moves file %q: %w", entry.File, err)
		}

		moves = append(moves, fileMoves...)
	}
	m.Moves = moves
	return nil
}

// Move represents a single terraform state mv operation.
// The `from` field supports two formats:
//   - Simple string: "module.foo.null_resource.bar" (auto-detects source workspace)
//   - Object: { workspace: "dev", resource: "module.foo.null_resource.bar" }
// The `to` field supports two formats:
//   - Simple string: "module.foo.null_resource.bar"
//   - Object: { workspace: "networking", resource: "null_resource.bar" }
type Move struct {
	From MoveFrom `yaml:"-"` // Custom unmarshaling
	To   MoveTo   `yaml:"-"` // Custom unmarshaling
	// TargetWorkspace is deprecated, use To.Workspace instead
	TargetWorkspace string `yaml:"target_workspace,omitempty"`
	// ProviderRemap maps provider short names for this specific move.
	// Takes precedence over Migration-level ProviderRemap.
	ProviderRemap map[string]string `yaml:"-"` // Custom unmarshaling
	// Line is the YAML source line number (1-indexed, 0 means unknown)
	Line int `yaml:"-"`
}

// MoveFrom represents the source of a state move.
type MoveFrom struct {
	Resource  string // The resource address
	Workspace string // Source workspace (for multi-workspace, empty means auto-detect)
}

// MoveTo represents the destination of a state move.
type MoveTo struct {
	Resource  string // The resource address
	Workspace string // Target workspace (for multi-workspace)
}

// UnmarshalYAML implements custom unmarshaling for Move to handle both formats.
func (m *Move) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// First unmarshal into a temporary struct
	type rawMove struct {
		From            interface{}       `yaml:"from"`
		To              interface{}       `yaml:"to"`
		TargetWorkspace string            `yaml:"target_workspace,omitempty"`
		ProviderRemap   map[string]string `yaml:"provider_remap,omitempty"`
	}
	var raw rawMove
	if err := unmarshal(&raw); err != nil {
		return err
	}

	m.TargetWorkspace = raw.TargetWorkspace
	m.ProviderRemap = raw.ProviderRemap

	// Handle the `from` field - can be string or object
	switch from := raw.From.(type) {
	case string:
		m.From = MoveFrom{Resource: from}
	case map[string]interface{}:
		if res, ok := from["resource"].(string); ok {
			m.From.Resource = res
		}
		if ws, ok := from["workspace"].(string); ok {
			m.From.Workspace = ws
		}
		if m.From.Resource == "" {
			return fmt.Errorf("invalid 'from' object: resource is required")
		}
	default:
		return fmt.Errorf("invalid 'from' field: must be string or object with resource/workspace")
	}

	// Handle the `to` field - can be string or object
	switch to := raw.To.(type) {
	case string:
		m.To = MoveTo{Resource: to}
	case map[string]interface{}:
		if res, ok := to["resource"].(string); ok {
			m.To.Resource = res
		}
		if ws, ok := to["workspace"].(string); ok {
			m.To.Workspace = ws
		}
	default:
		return fmt.Errorf("invalid 'to' field: must be string or object with resource/workspace")
	}

	// Merge target_workspace into To.Workspace (backwards compatibility)
	if m.To.Workspace == "" && m.TargetWorkspace != "" {
		m.To.Workspace = m.TargetWorkspace
	}

	return nil
}

// GetFromResource returns the source resource address.
func (m *Move) GetFromResource() string {
	return m.From.Resource
}

// GetFromWorkspace returns the source workspace (empty means auto-detect).
func (m *Move) GetFromWorkspace() string {
	return m.From.Workspace
}

// GetToResource returns the destination resource address.
func (m *Move) GetToResource() string {
	if m.To.Resource != "" {
		return m.To.Resource
	}
	return m.From.Resource // Default to same address if not specified
}

// GetToWorkspace returns the destination workspace.
func (m *Move) GetToWorkspace() string {
	if m.To.Workspace != "" {
		return m.To.Workspace
	}
	return m.TargetWorkspace
}

// TF holds tf/tofu execution settings.
type TF struct {
	Tool        string   `yaml:"tool,omitempty"`        // tofu, terraform, terragrunt
	Parallelism int      `yaml:"parallelism,omitempty"` // -parallelism flag
	VarFiles    []string `yaml:"var_files,omitempty"`   // -var-file flags
	Vars        []string `yaml:"vars,omitempty"`        // -var flags
}

// Cleanup controls state cleanup behavior.
type Cleanup struct {
	OnSuccess bool `yaml:"on_success"`
	OnFailure bool `yaml:"on_failure"`
}

// Load reads and parses a tfsync configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Resolve file references in moves (relative to config file directory)
	baseDir := filepath.Dir(path)
	if err := cfg.Migration.ResolveFiles(baseDir); err != nil {
		return nil, fmt.Errorf("failed to resolve move files: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// ParseYAMLRaw parses YAML data into a Config without validation.
// Used by autosuggest which doesn't require the migration section.
// File references in moves are resolved relative to the current directory.
func ParseYAMLRaw(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return err
	}
	// Resolve inline moves (file references will fail if paths don't exist,
	// but ParseYAMLRaw callers typically don't depend on moves).
	_ = cfg.Migration.ResolveFiles(".")
	return nil
}

// Validate checks that the configuration is valid.
func (c *Config) Validate() error {
	if c.Version == "" {
		return fmt.Errorf("version is required")
	}
	if c.Version != "1" {
		return fmt.Errorf("unsupported config version: %s", c.Version)
	}

	if err := c.Source.Validate(); err != nil {
		return fmt.Errorf("source: %w", err)
	}

	if err := c.Target.Validate(); err != nil {
		return fmt.Errorf("target: %w", err)
	}

	if err := c.Migration.Validate(); err != nil {
		return fmt.Errorf("migration: %w", err)
	}

	return nil
}

// Validate checks that the source configuration is valid.
func (s *Source) Validate() error {
	hasPath := s.Path != ""
	hasWorkspaces := len(s.Workspaces) > 0

	if !hasPath && !hasWorkspaces {
		return fmt.Errorf("either path or workspaces must be specified")
	}
	if hasPath && hasWorkspaces {
		return fmt.Errorf("cannot specify both path and workspaces")
	}

	for name, ws := range s.Workspaces {
		if ws.Path == "" {
			return fmt.Errorf("workspace %q: path is required", name)
		}
	}

	return nil
}

// Validate checks that the target configuration is valid.
func (t *Target) Validate() error {
	hasPath := t.Path != ""
	hasWorkspaces := len(t.Workspaces) > 0

	if !hasPath && !hasWorkspaces {
		return fmt.Errorf("either path or workspaces must be specified")
	}
	if hasPath && hasWorkspaces {
		return fmt.Errorf("cannot specify both path and workspaces")
	}

	// Validate single-workspace plan config
	if err := validateTargetPlanConfig("default", t.Plan); err != nil {
		return err
	}

	for name, ws := range t.Workspaces {
		if ws.Path == "" {
			return fmt.Errorf("workspace %q: path is required", name)
		}
		if err := validateTargetPlanConfig(name, ws.Plan); err != nil {
			return err
		}
		// Validate depends_on references
		for _, dep := range ws.DependsOn {
			if _, ok := t.Workspaces[dep]; !ok {
				return fmt.Errorf("workspace %q: depends_on references unknown workspace %q", name, dep)
			}
			if dep == name {
				return fmt.Errorf("workspace %q: depends_on cannot reference itself", name)
			}
		}
		// Validate remote_state references
		depSet := make(map[string]bool)
		for _, dep := range ws.DependsOn {
			depSet[dep] = true
		}
		for rsKey := range ws.RemoteState {
			if !depSet[rsKey] {
				return fmt.Errorf("workspace %q: remote_state references %q which is not in depends_on", name, rsKey)
			}
		}
	}

	// Check for dependency cycles
	if err := validateNoCycles(t.Workspaces); err != nil {
		return err
	}

	return nil
}

// validateTargetPlanConfig checks that a target workspace's plan config
// doesn't contain options that are incompatible with tfsync's validation.
func validateTargetPlanConfig(wsName string, plan *PlanConfig) error {
	if plan == nil {
		return nil
	}
	if plan.Refresh != nil && !*plan.Refresh {
		return fmt.Errorf("workspace %q: plan.refresh=false is not allowed on target workspaces; "+
			"tfsync validates migrations by running a real plan against the migrated state, "+
			"which requires refresh to detect actual infrastructure drift", wsName)
	}
	return nil
}

// validateNoCycles checks that workspace depends_on doesn't form cycles.
func validateNoCycles(workspaces map[string]Workspace) error {
	// Standard DFS cycle detection with three states: unvisited, visiting, visited
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int)

	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		if state[name] == visited {
			return nil
		}
		if state[name] == visiting {
			// Build cycle description
			cycle := append(path, name)
			return fmt.Errorf("depends_on cycle detected: %s", strings.Join(cycle, " -> "))
		}
		state[name] = visiting
		ws := workspaces[name]
		for _, dep := range ws.DependsOn {
			if err := visit(dep, append(path, name)); err != nil {
				return err
			}
		}
		state[name] = visited
		return nil
	}

	for name := range workspaces {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// TopologicalSort returns workspace names ordered so that dependencies come first.
// Workspaces with no dependencies (or whose dependencies are all satisfied) can
// run in parallel within the same "level". Returns a slice of levels, where each
// level is a set of workspace names that can run concurrently.
func TopologicalSort(workspaces map[string]Workspace) [][]string {
	// Build in-degree counts and adjacency
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // dep -> list of workspaces that depend on it

	for name := range workspaces {
		inDegree[name] = 0
	}
	for name, ws := range workspaces {
		for _, dep := range ws.DependsOn {
			// Only count dependencies on workspaces that are in the map.
			// When running a targeted subset, external dependencies should
			// be ignored for ordering purposes.
			if _, ok := workspaces[dep]; !ok {
				continue
			}
			inDegree[name]++
			dependents[dep] = append(dependents[dep], name)
		}
	}

	// Kahn's algorithm, collecting nodes by level
	var levels [][]string
	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue) // deterministic ordering within levels

	for len(queue) > 0 {
		levels = append(levels, queue)
		var next []string
		for _, name := range queue {
			for _, dep := range dependents[name] {
				inDegree[dep]--
				if inDegree[dep] == 0 {
					next = append(next, dep)
				}
			}
		}
		sort.Strings(next)
		queue = next
	}

	return levels
}

// Validate checks that the migration configuration is valid.
func (m *Migration) Validate() error {
	hasScript := m.Script != ""
	hasMoves := len(m.Moves) > 0

	if !hasScript && !hasMoves {
		return fmt.Errorf("either script or moves must be specified")
	}

	for i, mv := range m.Moves {
		if mv.GetFromResource() == "" {
			return fmt.Errorf("move[%d]: from is required", i)
		}
		// To can be empty if it's same as From (for workspace-only moves)
		// GetToResource() will return From in that case
	}

	return nil
}
// IsMultiWorkspace returns true if the source uses multiple workspaces.
func (s *Source) IsMultiWorkspace() bool {
	return len(s.Workspaces) > 0
}

// IsMultiWorkspace returns true if the target uses multiple workspaces.
func (t *Target) IsMultiWorkspace() bool {
	return len(t.Workspaces) > 0
}

// GetWorkspaces returns all workspaces, normalizing single-workspace to a map.
func (s *Source) GetWorkspaces() map[string]Workspace {
	if s.IsMultiWorkspace() {
		return s.Workspaces
	}
	return map[string]Workspace{
		"default": {Path: s.Path, Init: s.Init, Plan: s.Plan, Backend: s.Backend},
	}
}

// GetWorkspaces returns all workspaces, normalizing single-workspace to a map.
func (t *Target) GetWorkspaces() map[string]Workspace {
	if t.IsMultiWorkspace() {
		return t.Workspaces
	}
	return map[string]Workspace{
		"default": {Path: t.Path, Init: t.Init, Plan: t.Plan, Backend: t.Backend},
	}
}

// GetInitConfig returns the effective init configuration for a workspace.
// Merges deprecated Backend field into Init.Backend for backwards compatibility.
func (w *Workspace) GetInitConfig() *InitConfig {
	if w.Init != nil {
		// If deprecated Backend is set but Init.Backend is not, merge it
		if len(w.Backend) > 0 && len(w.Init.Backend) == 0 {
			merged := *w.Init
			merged.Backend = w.Backend
			return &merged
		}
		return w.Init
	}
	// No Init config, but maybe deprecated Backend is set
	if len(w.Backend) > 0 {
		return &InitConfig{Backend: w.Backend}
	}
	return nil
}

// GetPlanConfig returns the plan configuration for a workspace.
func (w *Workspace) GetPlanConfig() *PlanConfig {
	return w.Plan
}

// GetEnvSlice returns the workspace env as a KEY=VALUE slice suitable for CLI.Env.
// Values have environment variable expansion applied.
func (w *Workspace) GetEnvSlice() []string {
	if len(w.Env) == 0 {
		return nil
	}
	env := make([]string, 0, len(w.Env))
	for k, v := range w.Env {
		env = append(env, k+"="+ExpandEnv(v))
	}
	sort.Strings(env) // deterministic ordering
	return env
}

// AutoSetTFDataDir detects target workspaces that share the same path and
// auto-sets TF_DATA_DIR in their env to prevent .terraform directory conflicts.
// Only sets TF_DATA_DIR if the workspace doesn't already have it configured.
func (t *Target) AutoSetTFDataDir() {
	if !t.IsMultiWorkspace() {
		return
	}

	// Count how many workspaces use each path
	pathCount := make(map[string]int)
	for _, ws := range t.Workspaces {
		absPath, err := filepath.Abs(ws.Path)
		if err != nil {
			absPath = ws.Path
		}
		pathCount[absPath]++
	}

	// For shared paths, auto-set TF_DATA_DIR
	for name, ws := range t.Workspaces {
		absPath, err := filepath.Abs(ws.Path)
		if err != nil {
			absPath = ws.Path
		}
		if pathCount[absPath] <= 1 {
			continue // unique path, no conflict
		}

		// Check if TF_DATA_DIR is already configured
		if ws.Env != nil {
			if _, ok := ws.Env["TF_DATA_DIR"]; ok {
				continue
			}
		}

		// Auto-set TF_DATA_DIR to isolate .terraform directories
		if ws.Env == nil {
			ws.Env = make(map[string]string)
		}
		ws.Env["TF_DATA_DIR"] = filepath.Join(ws.Path, ".tfsync", name)
		t.Workspaces[name] = ws
	}
}

// GetStateFileName returns the local state file name for this workspace.
// Defaults to "terraform.tfstate" when not explicitly set.
func (w *Workspace) GetStateFileName() string {
	if w.stateFileName != "" {
		return w.stateFileName
	}
	return "terraform.tfstate"
}

// AutoSetSharedPathFiles detects target workspaces sharing the same path and
// auto-sets workspace-specific state file names to prevent conflicts.
// Should be called after AutoSetTFDataDir.
func (t *Target) AutoSetSharedPathFiles() {
	if !t.IsMultiWorkspace() {
		return
	}

	// Count how many workspaces use each path
	pathCount := make(map[string]int)
	for _, ws := range t.Workspaces {
		absPath, err := filepath.Abs(ws.Path)
		if err != nil {
			absPath = ws.Path
		}
		pathCount[absPath]++
	}

	// For shared paths, set workspace-specific state file names
	for name, ws := range t.Workspaces {
		absPath, err := filepath.Abs(ws.Path)
		if err != nil {
			absPath = ws.Path
		}
		if pathCount[absPath] <= 1 {
			continue // unique path, no conflict
		}

		ws.stateFileName = fmt.Sprintf("terraform-%s.tfstate", name)
		t.Workspaces[name] = ws
	}
}

// GetTool returns the tf tool to use, defaulting to "tofu".
func (t *TF) GetTool() string {
	if t == nil || t.Tool == "" {
		return "tofu"
	}
	return t.Tool
}

// ExpandEnv expands environment variables in a string using ${VAR} syntax.
// Falls back to empty string if the variable is not set.
func ExpandEnv(s string) string {
	return os.Expand(s, os.Getenv)
}

// ExpandEnvMap expands environment variables in all values of a map.
func ExpandEnvMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = ExpandEnv(v)
	}
	return result
}

// ExpandEnvSlice expands environment variables in all elements of a slice.
func ExpandEnvSlice(s []string) []string {
	if s == nil {
		return nil
	}
	result := make([]string, len(s))
	for i, v := range s {
		result[i] = ExpandEnv(v)
	}
	return result
}
