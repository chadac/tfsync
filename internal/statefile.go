package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TerraformState represents the structure of a terraform.tfstate file.
// This is a simplified representation that preserves unknown fields.
type TerraformState struct {
	Version          int                      `json:"version"`
	TerraformVersion string                   `json:"terraform_version"`
	Serial           int64                    `json:"serial"`
	Lineage          string                   `json:"lineage"`
	Outputs          map[string]interface{}   `json:"outputs"`
	Resources        []StateResource          `json:"resources"`
	CheckResults     interface{}              `json:"check_results,omitempty"`
}

// StateResource represents a single resource in terraform state.
type StateResource struct {
	Mode         string                   `json:"mode"`
	Type         string                   `json:"type"`
	Name         string                   `json:"name"`
	Provider     string                   `json:"provider"`
	Module       string                   `json:"module,omitempty"`
	Instances    []StateResourceInstance  `json:"instances"`
}

// StateResourceInstance represents a single instance of a resource.
type StateResourceInstance struct {
	SchemaVersion         int                    `json:"schema_version"`
	Attributes            map[string]interface{} `json:"attributes,omitempty"`
	AttributesFlat        map[string]string      `json:"attributes_flat,omitempty"`
	SensitiveAttributes   []interface{}          `json:"sensitive_attributes,omitempty"`
	Private               string                 `json:"private,omitempty"`
	Dependencies          []string               `json:"dependencies,omitempty"`
	CreateBeforeDestroy   bool                   `json:"create_before_destroy,omitempty"`
	IndexKey              interface{}            `json:"index_key,omitempty"`
	IdentitySchemaVersion int                    `json:"identity_schema_version,omitempty"`
}

// Address returns the full resource address for this resource.
func (r *StateResource) Address() string {
	var parts []string
	if r.Module != "" {
		parts = append(parts, r.Module)
	}

	prefix := ""
	if r.Mode == "data" {
		prefix = "data."
	}

	parts = append(parts, fmt.Sprintf("%s%s.%s", prefix, r.Type, r.Name))
	return strings.Join(parts, ".")
}

// StateFile provides operations on terraform state files.
type StateFile struct {
	state *TerraformState
	path  string
}

// LoadStateFile loads a terraform state file from disk.
// Returns the underlying error (unwrapped) if the file doesn't exist,
// so callers can use os.IsNotExist() or errors.Is().
func LoadStateFile(path string) (*StateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err // Return unwrapped for easy checking
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	return LoadStateFromBytes(data, path)
}

// LoadStateFromBytes loads terraform state from raw JSON bytes.
func LoadStateFromBytes(data []byte, path string) (*StateFile, error) {
	var state TerraformState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state JSON: %w", err)
	}

	return &StateFile{
		state: &state,
		path:  path,
	}, nil
}

// Save writes the state back to disk.
func (sf *StateFile) Save() error {
	return sf.SaveTo(sf.path)
}

// SaveTo writes the state to a specific path.
func (sf *StateFile) SaveTo(path string) error {
	// Increment serial number on every save
	sf.state.Serial++

	data, err := json.MarshalIndent(sf.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// ListResources returns all resource addresses in the state.
func (sf *StateFile) ListResources() []string {
	var addrs []string
	for _, r := range sf.state.Resources {
		addrs = append(addrs, r.Address())
	}
	return addrs
}

// RemoveResource removes a resource from the state by address.
// Returns true if the resource was found and removed.
func (sf *StateFile) RemoveResource(addr string) bool {
	for i, r := range sf.state.Resources {
		if r.Address() == addr {
			// Remove by swapping with last element and truncating
			sf.state.Resources[i] = sf.state.Resources[len(sf.state.Resources)-1]
			sf.state.Resources = sf.state.Resources[:len(sf.state.Resources)-1]
			return true
		}
	}
	return false
}

// RemoveResources removes multiple resources from the state.
// Returns the number of resources removed.
func (sf *StateFile) RemoveResources(addrs []string) int {
	if len(addrs) == 0 {
		return 0
	}

	// Build a set of addresses to remove for O(1) lookup
	toRemove := make(map[string]bool, len(addrs))
	for _, addr := range addrs {
		toRemove[addr] = true
	}

	// Filter in place
	removed := 0
	n := 0
	for _, r := range sf.state.Resources {
		if !toRemove[r.Address()] {
			sf.state.Resources[n] = r
			n++
		} else {
			removed++
		}
	}
	sf.state.Resources = sf.state.Resources[:n]

	return removed
}

// KeepOnlyResources keeps only the specified resources, removing all others.
// Returns the number of resources removed.
func (sf *StateFile) KeepOnlyResources(addrs []string) int {
	if len(addrs) == 0 {
		removed := len(sf.state.Resources)
		sf.state.Resources = nil
		return removed
	}

	// Build a set of addresses to keep for O(1) lookup
	toKeep := make(map[string]bool, len(addrs))
	for _, addr := range addrs {
		toKeep[addr] = true
	}

	// Filter in place
	removed := 0
	n := 0
	for _, r := range sf.state.Resources {
		if toKeep[r.Address()] {
			sf.state.Resources[n] = r
			n++
		} else {
			removed++
		}
	}
	sf.state.Resources = sf.state.Resources[:n]

	return removed
}

// MoveResource renames a resource from srcAddr to dstAddr.
// This handles both simple renames and module moves.
// Returns true if the resource was found and moved.
func (sf *StateFile) MoveResource(srcAddr, dstAddr string) bool {
	for i, r := range sf.state.Resources {
		if r.Address() == srcAddr {
			// Parse the destination address
			newModule, newType, newName, isData := parseStateAddress(dstAddr)

			sf.state.Resources[i].Module = newModule
			sf.state.Resources[i].Type = newType
			sf.state.Resources[i].Name = newName
			if isData {
				sf.state.Resources[i].Mode = "data"
			} else {
				sf.state.Resources[i].Mode = "managed"
			}

			return true
		}
	}
	return false
}

// MoveModule renames all resources under srcModule to be under dstModule.
// This is a module-level move that replaces the module prefix for all matching resources.
// Returns the number of resources moved (0 if no resources found under srcModule).
// Example: MoveModule("module.projects[\"foo\"]", "module.foo") would rename
// "module.projects[\"foo\"].aws_instance.bar" to "module.foo.aws_instance.bar"
func (sf *StateFile) MoveModule(srcModule, dstModule string) int {
	moved := 0
	srcPrefix := srcModule + "."
	dstPrefix := ""
	if dstModule != "" {
		dstPrefix = dstModule + "."
	}

	for i, r := range sf.state.Resources {
		// Check if resource is under the source module
		if r.Module == srcModule {
			// Resource is directly in the module - update module path
			sf.state.Resources[i].Module = dstModule
			moved++
		} else if len(r.Module) > len(srcModule) && r.Module[:len(srcPrefix)] == srcPrefix {
			// Resource is in a nested module under srcModule
			// Replace the prefix
			newModule := dstPrefix + r.Module[len(srcPrefix):]
			// Trim trailing dot if dstModule is empty
			if dstModule == "" && len(newModule) > 0 && newModule[len(newModule)-1] == '.' {
				newModule = newModule[:len(newModule)-1]
			}
			sf.state.Resources[i].Module = newModule
			moved++
		}
	}
	return moved
}

// MoveResources performs multiple resource moves.
// Returns the number of successful moves.
func (sf *StateFile) MoveResources(moves map[string]string) int {
	moved := 0
	for src, dst := range moves {
		if sf.MoveResource(src, dst) {
			moved++
		}
	}
	return moved
}

// GetState returns the underlying state for direct access.
func (sf *StateFile) GetState() *TerraformState {
	return sf.state
}

// parseStateAddress parses a terraform resource address into its components.
// Handles indexed modules like module.foo["bar"] and module.foo[0].
// Examples:
//   - "aws_instance.foo" -> ("", "aws_instance", "foo", false)
//   - "data.aws_ami.latest" -> ("", "aws_ami", "latest", true)
//   - "module.vpc.aws_subnet.private" -> ("module.vpc", "aws_subnet", "private", false)
//   - "module.vpc.module.subnets.aws_subnet.main" -> ("module.vpc.module.subnets", "aws_subnet", "main", false)
//   - "module.projects[\"foo\"].aws_instance.bar" -> ("module.projects[\"foo\"]", "aws_instance", "bar", false)
//   - "module.a[0].module.b[\"x\"].aws_instance.c" -> ("module.a[0].module.b[\"x\"]", "aws_instance", "c", false)
func parseStateAddress(addr string) (module, resourceType, name string, isData bool) {
	// Use tokenizeAddress to properly handle brackets
	tokens := tokenizeAddress(addr)

	// Handle data source prefix at the start (e.g., "data.aws_ami.latest")
	if len(tokens) > 0 && tokens[0] == "data" {
		isData = true
		tokens = tokens[1:]
	}

	// Find where the module path ends and resource begins
	// Module segments are pairs: "module", "name[index]?" where the name may include an index
	moduleEndIdx := 0
	for i := 0; i+1 < len(tokens); i += 2 {
		if tokens[i] == "module" {
			moduleEndIdx = i + 2
		} else {
			break
		}
	}

	// Build module path
	if moduleEndIdx > 0 {
		module = strings.Join(tokens[:moduleEndIdx], ".")
		tokens = tokens[moduleEndIdx:]
	}

	// Check for data source prefix after modules (e.g., "module.foo.data.aws_ami.latest")
	if len(tokens) > 0 && tokens[0] == "data" {
		isData = true
		tokens = tokens[1:]
	}

	// Remaining tokens should be type.name
	if len(tokens) >= 2 {
		resourceType = tokens[0]
		name = strings.Join(tokens[1:], ".") // Handle names with dots (rare but possible)
	} else if len(tokens) == 1 {
		resourceType = tokens[0]
	}

	return
}

// IsModuleAddress returns true if addr is a module-only address (no resource type/name).
// Examples:
//   - "module.foo" -> true
//   - "module.foo[\"bar\"]" -> true
//   - "module.a.module.b" -> true
//   - "aws_instance.foo" -> false
//   - "module.foo.aws_instance.bar" -> false
func IsModuleAddress(addr string) bool {
	module, resourceType, _, _ := parseStateAddress(addr)
	// It's a module address if there's a module path but no resource type
	return module != "" && resourceType == ""
}

// resourceUnderModule returns true if the resource address is under the given module prefix.
// This checks if a resource like "module.foo.aws_instance.bar" belongs under "module.foo".
// Examples:
//   - resourceUnderModule("module.foo.aws_instance.bar", "module.foo") -> true
//   - resourceUnderModule("module.foo.module.nested.aws_instance.bar", "module.foo") -> true
//   - resourceUnderModule("module.bar.aws_instance.baz", "module.foo") -> false
//   - resourceUnderModule("aws_instance.bar", "module.foo") -> false
func resourceUnderModule(resourceAddr, modulePrefix string) bool {
	// Parse the resource address to get its module path
	resourceModule, _, _, _ := parseStateAddress(resourceAddr)
	if resourceModule == "" {
		return false // Resource is at root, not under any module
	}

	// Check if the resource's module matches exactly or is nested under the prefix
	if resourceModule == modulePrefix {
		return true
	}

	// Check if it's a nested module under the prefix
	prefixWithDot := modulePrefix + "."
	return len(resourceModule) > len(prefixWithDot) && resourceModule[:len(prefixWithDot)] == prefixWithDot
}

// tokenizeAddress splits a terraform address into tokens, respecting brackets.
// For example: "module.foo[\"bar\"].aws_instance.baz" -> ["module", "foo[\"bar\"]", "aws_instance", "baz"]
func tokenizeAddress(addr string) []string {
	var tokens []string
	var current strings.Builder
	depth := 0 // Track bracket depth

	for _, ch := range addr {
		switch ch {
		case '[':
			depth++
			current.WriteRune(ch)
		case ']':
			depth--
			current.WriteRune(ch)
		case '.':
			if depth == 0 {
				// Only split on dots when not inside brackets
				if current.Len() > 0 {
					tokens = append(tokens, current.String())
					current.Reset()
				}
			} else {
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
	}

	// Don't forget the last token
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens
}

// stripInstanceIndex removes the trailing instance index from a resource address.
// Terraform state list returns instance-level addresses like "aws_instance.foo[0]"
// or "aws_instance.foo[\"key\"]", but the state JSON stores resources at the
// resource level ("aws_instance.foo") with instances as a nested array.
// This function strips the trailing index so we can look up the resource block.
// Examples:
//   - "module.foo.aws_instance.bar[0]" -> "module.foo.aws_instance.bar"
//   - "module.foo.aws_instance.bar[\"key\"]" -> "module.foo.aws_instance.bar"
//   - "module.foo[0].aws_instance.bar[0]" -> "module.foo[0].aws_instance.bar"
//   - "module.foo.aws_instance.bar" -> "module.foo.aws_instance.bar" (unchanged)
func stripInstanceIndex(addr string) string {
	if len(addr) == 0 || addr[len(addr)-1] != ']' {
		return addr
	}
	// Find the matching opening bracket for the trailing ]
	depth := 0
	for i := len(addr) - 1; i >= 0; i-- {
		switch addr[i] {
		case ']':
			depth++
		case '[':
			depth--
			if depth == 0 {
				// Check if this bracket is the resource instance index (at the end)
				// vs a module index (followed by a dot).
				// If what precedes is "module.foo[" pattern (i.e., the bracket is
				// part of a module path), we should NOT strip it.
				// Simple check: if there's nothing after the ']' or it's the end,
				// it's the instance index.
				candidate := addr[:i]
				// Verify: the part before the bracket should end with a resource name
				// (not "module.xxx"), which means it can't be a module index.
				return candidate
			}
		}
	}
	return addr // Malformed, return as-is
}

// BatchStateOperations allows performing multiple state operations efficiently.
type BatchStateOperations struct {
	sf          *StateFile
	removes     map[string]bool
	moves       map[string]string // src -> dst (individual resource moves)
	moduleMoves map[string]string // srcModule -> dstModule (module-level moves)
}

// NewBatchStateOperations creates a new batch operation on a state file.
func NewBatchStateOperations(sf *StateFile) *BatchStateOperations {
	return &BatchStateOperations{
		sf:          sf,
		removes:     make(map[string]bool),
		moves:       make(map[string]string),
		moduleMoves: make(map[string]string),
	}
}

// Remove queues a resource for removal.
func (b *BatchStateOperations) Remove(addr string) {
	b.removes[addr] = true
}

// Move queues a resource move.
func (b *BatchStateOperations) Move(src, dst string) {
	b.moves[src] = dst
}

// MoveModule queues a module-level move (renames all resources under srcModule to dstModule).
func (b *BatchStateOperations) MoveModule(srcModule, dstModule string) {
	b.moduleMoves[srcModule] = dstModule
}

// Apply executes all queued operations.
// Order: module moves first, then individual moves, then removes.
// Returns the number of operations performed and any errors for failed moves.
func (b *BatchStateOperations) Apply() (moved, removed int, failedMoves []string) {
	// Apply module moves first
	for srcModule, dstModule := range b.moduleMoves {
		n := b.sf.MoveModule(srcModule, dstModule)
		if n == 0 {
			failedMoves = append(failedMoves, fmt.Sprintf("%s -> %s (module)", srcModule, dstModule))
		} else {
			moved += n
		}
	}

	// Then apply individual resource moves
	for src, dst := range b.moves {
		if b.sf.MoveResource(src, dst) {
			moved++
		} else {
			failedMoves = append(failedMoves, fmt.Sprintf("%s -> %s", src, dst))
		}
	}

	// Then apply removes
	removed = b.sf.RemoveResources(keysFromMap(b.removes))

	return
}

// keysFromMap returns the keys of a map as a slice.
func keysFromMap(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// StateFileOperator provides state file operations for migration execution.
type StateFileOperator struct {
	debug bool
}

// NewStateFileOperator creates a new state file operator.
func NewStateFileOperator() *StateFileOperator {
	return &StateFileOperator{}
}

// SetDebug enables debug logging.
func (o *StateFileOperator) SetDebug(debug bool) {
	o.debug = debug
}

// RemoveFromState removes resources from the state file in a directory.
func (o *StateFileOperator) RemoveFromState(targetDir string, addrs []string) error {
	if len(addrs) == 0 {
		return nil
	}

	statePath := filepath.Join(targetDir, "terraform.tfstate")
	sf, err := LoadStateFile(statePath)
	if err != nil {
		return err
	}

	removed := sf.RemoveResources(addrs)
	if o.debug {
		fmt.Printf("[DEBUG] Removed %d resources from state in %s\n", removed, targetDir)
	}

	return sf.Save()
}

// MoveInState moves resources within the state file in a directory.
func (o *StateFileOperator) MoveInState(targetDir string, moves map[string]string) error {
	if len(moves) == 0 {
		return nil
	}

	statePath := filepath.Join(targetDir, "terraform.tfstate")
	sf, err := LoadStateFile(statePath)
	if err != nil {
		return err
	}

	moved := sf.MoveResources(moves)
	if o.debug {
		fmt.Printf("[DEBUG] Moved %d resources in state in %s\n", moved, targetDir)
	}

	return sf.Save()
}

// ApplyMigrationBatch performs remove and move operations in a single pass.
func (o *StateFileOperator) ApplyMigrationBatch(targetDir string, removes []string, moves map[string]string) error {
	statePath := filepath.Join(targetDir, "terraform.tfstate")
	sf, err := LoadStateFile(statePath)
	if err != nil {
		return err
	}

	batch := NewBatchStateOperations(sf)

	for _, addr := range removes {
		batch.Remove(addr)
	}

	for src, dst := range moves {
		batch.Move(src, dst)
	}

	moved, removed, failedMoves := batch.Apply()
	if o.debug {
		fmt.Printf("[DEBUG] Batch: moved %d, removed %d resources in %s\n", moved, removed, targetDir)
	}

	if len(failedMoves) > 0 {
		return fmt.Errorf("%d move(s) failed - resources not found: %v", len(failedMoves), failedMoves)
	}

	return sf.Save()
}

// providerStringRegexp parses the terraform provider format:
//   provider["registry.terraform.io/hashicorp/aws"].ireland
//   provider["registry.terraform.io/hashicorp/aws"]
var providerStringRegexp = regexp.MustCompile(`^provider\["([^"]+)"\](?:\.(.+))?$`)

// ParseProviderString parses a full terraform provider string into its components.
// Input: provider["registry.terraform.io/hashicorp/aws"].ireland
// Returns: registryPath="registry.terraform.io/hashicorp/aws", alias="ireland"
// Input: provider["registry.terraform.io/hashicorp/aws"]
// Returns: registryPath="registry.terraform.io/hashicorp/aws", alias=""
func ParseProviderString(provider string) (registryPath, alias string, ok bool) {
	matches := providerStringRegexp.FindStringSubmatch(provider)
	if matches == nil {
		return "", "", false
	}
	registryPath = matches[1]
	if len(matches) > 2 {
		alias = matches[2]
	}
	return registryPath, alias, true
}

// BuildProviderString constructs a full terraform provider string from components.
// BuildProviderString("registry.terraform.io/hashicorp/aws", "ireland")
//
//	-> provider["registry.terraform.io/hashicorp/aws"].ireland
//
// BuildProviderString("registry.terraform.io/hashicorp/aws", "")
//
//	-> provider["registry.terraform.io/hashicorp/aws"]
func BuildProviderString(registryPath, alias string) string {
	if alias != "" {
		return fmt.Sprintf("provider[\"%s\"].%s", registryPath, alias)
	}
	return fmt.Sprintf("provider[\"%s\"]", registryPath)
}

// ParseProviderShortName parses a user-friendly short provider name.
// "aws.ireland" -> ("aws", "ireland")
// "aws" -> ("aws", "")
// "hashicorp/aws.ireland" -> ("hashicorp/aws", "ireland")
func ParseProviderShortName(shortName string) (providerType, alias string) {
	// Split on the last dot to handle namespaced types like "hashicorp/aws"
	// But we need to be careful: "aws.ireland" splits to ("aws", "ireland")
	// and "hashicorp/aws.ireland" splits to ("hashicorp/aws", "ireland")
	lastDot := strings.LastIndex(shortName, ".")
	if lastDot == -1 {
		return shortName, ""
	}
	// If there's a slash after the dot, it's part of the type, not an alias
	afterDot := shortName[lastDot+1:]
	if strings.Contains(afterDot, "/") {
		return shortName, ""
	}
	return shortName[:lastDot], afterDot
}

// ShortNameToRegistryPath converts a short provider type name to a full registry path.
// "aws" -> "registry.terraform.io/hashicorp/aws"
// "hashicorp/aws" -> "registry.terraform.io/hashicorp/aws"
// "cloudflare/cloudflare" -> "registry.terraform.io/cloudflare/cloudflare"
func ShortNameToRegistryPath(providerType string) string {
	if strings.Contains(providerType, "/") {
		// Already has namespace
		return "registry.terraform.io/" + providerType
	}
	// Default to hashicorp namespace
	return "registry.terraform.io/hashicorp/" + providerType
}

// BuildProviderRemapTable converts a short-name remap map into a full provider string remap table.
// Input:  {"aws.ireland": "aws", "aws.sydney": "aws.bucket"}
// Output: {"provider[\"registry.terraform.io/hashicorp/aws\"].ireland": "provider[\"registry.terraform.io/hashicorp/aws\"]",
//
//	"provider[\"registry.terraform.io/hashicorp/aws\"].sydney": "provider[\"registry.terraform.io/hashicorp/aws\"].bucket"}
func BuildProviderRemapTable(shortRemap map[string]string) map[string]string {
	if len(shortRemap) == 0 {
		return nil
	}
	table := make(map[string]string, len(shortRemap))
	for fromShort, toShort := range shortRemap {
		fromType, fromAlias := ParseProviderShortName(fromShort)
		toType, toAlias := ParseProviderShortName(toShort)
		fromFull := BuildProviderString(ShortNameToRegistryPath(fromType), fromAlias)
		toFull := BuildProviderString(ShortNameToRegistryPath(toType), toAlias)
		table[fromFull] = toFull
	}
	return table
}

// ExtractResourceProviders parses state JSON and returns a map of resource address -> provider string.
func ExtractResourceProviders(stateData []byte) (map[string]string, error) {
	sf, err := LoadStateFromBytes(stateData, "")
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, r := range sf.GetState().Resources {
		result[r.Address()] = r.Provider
	}
	return result, nil
}
