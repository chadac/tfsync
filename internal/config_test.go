package internal

import (
	"fmt"
	"os"
	"path/filepath"
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
	if err := ParseYAMLRaw(yamlData, &cfg); err != nil {
		t.Fatal(err)
	}

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

func TestMovesFromFile(t *testing.T) {
	// Create a temp directory with external moves file
	tmpDir := t.TempDir()

	movesData := []byte(`- from: "module.networking"
  to:
    workspace: networking
    resource: "module.networking"
- from: "module.compute"
  to:
    workspace: compute
    resource: "module.compute"
`)
	movesFile := filepath.Join(tmpDir, "networking-moves.yaml")
	if err := os.WriteFile(movesFile, movesData, 0644); err != nil {
		t.Fatal(err)
	}

	configData := fmt.Sprintf(`version: "1"
source:
  path: ./src
target:
  workspaces:
    networking:
      path: ./networking
    compute:
      path: ./compute
migration:
  moves:
    - from: "aws_vpc.main"
      to:
        workspace: networking
        resource: "aws_vpc.main"
    - file: %s
`, movesFile)

	cfg, err := Load(filepath.Join(tmpDir, "tfsync.yaml"))
	// Load will fail because the config file doesn't exist at that path,
	// so write it first
	configFile := filepath.Join(tmpDir, "tfsync.yaml")
	if err := os.WriteFile(configFile, []byte(configData), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(configFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Migration.Moves) != 3 {
		t.Fatalf("expected 3 moves, got %d", len(cfg.Migration.Moves))
	}

	// First move is inline
	if cfg.Migration.Moves[0].GetFromResource() != "aws_vpc.main" {
		t.Errorf("move[0] from: expected aws_vpc.main, got %s", cfg.Migration.Moves[0].GetFromResource())
	}

	// Second and third from file
	if cfg.Migration.Moves[1].GetFromResource() != "module.networking" {
		t.Errorf("move[1] from: expected module.networking, got %s", cfg.Migration.Moves[1].GetFromResource())
	}
	if cfg.Migration.Moves[1].GetToWorkspace() != "networking" {
		t.Errorf("move[1] to workspace: expected networking, got %s", cfg.Migration.Moves[1].GetToWorkspace())
	}
	if cfg.Migration.Moves[2].GetFromResource() != "module.compute" {
		t.Errorf("move[2] from: expected module.compute, got %s", cfg.Migration.Moves[2].GetFromResource())
	}

	// Inline move should have a line number, file-referenced moves won't
	if cfg.Migration.Moves[0].Line == 0 {
		t.Error("inline move should have a line number")
	}
}

func TestMovesFromFileRelativePath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a subdirectory for moves
	movesDir := filepath.Join(tmpDir, "moves")
	if err := os.MkdirAll(movesDir, 0755); err != nil {
		t.Fatal(err)
	}

	movesData := []byte(`- from: "module.app"
  to:
    workspace: app
    resource: "module.app"
`)
	if err := os.WriteFile(filepath.Join(movesDir, "app.yaml"), movesData, 0644); err != nil {
		t.Fatal(err)
	}

	configData := `version: "1"
source:
  path: ./src
target:
  workspaces:
    app:
      path: ./app
migration:
  moves:
    - file: moves/app.yaml
`
	configFile := filepath.Join(tmpDir, "tfsync.yaml")
	if err := os.WriteFile(configFile, []byte(configData), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Migration.Moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(cfg.Migration.Moves))
	}
	if cfg.Migration.Moves[0].GetFromResource() != "module.app" {
		t.Errorf("expected module.app, got %s", cfg.Migration.Moves[0].GetFromResource())
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

func TestAutoSetTFDataDir(t *testing.T) {
	target := &Target{
		Workspaces: map[string]Workspace{
			"ws-a": {Path: "./shared-path"},
			"ws-b": {Path: "./shared-path"},
			"ws-c": {Path: "./unique-path"},
		},
	}

	target.AutoSetTFDataDir()

	// ws-a and ws-b share a path — should get TF_DATA_DIR
	if target.Workspaces["ws-a"].Env == nil || target.Workspaces["ws-a"].Env["TF_DATA_DIR"] == "" {
		t.Error("ws-a: expected TF_DATA_DIR to be set")
	}
	if target.Workspaces["ws-b"].Env == nil || target.Workspaces["ws-b"].Env["TF_DATA_DIR"] == "" {
		t.Error("ws-b: expected TF_DATA_DIR to be set")
	}

	// TF_DATA_DIR should be different for each workspace
	if target.Workspaces["ws-a"].Env["TF_DATA_DIR"] == target.Workspaces["ws-b"].Env["TF_DATA_DIR"] {
		t.Errorf("ws-a and ws-b should have different TF_DATA_DIR, both got %q",
			target.Workspaces["ws-a"].Env["TF_DATA_DIR"])
	}

	// ws-c has a unique path — should NOT get TF_DATA_DIR
	if target.Workspaces["ws-c"].Env != nil {
		if _, ok := target.Workspaces["ws-c"].Env["TF_DATA_DIR"]; ok {
			t.Error("ws-c: should not have TF_DATA_DIR (unique path)")
		}
	}
}

func TestAutoSetTFDataDir_PreservesExisting(t *testing.T) {
	target := &Target{
		Workspaces: map[string]Workspace{
			"ws-a": {Path: "./shared", Env: map[string]string{"TF_DATA_DIR": "/custom/path"}},
			"ws-b": {Path: "./shared"},
		},
	}

	target.AutoSetTFDataDir()

	// ws-a already has TF_DATA_DIR — should be preserved
	if target.Workspaces["ws-a"].Env["TF_DATA_DIR"] != "/custom/path" {
		t.Errorf("ws-a: TF_DATA_DIR should be preserved, got %q", target.Workspaces["ws-a"].Env["TF_DATA_DIR"])
	}

	// ws-b should get auto-set TF_DATA_DIR
	if target.Workspaces["ws-b"].Env == nil || target.Workspaces["ws-b"].Env["TF_DATA_DIR"] == "" {
		t.Error("ws-b: expected TF_DATA_DIR to be auto-set")
	}
}

func TestGetEnvSlice(t *testing.T) {
	ws := Workspace{
		Env: map[string]string{
			"FOO": "bar",
			"BAZ": "qux",
		},
	}

	env := ws.GetEnvSlice()
	if len(env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(env))
	}
	// Sorted order
	if env[0] != "BAZ=qux" {
		t.Errorf("expected BAZ=qux, got %s", env[0])
	}
	if env[1] != "FOO=bar" {
		t.Errorf("expected FOO=bar, got %s", env[1])
	}
}

func TestGetEnvSlice_Empty(t *testing.T) {
	ws := Workspace{}
	env := ws.GetEnvSlice()
	if env != nil {
		t.Errorf("expected nil for empty env, got %v", env)
	}
}

func TestAutoSetSharedPathFiles(t *testing.T) {
	target := Target{
		Workspaces: map[string]Workspace{
			"ws-a": {Path: "/shared/path"},
			"ws-b": {Path: "/shared/path"},
			"ws-c": {Path: "/unique/path"},
		},
	}

	target.AutoSetSharedPathFiles()

	// Shared paths should get workspace-specific state file names
	wsA := target.Workspaces["ws-a"]
	if wsA.GetStateFileName() != "terraform-ws-a.tfstate" {
		t.Errorf("ws-a: expected terraform-ws-a.tfstate, got %s", wsA.GetStateFileName())
	}

	wsB := target.Workspaces["ws-b"]
	if wsB.GetStateFileName() != "terraform-ws-b.tfstate" {
		t.Errorf("ws-b: expected terraform-ws-b.tfstate, got %s", wsB.GetStateFileName())
	}

	// Unique path should keep default
	wsC := target.Workspaces["ws-c"]
	if wsC.GetStateFileName() != "terraform.tfstate" {
		t.Errorf("ws-c: expected terraform.tfstate, got %s", wsC.GetStateFileName())
	}
}

func TestAutoSetSharedPathFiles_SingleWorkspace(t *testing.T) {
	target := Target{
		Path: "/some/path",
	}

	// Should be a no-op for single workspace
	target.AutoSetSharedPathFiles()
}

func TestGetStateFileName_Default(t *testing.T) {
	ws := Workspace{}
	if ws.GetStateFileName() != "terraform.tfstate" {
		t.Errorf("expected default terraform.tfstate, got %s", ws.GetStateFileName())
	}
}

func TestExecuteWorkspacePlan_CustomStateFileName(t *testing.T) {
	// Test that executeWorkspacePlan uses a custom state file name
	dir := t.TempDir()
	stateFileName := "terraform-custom.tfstate"

	// Write state to the custom-named file
	stateData := []byte(`{
		"version": 4,
		"terraform_version": "1.5.0",
		"serial": 1,
		"lineage": "test",
		"resources": [
			{
				"mode": "managed",
				"type": "null_resource",
				"name": "foo",
				"provider": "provider[\"registry.terraform.io/hashicorp/null\"]",
				"instances": [{"schema_version": 0, "attributes": {"id": "123"}}]
			}
		]
	}`)
	if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor("tofu")
	wsPlan := &WorkspacePlan{
		Keep: []string{"null_resource.foo"},
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan, stateFileName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 0 {
		t.Errorf("expected 0 moves, got %d", moved)
	}

	// Verify the custom state file was written (not terraform.tfstate)
	if _, err := os.Stat(filepath.Join(dir, stateFileName)); err != nil {
		t.Errorf("custom state file should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate")); !os.IsNotExist(err) {
		t.Errorf("default terraform.tfstate should NOT exist")
	}
}

func TestTopologicalSort_AllWorkspaces(t *testing.T) {
	workspaces := map[string]Workspace{
		"networking": {Path: "./networking"},
		"compute":    {Path: "./compute", DependsOn: []string{"networking"}},
		"monitoring": {Path: "./monitoring", DependsOn: []string{"compute"}},
	}

	levels := TopologicalSort(workspaces)

	if len(levels) != 3 {
		t.Fatalf("expected 3 levels, got %d: %v", len(levels), levels)
	}
	if levels[0][0] != "networking" {
		t.Errorf("level 0 should be [networking], got %v", levels[0])
	}
	if levels[1][0] != "compute" {
		t.Errorf("level 1 should be [compute], got %v", levels[1])
	}
	if levels[2][0] != "monitoring" {
		t.Errorf("level 2 should be [monitoring], got %v", levels[2])
	}
}

// TestTopologicalSort_TargetedSubset reproduces the bug where targeting a single
// workspace that has depends_on causes it to be dropped from the sort entirely.
// When only "compute" is in the map and it depends_on "networking" (not in the map),
// compute should still appear in the output levels.
func TestTopologicalSort_TargetedSubset(t *testing.T) {
	// Only "compute" is targeted, but it depends on "networking" which is not in the map
	workspaces := map[string]Workspace{
		"compute": {Path: "./compute", DependsOn: []string{"networking"}},
	}

	levels := TopologicalSort(workspaces)

	// compute should still appear in the output
	found := false
	for _, level := range levels {
		for _, name := range level {
			if name == "compute" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("compute should appear in topological sort output, got levels: %v", levels)
	}
}

// TestTopologicalSort_TargetedSubsetChain tests targeting a workspace at the end
// of a dependency chain where intermediate workspaces are missing.
func TestTopologicalSort_TargetedSubsetChain(t *testing.T) {
	// Only "monitoring" is targeted, depends on "compute" which depends on "networking"
	// Neither compute nor networking is in the map
	workspaces := map[string]Workspace{
		"monitoring": {Path: "./monitoring", DependsOn: []string{"compute"}},
	}

	levels := TopologicalSort(workspaces)

	found := false
	for _, level := range levels {
		for _, name := range level {
			if name == "monitoring" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("monitoring should appear in topological sort output, got levels: %v", levels)
	}
}

// TestTopologicalSort_TargetedSubsetMultiple tests targeting two workspaces where
// one depends on the other plus an external workspace.
func TestTopologicalSort_TargetedSubsetMultiple(t *testing.T) {
	workspaces := map[string]Workspace{
		"networking": {Path: "./networking"},
		"compute":    {Path: "./compute", DependsOn: []string{"networking", "external"}},
	}

	levels := TopologicalSort(workspaces)

	// Both should appear, networking before compute
	allNames := make(map[string]int) // name -> level index
	for i, level := range levels {
		for _, name := range level {
			allNames[name] = i
		}
	}

	if _, ok := allNames["networking"]; !ok {
		t.Errorf("networking should be in output, got levels: %v", levels)
	}
	if _, ok := allNames["compute"]; !ok {
		t.Errorf("compute should be in output, got levels: %v", levels)
	}
	if allNames["compute"] <= allNames["networking"] {
		t.Errorf("compute should be after networking, got levels: %v", levels)
	}
}

func TestStrictYAMLParsing_RejectsUnknownKeys(t *testing.T) {
	yamlData := `
version: "1"
source:
  path: ./source
target:
  workspaces:
    myws:
      path: ./target
      plan:
        vars:
          project_config: prod/myws.yml
        unknown_field: should_fail
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "tfsync.yaml")
	os.WriteFile(cfgPath, []byte(yamlData), 0644)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for unknown YAML key 'unknown_field', got nil")
	}
	if !strings.Contains(err.Error(), "unknown_field") {
		t.Errorf("expected error to mention 'unknown_field', got: %v", err)
	}
}

func TestStrictYAMLParsing_AcceptsValidKeys(t *testing.T) {
	yamlData := `
version: "1"
source:
  path: ./source
target:
  workspaces:
    myws:
      path: ./target
      plan:
        vars:
          project_config: prod/myws.yml
          aws_profile: my-profile
        var_files:
          - extra.tfvars
`
	var cfg Config
	err := ParseYAMLRaw([]byte(yamlData), &cfg)
	if err != nil {
		t.Fatalf("unexpected error parsing valid YAML: %v", err)
	}
	ws, ok := cfg.Target.Workspaces["myws"]
	if !ok {
		t.Fatal("expected workspace 'myws'")
	}
	if ws.Plan == nil {
		t.Fatal("expected plan config")
	}
	if ws.Plan.Vars["project_config"] != "prod/myws.yml" {
		t.Errorf("expected project_config=prod/myws.yml, got %s", ws.Plan.Vars["project_config"])
	}
	if ws.Plan.Vars["aws_profile"] != "my-profile" {
		t.Errorf("expected aws_profile=my-profile, got %s", ws.Plan.Vars["aws_profile"])
	}
	if len(ws.Plan.VarFiles) != 1 || ws.Plan.VarFiles[0] != "extra.tfvars" {
		t.Errorf("expected var_files=[extra.tfvars], got %v", ws.Plan.VarFiles)
	}
}

func TestParseYAMLRaw_RejectsUnknownKeys(t *testing.T) {
	yamlData := `
version: "1"
source:
  path: ./source
target:
  workspaces:
    myws:
      path: ./target
      bogus_key: nope
`
	var cfg Config
	err := ParseYAMLRaw([]byte(yamlData), &cfg)
	if err == nil {
		t.Fatal("expected error for unknown YAML key 'bogus_key', got nil")
	}
	if !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("expected error to mention 'bogus_key', got: %v", err)
	}
}

func TestRemoteStateMapping_ParsesCorrectly(t *testing.T) {
	yamlData := `
version: "1"
source:
  path: ./source
target:
  workspaces:
    networking:
      path: ./networking
    compute:
      path: ./compute
      depends_on: [networking]
      remote_state:
        networking:
          data_source: core
`
	var cfg Config
	err := ParseYAMLRaw([]byte(yamlData), &cfg)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	ws := cfg.Target.Workspaces["compute"]
	if len(ws.RemoteState) != 1 {
		t.Fatalf("expected 1 remote_state mapping, got %d", len(ws.RemoteState))
	}
	if ws.RemoteState["networking"].DataSource != "core" {
		t.Errorf("expected data_source=core, got %s", ws.RemoteState["networking"].DataSource)
	}
}

func TestRemoteState_ValidationRejectsInvalidRef(t *testing.T) {
	yamlData := `
version: "1"
source:
  path: ./source
target:
  workspaces:
    networking:
      path: ./networking
    compute:
      path: ./compute
      depends_on: [networking]
      remote_state:
        nonexistent:
          data_source: foo
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "tfsync.yaml")
	os.WriteFile(cfgPath, []byte(yamlData), 0644)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected validation error for remote_state referencing nonexistent dependency")
	}
	if !strings.Contains(err.Error(), "nonexistent") || !strings.Contains(err.Error(), "not in depends_on") {
		t.Errorf("expected error about nonexistent not in depends_on, got: %v", err)
	}
}

func TestGenerateRemoteStateOverride(t *testing.T) {
	dsNames := []string{"networking", "core"}

	// Test vars file
	varsContent := GenerateRemoteStateVars(dsNames)
	if !strings.Contains(varsContent, "Auto-generated by tfsync") {
		t.Error("expected header comment in vars")
	}
	if !strings.Contains(varsContent, `variable "tfsync_rs_core_path"`) {
		t.Error("expected core variable declaration")
	}
	if !strings.Contains(varsContent, `variable "tfsync_rs_networking_path"`) {
		t.Error("expected networking variable declaration")
	}

	// Test override file
	content := GenerateRemoteStateOverride(dsNames)
	if !strings.Contains(content, "Auto-generated by tfsync") {
		t.Error("expected header comment")
	}
	if !strings.Contains(content, `data "terraform_remote_state" "core"`) {
		t.Error("expected core data source")
	}
	if !strings.Contains(content, `data "terraform_remote_state" "networking"`) {
		t.Error("expected networking data source")
	}
	if !strings.Contains(content, `path = var.tfsync_rs_core_path`) {
		t.Error("expected core variable reference")
	}
	if !strings.Contains(content, `path = var.tfsync_rs_networking_path`) {
		t.Error("expected networking variable reference")
	}
	// core should appear before networking (sorted)
	coreIdx := strings.Index(content, `"core"`)
	netIdx := strings.Index(content, `"networking"`)
	if coreIdx > netIdx {
		t.Errorf("expected core before networking (sorted), got core at %d, networking at %d", coreIdx, netIdx)
	}
}

func TestRemoteStateVarName(t *testing.T) {
	if got := RemoteStateVarName("core"); got != "tfsync_rs_core_path" {
		t.Errorf("expected tfsync_rs_core_path, got %s", got)
	}
	if got := RemoteStateVarName("dev-core"); got != "tfsync_rs_dev_core_path" {
		t.Errorf("expected tfsync_rs_dev_core_path, got %s", got)
	}
}

func TestGenerateRemoteStateOverride_Empty(t *testing.T) {
	content := GenerateRemoteStateOverride(nil)
	if !strings.Contains(content, "Auto-generated") {
		t.Error("expected header even for empty overrides")
	}
	if strings.Contains(content, "terraform_remote_state") {
		t.Error("expected no data sources for empty overrides")
	}
}
