package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseStateAddress(t *testing.T) {
	tests := []struct {
		addr       string
		wantModule string
		wantType   string
		wantName   string
		wantData   bool
	}{
		// Basic resources
		{"aws_instance.foo", "", "aws_instance", "foo", false},
		{"data.aws_ami.latest", "", "aws_ami", "latest", true},
		{"data.aws_iam_policy_document.test", "", "aws_iam_policy_document", "test", true},

		// Simple modules
		{"module.vpc.aws_subnet.private", "module.vpc", "aws_subnet", "private", false},
		{"module.vpc.module.subnets.aws_subnet.main", "module.vpc.module.subnets", "aws_subnet", "main", false},

		// Indexed modules with numeric index
		{"module.ad_ec2[0].aws_instance.foo", "module.ad_ec2[0]", "aws_instance", "foo", false},
		{"module.kryptonite.module.ad_ec2[0].aws_instance.bar", "module.kryptonite.module.ad_ec2[0]", "aws_instance", "bar", false},

		// Indexed modules with string index (for_each)
		{`module.projects_us_east_1["suprunite2"].aws_instance.main`, `module.projects_us_east_1["suprunite2"]`, "aws_instance", "main", false},
		{`module.projects["foo"].module.networking["bar"].aws_vpc.main`, `module.projects["foo"].module.networking["bar"]`, "aws_vpc", "main", false},

		// Data sources in indexed modules
		{`module.projects_us_east_1["suprunite2"].data.aws_availability_zones.available`, `module.projects_us_east_1["suprunite2"]`, "aws_availability_zones", "available", true},

		// Deeply nested indexed modules
		{`module.a[0].module.b["x"].module.c[1].aws_instance.d`, `module.a[0].module.b["x"].module.c[1]`, "aws_instance", "d", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			module, resType, name, isData := parseStateAddress(tt.addr)
			if module != tt.wantModule {
				t.Errorf("module = %q, want %q", module, tt.wantModule)
			}
			if resType != tt.wantType {
				t.Errorf("type = %q, want %q", resType, tt.wantType)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if isData != tt.wantData {
				t.Errorf("isData = %v, want %v", isData, tt.wantData)
			}
		})
	}
}

func TestTokenizeAddress(t *testing.T) {
	tests := []struct {
		addr string
		want []string
	}{
		{"aws_instance.foo", []string{"aws_instance", "foo"}},
		{"module.vpc.aws_instance.foo", []string{"module", "vpc", "aws_instance", "foo"}},
		{`module.foo["bar"].aws_instance.baz`, []string{`module`, `foo["bar"]`, `aws_instance`, `baz`}},
		{`module.a[0].module.b["x"].aws_instance.c`, []string{`module`, `a[0]`, `module`, `b["x"]`, `aws_instance`, `c`}},
		// Edge case: dots inside string index (rare but possible)
		{`module.foo["a.b.c"].aws_instance.bar`, []string{`module`, `foo["a.b.c"]`, `aws_instance`, `bar`}},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := tokenizeAddress(tt.addr)
			if len(got) != len(tt.want) {
				t.Errorf("tokenizeAddress(%q) = %v, want %v", tt.addr, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenizeAddress(%q)[%d] = %q, want %q", tt.addr, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStateResourceAddress(t *testing.T) {
	tests := []struct {
		resource StateResource
		want     string
	}{
		{
			StateResource{Mode: "managed", Type: "aws_instance", Name: "foo"},
			"aws_instance.foo",
		},
		{
			StateResource{Mode: "data", Type: "aws_ami", Name: "latest"},
			"data.aws_ami.latest",
		},
		{
			StateResource{Mode: "managed", Type: "aws_subnet", Name: "private", Module: "module.vpc"},
			"module.vpc.aws_subnet.private",
		},
		// Indexed modules (as they appear in real state files)
		{
			StateResource{Mode: "managed", Type: "aws_instance", Name: "foo", Module: `module.projects_us_east_1["suprunite2"]`},
			`module.projects_us_east_1["suprunite2"].aws_instance.foo`,
		},
		{
			StateResource{Mode: "data", Type: "aws_availability_zones", Name: "available", Module: `module.projects_us_east_1["suprunite2"]`},
			`module.projects_us_east_1["suprunite2"].data.aws_availability_zones.available`,
		},
		{
			StateResource{Mode: "managed", Type: "aws_instance", Name: "bar", Module: `module.kryptonite.module.ad_ec2[0]`},
			`module.kryptonite.module.ad_ec2[0].aws_instance.bar`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.resource.Address()
			if got != tt.want {
				t.Errorf("Address() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStateFileRemoveResource(t *testing.T) {
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "foo"},
			{Mode: "managed", Type: "aws_instance", Name: "bar"},
			{Mode: "data", Type: "aws_ami", Name: "latest"},
		},
	}
	sf := &StateFile{state: state}

	// Remove one resource
	if !sf.RemoveResource("aws_instance.foo") {
		t.Error("RemoveResource returned false for existing resource")
	}

	if len(sf.state.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(sf.state.Resources))
	}

	// Check remaining resources
	addrs := sf.ListResources()
	expected := map[string]bool{"aws_instance.bar": true, "data.aws_ami.latest": true}
	for _, addr := range addrs {
		if !expected[addr] {
			t.Errorf("unexpected resource %q", addr)
		}
	}

	// Try to remove non-existent
	if sf.RemoveResource("aws_instance.notexist") {
		t.Error("RemoveResource returned true for non-existent resource")
	}
}

func TestStateFileMoveResource(t *testing.T) {
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "old_name"},
		},
	}
	sf := &StateFile{state: state}

	// Move resource
	if !sf.MoveResource("aws_instance.old_name", "aws_instance.new_name") {
		t.Error("MoveResource returned false for existing resource")
	}

	addrs := sf.ListResources()
	if len(addrs) != 1 || addrs[0] != "aws_instance.new_name" {
		t.Errorf("expected [aws_instance.new_name], got %v", addrs)
	}
}

func TestStateFileMoveToModule(t *testing.T) {
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "foo"},
		},
	}
	sf := &StateFile{state: state}

	// Move to module
	if !sf.MoveResource("aws_instance.foo", "module.compute.aws_instance.main") {
		t.Error("MoveResource returned false for existing resource")
	}

	addrs := sf.ListResources()
	if len(addrs) != 1 || addrs[0] != "module.compute.aws_instance.main" {
		t.Errorf("expected [module.compute.aws_instance.main], got %v", addrs)
	}

	// Check the actual state
	r := sf.state.Resources[0]
	if r.Module != "module.compute" {
		t.Errorf("Module = %q, want %q", r.Module, "module.compute")
	}
	if r.Type != "aws_instance" {
		t.Errorf("Type = %q, want %q", r.Type, "aws_instance")
	}
	if r.Name != "main" {
		t.Errorf("Name = %q, want %q", r.Name, "main")
	}
}

func TestStateFileMoveToIndexedModule(t *testing.T) {
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "foo"},
		},
	}
	sf := &StateFile{state: state}

	// Move to indexed module with string key
	if !sf.MoveResource("aws_instance.foo", `module.projects["prod"].aws_instance.main`) {
		t.Error("MoveResource returned false for existing resource")
	}

	addrs := sf.ListResources()
	if len(addrs) != 1 || addrs[0] != `module.projects["prod"].aws_instance.main` {
		t.Errorf("expected [module.projects[\"prod\"].aws_instance.main], got %v", addrs)
	}

	// Check the actual state
	r := sf.state.Resources[0]
	if r.Module != `module.projects["prod"]` {
		t.Errorf("Module = %q, want %q", r.Module, `module.projects["prod"]`)
	}
	if r.Type != "aws_instance" {
		t.Errorf("Type = %q, want %q", r.Type, "aws_instance")
	}
	if r.Name != "main" {
		t.Errorf("Name = %q, want %q", r.Name, "main")
	}
}

func TestStateFileMoveWithinIndexedModule(t *testing.T) {
	// Test moving a resource that's already in an indexed module
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "old", Module: `module.projects["dev"]`},
		},
	}
	sf := &StateFile{state: state}

	// Move to a different indexed module
	if !sf.MoveResource(`module.projects["dev"].aws_instance.old`, `module.projects["prod"].aws_instance.new`) {
		t.Error("MoveResource returned false for existing resource")
	}

	addrs := sf.ListResources()
	if len(addrs) != 1 || addrs[0] != `module.projects["prod"].aws_instance.new` {
		t.Errorf("expected [module.projects[\"prod\"].aws_instance.new], got %v", addrs)
	}

	// Check the actual state
	r := sf.state.Resources[0]
	if r.Module != `module.projects["prod"]` {
		t.Errorf("Module = %q, want %q", r.Module, `module.projects["prod"]`)
	}
}

func TestStateFileBatchOperations(t *testing.T) {
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "a"},
			{Mode: "managed", Type: "aws_instance", Name: "b"},
			{Mode: "managed", Type: "aws_instance", Name: "c"},
			{Mode: "managed", Type: "aws_instance", Name: "d"},
		},
	}
	sf := &StateFile{state: state}

	batch := NewBatchStateOperations(sf)
	batch.Move("aws_instance.a", "aws_instance.alpha")
	batch.Remove("aws_instance.b")
	batch.Remove("aws_instance.c")

	moved, removed, failedMoves := batch.Apply()
	if moved != 1 {
		t.Errorf("moved = %d, want 1", moved)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if len(failedMoves) != 0 {
		t.Errorf("failedMoves = %v, want empty", failedMoves)
	}

	addrs := sf.ListResources()
	expected := map[string]bool{"aws_instance.alpha": true, "aws_instance.d": true}
	if len(addrs) != 2 {
		t.Errorf("expected 2 resources, got %d: %v", len(addrs), addrs)
	}
	for _, addr := range addrs {
		if !expected[addr] {
			t.Errorf("unexpected resource %q", addr)
		}
	}
}

func TestStateFileBatchOperationsFailedMoves(t *testing.T) {
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "a"},
		},
	}
	sf := &StateFile{state: state}

	batch := NewBatchStateOperations(sf)
	batch.Move("aws_instance.a", "aws_instance.alpha")           // Should succeed
	batch.Move("aws_instance.nonexistent", "aws_instance.beta")  // Should fail

	moved, _, failedMoves := batch.Apply()
	if moved != 1 {
		t.Errorf("moved = %d, want 1", moved)
	}
	if len(failedMoves) != 1 {
		t.Errorf("failedMoves = %v, want 1 failure", failedMoves)
	}
}

func TestStateFileSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "terraform.tfstate")

	// Create and save state
	state := &TerraformState{
		Version:          4,
		TerraformVersion: "1.5.0",
		Serial:           100,
		Lineage:          "test-lineage",
		Outputs:          map[string]interface{}{},
		Resources: []StateResource{
			{
				Mode:     "managed",
				Type:     "aws_instance",
				Name:     "test",
				Provider: "provider[\"registry.terraform.io/hashicorp/aws\"]",
				Instances: []StateResourceInstance{
					{
						SchemaVersion: 1,
						Attributes:    map[string]interface{}{"id": "i-12345"},
					},
				},
			},
		},
	}

	// Write initial state
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Load state
	sf, err := LoadStateFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	// Verify loaded
	if len(sf.ListResources()) != 1 {
		t.Errorf("expected 1 resource after load")
	}

	// Modify and save
	sf.MoveResource("aws_instance.test", "aws_instance.renamed")
	if err := sf.Save(); err != nil {
		t.Fatal(err)
	}

	// Reload and verify
	sf2, err := LoadStateFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	addrs := sf2.ListResources()
	if len(addrs) != 1 || addrs[0] != "aws_instance.renamed" {
		t.Errorf("expected [aws_instance.renamed], got %v", addrs)
	}

	// Serial should have incremented
	if sf2.state.Serial != 101 {
		t.Errorf("Serial = %d, want 101", sf2.state.Serial)
	}
}

func TestIsModuleAddress(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		// Module-only addresses (should return true)
		{"module.foo", true},
		{`module.foo["bar"]`, true},
		{"module.a.module.b", true},
		{`module.projects_us_east_1["suprunite2"]`, true},
		{`module.a[0].module.b["x"]`, true},

		// Resource addresses (should return false)
		{"aws_instance.foo", false},
		{"data.aws_ami.latest", false},
		{"module.foo.aws_instance.bar", false},
		{`module.projects["prod"].aws_instance.main`, false},
		{`module.projects_us_east_1["suprunite2"].aws_ssm_parameter.data_bucket`, false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := IsModuleAddress(tt.addr)
			if got != tt.want {
				t.Errorf("IsModuleAddress(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestResourceUnderModule(t *testing.T) {
	tests := []struct {
		resourceAddr string
		modulePrefix string
		want         bool
	}{
		// Resources directly under the module
		{"module.foo.aws_instance.bar", "module.foo", true},
		{`module.projects["prod"].aws_instance.main`, `module.projects["prod"]`, true},
		{`module.projects_us_east_1["suprunite2"].aws_ssm_parameter.data_bucket`, `module.projects_us_east_1["suprunite2"]`, true},

		// Resources in nested modules under the prefix
		{"module.foo.module.nested.aws_instance.bar", "module.foo", true},
		{`module.a[0].module.b["x"].aws_instance.c`, `module.a[0]`, true},

		// Resources NOT under the module
		{"module.bar.aws_instance.baz", "module.foo", false},
		{"aws_instance.bar", "module.foo", false},
		{`module.projects["dev"].aws_instance.main`, `module.projects["prod"]`, false},

		// Edge cases - similar prefixes but not actually under
		{"module.foo_extra.aws_instance.bar", "module.foo", false},
		{"module.foobar.aws_instance.baz", "module.foo", false},
	}

	for _, tt := range tests {
		name := tt.resourceAddr + "_under_" + tt.modulePrefix
		t.Run(name, func(t *testing.T) {
			got := resourceUnderModule(tt.resourceAddr, tt.modulePrefix)
			if got != tt.want {
				t.Errorf("resourceUnderModule(%q, %q) = %v, want %v", tt.resourceAddr, tt.modulePrefix, got, tt.want)
			}
		})
	}
}

func TestStateFileMoveModule(t *testing.T) {
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "a", Module: `module.projects["foo"]`},
			{Mode: "managed", Type: "aws_instance", Name: "b", Module: `module.projects["foo"]`},
			{Mode: "data", Type: "aws_ami", Name: "latest", Module: `module.projects["foo"]`},
			{Mode: "managed", Type: "aws_vpc", Name: "main", Module: `module.projects["bar"]`}, // Different module
			{Mode: "managed", Type: "aws_instance", Name: "root"}, // Root level
		},
	}
	sf := &StateFile{state: state}

	// Move module.projects["foo"] to module.foo
	moved := sf.MoveModule(`module.projects["foo"]`, "module.foo")
	if moved != 3 {
		t.Errorf("MoveModule returned %d, want 3", moved)
	}

	// Check the resources were renamed correctly
	addrs := sf.ListResources()
	expected := map[string]bool{
		"module.foo.aws_instance.a":        true,
		"module.foo.aws_instance.b":        true,
		"module.foo.data.aws_ami.latest":   true,
		`module.projects["bar"].aws_vpc.main`: true,
		"aws_instance.root":                true,
	}
	if len(addrs) != len(expected) {
		t.Errorf("expected %d resources, got %d: %v", len(expected), len(addrs), addrs)
	}
	for _, addr := range addrs {
		if !expected[addr] {
			t.Errorf("unexpected resource %q", addr)
		}
	}
}

func TestStateFileMoveModuleNested(t *testing.T) {
	// Test moving a module that has nested modules under it
	state := &TerraformState{
		Version: 4,
		Serial:  1,
		Resources: []StateResource{
			{Mode: "managed", Type: "aws_instance", Name: "a", Module: `module.projects["foo"]`},
			{Mode: "managed", Type: "aws_subnet", Name: "main", Module: `module.projects["foo"].module.networking`},
			{Mode: "managed", Type: "aws_vpc", Name: "main", Module: `module.projects["bar"]`}, // Different module
		},
	}
	sf := &StateFile{state: state}

	// Move module.projects["foo"] to module.foo (should also move nested modules)
	moved := sf.MoveModule(`module.projects["foo"]`, "module.foo")
	if moved != 2 {
		t.Errorf("MoveModule returned %d, want 2", moved)
	}

	// Check the resources were renamed correctly
	addrs := sf.ListResources()
	expected := map[string]bool{
		"module.foo.aws_instance.a":                 true,
		"module.foo.module.networking.aws_subnet.main": true,
		`module.projects["bar"].aws_vpc.main`:       true,
	}
	if len(addrs) != len(expected) {
		t.Errorf("expected %d resources, got %d: %v", len(expected), len(addrs), addrs)
	}
	for _, addr := range addrs {
		if !expected[addr] {
			t.Errorf("unexpected resource %q", addr)
		}
	}
}

func TestStripInstanceIndex(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"aws_instance.foo", "aws_instance.foo"},
		{"aws_instance.foo[0]", "aws_instance.foo"},
		{"aws_instance.foo[1]", "aws_instance.foo"},
		{`aws_instance.foo["key"]`, "aws_instance.foo"},
		{"module.foo.aws_instance.bar", "module.foo.aws_instance.bar"},
		{"module.foo.aws_instance.bar[0]", "module.foo.aws_instance.bar"},
		{`module.foo.aws_instance.bar["key"]`, "module.foo.aws_instance.bar"},
		{"module.foo[0].aws_instance.bar[0]", "module.foo[0].aws_instance.bar"},
		{`module.foo["x"].aws_instance.bar[0]`, `module.foo["x"].aws_instance.bar`},
		{"", ""},
		{"data.aws_ami.latest[0]", "data.aws_ami.latest"},
	}
	for _, tt := range tests {
		got := stripInstanceIndex(tt.input)
		if got != tt.expected {
			t.Errorf("stripInstanceIndex(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
