package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// helper: write a TerraformState as JSON to a temp directory and return the dir path.
func writeTestState(t *testing.T, state *TerraformState) string {
	t.Helper()
	dir := t.TempDir()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), data, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	return dir
}

// helper: load the state file back from a directory.
func loadTestState(t *testing.T, dir string) *TerraformState {
	t.Helper()
	sf, err := LoadStateFile(filepath.Join(dir, "terraform.tfstate"))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return sf.GetState()
}

// helper: collect resource addresses from a TerraformState, sorted.
func resourceAddrs(state *TerraformState) []string {
	var addrs []string
	for _, r := range state.Resources {
		addrs = append(addrs, r.Address())
	}
	sort.Strings(addrs)
	return addrs
}

func makeResource(module, typ, name, provider string) StateResource {
	return StateResource{
		Mode:     "managed",
		Type:     typ,
		Name:     name,
		Provider: provider,
		Module:   module,
		Instances: []StateResourceInstance{
			{
				SchemaVersion: 0,
				Attributes: map[string]interface{}{
					"id": typ + "-" + name + "-id",
				},
			},
		},
	}
}

func makeDataResource(module, typ, name, provider string) StateResource {
	return StateResource{
		Mode:     "data",
		Type:     typ,
		Name:     name,
		Provider: provider,
		Module:   module,
		Instances: []StateResourceInstance{
			{
				SchemaVersion: 0,
				Attributes: map[string]interface{}{
					"id": typ + "-" + name + "-id",
				},
			},
		},
	}
}

func baseState() *TerraformState {
	return &TerraformState{
		Version:          4,
		TerraformVersion: "1.6.0",
		Serial:           10,
		Lineage:          "test-lineage",
	}
}

func TestExecuteWorkspacePlan_KeepOnly(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		makeResource("", "aws_instance", "web", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
		makeResource("", "aws_instance", "db", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
		makeResource("", "aws_s3_bucket", "logs", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Keep:        []string{"aws_instance.web", "aws_s3_bucket.logs"},
		Moves:       make(map[string]string),
		ModuleMoves: make(map[string]string),
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 0 {
		t.Errorf("expected 0 moves, got %d", moved)
	}

	result := loadTestState(t, dir)
	addrs := resourceAddrs(result)

	expected := []string{"aws_instance.web", "aws_s3_bucket.logs"}
	sort.Strings(expected)

	if len(addrs) != len(expected) {
		t.Fatalf("expected %d resources, got %d: %v", len(expected), len(addrs), addrs)
	}
	for i, addr := range addrs {
		if addr != expected[i] {
			t.Errorf("resource[%d]: expected %q, got %q", i, expected[i], addr)
		}
	}
}

func TestExecuteWorkspacePlan_IndividualMoves(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		makeResource("", "aws_instance", "old_name", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
		makeResource("", "aws_instance", "keep_me", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Keep: []string{"aws_instance.keep_me"},
		Moves: map[string]string{
			"aws_instance.old_name": "aws_instance.new_name",
		},
		ModuleMoves: make(map[string]string),
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 1 {
		t.Errorf("expected 1 move, got %d", moved)
	}

	result := loadTestState(t, dir)
	addrs := resourceAddrs(result)

	expected := []string{"aws_instance.keep_me", "aws_instance.new_name"}
	sort.Strings(expected)

	if len(addrs) != len(expected) {
		t.Fatalf("expected %d resources, got %d: %v", len(expected), len(addrs), addrs)
	}
	for i, addr := range addrs {
		if addr != expected[i] {
			t.Errorf("resource[%d]: expected %q, got %q", i, expected[i], addr)
		}
	}

	// Verify provider is preserved
	for _, r := range result.Resources {
		if r.Provider != "provider[\"registry.terraform.io/hashicorp/aws\"]" {
			t.Errorf("provider not preserved for %s: got %q", r.Address(), r.Provider)
		}
	}
}

func TestExecuteWorkspacePlan_MoveToModule(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		makeResource("", "aws_instance", "web", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Moves: map[string]string{
			"aws_instance.web": "module.app.aws_instance.web",
		},
		ModuleMoves: make(map[string]string),
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 1 {
		t.Errorf("expected 1 move, got %d", moved)
	}

	result := loadTestState(t, dir)
	if len(result.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(result.Resources))
	}
	r := result.Resources[0]
	if r.Module != "module.app" {
		t.Errorf("expected module %q, got %q", "module.app", r.Module)
	}
	if r.Type != "aws_instance" {
		t.Errorf("expected type %q, got %q", "aws_instance", r.Type)
	}
	if r.Name != "web" {
		t.Errorf("expected name %q, got %q", "web", r.Name)
	}
}

func TestExecuteWorkspacePlan_ModuleMove(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		makeResource("module.old", "aws_instance", "web", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
		makeResource("module.old", "aws_s3_bucket", "data", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
		makeResource("module.old.module.nested", "aws_instance", "inner", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
		makeResource("module.other", "aws_instance", "unrelated", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Keep:  []string{"module.other.aws_instance.unrelated"},
		Moves: make(map[string]string),
		ModuleMoves: map[string]string{
			"module.old": "module.new",
		},
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 3 {
		t.Errorf("expected 3 moves, got %d", moved)
	}

	result := loadTestState(t, dir)
	addrs := resourceAddrs(result)

	expected := []string{
		"module.new.aws_instance.web",
		"module.new.aws_s3_bucket.data",
		"module.new.module.nested.aws_instance.inner",
		"module.other.aws_instance.unrelated",
	}
	sort.Strings(expected)

	if len(addrs) != len(expected) {
		t.Fatalf("expected %d resources, got %d: %v", len(expected), len(addrs), addrs)
	}
	for i, addr := range addrs {
		if addr != expected[i] {
			t.Errorf("resource[%d]: expected %q, got %q", i, expected[i], addr)
		}
	}
}

func TestExecuteWorkspacePlan_NoOutputsCopied(t *testing.T) {
	state := baseState()
	state.Outputs = map[string]interface{}{
		"vpc_id": map[string]interface{}{
			"value": "vpc-123",
			"type":  "string",
		},
	}
	state.Resources = []StateResource{
		makeResource("", "aws_instance", "web", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Keep:        []string{"aws_instance.web"},
		Moves:       make(map[string]string),
		ModuleMoves: make(map[string]string),
	}

	_, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := loadTestState(t, dir)
	if result.Outputs != nil {
		t.Errorf("expected nil outputs, got %v", result.Outputs)
	}
}

func TestExecuteWorkspacePlan_EmptyPlan(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		makeResource("", "aws_instance", "web", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Moves:       make(map[string]string),
		ModuleMoves: make(map[string]string),
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 0 {
		t.Errorf("expected 0 moves, got %d", moved)
	}

	result := loadTestState(t, dir)
	if len(result.Resources) != 0 {
		t.Errorf("expected 0 resources, got %d: %v", len(result.Resources), resourceAddrs(result))
	}
}

func TestExecuteWorkspacePlan_MoveNotFound(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		makeResource("", "aws_instance", "web", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Moves: map[string]string{
			"aws_instance.nonexistent": "aws_instance.new_name",
		},
		ModuleMoves: make(map[string]string),
	}

	_, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err == nil {
		t.Fatal("expected error for missing move source, got nil")
	}
	if !stringContains(err.Error(), "not found in state") {
		t.Errorf("expected error about 'not found in state', got: %v", err)
	}
}

func TestExecuteWorkspacePlan_PreservesInstances(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		{
			Mode:     "managed",
			Type:     "aws_instance",
			Name:     "web",
			Provider: "provider[\"registry.terraform.io/hashicorp/aws\"]",
			Instances: []StateResourceInstance{
				{
					SchemaVersion: 1,
					Attributes: map[string]interface{}{
						"id":            "i-1234567890",
						"instance_type": "t3.micro",
						"tags": map[string]interface{}{
							"Name": "web-server",
						},
					},
					Private:      "eyJzY2hlbWFfdmVyc2lvbiI6IjEifQ==",
					Dependencies: []string{"aws_vpc.main", "aws_subnet.primary"},
				},
				{
					SchemaVersion: 1,
					Attributes: map[string]interface{}{
						"id":            "i-0987654321",
						"instance_type": "t3.micro",
					},
					IndexKey: float64(1),
				},
			},
		},
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Moves: map[string]string{
			"aws_instance.web": "module.app.aws_instance.server",
		},
		ModuleMoves: make(map[string]string),
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 1 {
		t.Errorf("expected 1 move, got %d", moved)
	}

	result := loadTestState(t, dir)
	if len(result.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(result.Resources))
	}

	r := result.Resources[0]
	if r.Address() != "module.app.aws_instance.server" {
		t.Errorf("expected address %q, got %q", "module.app.aws_instance.server", r.Address())
	}

	// Verify instances preserved
	if len(r.Instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(r.Instances))
	}

	inst := r.Instances[0]
	if inst.SchemaVersion != 1 {
		t.Errorf("expected schema_version 1, got %d", inst.SchemaVersion)
	}
	if inst.Private != "eyJzY2hlbWFfdmVyc2lvbiI6IjEifQ==" {
		t.Errorf("private data not preserved: got %q", inst.Private)
	}
	if len(inst.Dependencies) != 2 {
		t.Errorf("expected 2 dependencies, got %d", len(inst.Dependencies))
	}
	if inst.Attributes["id"] != "i-1234567890" {
		t.Errorf("attribute id not preserved: got %v", inst.Attributes["id"])
	}

	// Second instance should preserve index_key
	inst2 := r.Instances[1]
	if inst2.IndexKey != float64(1) {
		t.Errorf("index_key not preserved: got %v", inst2.IndexKey)
	}
}

func TestExecuteWorkspacePlan_PreservesMetadata(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		makeResource("", "aws_instance", "web", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Keep:        []string{"aws_instance.web"},
		Moves:       make(map[string]string),
		ModuleMoves: make(map[string]string),
	}

	_, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := loadTestState(t, dir)
	if result.Version != 4 {
		t.Errorf("expected version 4, got %d", result.Version)
	}
	if result.TerraformVersion != "1.6.0" {
		t.Errorf("expected terraform_version %q, got %q", "1.6.0", result.TerraformVersion)
	}
	if result.Lineage != "test-lineage" {
		t.Errorf("expected lineage %q, got %q", "test-lineage", result.Lineage)
	}
	// Serial should be incremented by Save()
	if result.Serial <= 10 {
		t.Errorf("expected serial > 10, got %d", result.Serial)
	}
}

func TestExecuteWorkspacePlan_DataSource(t *testing.T) {
	state := baseState()
	state.Resources = []StateResource{
		makeDataResource("", "aws_ami", "latest", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
		makeResource("", "aws_instance", "web", "provider[\"registry.terraform.io/hashicorp/aws\"]"),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Keep: []string{"data.aws_ami.latest"},
		Moves: map[string]string{
			"aws_instance.web": "module.app.aws_instance.web",
		},
		ModuleMoves: make(map[string]string),
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 1 {
		t.Errorf("expected 1 move, got %d", moved)
	}

	result := loadTestState(t, dir)
	addrs := resourceAddrs(result)
	expected := []string{"data.aws_ami.latest", "module.app.aws_instance.web"}
	sort.Strings(expected)

	if len(addrs) != len(expected) {
		t.Fatalf("expected %d resources, got %d: %v", len(expected), len(addrs), addrs)
	}
	for i, addr := range addrs {
		if addr != expected[i] {
			t.Errorf("resource[%d]: expected %q, got %q", i, expected[i], addr)
		}
	}
}

// stringContains checks if s contains substr.
func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestExecuteWorkspacePlan_ProviderRemap(t *testing.T) {
	state := baseState()
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`
	awsDefault := `provider["registry.terraform.io/hashicorp/aws"]`
	state.Resources = []StateResource{
		makeResource("module.mymod", "aws_instance", "web", awsIreland),
		makeResource("module.mymod", "aws_s3_bucket", "data", awsIreland),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	wsPlan := &WorkspacePlan{
		Keep:        []string{"module.mymod.aws_instance.web", "module.mymod.aws_s3_bucket.data"},
		Moves:       make(map[string]string),
		ModuleMoves: make(map[string]string),
		ProviderRemaps: map[string]string{
			awsIreland: awsDefault,
		},
	}

	moved, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moved != 0 {
		t.Errorf("expected 0 moves, got %d", moved)
	}

	result := loadTestState(t, dir)
	for _, r := range result.Resources {
		if r.Provider != awsDefault {
			t.Errorf("resource %s: expected provider %q, got %q", r.Address(), awsDefault, r.Provider)
		}
	}
}

func TestExecuteWorkspacePlan_ProviderRemapNoMatch(t *testing.T) {
	state := baseState()
	awsSydney := `provider["registry.terraform.io/hashicorp/aws"].sydney`
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`
	awsDefault := `provider["registry.terraform.io/hashicorp/aws"]`
	state.Resources = []StateResource{
		makeResource("", "aws_instance", "web", awsSydney),
		makeResource("", "aws_s3_bucket", "data", awsIreland),
	}

	dir := writeTestState(t, state)
	executor := &Executor{debug: false}

	// Only remap ireland -> default, sydney should be untouched
	wsPlan := &WorkspacePlan{
		Keep:        []string{"aws_instance.web", "aws_s3_bucket.data"},
		Moves:       make(map[string]string),
		ModuleMoves: make(map[string]string),
		ProviderRemaps: map[string]string{
			awsIreland: awsDefault,
		},
	}

	_, err := executor.executeWorkspacePlan("test", dir, wsPlan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := loadTestState(t, dir)
	for _, r := range result.Resources {
		switch r.Name {
		case "web":
			if r.Provider != awsSydney {
				t.Errorf("resource %s: expected provider %q (unchanged), got %q", r.Address(), awsSydney, r.Provider)
			}
		case "data":
			if r.Provider != awsDefault {
				t.Errorf("resource %s: expected provider %q (remapped), got %q", r.Address(), awsDefault, r.Provider)
			}
		}
	}
}

func TestBuildProviderRemapTable(t *testing.T) {
	table := BuildProviderRemapTable(map[string]string{
		"aws.ireland": "aws",
		"aws.sydney":  "aws.bucket",
	})

	expected := map[string]string{
		`provider["registry.terraform.io/hashicorp/aws"].ireland`: `provider["registry.terraform.io/hashicorp/aws"]`,
		`provider["registry.terraform.io/hashicorp/aws"].sydney`:  `provider["registry.terraform.io/hashicorp/aws"].bucket`,
	}

	if len(table) != len(expected) {
		t.Fatalf("expected %d entries, got %d", len(expected), len(table))
	}
	for k, v := range expected {
		if table[k] != v {
			t.Errorf("key %q: expected %q, got %q", k, v, table[k])
		}
	}
}

func TestParseProviderString(t *testing.T) {
	tests := []struct {
		input        string
		registryPath string
		alias        string
		ok           bool
	}{
		{`provider["registry.terraform.io/hashicorp/aws"].ireland`, "registry.terraform.io/hashicorp/aws", "ireland", true},
		{`provider["registry.terraform.io/hashicorp/aws"]`, "registry.terraform.io/hashicorp/aws", "", true},
		{`provider["registry.terraform.io/cloudflare/cloudflare"].main`, "registry.terraform.io/cloudflare/cloudflare", "main", true},
		{"invalid", "", "", false},
		{"", "", "", false},
	}

	for _, tt := range tests {
		registryPath, alias, ok := ParseProviderString(tt.input)
		if ok != tt.ok {
			t.Errorf("ParseProviderString(%q): ok=%v, want %v", tt.input, ok, tt.ok)
			continue
		}
		if registryPath != tt.registryPath {
			t.Errorf("ParseProviderString(%q): registryPath=%q, want %q", tt.input, registryPath, tt.registryPath)
		}
		if alias != tt.alias {
			t.Errorf("ParseProviderString(%q): alias=%q, want %q", tt.input, alias, tt.alias)
		}
	}
}

func TestParseProviderShortName(t *testing.T) {
	tests := []struct {
		input        string
		providerType string
		alias        string
	}{
		{"aws.ireland", "aws", "ireland"},
		{"aws", "aws", ""},
		{"hashicorp/aws.ireland", "hashicorp/aws", "ireland"},
		{"cloudflare/cloudflare", "cloudflare/cloudflare", ""},
	}

	for _, tt := range tests {
		providerType, alias := ParseProviderShortName(tt.input)
		if providerType != tt.providerType {
			t.Errorf("ParseProviderShortName(%q): type=%q, want %q", tt.input, providerType, tt.providerType)
		}
		if alias != tt.alias {
			t.Errorf("ParseProviderShortName(%q): alias=%q, want %q", tt.input, alias, tt.alias)
		}
	}
}

func TestBuildProviderRemapTable_Empty(t *testing.T) {
	table := BuildProviderRemapTable(nil)
	if table != nil {
		t.Errorf("expected nil, got %v", table)
	}

	table = BuildProviderRemapTable(map[string]string{})
	if table != nil {
		t.Errorf("expected nil for empty map, got %v", table)
	}
}

// TestProviderRemap_EndToEnd tests the full flow: YAML config parsing -> BuildMigrationPlan -> ExecutePlan
// This simulates the exact xcover scenario where source state has aws.ireland provider
// but target expects default aws provider.
func TestProviderRemap_EndToEnd(t *testing.T) {
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`
	awsDefault := `provider["registry.terraform.io/hashicorp/aws"]`

	// 1. Parse config from YAML (simulating tfsync.yaml with provider_remap)
	yamlConfig := `
version: "1"
source:
  workspaces:
    production:
      path: ./source-prod
target:
  workspaces:
    spearhead:
      path: ./target-spearhead
      prefer_workspace: production
migration:
  provider_remap:
    "aws.ireland": "aws"
  moves:
    - from: "module.spearhead"
      to:
        workspace: spearhead
        resource: "module.spearhead"
`
	var cfg Config
	if err := ParseYAMLRaw([]byte(yamlConfig), &cfg); err != nil {
		t.Fatalf("parse yaml: %v", err)
	}
	if len(cfg.Migration.ProviderRemap) == 0 {
		t.Fatal("expected provider_remap to be parsed from YAML")
	}
	if cfg.Migration.ProviderRemap["aws.ireland"] != "aws" {
		t.Fatalf("expected provider_remap[aws.ireland]=aws, got %q", cfg.Migration.ProviderRemap["aws.ireland"])
	}

	// 2. Build source resources (simulating what we'd read from terraform state list)
	sourceResources := SourceWorkspaceResources{
		"production": {
			"module.spearhead.aws_instance.web",
			"module.spearhead.aws_s3_bucket.data",
			"module.spearhead.aws_iam_role.role",
		},
	}
	targetToSource := map[string]string{
		"spearhead": "production",
	}

	// 3. Build migration plan
	plan, err := BuildMigrationPlan(&cfg.Migration, sourceResources, targetToSource, nil)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	// Verify provider remaps were set on the workspace plan
	wsPlan := plan.WorkspacePlans["spearhead"]
	if wsPlan == nil {
		t.Fatal("expected workspace plan for 'spearhead'")
	}
	if len(wsPlan.ProviderRemaps) == 0 {
		t.Fatal("expected ProviderRemaps to be populated on workspace plan")
	}
	if wsPlan.ProviderRemaps[awsIreland] != awsDefault {
		t.Fatalf("expected remap %q -> %q, got %q", awsIreland, awsDefault, wsPlan.ProviderRemaps[awsIreland])
	}

	// 4. Write source state to a temp dir (simulating copied state file)
	state := baseState()
	state.Resources = []StateResource{
		makeResource("module.spearhead", "aws_instance", "web", awsIreland),
		makeResource("module.spearhead", "aws_s3_bucket", "data", awsIreland),
		makeResource("module.spearhead", "aws_iam_role", "role", awsIreland),
	}
	dir := writeTestState(t, state)

	// 5. Execute the plan
	executor := &Executor{debug: true}
	targetDirs := map[string]string{"spearhead": dir}
	result, err := executor.ExecutePlan(nil, plan, targetDirs, nil)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	t.Logf("Moves executed: %d", result.MovesExecuted)

	// 6. Verify the output state has remapped providers
	outputState := loadTestState(t, dir)
	if len(outputState.Resources) != 3 {
		t.Fatalf("expected 3 resources, got %d", len(outputState.Resources))
	}
	for _, r := range outputState.Resources {
		if r.Provider != awsDefault {
			t.Errorf("resource %s: expected provider %q, got %q", r.Address(), awsDefault, r.Provider)
		}
	}
	t.Log("All resources have correct provider after remapping")
}

func TestProviderRemap_PerWorkspace(t *testing.T) {
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`
	awsDefault := `provider["registry.terraform.io/hashicorp/aws"]`
	awsBucket := `provider["registry.terraform.io/hashicorp/aws"].bucket`

	yamlConfig := `
version: "1"
source:
  workspaces:
    production:
      path: ./source-prod
target:
  workspaces:
    ws-a:
      path: ./target-a
      prefer_workspace: production
      provider_remap:
        "aws.ireland": "aws"
    ws-b:
      path: ./target-b
      prefer_workspace: production
      provider_remap:
        "aws.ireland": "aws.bucket"
migration:
  moves:
    - from: "module.a"
      to:
        workspace: ws-a
        resource: "module.a"
    - from: "module.b"
      to:
        workspace: ws-b
        resource: "module.b"
`
	var cfg Config
	if err := ParseYAMLRaw([]byte(yamlConfig), &cfg); err != nil {
		t.Fatalf("parse yaml: %v", err)
	}

	// Verify per-workspace provider_remap parsed correctly
	if cfg.Target.Workspaces["ws-a"].ProviderRemap["aws.ireland"] != "aws" {
		t.Fatal("expected ws-a provider_remap[aws.ireland]=aws")
	}
	if cfg.Target.Workspaces["ws-b"].ProviderRemap["aws.ireland"] != "aws.bucket" {
		t.Fatal("expected ws-b provider_remap[aws.ireland]=aws.bucket")
	}

	sourceResources := SourceWorkspaceResources{
		"production": {
			"module.a.aws_instance.one",
			"module.b.aws_instance.two",
		},
	}
	targetToSource := map[string]string{
		"ws-a": "production",
		"ws-b": "production",
	}

	// Build opts with per-workspace remaps
	opts := &PlanOptions{
		TargetProviderRemaps: map[string]map[string]string{
			"ws-a": cfg.Target.Workspaces["ws-a"].ProviderRemap,
			"ws-b": cfg.Target.Workspaces["ws-b"].ProviderRemap,
		},
	}

	plan, err := BuildMigrationPlan(&cfg.Migration, sourceResources, targetToSource, opts)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	// ws-a should remap ireland -> default
	if plan.WorkspacePlans["ws-a"].ProviderRemaps[awsIreland] != awsDefault {
		t.Errorf("ws-a: expected remap to %q, got %q", awsDefault, plan.WorkspacePlans["ws-a"].ProviderRemaps[awsIreland])
	}
	// ws-b should remap ireland -> bucket
	if plan.WorkspacePlans["ws-b"].ProviderRemaps[awsIreland] != awsBucket {
		t.Errorf("ws-b: expected remap to %q, got %q", awsBucket, plan.WorkspacePlans["ws-b"].ProviderRemaps[awsIreland])
	}
}

func TestProviderRemap_PerWorkspaceOverridesGlobal(t *testing.T) {
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`
	awsDefault := `provider["registry.terraform.io/hashicorp/aws"]`
	awsBucket := `provider["registry.terraform.io/hashicorp/aws"].bucket`

	sourceResources := SourceWorkspaceResources{
		"prod": {
			"module.a.aws_instance.one",
			"module.b.aws_instance.two",
		},
	}
	targetToSource := map[string]string{
		"ws-a": "prod",
		"ws-b": "prod",
	}

	cfg := &Migration{
		ProviderRemap: map[string]string{"aws.ireland": "aws"}, // global default
		Moves: []Move{
			{From: MoveFrom{Resource: "module.a"}, To: MoveTo{Resource: "module.a", Workspace: "ws-a"}},
			{From: MoveFrom{Resource: "module.b"}, To: MoveTo{Resource: "module.b", Workspace: "ws-b"}},
		},
	}

	opts := &PlanOptions{
		TargetProviderRemaps: map[string]map[string]string{
			// ws-b overrides global
			"ws-b": {"aws.ireland": "aws.bucket"},
		},
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, opts)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	// ws-a uses global: ireland -> default
	if plan.WorkspacePlans["ws-a"].ProviderRemaps[awsIreland] != awsDefault {
		t.Errorf("ws-a: expected global remap to %q, got %q", awsDefault, plan.WorkspacePlans["ws-a"].ProviderRemaps[awsIreland])
	}
	// ws-b uses per-workspace override: ireland -> bucket
	if plan.WorkspacePlans["ws-b"].ProviderRemaps[awsIreland] != awsBucket {
		t.Errorf("ws-b: expected per-workspace remap to %q, got %q", awsBucket, plan.WorkspacePlans["ws-b"].ProviderRemaps[awsIreland])
	}
}

func TestProviderRemap_ValidationError(t *testing.T) {
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`

	sourceResources := SourceWorkspaceResources{
		"prod": {"module.a.aws_instance.one"},
	}
	targetToSource := map[string]string{
		"ws-a": "prod",
	}
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "module.a"}, To: MoveTo{Resource: "module.a", Workspace: "ws-a"}},
		},
	}

	// Provide source providers but NO remap configured
	opts := &PlanOptions{
		SourceProviders: SourceResourceProviders{
			"prod": {"module.a.aws_instance.one": awsIreland},
		},
	}

	_, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, opts)
	if err == nil {
		t.Fatal("expected an error about unmapped aliased provider, got nil")
	}

	errStr := err.Error()
	if !stringContains(errStr, "provider_remap") || !stringContains(errStr, "ireland") {
		t.Errorf("expected error mentioning provider_remap and ireland, got: %v", err)
	}
}

func TestProviderRemap_NoWarningForDefault(t *testing.T) {
	awsDefault := `provider["registry.terraform.io/hashicorp/aws"]`

	sourceResources := SourceWorkspaceResources{
		"prod": {"aws_instance.one"},
	}
	targetToSource := map[string]string{
		"ws-a": "prod",
	}
	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "aws_instance.one"}, To: MoveTo{Resource: "aws_instance.one", Workspace: "ws-a"}},
		},
	}

	// Source uses default provider (no alias) — no warning expected
	opts := &PlanOptions{
		SourceProviders: SourceResourceProviders{
			"prod": {"aws_instance.one": awsDefault},
		},
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, opts)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	if len(plan.Warnings) != 0 {
		t.Errorf("expected no warnings for default provider, got: %v", plan.Warnings)
	}
}

func TestProviderRemap_NoWarningWhenRemapConfigured(t *testing.T) {
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`

	sourceResources := SourceWorkspaceResources{
		"prod": {"module.a.aws_instance.one"},
	}
	targetToSource := map[string]string{
		"ws-a": "prod",
	}
	cfg := &Migration{
		ProviderRemap: map[string]string{"aws.ireland": "aws"},
		Moves: []Move{
			{From: MoveFrom{Resource: "module.a"}, To: MoveTo{Resource: "module.a", Workspace: "ws-a"}},
		},
	}

	opts := &PlanOptions{
		SourceProviders: SourceResourceProviders{
			"prod": {"module.a.aws_instance.one": awsIreland},
		},
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, opts)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	if len(plan.Warnings) != 0 {
		t.Errorf("expected no warnings when remap is configured, got: %v", plan.Warnings)
	}
}

func TestProviderRemap_PerSourceWorkspace(t *testing.T) {
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`
	awsDefault := `provider["registry.terraform.io/hashicorp/aws"]`
	awsSydney := `provider["registry.terraform.io/hashicorp/aws"].sydney`
	awsBucket := `provider["registry.terraform.io/hashicorp/aws"].bucket`

	sourceResources := SourceWorkspaceResources{
		"dev":  {"module.a.aws_instance.one"},
		"prod": {"module.b.aws_instance.two"},
	}
	targetToSource := map[string]string{
		"ws-dev":  "dev",
		"ws-prod": "prod",
	}

	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "module.a"}, To: MoveTo{Resource: "module.a", Workspace: "ws-dev"}},
			{From: MoveFrom{Resource: "module.b"}, To: MoveTo{Resource: "module.b", Workspace: "ws-prod"}},
		},
	}

	// dev uses aws.ireland, prod uses aws.sydney — different source remaps
	opts := &PlanOptions{
		SourceProviderRemaps: map[string]map[string]string{
			"dev":  {"aws.ireland": "aws"},
			"prod": {"aws.sydney": "aws.bucket"},
		},
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, opts)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	// ws-dev inherits remap from source "dev": ireland -> default
	if plan.WorkspacePlans["ws-dev"].ProviderRemaps[awsIreland] != awsDefault {
		t.Errorf("ws-dev: expected remap %q -> %q, got %q",
			awsIreland, awsDefault, plan.WorkspacePlans["ws-dev"].ProviderRemaps[awsIreland])
	}

	// ws-prod inherits remap from source "prod": sydney -> bucket
	if plan.WorkspacePlans["ws-prod"].ProviderRemaps[awsSydney] != awsBucket {
		t.Errorf("ws-prod: expected remap %q -> %q, got %q",
			awsSydney, awsBucket, plan.WorkspacePlans["ws-prod"].ProviderRemaps[awsSydney])
	}

	// ws-dev should NOT have the sydney remap
	if _, has := plan.WorkspacePlans["ws-dev"].ProviderRemaps[awsSydney]; has {
		t.Error("ws-dev: should not have sydney remap from prod source")
	}
}

func TestProviderRemap_SourceOverriddenByTarget(t *testing.T) {
	awsIreland := `provider["registry.terraform.io/hashicorp/aws"].ireland`
	awsBucket := `provider["registry.terraform.io/hashicorp/aws"].bucket`

	sourceResources := SourceWorkspaceResources{
		"prod": {"module.a.aws_instance.one"},
	}
	targetToSource := map[string]string{
		"ws-a": "prod",
	}

	cfg := &Migration{
		Moves: []Move{
			{From: MoveFrom{Resource: "module.a"}, To: MoveTo{Resource: "module.a", Workspace: "ws-a"}},
		},
	}

	// Source says ireland -> default, but target overrides to ireland -> bucket
	opts := &PlanOptions{
		SourceProviderRemaps: map[string]map[string]string{
			"prod": {"aws.ireland": "aws"},
		},
		TargetProviderRemaps: map[string]map[string]string{
			"ws-a": {"aws.ireland": "aws.bucket"},
		},
	}

	plan, err := BuildMigrationPlan(cfg, sourceResources, targetToSource, opts)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	// Target override wins: ireland -> bucket
	if plan.WorkspacePlans["ws-a"].ProviderRemaps[awsIreland] != awsBucket {
		t.Errorf("expected target override %q -> %q, got %q",
			awsIreland, awsBucket, plan.WorkspacePlans["ws-a"].ProviderRemaps[awsIreland])
	}
}
