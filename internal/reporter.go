package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
)

// DiffReport is the JSON-serializable summary of all plan results.
type DiffReport struct {
	Success    bool                     `json:"success"`
	Workspaces map[string]*WorkspaceDiff `json:"workspaces"`
}

// WorkspaceDiff summarizes plan results for a single workspace.
type WorkspaceDiff struct {
	Status           string         `json:"status"` // "ok", "changes", "error"
	Error            string         `json:"error,omitempty"`
	ResourceDiffs    []ResourceDiff `json:"resource_diffs,omitempty"`
	IgnoredAddresses []string       `json:"ignored_addresses,omitempty"`
	IgnoredErrors    []string       `json:"ignored_errors,omitempty"`
	Summary          DiffCounts     `json:"summary"`
	// UpstreamDiffs lists resource changes detected in the source workspace plan.
	// These represent pre-existing drift that may explain target plan diffs.
	UpstreamDiffs []ResourceDiff `json:"upstream_diffs,omitempty"`
}

// DiffCounts holds per-action counts.
type DiffCounts struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
	Read    int `json:"read"`
}

// BuildDiffReport creates a DiffReport from plan results.
func BuildDiffReport(results []PlanResult, allPassed bool) *DiffReport {
	report := &DiffReport{
		Success:    allPassed,
		Workspaces: make(map[string]*WorkspaceDiff),
	}

	for _, res := range results {
		wsName := res.Workspace
		if wsName == "" {
			wsName = "default"
		}

		// Build upstream lookup: address -> action for cross-referencing
		upstreamByAddr := make(map[string]string)
		for _, ud := range res.UpstreamDiffs {
			upstreamByAddr[ud.Address] = ud.Action
		}

		// Annotate resource diffs with upstream info
		annotatedDiffs := make([]ResourceDiff, len(res.ResourceDiffs))
		copy(annotatedDiffs, res.ResourceDiffs)
		for i, d := range annotatedDiffs {
			if upAction, ok := upstreamByAddr[d.Address]; ok {
				annotatedDiffs[i].UpstreamChanged = true
				annotatedDiffs[i].UpstreamAction = upAction
			}
		}

		wd := &WorkspaceDiff{
			ResourceDiffs:    annotatedDiffs,
			IgnoredAddresses: res.IgnoredAddresses,
			IgnoredErrors:    res.IgnoredErrors,
			UpstreamDiffs:    res.UpstreamDiffs,
		}

		if res.Error != nil {
			wd.Status = "error"
			wd.Error = res.Error.Error()
		} else if res.HasChanges {
			wd.Status = "changes"
		} else {
			wd.Status = "ok"
		}

		// Compute counts
		for _, d := range annotatedDiffs {
			switch d.Action {
			case "create":
				wd.Summary.Create++
			case "update":
				wd.Summary.Update++
			case "replace":
				wd.Summary.Replace++
			case "delete":
				wd.Summary.Delete++
			case "read":
				wd.Summary.Read++
			}
		}

		report.Workspaces[wsName] = wd
	}

	return report
}

// WriteDiffReport writes the diff report as JSON to .tfsync/diff.json.
func WriteDiffReport(report *DiffReport) error {
	if err := os.MkdirAll(OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal diff report: %w", err)
	}
	path := filepath.Join(OutputDir, "diff.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write diff report: %w", err)
	}
	return nil
}

// OutputDir is the base directory for all tfsync output files.
const OutputDir = ".tfsync"

// CleanOutputDir removes stale plan output files from a previous run.
// Preserves diff.json and warnings.json since those are useful to inspect
// between runs and get overwritten naturally.
func CleanOutputDir() error {
	for _, subdir := range []string{"source", "target"} {
		dir := filepath.Join(OutputDir, subdir)
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to clean %s: %w", dir, err)
		}
	}
	return nil
}

// WritePlanOutputs writes raw plan output for each workspace to .tfsync/.
// sourceOutputs maps source workspace name -> plan text output.
// targetOutputs maps target workspace name -> plan text output.
func WritePlanOutputs(sourceOutputs map[string]string, targetOutputs map[string]string) error {
	for subdir, outputs := range map[string]map[string]string{
		"source": sourceOutputs,
		"target": targetOutputs,
	} {
		if len(outputs) == 0 {
			continue
		}
		outDir := filepath.Join(OutputDir, subdir)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("failed to create output dir %s: %w", outDir, err)
		}
		for name, output := range outputs {
			if output == "" {
				continue
			}
			outPath := filepath.Join(outDir, name+".plan.txt")
			if err := os.WriteFile(outPath, []byte(output), 0644); err != nil {
				return fmt.Errorf("failed to write plan output for %s: %w", name, err)
			}
		}
	}
	return nil
}

// Warning represents a collected warning for JSON output.
type Warning struct {
	Workspace string `json:"workspace,omitempty"`
	Message   string `json:"message"`
}

// WriteWarnings writes collected warnings to .tfsync/warnings.json.
func WriteWarnings(warnings []Warning) error {
	if len(warnings) == 0 {
		return nil
	}
	if err := os.MkdirAll(OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}
	data, err := json.MarshalIndent(warnings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal warnings: %w", err)
	}
	path := filepath.Join(OutputDir, "warnings.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write warnings: %w", err)
	}
	return nil
}

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
	ChangedAddresses []string       // Resource addresses that have changes
	ResourceDiffs    []ResourceDiff // Structured per-resource changes
	IgnoredAddresses []string       // Resource addresses that were ignored via config
	IgnoredErrors    []string       // Resource addresses whose errors were ignored via config
	Error            error
	UpstreamDiffs    []ResourceDiff // Resource changes detected in the source workspace plan
}

// ReportPlanResults reports the results of plan checks.
func (r *Reporter) ReportPlanResults(results []PlanResult) bool {
	allPassed := true

	for _, res := range results {
		prefix := ""
		if res.Workspace != "" && res.Workspace != "default" {
			prefix = fmt.Sprintf("[%s] ", res.Workspace)
		}

		wsFile := res.Workspace
		if wsFile == "" || wsFile == "default" {
			wsFile = "default"
		}
		planPath := filepath.Join(OutputDir, "target", wsFile+".plan.txt")

		if res.Error != nil {
			r.Error("%sPlan failed: %v", prefix, res.Error)
			r.Detail("  Full output: %s", planPath)
			allPassed = false
		} else if res.HasChanges {
			r.Error("%sPlan shows changes - migration incomplete", prefix)
			if len(res.ResourceDiffs) > 0 {
				r.reportResourceDiffs(prefix, res.ResourceDiffs)
			}
			r.Detail("  Full output: %s", planPath)
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

// reportResourceDiffs prints a structured summary of resource changes.
func (r *Reporter) reportResourceDiffs(prefix string, diffs []ResourceDiff) {
	// Build upstream lookup for annotations
	upstreamInfo := make(map[string]string) // address -> upstream action
	for _, d := range diffs {
		if d.UpstreamChanged {
			upstreamInfo[d.Address] = d.UpstreamAction
		}
	}

	// Group by action
	groups := map[string][]string{}
	for _, d := range diffs {
		groups[d.Action] = append(groups[d.Action], d.Address)
	}

	// Print counts
	actionOrder := []string{"create", "update", "replace", "delete", "read"}
	actionSymbols := map[string]string{
		"create":  "+",
		"update":  "~",
		"replace": "-/+",
		"delete":  "-",
		"read":    "<=",
	}
	for _, action := range actionOrder {
		addrs := groups[action]
		if len(addrs) == 0 {
			continue
		}
		sym := actionSymbols[action]
		if sym == "" {
			sym = "?"
		}
		sort.Strings(addrs)
		for _, addr := range addrs {
			suffix := ""
			if upAction, ok := upstreamInfo[addr]; ok {
				yellow := color.New(color.FgYellow).SprintFunc()
				suffix = yellow(fmt.Sprintf(" (also %s upstream)", upAction))
			}
			r.Detail("  %s%s %s %s%s", prefix, sym, action, addr, suffix)
		}
	}
	// Handle any unknown actions
	for action, addrs := range groups {
		known := false
		for _, a := range actionOrder {
			if a == action {
				known = true
				break
			}
		}
		if !known {
			sort.Strings(addrs)
			for _, addr := range addrs {
				suffix := ""
				if upAction, ok := upstreamInfo[addr]; ok {
					yellow := color.New(color.FgYellow).SprintFunc()
					suffix = yellow(fmt.Sprintf(" (also %s upstream)", upAction))
				}
				r.Detail("  %s? %s %s%s", prefix, action, addr, suffix)
			}
		}
	}
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

	r.Newline()
	r.Detail("Output files:")
	r.Detail("  %s/diff.json          Structured diff report", OutputDir)
	r.Detail("  %s/warnings.json      Warnings from this run", OutputDir)
	r.Detail("  %s/source/<name>.plan.txt Source workspace plan outputs", OutputDir)
	r.Detail("  %s/target/<name>.plan.txt Target workspace plan outputs", OutputDir)
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
