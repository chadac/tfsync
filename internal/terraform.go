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

// PlanHasChanges runs terraform plan and returns whether there are changes.
func (c *CLI) PlanHasChanges(ctx context.Context) (bool, string, error) {
	// Run plan with -detailed-exitcode
	args := c.planArgs("")
	args = append(args, "-detailed-exitcode")

	output, err := c.runWithExitCode(ctx, args...)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Exit code 2 means changes detected
			if exitErr.ExitCode() == 2 {
				return true, string(output), nil
			}
		}
		return false, string(output), err
	}

	// Exit code 0 means no changes
	return false, string(output), nil
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
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Dir = c.WorkDir
	cmd.Env = append(os.Environ(), c.Env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %v failed: %w\nstderr: %s", c.Binary, args, err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// runWithExitCode executes a terraform command, returning output even on non-zero exit.
func (c *CLI) runWithExitCode(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Dir = c.WorkDir
	cmd.Env = append(os.Environ(), c.Env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	// Combine stdout and stderr for full output
	output := append(stdout.Bytes(), stderr.Bytes()...)
	return output, err
}

// WorkDir returns the absolute working directory.
func (c *CLI) AbsWorkDir() (string, error) {
	return filepath.Abs(c.WorkDir)
}
