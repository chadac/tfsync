package internal

import (
	"os"
	"testing"
)

func TestNewCLI(t *testing.T) {
	tests := []struct {
		name           string
		binary         string
		workDir        string
		expectedBinary string
	}{
		{
			name:           "default binary",
			binary:         "",
			workDir:        "/test",
			expectedBinary: "tofu",
		},
		{
			name:           "custom binary",
			binary:         "terraform",
			workDir:        "/test",
			expectedBinary: "terraform",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := NewCLI(tt.binary, tt.workDir)
			if cli.Binary != tt.expectedBinary {
				t.Errorf("NewCLI().Binary = %q, want %q", cli.Binary, tt.expectedBinary)
			}
			if cli.WorkDir != tt.workDir {
				t.Errorf("NewCLI().WorkDir = %q, want %q", cli.WorkDir, tt.workDir)
			}
		})
	}
}

func TestNewCLIWithConfig(t *testing.T) {
	lockFalse := false
	refreshTrue := true

	planCfg := &PlanConfig{
		Lock:      &lockFalse,
		Refresh:   &refreshTrue,
		ExtraArgs: []string{"-compact-warnings"},
	}

	cli := NewCLIWithConfig("tofu", "/test", planCfg)

	if cli.Binary != "tofu" {
		t.Errorf("NewCLIWithConfig().Binary = %q, want %q", cli.Binary, "tofu")
	}
	if cli.WorkDir != "/test" {
		t.Errorf("NewCLIWithConfig().WorkDir = %q, want %q", cli.WorkDir, "/test")
	}
	if cli.PlanConfig == nil {
		t.Errorf("NewCLIWithConfig().PlanConfig = nil, want non-nil")
	}
	if cli.PlanConfig != nil && cli.PlanConfig.Lock == nil {
		t.Errorf("NewCLIWithConfig().PlanConfig.Lock = nil, want non-nil")
	}
}

func TestCLIPlanArgs(t *testing.T) {
	lockFalse := false
	lockTrue := true
	refreshFalse := false

	tests := []struct {
		name         string
		cli          *CLI
		outPath      string
		expectedArgs []string
	}{
		{
			name:         "basic plan args",
			cli:          NewCLI("tofu", "/test"),
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false"},
		},
		{
			name:         "with output path",
			cli:          NewCLI("tofu", "/test"),
			outPath:      "/tmp/plan.out",
			expectedArgs: []string{"plan", "-input=false", "-out=/tmp/plan.out"},
		},
		{
			name: "with lock=false",
			cli: &CLI{
				Binary:     "tofu",
				WorkDir:    "/test",
				PlanConfig: &PlanConfig{Lock: &lockFalse},
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-lock=false"},
		},
		{
			name: "with lock=true",
			cli: &CLI{
				Binary:     "tofu",
				WorkDir:    "/test",
				PlanConfig: &PlanConfig{Lock: &lockTrue},
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-lock=true"},
		},
		{
			name: "with refresh=false",
			cli: &CLI{
				Binary:     "tofu",
				WorkDir:    "/test",
				PlanConfig: &PlanConfig{Refresh: &refreshFalse},
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-refresh=false"},
		},
		{
			name: "with parallelism",
			cli: &CLI{
				Binary:      "tofu",
				WorkDir:     "/test",
				Parallelism: 10,
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-parallelism=10"},
		},
		{
			name: "with var files",
			cli: &CLI{
				Binary:   "tofu",
				WorkDir:  "/test",
				VarFiles: []string{"vars.tfvars", "prod.tfvars"},
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-var-file=vars.tfvars", "-var-file=prod.tfvars"},
		},
		{
			name: "with vars",
			cli: &CLI{
				Binary:  "tofu",
				WorkDir: "/test",
				Vars:    []string{"foo=bar", "baz=qux"},
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-var=foo=bar", "-var=baz=qux"},
		},
		{
			name: "with extra args",
			cli: &CLI{
				Binary:  "tofu",
				WorkDir: "/test",
				PlanConfig: &PlanConfig{
					ExtraArgs: []string{"-compact-warnings", "-no-color"},
				},
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-compact-warnings", "-no-color"},
		},
		{
			name: "combined options",
			cli: &CLI{
				Binary:      "tofu",
				WorkDir:     "/test",
				Parallelism: 5,
				PlanConfig: &PlanConfig{
					Lock:      &lockFalse,
					ExtraArgs: []string{"-no-color"},
				},
			},
			outPath:      "/tmp/out.tfplan",
			expectedArgs: []string{"plan", "-input=false", "-out=/tmp/out.tfplan", "-lock=false", "-parallelism=5", "-no-color"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.cli.planArgs(tt.outPath)
			if len(result) != len(tt.expectedArgs) {
				t.Errorf("planArgs() returned %d args, want %d\ngot:  %v\nwant: %v",
					len(result), len(tt.expectedArgs), result, tt.expectedArgs)
				return
			}
			for i, arg := range tt.expectedArgs {
				if result[i] != arg {
					t.Errorf("planArgs()[%d] = %q, want %q\nfull result: %v",
						i, result[i], arg, result)
				}
			}
		})
	}
}

func TestCLIPlanArgsWithEnvExpansion(t *testing.T) {
	os.Setenv("TEST_EXTRA_ARG", "-target=module.foo")
	defer os.Unsetenv("TEST_EXTRA_ARG")

	cli := &CLI{
		Binary:  "tofu",
		WorkDir: "/test",
		PlanConfig: &PlanConfig{
			ExtraArgs: []string{"${TEST_EXTRA_ARG}"},
		},
	}

	result := cli.planArgs("")
	expectedArgs := []string{"plan", "-input=false", "-target=module.foo"}

	if len(result) != len(expectedArgs) {
		t.Errorf("planArgs() returned %d args, want %d\ngot:  %v\nwant: %v",
			len(result), len(expectedArgs), result, expectedArgs)
		return
	}

	for i, arg := range expectedArgs {
		if result[i] != arg {
			t.Errorf("planArgs()[%d] = %q, want %q", i, result[i], arg)
		}
	}
}

func TestTFGetTool(t *testing.T) {
	tests := []struct {
		name     string
		tf       *TF
		expected string
	}{
		{
			name:     "nil TF",
			tf:       nil,
			expected: "tofu",
		},
		{
			name:     "empty tool",
			tf:       &TF{},
			expected: "tofu",
		},
		{
			name:     "terraform",
			tf:       &TF{Tool: "terraform"},
			expected: "terraform",
		},
		{
			name:     "tofu explicit",
			tf:       &TF{Tool: "tofu"},
			expected: "tofu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.tf.GetTool()
			if result != tt.expected {
				t.Errorf("GetTool() = %q, want %q", result, tt.expected)
			}
		})
	}
}
