package internal

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
)

// Reporter handles formatted output.
type Reporter struct {
	out     io.Writer
	verbose bool
	isTTY   bool
}

// NewReporter creates a new reporter.
func NewReporter(verbose bool) *Reporter {
	// Check if stdout is a TTY for interactive features
	isTTY := false
	stat, err := os.Stdout.Stat()
	if err == nil {
		isTTY = (stat.Mode() & os.ModeCharDevice) != 0
	}
	return &Reporter{
		out:     os.Stdout,
		verbose: verbose,
		isTTY:   isTTY,
	}
}

// NewWithWriter creates a reporter with a custom writer.
func NewWithWriter(w io.Writer, verbose bool) *Reporter {
	return &Reporter{
		out:     w,
		verbose: verbose,
		isTTY:   false,
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
	Workspace        string
	HasChanges       bool
	Output           string
	ChangedAddresses []string // Resource addresses that have changes
	IgnoredAddresses []string // Resource addresses that were ignored via config
	IgnoredErrors    []string // Resource addresses whose errors were ignored via config
	Error            error
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
			if res.Output != "" {
				r.Newline()
				fmt.Fprintln(r.out, res.Output)
			}
			allPassed = false
		} else {
			var notes []string
			if len(res.IgnoredAddresses) > 0 {
				notes = append(notes, fmt.Sprintf("ignored %d changed addresses", len(res.IgnoredAddresses)))
			}
			if len(res.IgnoredErrors) > 0 {
				notes = append(notes, fmt.Sprintf("ignored %d errored addresses", len(res.IgnoredErrors)))
			}
			if len(notes) > 0 {
				r.Success("%sNo unexpected changes (%s)", prefix, strings.Join(notes, ", "))
				if r.verbose {
					for _, addr := range res.IgnoredAddresses {
						r.Detail("  ignored change: %s", addr)
					}
					for _, addr := range res.IgnoredErrors {
						r.Detail("  ignored error: %s", addr)
					}
				}
			} else {
				r.Success("%sNo changes detected - migration successful", prefix)
			}
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

// ProgressBar provides a simple progress bar for parallel operations.
type ProgressBar struct {
	reporter   *Reporter
	total      int
	completed  int
	mu         sync.Mutex
	tasks      map[string]string // task name -> current status
	startTime  time.Time
	lastRender time.Time
}

// NewProgressBar creates a new progress bar.
func (r *Reporter) NewProgressBar(total int) *ProgressBar {
	return &ProgressBar{
		reporter:  r,
		total:     total,
		tasks:     make(map[string]string),
		startTime: time.Now(),
	}
}

// Start marks a task as started with an initial status.
func (p *ProgressBar) Start(taskName, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tasks[taskName] = status
	p.render()
}

// Update updates the status of a task.
func (p *ProgressBar) Update(taskName, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tasks[taskName] = status
	p.render()
}

// Complete marks a task as completed.
func (p *ProgressBar) Complete(taskName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.tasks, taskName)
	p.completed++
	p.render()
}

// Finish clears the progress bar and prints a final message.
func (p *ProgressBar) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reporter.isTTY {
		// Clear the progress line
		fmt.Fprintf(p.reporter.out, "\r%s\r", strings.Repeat(" ", 80))
	}
}

// render draws the current progress state.
func (p *ProgressBar) render() {
	// Throttle rendering to avoid flickering
	if time.Since(p.lastRender) < 50*time.Millisecond {
		return
	}
	p.lastRender = time.Now()

	if !p.reporter.isTTY {
		// Non-TTY: just show status updates
		return
	}

	// Build progress bar
	barWidth := 20
	filled := 0
	if p.total > 0 {
		filled = (p.completed * barWidth) / p.total
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	// Build status line showing active tasks
	var statusParts []string
	for name, status := range p.tasks {
		statusParts = append(statusParts, fmt.Sprintf("%s: %s", name, status))
	}
	statusLine := strings.Join(statusParts, " | ")

	// Truncate if too long
	maxStatus := 50
	if len(statusLine) > maxStatus {
		statusLine = statusLine[:maxStatus-3] + "..."
	}

	// Print progress
	cyan := color.New(color.FgCyan).SprintFunc()
	fmt.Fprintf(p.reporter.out, "\r%s [%s] %d/%d %s",
		cyan("⟳"),
		bar,
		p.completed,
		p.total,
		statusLine,
	)
}

// TaskUpdate represents an update from a parallel task.
type TaskUpdate struct {
	TaskName string
	Status   string
	Done     bool
	Error    error
}

// ParallelProgress tracks progress of multiple parallel operations.
type ParallelProgress struct {
	reporter *Reporter
	tasks    []string
	statuses map[string]string
	done     map[string]bool
	errors   map[string]error
	mu       sync.Mutex
}

// NewParallelProgress creates a new parallel progress tracker.
func (r *Reporter) NewParallelProgress(tasks []string) *ParallelProgress {
	statuses := make(map[string]string)
	for _, t := range tasks {
		statuses[t] = "pending"
	}
	return &ParallelProgress{
		reporter: r,
		tasks:    tasks,
		statuses: statuses,
		done:     make(map[string]bool),
		errors:   make(map[string]error),
	}
}

// Update updates the status of a task.
func (p *ParallelProgress) Update(taskName, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses[taskName] = status
	p.render()
}

// Complete marks a task as done.
func (p *ParallelProgress) Complete(taskName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done[taskName] = true
	p.statuses[taskName] = "done"
	p.render()
}

// Fail marks a task as failed.
func (p *ParallelProgress) Fail(taskName string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done[taskName] = true
	p.errors[taskName] = err
	p.statuses[taskName] = "failed"
	p.render()
}

// render shows current status.
func (p *ParallelProgress) render() {
	if !p.reporter.isTTY {
		return
	}

	// Count completed
	completed := 0
	for _, d := range p.done {
		if d {
			completed++
		}
	}

	// Build progress bar
	barWidth := 20
	filled := 0
	if len(p.tasks) > 0 {
		filled = (completed * barWidth) / len(p.tasks)
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	// Build active task list
	var active []string
	for _, t := range p.tasks {
		if !p.done[t] {
			active = append(active, fmt.Sprintf("%s: %s", t, p.statuses[t]))
		}
	}
	statusLine := strings.Join(active, " | ")
	if len(statusLine) > 60 {
		statusLine = statusLine[:57] + "..."
	}

	cyan := color.New(color.FgCyan).SprintFunc()
	fmt.Fprintf(p.reporter.out, "\r%s [%s] %d/%d %s    ",
		cyan("⟳"),
		bar,
		completed,
		len(p.tasks),
		statusLine,
	)
}

// Finish clears the progress display.
func (p *ParallelProgress) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reporter.isTTY {
		fmt.Fprintf(p.reporter.out, "\r%s\r", strings.Repeat(" ", 100))
	}
}

// HasErrors returns true if any tasks failed.
func (p *ParallelProgress) HasErrors() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.errors) > 0
}

// Errors returns all task errors.
func (p *ParallelProgress) Errors() map[string]error {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]error)
	for k, v := range p.errors {
		result[k] = v
	}
	return result
}
