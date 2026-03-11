package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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

	_, err := c.run(ctx, args...)
	return err
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

// PlanCheckResult contains the results of a plan check.
type PlanCheckResult struct {
	// HasChanges is true if the plan detected any resource changes.
	HasChanges bool
	// Output is the human-readable plan output.
	Output string
	// ChangedAddresses is the list of resource addresses that have changes.
	// Only populated when HasChanges is true.
	ChangedAddresses []string
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
				// Get JSON plan to extract changed addresses
				addresses, planErr := c.planChangedAddresses(ctx)
				if planErr != nil {
					// Fall back to reporting changes without addresses
					return &PlanCheckResult{
						HasChanges: true,
						Output:     string(output),
					}, nil
				}
				return &PlanCheckResult{
					HasChanges:       true,
					Output:           string(output),
					ChangedAddresses: addresses,
				}, nil
			}
			// Exit code 1 means error - include output in error message
			return nil, fmt.Errorf("plan failed (exit code %d):\n%s", exitErr.ExitCode(), string(output))
		}
		return nil, err
	}

	// Exit code 0 means no changes
	return &PlanCheckResult{
		HasChanges: false,
		Output:     string(output),
	}, nil
}

// planChangedAddresses runs a JSON plan and extracts addresses of changed resources.
func (c *CLI) planChangedAddresses(ctx context.Context) ([]string, error) {
	plan, err := c.Plan(ctx)
	if err != nil {
		return nil, err
	}

	var addresses []string
	if plan.ResourceChanges != nil {
		for _, rc := range plan.ResourceChanges {
			if rc.Change != nil && len(rc.Change.Actions) > 0 {
				// Skip no-op changes
				isNoop := len(rc.Change.Actions) == 1 && rc.Change.Actions[0] == tfjson.ActionNoop
				if !isNoop {
					addresses = append(addresses, rc.Address)
				}
			}
		}
	}

	return addresses, nil
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

	// Apply PlanConfig settings
	if c.PlanConfig != nil {
		if c.PlanConfig.Lock != nil {
			args = append(args, fmt.Sprintf("-lock=%t", *c.PlanConfig.Lock))
		}
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
