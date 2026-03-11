package internal

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExpandEnv(t *testing.T) {
	// Set up test environment variables
	os.Setenv("TEST_VAR", "test_value")
	os.Setenv("TEST_USER", "myuser")
	defer func() {
		os.Unsetenv("TEST_VAR")
		os.Unsetenv("TEST_USER")
	}()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple variable",
			input:    "${TEST_VAR}",
			expected: "test_value",
		},
		{
			name:     "variable in string",
			input:    "prefix_${TEST_VAR}_suffix",
			expected: "prefix_test_value_suffix",
		},
		{
			name:     "multiple variables",
			input:    "${TEST_USER}:${TEST_VAR}",
			expected: "myuser:test_value",
		},
		{
			name:     "unset variable",
			input:    "${UNSET_VAR}",
			expected: "",
		},
		{
			name:     "no variables",
			input:    "plain string",
			expected: "plain string",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandEnv(tt.input)
			if result != tt.expected {
				t.Errorf("ExpandEnv(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExpandEnvMap(t *testing.T) {
	os.Setenv("TEST_TOKEN", "secret123")
	defer os.Unsetenv("TEST_TOKEN")

	tests := []struct {
		name     string
		input    map[string]string
		expected map[string]string
	}{
		{
			name:     "nil map",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty map",
			input:    map[string]string{},
			expected: map[string]string{},
		},
		{
			name: "map with variables",
			input: map[string]string{
				"password": "${TEST_TOKEN}",
				"username": "static_user",
			},
			expected: map[string]string{
				"password": "secret123",
				"username": "static_user",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandEnvMap(tt.input)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("ExpandEnvMap(nil) = %v, want nil", result)
				}
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("ExpandEnvMap() returned %d items, want %d", len(result), len(tt.expected))
			}
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("ExpandEnvMap()[%q] = %q, want %q", k, result[k], v)
				}
			}
		})
	}
}

func TestExpandEnvSlice(t *testing.T) {
	os.Setenv("TEST_FLAG", "value1")
	defer os.Unsetenv("TEST_FLAG")

	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "nil slice",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty slice",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "slice with variables",
			input:    []string{"-var=${TEST_FLAG}", "-static"},
			expected: []string{"-var=value1", "-static"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandEnvSlice(tt.input)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("ExpandEnvSlice(nil) = %v, want nil", result)
				}
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("ExpandEnvSlice() returned %d items, want %d", len(result), len(tt.expected))
			}
			for i, v := range tt.expected {
				if result[i] != v {
					t.Errorf("ExpandEnvSlice()[%d] = %q, want %q", i, result[i], v)
				}
			}
		})
	}
}

func TestWorkspaceGetInitConfig(t *testing.T) {
	tests := []struct {
		name            string
		workspace       Workspace
		expectNil       bool
		expectedBackend map[string]string
	}{
		{
			name:      "no init config, no backend",
			workspace: Workspace{Path: "/test"},
			expectNil: true,
		},
		{
			name: "deprecated backend only",
			workspace: Workspace{
				Path:    "/test",
				Backend: map[string]string{"key": "value"},
			},
			expectNil:       false,
			expectedBackend: map[string]string{"key": "value"},
		},
		{
			name: "init config only",
			workspace: Workspace{
				Path: "/test",
				Init: &InitConfig{
					Backend: map[string]string{"address": "http://example.com"},
				},
			},
			expectNil:       false,
			expectedBackend: map[string]string{"address": "http://example.com"},
		},
		{
			name: "init config with deprecated backend (init takes precedence)",
			workspace: Workspace{
				Path: "/test",
				Init: &InitConfig{
					Backend: map[string]string{"address": "http://init.com"},
				},
				Backend: map[string]string{"address": "http://deprecated.com"},
			},
			expectNil:       false,
			expectedBackend: map[string]string{"address": "http://init.com"},
		},
		{
			name: "init config without backend, deprecated backend set (merge)",
			workspace: Workspace{
				Path: "/test",
				Init: &InitConfig{
					ExtraArgs: []string{"-upgrade"},
				},
				Backend: map[string]string{"address": "http://deprecated.com"},
			},
			expectNil:       false,
			expectedBackend: map[string]string{"address": "http://deprecated.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.workspace.GetInitConfig()
			if tt.expectNil {
				if result != nil {
					t.Errorf("GetInitConfig() = %v, want nil", result)
				}
				return
			}
			if result == nil {
				t.Errorf("GetInitConfig() = nil, want non-nil")
				return
			}
			if len(result.Backend) != len(tt.expectedBackend) {
				t.Errorf("GetInitConfig().Backend has %d items, want %d", len(result.Backend), len(tt.expectedBackend))
			}
			for k, v := range tt.expectedBackend {
				if result.Backend[k] != v {
					t.Errorf("GetInitConfig().Backend[%q] = %q, want %q", k, result.Backend[k], v)
				}
			}
		})
	}
}

func TestWorkspaceGetPlanConfig(t *testing.T) {
	lockFalse := false
	refreshTrue := true

	tests := []struct {
		name      string
		workspace Workspace
		expectNil bool
		checkLock *bool
	}{
		{
			name:      "no plan config",
			workspace: Workspace{Path: "/test"},
			expectNil: true,
		},
		{
			name: "with plan config",
			workspace: Workspace{
				Path: "/test",
				Plan: &PlanConfig{
					Lock:    &lockFalse,
					Refresh: &refreshTrue,
				},
			},
			expectNil: false,
			checkLock: &lockFalse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.workspace.GetPlanConfig()
			if tt.expectNil {
				if result != nil {
					t.Errorf("GetPlanConfig() = %v, want nil", result)
				}
				return
			}
			if result == nil {
				t.Errorf("GetPlanConfig() = nil, want non-nil")
				return
			}
			if tt.checkLock != nil && (result.Lock == nil || *result.Lock != *tt.checkLock) {
				t.Errorf("GetPlanConfig().Lock = %v, want %v", result.Lock, *tt.checkLock)
			}
		})
	}
}

func TestSourceGetWorkspaces(t *testing.T) {
	lockFalse := false

	tests := []struct {
		name           string
		source         Source
		expectedCount  int
		checkDefault   bool
		defaultPath    string
		defaultHasInit bool
		defaultHasPlan bool
	}{
		{
			name: "single workspace mode",
			source: Source{
				Path: "/single",
				Init: &InitConfig{Backend: map[string]string{"key": "value"}},
				Plan: &PlanConfig{Lock: &lockFalse},
			},
			expectedCount:  1,
			checkDefault:   true,
			defaultPath:    "/single",
			defaultHasInit: true,
			defaultHasPlan: true,
		},
		{
			name: "multi workspace mode",
			source: Source{
				Workspaces: map[string]Workspace{
					"dev":  {Path: "/dev"},
					"prod": {Path: "/prod"},
				},
			},
			expectedCount: 2,
			checkDefault:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.source.GetWorkspaces()
			if len(result) != tt.expectedCount {
				t.Errorf("GetWorkspaces() returned %d workspaces, want %d", len(result), tt.expectedCount)
			}
			if tt.checkDefault {
				ws, ok := result["default"]
				if !ok {
					t.Errorf("GetWorkspaces() missing 'default' workspace")
					return
				}
				if ws.Path != tt.defaultPath {
					t.Errorf("default workspace Path = %q, want %q", ws.Path, tt.defaultPath)
				}
				if tt.defaultHasInit && ws.Init == nil {
					t.Errorf("default workspace Init = nil, want non-nil")
				}
				if tt.defaultHasPlan && ws.Plan == nil {
					t.Errorf("default workspace Plan = nil, want non-nil")
				}
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name: "valid config",
			config: Config{
				Version: "1",
				Source:  Source{Path: "/source"},
				Target:  Target{Path: "/target"},
				Migration: Migration{
					Moves: []Move{{From: MoveFrom{Resource: "a.b"}, To: MoveTo{Resource: "c.d"}}},
				},
			},
			expectError: false,
		},
		{
			name: "missing version",
			config: Config{
				Source:    Source{Path: "/source"},
				Target:    Target{Path: "/target"},
				Migration: Migration{Moves: []Move{{From: MoveFrom{Resource: "a.b"}, To: MoveTo{Resource: "c.d"}}}},
			},
			expectError: true,
		},
		{
			name: "invalid version",
			config: Config{
				Version:   "2",
				Source:    Source{Path: "/source"},
				Target:    Target{Path: "/target"},
				Migration: Migration{Moves: []Move{{From: MoveFrom{Resource: "a.b"}, To: MoveTo{Resource: "c.d"}}}},
			},
			expectError: true,
		},
		{
			name: "missing source path",
			config: Config{
				Version:   "1",
				Source:    Source{},
				Target:    Target{Path: "/target"},
				Migration: Migration{Moves: []Move{{From: MoveFrom{Resource: "a.b"}, To: MoveTo{Resource: "c.d"}}}},
			},
			expectError: true,
		},
		{
			name: "both path and workspaces",
			config: Config{
				Version: "1",
				Source: Source{
					Path:       "/source",
					Workspaces: map[string]Workspace{"dev": {Path: "/dev"}},
				},
				Target:    Target{Path: "/target"},
				Migration: Migration{Moves: []Move{{From: MoveFrom{Resource: "a.b"}, To: MoveTo{Resource: "c.d"}}}},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.expectError && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestMoveUnmarshalYAML(t *testing.T) {
	tests := []struct {
		name              string
		yaml              string
		expectedFromRes   string
		expectedFromWS    string
		expectedToRes     string
		expectedToWS      string
		expectError       bool
	}{
		{
			name: "simple string from and to",
			yaml: `
from: "module.foo"
to: "module.bar"
`,
			expectedFromRes: "module.foo",
			expectedFromWS:  "",
			expectedToRes:   "module.bar",
			expectedToWS:    "",
		},
		{
			name: "object from with workspace",
			yaml: `
from:
  workspace: dev
  resource: "module.foo"
to: "module.bar"
`,
			expectedFromRes: "module.foo",
			expectedFromWS:  "dev",
			expectedToRes:   "module.bar",
			expectedToWS:    "",
		},
		{
			name: "object to with workspace",
			yaml: `
from: "module.foo"
to:
  workspace: prod
  resource: "module.bar"
`,
			expectedFromRes: "module.foo",
			expectedFromWS:  "",
			expectedToRes:   "module.bar",
			expectedToWS:    "prod",
		},
		{
			name: "both from and to as objects",
			yaml: `
from:
  workspace: dev
  resource: "module.foo"
to:
  workspace: prod
  resource: "module.bar"
`,
			expectedFromRes: "module.foo",
			expectedFromWS:  "dev",
			expectedToRes:   "module.bar",
			expectedToWS:    "prod",
		},
		{
			name: "deprecated target_workspace",
			yaml: `
from: "module.foo"
to: "module.bar"
target_workspace: legacy
`,
			expectedFromRes: "module.foo",
			expectedFromWS:  "",
			expectedToRes:   "module.bar",
			expectedToWS:    "legacy",
		},
		{
			name: "to.workspace overrides target_workspace",
			yaml: `
from: "module.foo"
to:
  workspace: new
  resource: "module.bar"
target_workspace: legacy
`,
			expectedFromRes: "module.foo",
			expectedFromWS:  "",
			expectedToRes:   "module.bar",
			expectedToWS:    "new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mv Move
			err := yaml.Unmarshal([]byte(tt.yaml), &mv)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if mv.GetFromResource() != tt.expectedFromRes {
				t.Errorf("GetFromResource() = %q, want %q", mv.GetFromResource(), tt.expectedFromRes)
			}
			if mv.GetFromWorkspace() != tt.expectedFromWS {
				t.Errorf("GetFromWorkspace() = %q, want %q", mv.GetFromWorkspace(), tt.expectedFromWS)
			}
			if mv.GetToResource() != tt.expectedToRes {
				t.Errorf("GetToResource() = %q, want %q", mv.GetToResource(), tt.expectedToRes)
			}
			if mv.GetToWorkspace() != tt.expectedToWS {
				t.Errorf("GetToWorkspace() = %q, want %q", mv.GetToWorkspace(), tt.expectedToWS)
			}
		})
	}
}

func TestMoveGetToResourceDefault(t *testing.T) {
	// When To.Resource is empty, GetToResource should return From.Resource
	mv := Move{
		From: MoveFrom{Resource: "module.foo"},
		To:   MoveTo{Workspace: "prod"}, // Resource empty
	}

	if got := mv.GetToResource(); got != "module.foo" {
		t.Errorf("GetToResource() = %q, want %q", got, "module.foo")
	}
}

func TestWorkspacePreferWorkspace(t *testing.T) {
	// Test that prefer_workspace field is properly parsed from YAML
	yamlData := `
path: /target/dev-core
prefer_workspace: development
init:
  backend:
    key: dev-core
`
	var ws Workspace
	err := yaml.Unmarshal([]byte(yamlData), &ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ws.Path != "/target/dev-core" {
		t.Errorf("Path = %q, want %q", ws.Path, "/target/dev-core")
	}
	if ws.PreferWorkspace != "development" {
		t.Errorf("PreferWorkspace = %q, want %q", ws.PreferWorkspace, "development")
	}
	if ws.Init == nil || ws.Init.Backend["key"] != "dev-core" {
		t.Errorf("Init.Backend not parsed correctly")
	}
}

func TestWorkspacePreferWorkspaceEmpty(t *testing.T) {
	// Test that prefer_workspace defaults to empty when not specified
	yamlData := `
path: /target/dev
`
	var ws Workspace
	err := yaml.Unmarshal([]byte(yamlData), &ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ws.PreferWorkspace != "" {
		t.Errorf("PreferWorkspace = %q, want empty string", ws.PreferWorkspace)
	}
}

func TestExtractMoveLineNumbers(t *testing.T) {
	yamlData := []byte(`version: "1"
source:
  path: ./src
target:
  path: ./tgt
migration:
  moves:
    - from: "aws_instance.old"
      to: "aws_instance.new"
    - from: { workspace: dev, resource: "module.foo" }
      to: { workspace: networking, resource: "module.bar" }
    - from: "aws_vpc.main"
      to: "aws_vpc.main"
`)

	var cfg Config
	if err := yaml.Unmarshal(yamlData, &cfg); err != nil {
		t.Fatal(err)
	}

	extractMoveLineNumbers(yamlData, &cfg)

	if len(cfg.Migration.Moves) != 3 {
		t.Fatalf("expected 3 moves, got %d", len(cfg.Migration.Moves))
	}

	// Line numbers should be populated (exact values depend on YAML layout)
	for i, mv := range cfg.Migration.Moves {
		if mv.Line == 0 {
			t.Errorf("move[%d]: expected non-zero line number", i)
		}
	}

	// YAML content line numbers (1-indexed within the YAML data):
	// line 1: version, 2: source, 3: path, 4: target, 5: path, 6: migration, 7: moves
	// line 8: first move, line 10: second move, line 12: third move
	if cfg.Migration.Moves[0].Line != 8 {
		t.Errorf("move[0]: expected line 8, got %d", cfg.Migration.Moves[0].Line)
	}
	if cfg.Migration.Moves[1].Line != 10 {
		t.Errorf("move[1]: expected line 10, got %d", cfg.Migration.Moves[1].Line)
	}
	if cfg.Migration.Moves[2].Line != 12 {
		t.Errorf("move[2]: expected line 12, got %d", cfg.Migration.Moves[2].Line)
	}
}

func TestMoveLineNumbersInErrors(t *testing.T) {
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_vpc.main"}, To: MoveTo{Resource: "aws_vpc.main", Workspace: "networking"}, Line: 42},
			{From: MoveFrom{Resource: "aws_vpc.main"}, To: MoveTo{Resource: "aws_vpc.main", Workspace: "compute"}, Line: 44},
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
	// Should contain line numbers
	if !strings.Contains(errMsg, "line 42") {
		t.Errorf("expected 'line 42' in error, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "line 44") {
		t.Errorf("expected 'line 44' in error, got: %v", errMsg)
	}
}
