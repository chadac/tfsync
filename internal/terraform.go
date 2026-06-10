package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// CLI wraps terraform/tofu command execution.
type CLI struct {
	// Binary is the terraform binary to use (terraform, tofu, terragrunt)
	Binary string
	// WorkDir is the working directory for terraform commands
	WorkDir string
	// Env is additional environment variables to set
	Env []string
	// Parallelism is the -parallelism flag value (0 means default)
	Parallelism int
	// VarFiles is a list of -var-file paths
	VarFiles []string
	// Vars is a list of -var values
	Vars []string
	// PlanConfig holds plan-specific configuration
	PlanConfig *PlanConfig
	// Debug enables verbose logging of commands
	Debug bool
}

// NewCLI creates a new terraform CLI wrapper.
func NewCLI(binary, workDir string) *CLI {
	if binary == "" {
		binary = "tofu"
	}
	return &CLI{
		Binary:  binary,
		WorkDir: workDir,
	}
}

// NewCLIWithConfig creates a new terraform CLI wrapper with plan configuration.
func NewCLIWithConfig(binary, workDir string, planCfg *PlanConfig) *CLI {
	cli := NewCLI(binary, workDir)
	cli.PlanConfig = planCfg
	return cli
}

// WriteOverrideFiles writes any configured override files to the working directory.
// These files are typically used to configure provider overrides for testing.
// Returns a list of files written (for cleanup purposes).
func (c *CLI) WriteOverrideFiles() ([]string, error) {
	if c.PlanConfig == nil || len(c.PlanConfig.OverrideFiles) == 0 {
		return nil, nil
	}

	var written []string
	for filename, content := range c.PlanConfig.OverrideFiles {
		path := filepath.Join(c.WorkDir, filename)
		// Expand environment variables in content
		expandedContent := ExpandEnv(content)
		if err := os.WriteFile(path, []byte(expandedContent), 0644); err != nil {
			return written, fmt.Errorf("failed to write override file %s: %w", filename, err)
		}
		written = append(written, path)
	}

	return written, nil
}

// CleanupOverrideFiles removes override files that were written.
func (c *CLI) CleanupOverrideFiles(files []string) {
	for _, path := range files {
		os.Remove(path)
	}
}

// Init runs terraform init.
func (c *CLI) Init(ctx context.Context) error {
	return c.InitWithConfig(ctx, nil)
}

// InitWithBackendConfig runs terraform init with backend config overrides.
// Deprecated: use InitWithConfig instead.
func (c *CLI) InitWithBackendConfig(ctx context.Context, backendConfig map[string]string) error {
	return c.InitWithConfig(ctx, &InitConfig{Backend: backendConfig})
}

// InitWithConfig runs terraform init with the given configuration.
func (c *CLI) InitWithConfig(ctx context.Context, cfg *InitConfig) error {
	args := []string{"init", "-input=false"}

	if cfg != nil {
		// Add backend config overrides (with env var expansion)
		if len(cfg.Backend) > 0 {
			args = append(args, "-reconfigure")
			for k, v := range ExpandEnvMap(cfg.Backend) {
				args = append(args, fmt.Sprintf("-backend-config=%s=%s", k, v))
			}
		}
		// Add extra args (with env var expansion)
		args = append(args, ExpandEnvSlice(cfg.ExtraArgs)...)
	}

	output, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	if strings.Contains(string(output), "initialized in an empty directory") ||
		strings.Contains(string(output), "has no Terraform configuration files") {
		return fmt.Errorf("terraform init found no configuration files in %s — check that the source path contains .tf files", c.WorkDir)
	}
	return nil
}

// Plan runs terraform plan and returns the plan output.
func (c *CLI) Plan(ctx context.Context) (*tfjson.Plan, error) {
	// Create a temp file for the plan
	planFile, err := os.CreateTemp("", "tfsync-plan-*.tfplan")
	if err != nil {
		return nil, fmt.Errorf("failed to create plan file: %w", err)
	}
	planPath := planFile.Name()
	planFile.Close()
	defer os.Remove(planPath)

	// Run plan
	args := c.planArgs(planPath)
	if _, err := c.run(ctx, args...); err != nil {
		return nil, err
	}

	// Convert plan to JSON
	jsonOutput, err := c.run(ctx, "show", "-json", planPath)
	if err != nil {
		return nil, fmt.Errorf("failed to show plan as JSON: %w", err)
	}

	var plan tfjson.Plan
	if err := json.Unmarshal(jsonOutput, &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w", err)
	}

	return &plan, nil
}

// PlannedOutputs runs a terraform plan and extracts the planned output values.
// Returns outputs in terraform state file format (map of name -> {"value": ..., "type": ...}).
// This is used to inject outputs into dependency workspaces' state files so that
// terraform_remote_state data sources can read them during validation.
func (c *CLI) PlannedOutputs(ctx context.Context) (map[string]interface{}, error) {
	plan, err := c.Plan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get plan for outputs: %w", err)
	}

	if plan.PlannedValues == nil || len(plan.PlannedValues.Outputs) == 0 {
		return nil, nil
	}

	outputs := make(map[string]interface{})
	for name, out := range plan.PlannedValues.Outputs {
		entry := map[string]interface{}{
			"value": out.Value,
			"type":  out.Type,
		}
		if out.Sensitive {
			entry["sensitive"] = true
		}
		outputs[name] = entry
	}
	return outputs, nil
}

// ResourceDiff describes a single resource change from a terraform plan.
type ResourceDiff struct {
	// Address is the absolute resource address (e.g., "module.foo.aws_instance.bar").
	Address string `json:"address"`
	// Action is the change action: "create", "update", "delete", "replace", "read".
	Action string `json:"action"`
	// UpstreamChanged indicates this resource also has planned changes in the source workspace.
	// When true, the diff may be pre-existing drift rather than a migration issue.
	UpstreamChanged bool `json:"upstream_changed,omitempty"`
	// UpstreamAction is the action planned for this resource in the source workspace
	// (only set when UpstreamChanged is true).
	UpstreamAction string `json:"upstream_action,omitempty"`
}

// SourcePlanResult holds the results of running a plan on a source workspace.
type SourcePlanResult struct {
	// Workspace is the source workspace name.
	Workspace string
	// ResourceDiffs are the resource changes detected in the source plan.
	ResourceDiffs []ResourceDiff
	// Error is set if the source plan failed entirely.
	Error error
}

// PlanCheckResult contains the results of a plan check.
type PlanCheckResult struct {
	// HasChanges is true if the plan detected any resource changes.
	HasChanges bool
	// Output is the human-readable plan output.
	Output string
	// ChangedAddresses is the list of resource addresses that have changes.
	// Only populated when HasChanges is true.
	ChangedAddresses []string
	// ResourceDiffs is the structured list of per-resource changes.
	// Only populated when HasChanges is true and JSON plan extraction succeeds.
	ResourceDiffs []ResourceDiff
	// IgnoredErrors is the list of resource addresses whose errors were ignored.
	IgnoredErrors []string
}

// PlanHasChanges runs terraform plan and returns whether there are changes.
// When changes are detected, it also runs a JSON plan to extract changed resource addresses.
func (c *CLI) PlanHasChanges(ctx context.Context) (*PlanCheckResult, error) {
	// Run plan with -detailed-exitcode
	args := c.planArgs("")
	args = append(args, "-detailed-exitcode")

	output, err := c.runWithExitCode(ctx, args...)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Exit code 2 means changes detected
			if exitErr.ExitCode() == 2 {
				// Get JSON plan to extract changed addresses and diffs
				diffs, planErr := c.planResourceDiffs(ctx)
				if planErr != nil {
					// Fall back to reporting changes without addresses
					return &PlanCheckResult{
						HasChanges: true,
						Output:     string(output),
					}, nil
				}
				var addresses []string
				for _, d := range diffs {
					addresses = append(addresses, d.Address)
				}
				return &PlanCheckResult{
					HasChanges:       true,
					Output:           string(output),
					ChangedAddresses: addresses,
					ResourceDiffs:    diffs,
				}, nil
			}
			// Exit code 1 means error - check if all errors can be ignored
			if c.PlanConfig != nil && len(c.PlanConfig.IgnoreErrors) > 0 {
				errorAddrs := parseErrorResourceAddresses(string(output))
				if c.Debug {
					fmt.Fprintf(os.Stderr, "[DEBUG] ignore_errors configured: %v\n", c.PlanConfig.IgnoreErrors)
					fmt.Fprintf(os.Stderr, "[DEBUG] parsed error addresses: %v\n", errorAddrs)
					// Dump lines containing "with" for debugging format issues
					for _, line := range strings.Split(string(output), "\n") {
						if strings.Contains(line, "with ") {
							fmt.Fprintf(os.Stderr, "[DEBUG] line with 'with': %q\n", line)
						}
					}
				}
				if (len(errorAddrs) > 0 || hasWildcardIgnore(c.PlanConfig.IgnoreErrors)) && allAddressesIgnored(errorAddrs, c.PlanConfig.IgnoreErrors) {
					return &PlanCheckResult{
						HasChanges:    false,
						Output:        string(output),
						IgnoredErrors: errorAddrs,
					}, nil
				}
			}
			// Parse resource diffs from text output even on error
			diffs := parseTextPlanDiffs(string(output))
			var addresses []string
			for _, d := range diffs {
				addresses = append(addresses, d.Address)
			}
			return &PlanCheckResult{
				HasChanges:       len(diffs) > 0,
				Output:           string(output),
				ChangedAddresses: addresses,
				ResourceDiffs:    diffs,
			}, fmt.Errorf("plan failed (exit code %d):\n%s", exitErr.ExitCode(), string(output))
		}
		return nil, err
	}

	// Exit code 0 means no changes
	return &PlanCheckResult{
		HasChanges: false,
		Output:     string(output),
	}, nil
}

// PlanResourceDiffs runs a JSON plan and extracts per-resource change information.
// This is the public API; used by the upstream plan diff feature to plan source workspaces.
func (c *CLI) PlanResourceDiffs(ctx context.Context) ([]ResourceDiff, error) {
	return c.planResourceDiffs(ctx)
}

// planResourceDiffs runs a JSON plan and extracts per-resource change information.
func (c *CLI) planResourceDiffs(ctx context.Context) ([]ResourceDiff, error) {
	plan, err := c.Plan(ctx)
	if err != nil {
		return nil, err
	}

	var diffs []ResourceDiff
	if plan.ResourceChanges != nil {
		for _, rc := range plan.ResourceChanges {
			if rc.Change == nil || len(rc.Change.Actions) == 0 {
				continue
			}
			action := actionsToString(rc.Change.Actions)
			if action == "no-op" {
				continue
			}
			diffs = append(diffs, ResourceDiff{
				Address: rc.Address,
				Action:  action,
			})
		}
	}

	return diffs, nil
}

// actionsToString converts terraform plan actions to a human-readable string.
func actionsToString(actions tfjson.Actions) string {
	if len(actions) == 1 {
		return string(actions[0])
	}
	if len(actions) == 2 {
		if actions[0] == tfjson.ActionDelete && actions[1] == tfjson.ActionCreate {
			return "replace"
		}
		if actions[0] == tfjson.ActionCreate && actions[1] == tfjson.ActionDelete {
			return "replace"
		}
	}
	// Fallback: join action names
	var parts []string
	for _, a := range actions {
		parts = append(parts, string(a))
	}
	return strings.Join(parts, ",")
}

// parseTextPlanDiffs extracts resource diffs from terraform plan text output.
// This is used as a fallback when JSON plan is unavailable (e.g., exit code 1 errors).
// It parses lines like:
//
//	# module.foo.aws_instance.bar will be created
//	# module.foo.aws_instance.bar will be updated in-place
//	# module.foo.aws_instance.bar will be destroyed
//	# module.foo.aws_instance.bar must be replaced
//	# module.foo.aws_instance.bar will be read during apply
var textPlanDiffRe = regexp.MustCompile(
	`(?m)^\s*#\s+(\S+)\s+(?:will be |must be )(created|updated|destroyed|replaced|read)`,
)

func parseTextPlanDiffs(output string) []ResourceDiff {
	// Strip ANSI escape codes
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	clean := ansiRe.ReplaceAllString(output, "")

	matches := textPlanDiffRe.FindAllStringSubmatch(clean, -1)
	var diffs []ResourceDiff
	seen := map[string]bool{}
	for _, m := range matches {
		addr := m[1]
		if seen[addr] {
			continue
		}
		seen[addr] = true
		action := m[2]
		// Normalize to match JSON plan action names
		switch action {
		case "created":
			action = "create"
		case "updated":
			action = "update"
		case "destroyed":
			action = "delete"
		case "replaced":
			action = "replace"
		}
		diffs = append(diffs, ResourceDiff{Address: addr, Action: action})
	}
	return diffs
}

// StatePull pulls the current state and returns it as JSON bytes.
func (c *CLI) StatePull(ctx context.Context) ([]byte, error) {
	return c.run(ctx, "state", "pull")
}

// StatePush pushes state from a file.
func (c *CLI) StatePush(ctx context.Context, statePath string) error {
	_, err := c.run(ctx, "state", "push", "-force", statePath)
	return err
}

// StateMv moves a resource in the state.
func (c *CLI) StateMv(ctx context.Context, src, dst string) error {
	_, err := c.run(ctx, "state", "mv", src, dst)
	return err
}

// StateRm removes a resource from the state.
func (c *CLI) StateRm(ctx context.Context, addr string) error {
	_, err := c.run(ctx, "state", "rm", addr)
	return err
}

// StateList lists all resources in the state.
func (c *CLI) StateList(ctx context.Context) ([]string, error) {
	output, err := c.run(ctx, "state", "list")
	if err != nil {
		return nil, err
	}

	var resources []string
	for _, line := range bytes.Split(output, []byte("\n")) {
		if len(line) > 0 {
			resources = append(resources, string(line))
		}
	}
	return resources, nil
}

// planArgs builds the arguments for a plan command.
func (c *CLI) planArgs(outPath string) []string {
	args := []string{"plan", "-input=false"}

	if outPath != "" {
		args = append(args, "-out="+outPath)
	}

	// Default to -lock=false for validation plans (avoid lock contention in parallel plans).
	// Users can explicitly set lock=true to override.
	lockValue := false
	if c.PlanConfig != nil && c.PlanConfig.Lock != nil {
		lockValue = *c.PlanConfig.Lock
	}
	args = append(args, fmt.Sprintf("-lock=%t", lockValue))

	// Apply other PlanConfig settings
	if c.PlanConfig != nil {
		if c.PlanConfig.Refresh != nil {
			args = append(args, fmt.Sprintf("-refresh=%t", *c.PlanConfig.Refresh))
		}
	}

	if c.Parallelism > 0 {
		args = append(args, fmt.Sprintf("-parallelism=%d", c.Parallelism))
	}

	for _, vf := range c.VarFiles {
		args = append(args, "-var-file="+vf)
	}

	for _, v := range c.Vars {
		args = append(args, "-var="+v)
	}

	// Add extra args from PlanConfig (with env var expansion)
	if c.PlanConfig != nil && len(c.PlanConfig.ExtraArgs) > 0 {
		args = append(args, ExpandEnvSlice(c.PlanConfig.ExtraArgs)...)
	}

	return args
}

// run executes a terraform command and returns stdout.
func (c *CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] %s %v (in %s)\n", c.Binary, args, c.WorkDir)
	}

	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Dir = c.WorkDir
	cmd.Env = append(os.Environ(), c.Env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %v failed: %w\nstderr: %s", c.Binary, args, err, stderr.String())
	}

	if c.Debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] %s %v completed\n", c.Binary, args)
		if stdout.Len() > 0 {
			fmt.Fprintf(os.Stderr, "[DEBUG] stdout:\n%s\n", stdout.String())
		}
		if stderr.Len() > 0 {
			fmt.Fprintf(os.Stderr, "[DEBUG] stderr:\n%s\n", stderr.String())
		}
	}

	return stdout.Bytes(), nil
}

// runWithExitCode executes a terraform command, returning output even on non-zero exit.
func (c *CLI) runWithExitCode(ctx context.Context, args ...string) ([]byte, error) {
	if c.Debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] %s %v (in %s)\n", c.Binary, args, c.WorkDir)
	}

	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Dir = c.WorkDir
	cmd.Env = append(os.Environ(), c.Env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	// Combine stdout and stderr for full output
	output := append(stdout.Bytes(), stderr.Bytes()...)

	if c.Debug {
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
		}
		fmt.Fprintf(os.Stderr, "[DEBUG] %s %v completed (exit=%d)\n", c.Binary, args, exitCode)
		if stdout.Len() > 0 {
			fmt.Fprintf(os.Stderr, "[DEBUG] stdout:\n%s\n", stdout.String())
		}
		if stderr.Len() > 0 {
			fmt.Fprintf(os.Stderr, "[DEBUG] stderr:\n%s\n", stderr.String())
		}
	}

	return output, err
}

// WorkDir returns the absolute working directory.
func (c *CLI) AbsWorkDir() (string, error) {
	return filepath.Abs(c.WorkDir)
}

// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until we find the terminating letter
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++ // skip the terminating letter
			}
			i = j
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

// parseErrorResourceAddresses extracts resource addresses from terraform plan error output.
// Terraform formats errors with lines like:
//
//	│ Error: reading S3 Object ...
//	│
//	│   with module.foo.aws_s3_object.bar[0],
//
// Only addresses within Error blocks are returned; Warning blocks are skipped.
// The output may contain ANSI color codes and box-drawing characters.
func parseErrorResourceAddresses(output string) []string {
	// Strip ANSI escape sequences first
	clean := stripANSI(output)
	var addresses []string
	seen := make(map[string]bool)
	inErrorBlock := false
	for _, line := range strings.Split(clean, "\n") {
		trimmed := strings.TrimSpace(line)
		// Strip box-drawing prefixes
		stripped := strings.TrimPrefix(trimmed, "│")
		stripped = strings.TrimPrefix(stripped, "|")
		stripped = strings.TrimSpace(stripped)
		// Track whether we're in an Error or Warning block
		if strings.HasPrefix(stripped, "Error:") {
			inErrorBlock = true
		} else if strings.HasPrefix(stripped, "Warning:") {
			inErrorBlock = false
		} else if trimmed == "╷" {
			// New diagnostic block — reset state until we see Error/Warning
			inErrorBlock = false
		}
		if inErrorBlock && strings.HasPrefix(stripped, "with ") && strings.HasSuffix(stripped, ",") {
			addr := strings.TrimPrefix(stripped, "with ")
			addr = strings.TrimSuffix(addr, ",")
			addr = strings.TrimSpace(addr)
			if addr != "" && !seen[addr] {
				seen[addr] = true
				addresses = append(addresses, addr)
			}
		}
	}
	return addresses
}

// allAddressesIgnored returns true if every address in addrs is present in the ignoreList.
func allAddressesIgnored(addrs []string, ignoreList []string) bool {
	ignoreSet := make(map[string]bool, len(ignoreList))
	for _, a := range ignoreList {
		if a == "*" {
			return true
		}
		ignoreSet[a] = true
	}
	for _, a := range addrs {
		if !ignoreSet[a] {
			return false
		}
	}
	return true
}

// hasWildcardIgnore returns true if the ignore list contains "*".
func hasWildcardIgnore(ignoreList []string) bool {
	for _, a := range ignoreList {
		if a == "*" {
			return true
		}
	}
	return false
}
