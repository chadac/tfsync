package internal

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// Suggester generates migration suggestions.
type Suggester struct {
	binary string
}

// NewSuggester creates a new suggester.
func NewSuggester(binary string) *Suggester {
	if binary == "" {
		binary = "tofu"
	}
	return &Suggester{binary: binary}
}

// Suggestion represents a suggested state move.
type Suggestion struct {
	From            string
	To              string
	TargetWorkspace string
	Confidence      float64 // 0.0 to 1.0
	Reason          string
}

// SuggestResult contains the suggestion results.
type SuggestResult struct {
	Suggestions []Suggestion
	// Unmapped resources in source that couldn't be matched
	UnmappedSource []string
	// Unmapped resources in target that have no source match
	UnmappedTarget []string
}

// Suggest generates migration suggestions by comparing source and target resources.
func (s *Suggester) Suggest(ctx context.Context, cfg *Config) (*SuggestResult, error) {
	result := &SuggestResult{}

	// Get source resources
	sourceResources, err := s.getResources(ctx, cfg.Source.GetWorkspaces())
	if err != nil {
		return nil, fmt.Errorf("failed to get source resources: %w", err)
	}

	// Get target resources (from terraform config, not state)
	targetResources, err := s.getPlannedResources(ctx, cfg.Target.GetWorkspaces())
	if err != nil {
		return nil, fmt.Errorf("failed to get target resources: %w", err)
	}

	// Build suggestions
	usedSource := make(map[string]bool)
	usedTarget := make(map[string]bool)

	// First pass: exact type+name matches (highest confidence)
	for srcAddr, srcInfo := range sourceResources {
		for tgtAddr, tgtInfo := range targetResources {
			if usedTarget[tgtAddr] {
				continue
			}

			if srcInfo.Type == tgtInfo.Type && srcInfo.Name == tgtInfo.Name {
				result.Suggestions = append(result.Suggestions, Suggestion{
					From:            srcAddr,
					To:              tgtAddr,
					TargetWorkspace: tgtInfo.Workspace,
					Confidence:      1.0,
					Reason:          "exact type and name match",
				})
				usedSource[srcAddr] = true
				usedTarget[tgtAddr] = true
				break
			}
		}
	}

	// Second pass: same type, similar name (medium confidence)
	for srcAddr, srcInfo := range sourceResources {
		if usedSource[srcAddr] {
			continue
		}

		var bestMatch *Suggestion
		var bestScore float64

		for tgtAddr, tgtInfo := range targetResources {
			if usedTarget[tgtAddr] {
				continue
			}

			if srcInfo.Type != tgtInfo.Type {
				continue
			}

			// Calculate name similarity
			score := similarity(srcInfo.Name, tgtInfo.Name)
			if score > bestScore && score > 0.5 {
				bestScore = score
				bestMatch = &Suggestion{
					From:            srcAddr,
					To:              tgtAddr,
					TargetWorkspace: tgtInfo.Workspace,
					Confidence:      score * 0.8, // Max 0.8 for non-exact matches
					Reason:          fmt.Sprintf("same type, name similarity %.0f%%", score*100),
				}
			}
		}

		if bestMatch != nil {
			result.Suggestions = append(result.Suggestions, *bestMatch)
			usedSource[srcAddr] = true
			usedTarget[bestMatch.To] = true
		}
	}

	// Third pass: same type only (low confidence)
	for srcAddr, srcInfo := range sourceResources {
		if usedSource[srcAddr] {
			continue
		}

		for tgtAddr, tgtInfo := range targetResources {
			if usedTarget[tgtAddr] {
				continue
			}

			if srcInfo.Type == tgtInfo.Type {
				result.Suggestions = append(result.Suggestions, Suggestion{
					From:            srcAddr,
					To:              tgtAddr,
					TargetWorkspace: tgtInfo.Workspace,
					Confidence:      0.3,
					Reason:          "same resource type only",
				})
				usedSource[srcAddr] = true
				usedTarget[tgtAddr] = true
				break
			}
		}
	}

	// Collect unmapped resources
	for addr := range sourceResources {
		if !usedSource[addr] {
			result.UnmappedSource = append(result.UnmappedSource, addr)
		}
	}
	for addr := range targetResources {
		if !usedTarget[addr] {
			result.UnmappedTarget = append(result.UnmappedTarget, addr)
		}
	}

	// Sort suggestions by confidence (highest first)
	sort.Slice(result.Suggestions, func(i, j int) bool {
		return result.Suggestions[i].Confidence > result.Suggestions[j].Confidence
	})

	sort.Strings(result.UnmappedSource)
	sort.Strings(result.UnmappedTarget)

	return result, nil
}

// resourceInfo holds parsed resource information.
type resourceInfo struct {
	Type      string
	Name      string
	Module    string
	Workspace string
}

// getResources gets resources from terraform state.
func (s *Suggester) getResources(ctx context.Context, workspaces map[string]Workspace) (map[string]resourceInfo, error) {
	resources := make(map[string]resourceInfo)

	for wsName, ws := range workspaces {
		cli := NewCLI(s.binary, ws.Path)
		if err := cli.Init(ctx); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", wsName, err)
		}

		addrs, err := cli.StateList(ctx)
		if err != nil {
			// No state is ok - might be empty
			continue
		}

		for _, addr := range addrs {
			info := parseResourceAddress(addr)
			info.Workspace = wsName
			resources[addr] = info
		}
	}

	return resources, nil
}

// getPlannedResources gets resources from terraform plan (what the config defines).
func (s *Suggester) getPlannedResources(ctx context.Context, workspaces map[string]Workspace) (map[string]resourceInfo, error) {
	resources := make(map[string]resourceInfo)

	for wsName, ws := range workspaces {
		cli := NewCLI(s.binary, ws.Path)
		if err := cli.Init(ctx); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", wsName, err)
		}

		plan, err := cli.Plan(ctx)
		if err != nil {
			return nil, fmt.Errorf("workspace %s: %w", wsName, err)
		}

		if plan.PlannedValues != nil && plan.PlannedValues.RootModule != nil {
			extractResourcesFromStateModule(plan.PlannedValues.RootModule, "", wsName, resources)
		}
	}

	return resources, nil
}

// extractResourcesFromStateModule recursively extracts resources from a tfjson.StateModule.
func extractResourcesFromStateModule(module *tfjson.StateModule, prefix, workspace string, resources map[string]resourceInfo) {
	// Extract resources from this module
	for _, res := range module.Resources {
		addr := res.Address
		info := parseResourceAddress(addr)
		info.Workspace = workspace
		resources[addr] = info
	}

	// Recursively process child modules
	for _, child := range module.ChildModules {
		extractResourcesFromStateModule(child, prefix, workspace, resources)
	}
}

// parseResourceAddress parses a terraform resource address into components.
func parseResourceAddress(addr string) resourceInfo {
	info := resourceInfo{}

	// Handle module prefix
	if strings.HasPrefix(addr, "module.") {
		parts := strings.SplitN(addr, ".", 3)
		if len(parts) >= 3 {
			info.Module = parts[1]
			addr = parts[2]
		}
	}

	// Parse type.name
	parts := strings.SplitN(addr, ".", 2)
	if len(parts) == 2 {
		info.Type = parts[0]
		// Handle indexed resources like name[0]
		name := parts[1]
		if idx := strings.Index(name, "["); idx != -1 {
			name = name[:idx]
		}
		info.Name = name
	}

	return info
}

// similarity calculates the similarity between two strings (0.0 to 1.0).
func similarity(a, b string) float64 {
	if a == b {
		return 1.0
	}

	a = strings.ToLower(a)
	b = strings.ToLower(b)

	if a == b {
		return 0.95
	}

	// Check if one contains the other
	if strings.Contains(a, b) || strings.Contains(b, a) {
		shorter := len(a)
		if len(b) < shorter {
			shorter = len(b)
		}
		longer := len(a)
		if len(b) > longer {
			longer = len(b)
		}
		return float64(shorter) / float64(longer)
	}

	// Levenshtein distance based similarity
	dist := levenshtein(a, b)
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 1.0
	}

	return 1.0 - float64(dist)/float64(maxLen)
}

// levenshtein calculates the Levenshtein distance between two strings.
func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	matrix := make([][]int, len(a)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(b)+1)
		matrix[i][0] = i
	}
	for j := range matrix[0] {
		matrix[0][j] = j
	}

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			matrix[i][j] = min(
				matrix[i-1][j]+1,
				matrix[i][j-1]+1,
				matrix[i-1][j-1]+cost,
			)
		}
	}

	return matrix[len(a)][len(b)]
}

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// GenerateYAML generates YAML configuration from suggestions.
func (r *SuggestResult) GenerateYAML() string {
	var sb strings.Builder

	sb.WriteString("migration:\n")
	sb.WriteString("  moves:\n")

	for _, s := range r.Suggestions {
		sb.WriteString(fmt.Sprintf("    - from: %q\n", s.From))
		sb.WriteString(fmt.Sprintf("      to: %q\n", s.To))
		if s.TargetWorkspace != "" && s.TargetWorkspace != "default" {
			sb.WriteString(fmt.Sprintf("      target_workspace: %s\n", s.TargetWorkspace))
		}
		sb.WriteString(fmt.Sprintf("      # confidence: %.0f%% - %s\n", s.Confidence*100, s.Reason))
	}

	return sb.String()
}
