package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func minimalDef(name string) *Def {
	return &Def{
		Name:       name,
		EntryPoint: StepRef{ID: "s"},
		Steps:      map[string]StepDef{"s": {Type: "bash", Command: "true"}},
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Def)
		wantErr bool
	}{
		{"valid bash step", func(*Def) {}, false},
		{"missing name", func(d *Def) { d.Name = "" }, true},
		{"no steps", func(d *Def) { d.Steps = nil }, true},
		{"missing entry_point", func(d *Def) { d.EntryPoint = StepRef{} }, true},
		{"entry_point unknown step", func(d *Def) { d.EntryPoint = StepRef{ID: "nope"} }, true},
		{"step missing type", func(d *Def) { d.Steps["s"] = StepDef{Command: "true"} }, true},
		{"unknown step type", func(d *Def) { d.Steps["s"] = StepDef{Type: "magic"} }, true},
		{"bash without command", func(d *Def) { d.Steps["s"] = StepDef{Type: "bash"} }, true},
		{"next_step unknown", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "bash", Command: "true", NextSteps: []StepRef{{ID: "ghost"}}}
		}, true},
		{"agent without prompt", func(d *Def) { d.Steps["s"] = StepDef{Type: "agent", Agent: "general"} }, true},
		{"valid agent step", func(d *Def) { d.Steps["s"] = StepDef{Type: "agent", Agent: "general", Prompt: "go"} }, false},
		{"budget on_exceeded unknown", func(d *Def) { d.Budget = &Budget{OnExceeded: &StepRef{ID: "nope"}} }, true},
		{"budget negative", func(d *Def) { d.Budget = &Budget{MaxIterations: -1} }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := minimalDef("wf")
			tc.mutate(d)
			err := Validate(d)
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		if got := Load(""); got != nil {
			t.Fatalf("Load(\"\") = %v, want nil", got)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if got := Load(filepath.Join(t.TempDir(), "nope.json")); got != nil {
			t.Fatalf("Load(missing) = %v, want nil", got)
		}
	})
	t.Run("valid + skip invalid", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")
		// One valid workflow and one invalid (no steps); only the valid one loads.
		body := `{"workflows":[
			{"name":"good","entry_point":{"id":"s"},"steps":{"s":{"type":"bash","command":"true"}}},
			{"name":"bad","entry_point":{"id":"s"},"steps":{}}
		]}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		got := Load(path)
		if len(got) != 1 || got[0].Name != "good" {
			t.Fatalf("Load() = %+v, want one workflow named good", got)
		}
	})
	t.Run("duplicate names disambiguated", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")
		body := `{"workflows":[
			{"name":"dup","entry_point":{"id":"s"},"steps":{"s":{"type":"bash","command":"true"}}},
			{"name":"dup","entry_point":{"id":"s"},"steps":{"s":{"type":"bash","command":"true"}}}
		]}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		got := Load(path)
		if len(got) != 2 || got[0].Name != "dup (1)" || got[1].Name != "dup (2)" {
			t.Fatalf("Load() names = %q, %q; want \"dup (1)\", \"dup (2)\"", got[0].Name, got[1].Name)
		}
	})
}
