package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/Pandoxie/wannabe/packages/happy-sandbox/internal/sandbox"
)

// cmdConfig shows and changes config.json. Changes apply at the next up
// (happy-sandbox restart): the container is recreated, the home is kept.
//
//	config show
//	config set KEY VALUE      KEY dotted (access.resources.memory), VALUE JSON or text
//	config unset KEY
//	config edit               $EDITOR, then checked
//	config preset host|isolated
func cmdConfig(ctx context.Context, args []string) error {
	inst, rest, err := load(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 || rest[0] == "show" {
		data, _ := json.MarshalIndent(inst.Config, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	switch {
	case rest[0] == "set" && len(rest) == 3:
		err = editConfig(inst, func(m map[string]any) error { return setPath(m, rest[1], parseValue(rest[2]), false) })
	case rest[0] == "unset" && len(rest) == 2:
		err = editConfig(inst, func(m map[string]any) error { return setPath(m, rest[1], nil, true) })
	case rest[0] == "edit" && len(rest) == 1:
		err = editInEditor(inst)
	case rest[0] == "preset" && len(rest) == 2:
		switch rest[1] {
		case "host":
			inst.Config.Access = sandbox.HostPreset()
			inst.Config.Shares = addHostMounts(inst.Config.Shares)
		case "isolated":
			inst.Config.Access = sandbox.IsolatedPreset()
		default:
			return errors.New("config preset host | isolated")
		}
		err = saveChecked(inst, inst.Config)
	default:
		return errors.New("config show | set KEY VALUE | unset KEY | edit | preset host|isolated")
	}
	if err != nil {
		return err
	}
	fmt.Printf("Saved (access %s).\n", inst.Config.Access.Describe())
	if inst.State(ctx).Running {
		fmt.Printf("It applies at the next start: happy-sandbox restart %s (home, logins and data are kept).\n", inst.Config.Name)
	} else {
		fmt.Printf("It applies at the next: happy-sandbox up %s\n", inst.Config.Name)
	}
	return nil
}

// editConfig round-trips config.json through a generic map, so any field can
// be set by its JSON path, then decodes and checks the result.
func editConfig(inst *sandbox.Instance, change func(map[string]any) error) error {
	data, _ := json.Marshal(inst.Config)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if err := change(m); err != nil {
		return err
	}
	return decodeAndSave(inst, m)
}

func decodeAndSave(inst *sandbox.Instance, m map[string]any) error {
	data, _ := json.Marshal(m)
	var c sandbox.Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return fmt.Errorf("config: %v", err)
	}
	return saveChecked(inst, c)
}

// saveChecked validates and stores c. The name and the exit stay what init
// made them (a new exit is a new sandbox: init).
func saveChecked(inst *sandbox.Instance, c sandbox.Config) error {
	if c.Name != inst.Config.Name {
		return errors.New("config: the name cannot change")
	}
	if c.ExitIP != inst.Config.ExitIP {
		return errors.New("config: exit_ip cannot change here (a new exit: a new sandbox, happy-sandbox init)")
	}
	if err := c.Access.Validate(); err != nil {
		return err
	}
	for k := range c.Shares {
		s, err := sandbox.CheckShare(c.Shares[k], c.Shares[:k])
		if err != nil {
			return err
		}
		c.Shares[k] = s
	}
	inst.Config = c
	return inst.Save()
}

func editInEditor(inst *sandbox.Instance) error {
	f, err := os.CreateTemp("", "happy-sandbox-config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	data, _ := json.MarshalIndent(inst.Config, "", "  ")
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	f.Close()
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "nano"
	}
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "editor", f.Name())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	edited, err := os.ReadFile(f.Name())
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(edited, &m); err != nil {
		return fmt.Errorf("config: not valid JSON (nothing saved): %v", err)
	}
	return decodeAndSave(inst, m)
}

// parseValue: JSON when it parses (numbers, true, ["a","b"], {...}), else text.
func parseValue(v string) any {
	var out any
	if err := json.Unmarshal([]byte(v), &out); err == nil {
		return out
	}
	return v
}

// setPath sets (or removes) m[a][b][c] for "a.b.c"; list items by index.
func setPath(m map[string]any, path string, value any, remove bool) error {
	keys := strings.Split(path, ".")
	var cur any = m
	for k, key := range keys {
		last := k == len(keys)-1
		switch node := cur.(type) {
		case map[string]any:
			if last {
				if remove {
					delete(node, key)
				} else {
					node[key] = value
				}
				return nil
			}
			next, ok := node[key]
			if !ok || next == nil {
				next = map[string]any{}
				node[key] = next
			}
			cur = next
		case []any:
			n, err := strconv.Atoi(key)
			if err != nil || n < 0 || n >= len(node) {
				return fmt.Errorf("config: %s: no item %s", path, key)
			}
			if last {
				if remove {
					return fmt.Errorf("config: %s: remove list items with config edit", path)
				}
				node[n] = value
				return nil
			}
			cur = node[n]
		default:
			return fmt.Errorf("config: %s: %s is not an object", path, strings.Join(keys[:k], "."))
		}
	}
	return nil
}

// cmdPackages: list | add PKG... | rm PKG... — access.packages, the apt
// packages the sandbox keeps. Added ones are installed right away when the
// sandbox is up (through the gate); removed ones stay until the container is
// recreated.
func cmdPackages(ctx context.Context, args []string) error {
	inst, rest, err := load(args)
	if err != nil {
		return err
	}
	pkgs := inst.Config.Access.Packages
	if len(rest) == 0 || rest[0] == "list" {
		if len(pkgs) == 0 {
			fmt.Printf("no packages (happy-sandbox packages %s add NAME...)\n", inst.Config.Name)
		}
		for _, p := range pkgs {
			fmt.Println(p)
		}
		return nil
	}
	if len(rest) < 2 || (rest[0] != "add" && rest[0] != "rm") {
		return errors.New("packages list | add PKG... | rm PKG...")
	}
	c := inst.Config
	switch rest[0] {
	case "add":
		for _, p := range rest[1:] {
			if !slices.Contains(pkgs, p) {
				pkgs = append(pkgs, p)
			}
		}
	case "rm":
		pkgs = slices.DeleteFunc(slices.Clone(pkgs), func(p string) bool { return slices.Contains(rest[1:], p) })
	}
	c.Access.Packages = pkgs
	if err := saveChecked(inst, c); err != nil {
		return err
	}
	fmt.Printf("Packages: %s\n", strings.Join(pkgs, " "))
	if rest[0] == "add" && inst.State(ctx).Running {
		ensurePackages(ctx, inst)
	} else if rest[0] == "rm" {
		fmt.Println("Removed from the list; installed copies go when the container is next recreated.")
	}
	return nil
}
