package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readSettings(t *testing.T, p string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSeedDefaultMode(t *testing.T) {
	dir := t.TempDir()

	// No file: created.
	p := filepath.Join(dir, "a.json")
	if err := seedDefaultMode(p, "bypassPermissions"); err != nil {
		t.Fatal(err)
	}
	m := readSettings(t, p)
	if m["permissions"].(map[string]any)["defaultMode"] != "bypassPermissions" || m["skipDangerousModePermissionPrompt"] != true {
		t.Fatalf("new file: %v", m)
	}

	// Other keys kept.
	p = filepath.Join(dir, "b.json")
	os.WriteFile(p, []byte(`{"model":"opus","permissions":{"allow":["Bash"]}}`), 0o600)
	if err := seedDefaultMode(p, "bypassPermissions"); err != nil {
		t.Fatal(err)
	}
	m = readSettings(t, p)
	perms := m["permissions"].(map[string]any)
	if m["model"] != "opus" || perms["defaultMode"] != "bypassPermissions" || len(perms["allow"].([]any)) != 1 {
		t.Fatalf("merge: %v", m)
	}

	// A mode the user chose stays, file untouched.
	p = filepath.Join(dir, "c.json")
	orig := `{"permissions":{"defaultMode":"plan"}}`
	os.WriteFile(p, []byte(orig), 0o600)
	if err := seedDefaultMode(p, "bypassPermissions"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != orig {
		t.Fatalf("user mode rewritten: %s", data)
	}

	// Broken JSON: an error, not a rewrite.
	p = filepath.Join(dir, "d.json")
	os.WriteFile(p, []byte(`{`), 0o600)
	if err := seedDefaultMode(p, "bypassPermissions"); err == nil {
		t.Fatal("broken JSON accepted")
	}
}

func TestFullAccessClaude(t *testing.T) {
	if !HostPreset().FullAccessClaude() {
		t.Fatal("host preset is full access")
	}
	if !IsolatedPreset().FullAccessClaude() {
		t.Fatal("isolated preset is full access")
	}
	if err := IsolatedPreset().Validate(); err != nil {
		t.Fatalf("isolated preset invalid: %v", err)
	}
	if (Access{}).FullAccessClaude() {
		t.Fatal("empty access is not")
	}
}
