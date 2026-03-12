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
			expectedArgs: []string{"plan", "-input=false", "-lock=false"},
		},
		{
			name:         "with output path",
			cli:          NewCLI("tofu", "/test"),
			outPath:      "/tmp/plan.out",
			expectedArgs: []string{"plan", "-input=false", "-out=/tmp/plan.out", "-lock=false"},
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
			expectedArgs: []string{"plan", "-input=false", "-lock=false", "-refresh=false"},
		},
		{
			name: "with parallelism",
			cli: &CLI{
				Binary:      "tofu",
				WorkDir:     "/test",
				Parallelism: 10,
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-lock=false", "-parallelism=10"},
		},
		{
			name: "with var files",
			cli: &CLI{
				Binary:   "tofu",
				WorkDir:  "/test",
				VarFiles: []string{"vars.tfvars", "prod.tfvars"},
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-lock=false", "-var-file=vars.tfvars", "-var-file=prod.tfvars"},
		},
		{
			name: "with vars",
			cli: &CLI{
				Binary:  "tofu",
				WorkDir: "/test",
				Vars:    []string{"foo=bar", "baz=qux"},
			},
			outPath:      "",
			expectedArgs: []string{"plan", "-input=false", "-lock=false", "-var=foo=bar", "-var=baz=qux"},
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
			expectedArgs: []string{"plan", "-input=false", "-lock=false", "-compact-warnings", "-no-color"},
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
	expectedArgs := []string{"plan", "-input=false", "-lock=false", "-target=module.foo"}

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

func TestParseErrorResourceAddresses(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected []string
	}{
		{
			name:     "no errors",
			output:   "No changes. Your infrastructure matches the configuration.",
			expected: nil,
		},
		{
			name: "single error",
			output: `╷
│ Error: reading S3 Object (bucket/key): Forbidden
│ 
│   with module.stack.aws_s3_object.data[0],
│   on main.tf line 1, in resource "aws_s3_object" "data":
│    1: resource "aws_s3_object" "data" {
│ 
╵`,
			expected: []string{"module.stack.aws_s3_object.data[0]"},
		},
		{
			name: "multiple errors",
			output: `╷
│ Error: reading S3 Object (bucket/input/): Forbidden
│ 
│   with module.stack.aws_s3_object.inbound[0],
│   on s3_dir.tf line 1, in resource "aws_s3_object" "inbound":
│    1: resource "aws_s3_object" "inbound" {
│ 
╵
╷
│ Error: reading S3 Object (bucket/output/): Forbidden
│ 
│   with module.stack.aws_s3_object.outbound[0],
│   on s3_dir.tf line 11, in resource "aws_s3_object" "outbound":
│   11: resource "aws_s3_object" "outbound" {
│ 
╵`,
			expected: []string{
				"module.stack.aws_s3_object.inbound[0]",
				"module.stack.aws_s3_object.outbound[0]",
			},
		},
		{
			name:   "with ANSI color codes",
			output: "\x1b[31m│\x1b[0m \x1b[0mError: reading S3 Object\x1b[0m\n\x1b[31m│\x1b[0m \x1b[0m\x1b[0m  with module.stack.aws_s3_object.inbound[0],\n╷\n\x1b[31m│\x1b[0m \x1b[0mError: reading S3 Object\x1b[0m\n\x1b[31m│\x1b[0m \x1b[0m\x1b[0m  with module.stack.aws_s3_object.outbound[0],",
			expected: []string{
				"module.stack.aws_s3_object.inbound[0]",
				"module.stack.aws_s3_object.outbound[0]",
			},
		},
		{
			name: "duplicate addresses deduplicated",
			output: `╷
│ Error: some error
│   with aws_instance.foo,
╵
╷
│ Error: some error
│   with aws_instance.foo,
╵`,
			expected: []string{"aws_instance.foo"},
		},
		{
			name: "warning addresses excluded",
			output: `╷
│ Warning: Argument is deprecated
│ 
│   with module.stack.aws_iam_role.this,
│ 
╵
╷
│ Error: reading S3 Object: Forbidden
│ 
│   with module.stack.aws_s3_object.data[0],
│ 
╵`,
			expected: []string{"module.stack.aws_s3_object.data[0]"},
		},
		{
			name: "error without resource address",
			output: `╷
│ Error: Error configuring provider
│ 
│ Some provider error
╵`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseErrorResourceAddresses(tt.output)
			if len(result) != len(tt.expected) {
				t.Errorf("parseErrorResourceAddresses() = %v, want %v", result, tt.expected)
				return
			}
			for i, addr := range tt.expected {
				if result[i] != addr {
					t.Errorf("parseErrorResourceAddresses()[%d] = %q, want %q", i, result[i], addr)
				}
			}
		})
	}
}

func TestAllAddressesIgnored(t *testing.T) {
	tests := []struct {
		name       string
		addrs      []string
		ignoreList []string
		expected   bool
	}{
		{
			name:       "all ignored",
			addrs:      []string{"aws_s3_object.foo[0]", "aws_s3_object.bar[0]"},
			ignoreList: []string{"aws_s3_object.foo[0]", "aws_s3_object.bar[0]"},
			expected:   true,
		},
		{
			name:       "some not ignored",
			addrs:      []string{"aws_s3_object.foo[0]", "aws_instance.web"},
			ignoreList: []string{"aws_s3_object.foo[0]"},
			expected:   false,
		},
		{
			name:       "empty addrs",
			addrs:      []string{},
			ignoreList: []string{"aws_s3_object.foo[0]"},
			expected:   true,
		},
		{
			name:       "empty ignore list",
			addrs:      []string{"aws_s3_object.foo[0]"},
			ignoreList: []string{},
			expected:   false,
		},
		{
			name:       "superset ignore list",
			addrs:      []string{"aws_s3_object.foo[0]"},
			ignoreList: []string{"aws_s3_object.foo[0]", "aws_s3_object.bar[0]", "aws_instance.web"},
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := allAddressesIgnored(tt.addrs, tt.ignoreList)
			if result != tt.expected {
				t.Errorf("allAddressesIgnored(%v, %v) = %v, want %v", tt.addrs, tt.ignoreList, result, tt.expected)
			}
		})
	}
}
