// Package config handles parsing and validation of tfsync configuration files.
package config

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
	Path    string            `yaml:"path,omitempty"`
	Backend map[string]string `yaml:"backend,omitempty"`

	// Multi-workspace mode
	Workspaces map[string]Workspace `yaml:"workspaces,omitempty"`
}

// Target represents the target terraform configuration (where state goes).
type Target struct {
	// Single workspace mode
	Path    string            `yaml:"path,omitempty"`
	Backend map[string]string `yaml:"backend,omitempty"`

	// Multi-workspace mode
	Workspaces map[string]Workspace `yaml:"workspaces,omitempty"`
}

// Workspace represents a single terraform workspace configuration.
type Workspace struct {
	Path    string            `yaml:"path"`
	Backend map[string]string `yaml:"backend,omitempty"`
}

// Migration defines how state should be transformed.
type Migration struct {
	// Script is an external script to run for complex migrations
	Script string `yaml:"script,omitempty"`

	// Moves is a list of state move operations
	Moves []Move `yaml:"moves,omitempty"`
}

// Move represents a single terraform state mv operation.
// The `to` field supports two formats:
//   - Simple string: "module.foo.null_resource.bar"
//   - Object: { workspace: "networking", resource: "null_resource.bar" }
type Move struct {
	From string   `yaml:"from"`
	To   MoveTo   `yaml:"-"` // Custom unmarshaling
	RawTo interface{} `yaml:"to"` // For unmarshaling
	// TargetWorkspace is deprecated, use To.Workspace instead
	TargetWorkspace string `yaml:"target_workspace,omitempty"`
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
		From            string      `yaml:"from"`
		To              interface{} `yaml:"to"`
		TargetWorkspace string      `yaml:"target_workspace,omitempty"`
	}
	var raw rawMove
	if err := unmarshal(&raw); err != nil {
		return err
	}

	m.From = raw.From
	m.TargetWorkspace = raw.TargetWorkspace

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

// GetToResource returns the destination resource address.
func (m *Move) GetToResource() string {
	if m.To.Resource != "" {
		return m.To.Resource
	}
	return m.From // Default to same address if not specified
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
		if mv.From == "" {
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
		"default": {Path: s.Path, Backend: s.Backend},
	}
}

// GetWorkspaces returns all workspaces, normalizing single-workspace to a map.
func (t *Target) GetWorkspaces() map[string]Workspace {
	if t.IsMultiWorkspace() {
		return t.Workspaces
	}
	return map[string]Workspace{
		"default": {Path: t.Path, Backend: t.Backend},
	}
}

// GetTool returns the tf tool to use, defaulting to "tofu".
func (t *TF) GetTool() string {
	if t == nil || t.Tool == "" {
		return "tofu"
	}
	return t.Tool
}
