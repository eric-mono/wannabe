package sandbox

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Access is how much of the host a sandbox sees, besides its network (always
// the gate) and its identity (always its own). Everything here is in
// config.json and applies at the next up; an empty Access is the isolated
// sandbox happy-sandbox always made.
type Access struct {
	// Profile names the preset the fields were filled from: "isolated"
	// (default) or "host". It is informational; the fields decide.
	Profile string `json:"profile,omitempty"`
	// User "dev" (default): the image's user with a private home. "host": the
	// host user's name, uid, gid and home path.
	User      string    `json:"user,omitempty"`
	Home      HomeSpec  `json:"home"`
	Env       EnvSpec   `json:"env"`
	Resources Resources `json:"resources"`
	Devices   Devices   `json:"devices"`
	// ToolArgs are prepended by `happy-sandbox here` when the command is the
	// named tool, e.g. {"claude": ["--dangerously-skip-permissions"]}.
	ToolArgs map[string][]string `json:"tool_args,omitempty"`
	// Packages are apt packages the sandbox keeps: installed (through the
	// gate) at every up where one is missing, e.g. after the container was
	// recreated, which resets apt installs.
	Packages []string `json:"packages,omitempty"`
}

type HomeSpec struct {
	// Mode "sandbox" (default): ~/.happy-sandbox/NAME/home, filled from the
	// image once. "host": the host's own home, read-write, at its own path
	// (needs user "host").
	Mode string `json:"mode,omitempty"`
	// Private tools keep their state (logins, settings) in the sandbox
	// directory instead of the home: "claude", "codex", "happy" (its account
	// and daemon: the sandbox is a machine of its own in the app).
	Private []string `json:"private,omitempty"`
	// Protect: host paths mounted read-only over the home, so the sandbox
	// cannot change its own gate (~ is the host home).
	Protect []string `json:"protect,omitempty"`
}

type EnvSpec struct {
	Unset []string          `json:"unset,omitempty"`
	Set   map[string]string `json:"set,omitempty"`
}

// Resources: "" is the default, which is the host's; "max" lifts a limit.
type Resources struct {
	Memory  string `json:"memory,omitempty"`  // default: the host's less a tenth (hostDefaultMemory); "64G", "max"
	Pids    string `json:"pids,omitempty"`    // "max" default, "4096"
	CPUs    string `json:"cpus,omitempty"`    // "all" default, "8", "2.5"
	Shm     string `json:"shm,omitempty"`     // /dev/shm size: the host's default, "16G"
	Ulimits string `json:"ulimits,omitempty"` // "host" default: the host's limits; "podman": podman's
}

type Devices struct {
	// GPU "auto" (default: NVIDIA when the host has a CDI spec, else AMD with
	// ROCm, else none), "none", "nvidia" (all GPUs of the CDI spec), "amd"
	// (/dev/kfd, /dev/dri), or one NVIDIA device of the spec ("0", a UUID).
	GPU   string   `json:"gpu,omitempty"`
	Extra []string `json:"extra,omitempty"` // more host devices, e.g. /dev/ttyUSB0
}

// gpuRe: auto, none, nvidia, amd, or a device name of the NVIDIA CDI spec.
var gpuRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// packageRe: an apt package, optionally =version (no options, no paths).
var packageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(=[A-Za-z0-9.+:~-]+)?$`)

// The tools a home can keep private, and the variable that moves their state.
var privateTools = map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME", "happy": "HAPPY_HOME_DIR"}

// PrivateTool: the tool keeps its state in the sandbox directory.
func (a Access) PrivateTool(t string) bool {
	for _, p := range a.Home.Private {
		if p == t {
			return true
		}
	}
	return false
}

// hostDefaultMemory is the default memory limit: the host's, less a tenth
// (at least 4 GiB) so a sandbox cannot starve the host; "max" if unknown.
func hostDefaultMemory() string {
	total := hostMemory()
	keep := total / 10
	if keep < 4<<30 {
		keep = 4 << 30
	}
	if total <= keep {
		return "max"
	}
	return formatSize((total - keep) &^ (1<<30 - 1))
}

// FullAccessToolArgs: claude and codex without permission prompts. Every
// preset has them, so every new sandbox runs its tools with full access
// (HSB_SAFE=1, or access.tool_args removed, keeps the prompts).
func FullAccessToolArgs() map[string][]string {
	return map[string][]string{
		"claude": {"--dangerously-skip-permissions"},
		"codex":  {"--dangerously-bypass-approvals-and-sandbox"},
	}
}

// IsolatedPreset is the "isolated" profile (the default): the image's user
// with a home of its own, and the tools with full access.
func IsolatedPreset() Access {
	return Access{ToolArgs: FullAccessToolArgs()}
}

// HostPreset is the "host" profile: the sandbox runs like the host user, with
// the host's home, /mnt and /media and the GPU (resources are the host's by
// default), and only the gate and the identity of its own.
func HostPreset() Access {
	return Access{
		Profile: "host",
		User:    "host",
		Home: HomeSpec{
			Mode:    "host",
			Private: []string{"claude", "codex", "happy"},
			Protect: []string{"~/.happy-sandbox", "~/.local/lib/happy-sandbox", "~/.config/systemd/user",
				"~/build/happy-sandbox-v2/secrets", "~/build/happy-sandbox-build/secrets"},
		},
		Env: EnvSpec{Unset: []string{"http_proxy", "https_proxy", "ftp_proxy", "all_proxy", "no_proxy",
			"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "ALL_PROXY", "NO_PROXY"}},
		Devices:  Devices{GPU: "auto"},
		ToolArgs: FullAccessToolArgs(),
		Packages: []string{"openssh-client"},
	}
}

// HostMounts are the shares the host preset adds: /mnt and /media at their
// own paths, with everything mounted below them.
func HostMounts() []Share {
	var out []Share
	for _, p := range []string{"/mnt", "/media"} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, Share{Source: p, Target: p, Submounts: true})
		}
	}
	return out
}

func (a Access) HostUser() bool { return a.User == "host" }
func (a Access) HostHome() bool { return a.Home.Mode == "host" }

// Validate rejects what cannot work, before anything is started.
func (a Access) Validate() error {
	switch a.User {
	case "", "dev", "host":
	default:
		return fmt.Errorf("access.user %q: dev or host", a.User)
	}
	switch a.Home.Mode {
	case "", "sandbox":
	case "host":
		if !a.HostUser() {
			return errors.New(`access.home.mode "host" needs access.user "host" (the home must keep its path)`)
		}
	default:
		return fmt.Errorf("access.home.mode %q: sandbox or host", a.Home.Mode)
	}
	for _, t := range a.Home.Private {
		if privateTools[t] == "" {
			return fmt.Errorf("access.home.private %q: claude, codex or happy", t)
		}
	}
	for _, p := range a.Home.Protect {
		if !strings.HasPrefix(p, "~/") && !filepath.IsAbs(p) {
			return fmt.Errorf("access.home.protect %q: an absolute path or ~/…", p)
		}
	}
	for _, k := range a.Env.Unset {
		if !envNameOK(k) {
			return fmt.Errorf("access.env.unset %q is not a variable name", k)
		}
	}
	for k := range a.Env.Set {
		if !envNameOK(k) {
			return fmt.Errorf("access.env.set %q is not a variable name", k)
		}
	}
	r := a.Resources
	if _, err := sizeOrMax(r.Memory); err != nil {
		return fmt.Errorf("access.resources.memory: %v", err)
	}
	if _, err := sizeOrMax(r.Shm); err != nil || r.Shm == "max" {
		return fmt.Errorf("access.resources.shm %q: a size like 16G", r.Shm)
	}
	if r.Pids != "" && r.Pids != "max" {
		if n, err := strconv.Atoi(r.Pids); err != nil || n < 64 {
			return fmt.Errorf("access.resources.pids %q: max or a number (at least 64)", r.Pids)
		}
	}
	if r.CPUs != "" && r.CPUs != "all" {
		if f, err := strconv.ParseFloat(r.CPUs, 64); err != nil || f <= 0 {
			return fmt.Errorf("access.resources.cpus %q: all or a number of CPUs", r.CPUs)
		}
	}
	switch r.Ulimits {
	case "", "host", "podman":
	default:
		return fmt.Errorf("access.resources.ulimits %q: host or podman", r.Ulimits)
	}
	if g := a.Devices.GPU; !gpuRe.MatchString(g) && g != "" {
		return fmt.Errorf("access.devices.gpu %q: auto, none, nvidia, amd or an NVIDIA device (0, a GPU UUID)", g)
	}
	for _, p := range a.Packages {
		if !packageRe.MatchString(p) {
			return fmt.Errorf("access.packages %q is not an apt package name", p)
		}
	}
	for _, d := range a.Devices.Extra {
		if !strings.HasPrefix(filepath.Clean(d), "/dev/") {
			return fmt.Errorf("access.devices.extra %q: a device under /dev/", d)
		}
	}
	return nil
}

func envNameOK(k string) bool {
	if k == "" || (k[0] >= '0' && k[0] <= '9') {
		return false
	}
	for _, c := range k {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// sizeOrMax parses "", "max" (both -1) or a size: bytes, or K/M/G/T (binary).
func sizeOrMax(s string) (int64, error) {
	if s == "" || s == "max" {
		return -1, nil
	}
	mult := int64(1)
	num := strings.TrimSuffix(strings.ToUpper(s), "B")
	if n := len(num); n > 0 {
		switch num[n-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
		if mult > 1 {
			num = num[:n-1]
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%q is not a size (like 4G, 512M or max)", s)
	}
	return int64(v * float64(mult)), nil
}

func formatSize(b int64) string {
	switch {
	case b%(1<<30) == 0:
		return strconv.FormatInt(b>>30, 10) + "G"
	case b%(1<<20) == 0:
		return strconv.FormatInt(b>>20, 10) + "M"
	}
	return strconv.FormatInt(b, 10)
}

// hostMemory is MemTotal in bytes (0 if unknown).
func hostMemory() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

// hostShm is the host's /dev/shm size, the default (programs size their
// caches by it); 16G if unknown.
func hostShm() string {
	var st unix.Statfs_t
	if err := unix.Statfs("/dev/shm", &st); err != nil || st.Blocks == 0 {
		return "16G"
	}
	size := int64(st.Blocks) * st.Bsize
	if size >= 1<<30 {
		return formatSize(size &^ (1<<30 - 1))
	}
	return formatSize(size &^ (1<<20 - 1))
}

// expandHome turns ~/x into the host home's x.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(os.Getenv("HOME"), p[2:])
	}
	return p
}
