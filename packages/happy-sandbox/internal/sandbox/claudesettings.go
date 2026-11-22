package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// FullAccessClaude: access.tool_args gives claude --dangerously-skip-permissions,
// so a plain `claude` or `happy` typed inside the sandbox should run the same way.
func (a Access) FullAccessClaude() bool {
	return slices.Contains(a.ToolArgs["claude"], "--dangerously-skip-permissions")
}

// seedClaudeSettings gives the sandbox's own Claude config
// permissions.defaultMode "bypassPermissions" when the sandbox is full access
// (FullAccessClaude) and the settings name no default mode yet: without it a
// `happy` or `claude` started inside the sandbox (not through happyN /
// claudeN) falls back to Claude's own default mode. A mode the user set stays.
// The config is the private one (home.private "claude") or the sandbox
// home's ~/.claude; the host's own home is never written.
func (i *Instance) seedClaudeSettings() error {
	a := i.Config.Access
	if !a.FullAccessClaude() {
		return nil
	}
	var dir string
	switch {
	case a.PrivateTool("claude"):
		dir = i.StateDir("claude")
	case !a.HostHome():
		dir = filepath.Join(i.HomeDir(), ".claude")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	default:
		return nil
	}
	return seedDefaultMode(filepath.Join(dir, "settings.json"), "bypassPermissions")
}

func seedDefaultMode(path, mode string) error {
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		if settings == nil {
			settings = map[string]any{}
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		if _, set := settings["permissions"]; set {
			return nil // not an object: not ours to rewrite
		}
		perms = map[string]any{}
	}
	if _, set := perms["defaultMode"]; set {
		return nil
	}
	perms["defaultMode"] = mode
	settings["permissions"] = perms
	if _, set := settings["skipDangerousModePermissionPrompt"]; !set && mode == "bypassPermissions" {
		settings["skipDangerousModePermissionPrompt"] = true
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(out, '\n'), 0o600)
}
