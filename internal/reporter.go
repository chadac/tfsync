package internal

import (
	"fmt"
	"io"
	"os"

	"github.com/fatih/color"
)

// Reporter handles formatted output.
type Reporter struct {
	out     io.Writer
	verbose bool
}

// NewReporter creates a new reporter.
func NewReporter(verbose bool) *Reporter {
	return &Reporter{
		out:     os.Stdout,
		verbose: verbose,
	}
}

// NewWithWriter creates a reporter with a custom writer.
func NewWithWriter(w io.Writer, verbose bool) *Reporter {
	return &Reporter{
		out:     w,
		verbose: verbose,
	}
}

// Step prints a step in the process.
func (r *Reporter) Step(format string, args ...interface{}) {
	fmt.Fprintf(r.out, "→ "+format+"\n", args...)
}

// Success prints a success message.
func (r *Reporter) Success(format string, args ...interface{}) {
	green := color.New(color.FgGreen).SprintFunc()
	fmt.Fprintf(r.out, green("✓")+" "+format+"\n", args...)
}

// Error prints an error message.
func (r *Reporter) Error(format string, args ...interface{}) {
	red := color.New(color.FgRed).SprintFunc()
	fmt.Fprintf(r.out, red("✗")+" "+format+"\n", args...)
}

// Warning prints a warning message.
func (r *Reporter) Warning(format string, args ...interface{}) {
	yellow := color.New(color.FgYellow).SprintFunc()
	fmt.Fprintf(r.out, yellow("!")+" "+format+"\n", args...)
}

// Info prints an info message (only in verbose mode).
func (r *Reporter) Info(format string, args ...interface{}) {
	if r.verbose {
		fmt.Fprintf(r.out, "  "+format+"\n", args...)
	}
}

// Detail prints detailed info (always shown but indented).
func (r *Reporter) Detail(format string, args ...interface{}) {
	fmt.Fprintf(r.out, "  "+format+"\n", args...)
}

// Newline prints an empty line.
func (r *Reporter) Newline() {
	fmt.Fprintln(r.out)
}

// Header prints a section header.
func (r *Reporter) Header(title string) {
	bold := color.New(color.Bold).SprintFunc()
	fmt.Fprintf(r.out, "\n%s\n", bold(title))
	fmt.Fprintln(r.out, "─────────────────────────────────────────")
}

// PlanResult represents the outcome of a plan check.
type PlanResult struct {
	Workspace  string
	HasChanges bool
	Output     string
	Error      error
}

// ReportPlanResults reports the results of plan checks.
func (r *Reporter) ReportPlanResults(results []PlanResult) bool {
	allPassed := true

	for _, res := range results {
		prefix := ""
		if res.Workspace != "" && res.Workspace != "default" {
			prefix = fmt.Sprintf("[%s] ", res.Workspace)
		}

		if res.Error != nil {
			r.Error("%sPlan failed: %v", prefix, res.Error)
			allPassed = false
		} else if res.HasChanges {
			r.Error("%sPlan shows changes - migration incomplete", prefix)
			if r.verbose && res.Output != "" {
				r.Detail("Plan output:\n%s", res.Output)
			}
			allPassed = false
		} else {
			r.Success("%sNo changes detected - migration successful", prefix)
		}
	}

	return allPassed
}

// SummaryResult contains the overall sync result.
type SummaryResult struct {
	SourceWorkspaces int
	TargetWorkspaces int
	MovesExecuted    int
	ScriptRan        bool
	Success          bool
}

// ReportSummary prints the final summary.
func (r *Reporter) ReportSummary(result SummaryResult) {
	r.Header("Summary")

	r.Detail("Source workspaces: %d", result.SourceWorkspaces)
	r.Detail("Target workspaces: %d", result.TargetWorkspaces)
	r.Detail("State moves executed: %d", result.MovesExecuted)
	if result.ScriptRan {
		r.Detail("Migration script: ran")
	}

	r.Newline()
	if result.Success {
		r.Success("Migration validation PASSED")
	} else {
		r.Error("Migration validation FAILED")
	}
}

// DryRunActions prints what would be done in a dry run.
func (r *Reporter) DryRunActions(actions []string) {
	r.Header("Dry Run - Actions that would be taken")

	if len(actions) == 0 {
		r.Detail("No actions configured")
		return
	}

	for i, action := range actions {
		r.Detail("%d. %s", i+1, action)
	}
}
