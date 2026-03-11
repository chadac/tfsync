// Package internal contains the core tfsync implementation.
package internal

import (
	"fmt"
	"os"

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
	// OverrideFiles are terraform override files to write before running plan.
	// Map of filename -> content. Files are written to the target directory.
	// Example: {"provider_override.tf": "provider \"aws\" { ... }"}
	OverrideFiles map[string]string `yaml:"override_files,omitempty"`
	// IgnoreChanges is a list of resource addresses whose plan changes should be ignored.
	// If a plan has changes but all changed resources are in this list, the plan is considered passing.
	IgnoreChanges []string `yaml:"ignore_changes,omitempty"`
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
}

// Migration defines how state should be transformed.
type Migration struct {
	// Script is an external script to run for complex migrations
	Script string `yaml:"script,omitempty"`

	// Moves is a list of state move operations
	Moves []Move `yaml:"moves,omitempty"`

	// ProviderRemap maps provider short names from source to target.
	// Applied globally to all moves. Per-move ProviderRemap takes precedence.
	// Example: {"aws.ireland": "aws"} remaps provider alias "ireland" to the default provider.
	ProviderRemap map[string]string `yaml:"provider_remap,omitempty"`
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
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Extract YAML line numbers for moves via yaml.Node tree
	extractMoveLineNumbers(data, &cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// ParseYAMLRaw parses YAML data into a Config without validation.
// Used by autosuggest which doesn't require the migration section.
func ParseYAMLRaw(data []byte, cfg *Config) error {
	return yaml.Unmarshal(data, cfg)
}

// extractMoveLineNumbers walks the yaml.Node tree to find line numbers for each move entry
// and populates Move.Line fields on the already-parsed config.
func extractMoveLineNumbers(data []byte, cfg *Config) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return // Best-effort; line numbers are optional
	}

	// root is a Document node, its first child is the top-level mapping
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return
	}
	topMap := root.Content[0]
	if topMap.Kind != yaml.MappingNode {
		return
	}

	// Find "migration" key in top-level mapping
	var migrationNode *yaml.Node
	for i := 0; i+1 < len(topMap.Content); i += 2 {
		if topMap.Content[i].Value == "migration" {
			migrationNode = topMap.Content[i+1]
			break
		}
	}
	if migrationNode == nil || migrationNode.Kind != yaml.MappingNode {
		return
	}

	// Find "moves" key in migration mapping
	var movesNode *yaml.Node
	for i := 0; i+1 < len(migrationNode.Content); i += 2 {
		if migrationNode.Content[i].Value == "moves" {
			movesNode = migrationNode.Content[i+1]
			break
		}
	}
	if movesNode == nil || movesNode.Kind != yaml.SequenceNode {
		return
	}

	// Each child of the sequence node is a move entry
	for i, moveNode := range movesNode.Content {
		if i < len(cfg.Migration.Moves) {
			cfg.Migration.Moves[i].Line = moveNode.Line
		}
	}
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

	for name, ws := range t.Workspaces {
		if ws.Path == "" {
			return fmt.Errorf("workspace %q: path is required", name)
		}
	}

	return nil
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
