package internal

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
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

func TestActionsToString(t *testing.T) {
	tests := []struct {
		name     string
		actions  []string
		expected string
	}{
		{"create", []string{"create"}, "create"},
		{"delete", []string{"delete"}, "delete"},
		{"update", []string{"update"}, "update"},
		{"no-op", []string{"no-op"}, "no-op"},
		{"read", []string{"read"}, "read"},
		{"replace delete-create", []string{"delete", "create"}, "replace"},
		{"replace create-delete", []string{"create", "delete"}, "replace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert string slice to tfjson.Actions
			var actions tfjson.Actions
			for _, a := range tt.actions {
				actions = append(actions, tfjson.Action(a))
			}
			result := actionsToString(actions)
			if result != tt.expected {
				t.Errorf("actionsToString(%v) = %q, want %q", tt.actions, result, tt.expected)
			}
		})
	}
}

func TestBuildDiffReport(t *testing.T) {
	results := []PlanResult{
		{
			Workspace:  "networking",
			HasChanges: false,
		},
		{
			Workspace:  "compute",
			HasChanges: true,
			ResourceDiffs: []ResourceDiff{
				{Address: "aws_instance.web", Action: "create"},
				{Address: "aws_instance.api", Action: "update"},
				{Address: "aws_instance.old", Action: "delete"},
			},
		},
		{
			Workspace: "broken",
			Error:     fmt.Errorf("plan failed: unable to assume role"),
		},
	}

	report := BuildDiffReport(results, false)

	if report.Success {
		t.Error("expected success=false")
	}

	if len(report.Workspaces) != 3 {
		t.Fatalf("expected 3 workspaces, got %d", len(report.Workspaces))
	}

	// Check networking
	net := report.Workspaces["networking"]
	if net.Status != "ok" {
		t.Errorf("networking status = %q, want 'ok'", net.Status)
	}

	// Check compute
	comp := report.Workspaces["compute"]
	if comp.Status != "changes" {
		t.Errorf("compute status = %q, want 'changes'", comp.Status)
	}
	if comp.Summary.Create != 1 || comp.Summary.Update != 1 || comp.Summary.Delete != 1 {
		t.Errorf("compute summary = %+v, want 1 create, 1 update, 1 delete", comp.Summary)
	}

	// Check broken
	broken := report.Workspaces["broken"]
	if broken.Status != "error" {
		t.Errorf("broken status = %q, want 'error'", broken.Status)
	}
	if broken.Error == "" {
		t.Error("broken error should be non-empty")
	}
}

func TestReportPlanResults_WithResourceDiffs(t *testing.T) {
	var buf strings.Builder
	rep := NewWithWriter(&buf, false)

	results := []PlanResult{
		{
			Workspace:  "networking",
			HasChanges: false,
		},
		{
			Workspace:  "compute",
			HasChanges: true,
			ResourceDiffs: []ResourceDiff{
				{Address: "aws_instance.web", Action: "create"},
				{Address: "aws_security_group.web", Action: "create"},
				{Address: "aws_instance.api", Action: "update"},
				{Address: "aws_instance.old", Action: "delete"},
			},
		},
	}

	allPassed := rep.ReportPlanResults(results)
	output := buf.String()

	if allPassed {
		t.Error("expected allPassed=false")
	}

	// Should show structured diff, not raw output
	if !strings.Contains(output, "+ create aws_instance.web") {
		t.Errorf("expected structured create output, got:\n%s", output)
	}
	if !strings.Contains(output, "~ update aws_instance.api") {
		t.Errorf("expected structured update output, got:\n%s", output)
	}
	if !strings.Contains(output, "- delete aws_instance.old") {
		t.Errorf("expected structured delete output, got:\n%s", output)
	}
	// networking should pass
	if !strings.Contains(output, "No changes detected") {
		t.Errorf("expected networking to pass, got:\n%s", output)
	}
}

func TestReportPlanResults_ErrorWorkspace(t *testing.T) {
	var buf strings.Builder
	rep := NewWithWriter(&buf, false)

	results := []PlanResult{
		{
			Workspace: "broken",
			Error:     fmt.Errorf("unable to assume IAM role"),
		},
		{
			Workspace:  "working",
			HasChanges: false,
		},
	}

	allPassed := rep.ReportPlanResults(results)
	output := buf.String()

	if allPassed {
		t.Error("expected allPassed=false")
	}
	if !strings.Contains(output, "Plan failed: unable to assume IAM role") {
		t.Errorf("expected error message, got:\n%s", output)
	}
	if !strings.Contains(output, "No changes detected") {
		t.Errorf("expected working workspace to still report, got:\n%s", output)
	}
}

func TestBuildDiffReport_UpstreamDiffs(t *testing.T) {
	results := []PlanResult{
		{
			Workspace:  "compute",
			HasChanges: true,
			ResourceDiffs: []ResourceDiff{
				{Address: "aws_instance.web", Action: "update"},
				{Address: "aws_instance.api", Action: "create"},
				{Address: "aws_security_group.web", Action: "update"},
			},
			UpstreamDiffs: []ResourceDiff{
				{Address: "aws_instance.web", Action: "update"},
				{Address: "aws_iam_role.unrelated", Action: "create"},
			},
		},
	}

	report := BuildDiffReport(results, false)
	comp := report.Workspaces["compute"]

	// aws_instance.web should be annotated as upstream_changed
	var webDiff *ResourceDiff
	for i, d := range comp.ResourceDiffs {
		if d.Address == "aws_instance.web" {
			webDiff = &comp.ResourceDiffs[i]
			break
		}
	}
	if webDiff == nil {
		t.Fatal("expected aws_instance.web in resource diffs")
	}
	if !webDiff.UpstreamChanged {
		t.Error("expected aws_instance.web to have upstream_changed=true")
	}
	if webDiff.UpstreamAction != "update" {
		t.Errorf("expected upstream_action='update', got %q", webDiff.UpstreamAction)
	}

	// aws_instance.api should NOT be annotated (not in upstream diffs)
	for _, d := range comp.ResourceDiffs {
		if d.Address == "aws_instance.api" && d.UpstreamChanged {
			t.Error("aws_instance.api should not be marked as upstream_changed")
		}
	}

	// UpstreamDiffs should be included in the workspace diff
	if len(comp.UpstreamDiffs) != 2 {
		t.Errorf("expected 2 upstream diffs, got %d", len(comp.UpstreamDiffs))
	}
}

func TestBuildDiffReport_DefaultWorkspace(t *testing.T) {
	results := []PlanResult{
		{
			Workspace:  "",
			HasChanges: false,
		},
	}

	report := BuildDiffReport(results, true)
	if _, ok := report.Workspaces["default"]; !ok {
		t.Error("empty workspace name should become 'default'")
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

func TestParseTextPlanDiffs(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected []ResourceDiff
	}{
		{
			name: "basic actions",
			output: `
  # aws_instance.foo will be created
  # aws_instance.bar will be updated in-place
  # aws_instance.baz will be destroyed
  # aws_instance.qux must be replaced
  # data.aws_ami.latest will be read during apply
`,
			expected: []ResourceDiff{
				{Address: "aws_instance.foo", Action: "create"},
				{Address: "aws_instance.bar", Action: "update"},
				{Address: "aws_instance.baz", Action: "delete"},
				{Address: "aws_instance.qux", Action: "replace"},
				{Address: "data.aws_ami.latest", Action: "read"},
			},
		},
		{
			name: "with ANSI codes",
			output: "\x1b[1m  # module.this[0].aws_iam_role.foo will be updated in-place\x1b[0m\n" +
				"\x1b[31m  # module.this[0].aws_instance.bar will be destroyed\x1b[0m\n",
			expected: []ResourceDiff{
				{Address: "module.this[0].aws_iam_role.foo", Action: "update"},
				{Address: "module.this[0].aws_instance.bar", Action: "delete"},
			},
		},
		{
			name: "deduplicates addresses",
			output: `
  # aws_instance.foo will be created
  # aws_instance.foo will be created
`,
			expected: []ResourceDiff{
				{Address: "aws_instance.foo", Action: "create"},
			},
		},
		{
			name:     "no matches",
			output:   "Error: something went wrong\n",
			expected: nil,
		},
		{
			name: "module addresses",
			output: `
  # module.this[0].module.datasync_jobs["input"].aws_s3_object.data_folder[0] will be created
  # module.vpc.aws_subnet.private["us-east-1a"] will be destroyed
`,
			expected: []ResourceDiff{
				{Address: `module.this[0].module.datasync_jobs["input"].aws_s3_object.data_folder[0]`, Action: "create"},
				{Address: `module.vpc.aws_subnet.private["us-east-1a"]`, Action: "delete"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs := parseTextPlanDiffs(tt.output)
			if len(diffs) != len(tt.expected) {
				t.Fatalf("got %d diffs, want %d: %+v", len(diffs), len(tt.expected), diffs)
			}
			for i, d := range diffs {
				if d.Address != tt.expected[i].Address {
					t.Errorf("diff[%d].Address = %q, want %q", i, d.Address, tt.expected[i].Address)
				}
				if d.Action != tt.expected[i].Action {
					t.Errorf("diff[%d].Action = %q, want %q", i, d.Action, tt.expected[i].Action)
				}
			}
		})
	}
}
