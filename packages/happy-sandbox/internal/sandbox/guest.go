package sandbox

import (
	"bufio"
	"embed"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// GuestDir is mounted read-only at /etc/happy-sandbox: the setup script and
// the files every way in (shell, exec, here) starts from.
const GuestDir = "/etc/happy-sandbox"

// stateRoot is where private tool state (access.home.private) is mounted.
const stateRoot = "/var/lib/happy-sandbox"

//go:embed guest/hsb-setup.sh guest/hsb-setup.service
var guestFiles embed.FS

// GuestUser is the account inside the container.
type GuestUser struct {
	Name, Group, Home, Shell string
	UID, GID                 int
}

// User is dev (the image's user, private home at /home/dev) or, with
// access.user "host", the host user's name, group, home path and shell. The
// uid and gid are always the host user's (keep-id), so files stay theirs.
func (i *Instance) User() GuestUser {
	u := GuestUser{Name: "dev", Group: "dev", Home: "/home/dev", Shell: "/bin/bash", UID: os.Getuid(), GID: os.Getgid()}
	if !i.Config.Access.HostUser() {
		return u
	}
	if cur, err := user.Current(); err == nil {
		u.Name, u.Home = cur.Username, cur.HomeDir
		if g, err := user.LookupGroupId(cur.Gid); err == nil {
			u.Group = g.Name
		}
	}
	if h := os.Getenv("HOME"); h != "" {
		u.Home = h
	}
	if sh := loginShell(u.Name); sh != "" {
		u.Shell = sh
	}
	return u
}

func loginShell(name string) string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p := strings.Split(sc.Text(), ":"); len(p) == 7 && p[0] == name {
			return p[6]
		}
	}
	return ""
}

// MapHostPath is where a host path appears inside, if it does: the host home
// (home mode "host") and the shares, at their targets.
func (i *Instance) MapHostPath(p string) (string, bool) {
	p = filepath.Clean(p)
	best, target := "", ""
	consider := func(src, dst string) {
		if (p == src || strings.HasPrefix(p, src+"/")) && len(src) > len(best) {
			best, target = src, dst+p[len(src):]
		}
	}
	if i.Config.Access.HostHome() {
		h := i.User().Home
		consider(h, h)
	}
	for _, s := range i.Config.Shares {
		consider(s.Source, s.Target)
	}
	return target, best != ""
}

// submounts lists the mount points strictly below root (from mountinfo),
// shallowest first. An automount is triggered by opening it, so the bind
// gets the real filesystem.
func submounts(root string) []string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		mp := unescapeMount(fields[4])
		if strings.HasPrefix(mp, root+"/") {
			seen[mp] = true
		}
	}
	var out []string
	for mp := range seen {
		if d, err := os.Open(mp); err == nil {
			_, _ = d.Readdirnames(1)
			d.Close()
		}
		if st, err := os.Stat(mp); err == nil && st.IsDir() {
			out = append(out, mp)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if na, nb := strings.Count(out[a], "/"), strings.Count(out[b], "/"); na != nb {
			return na < nb
		}
		return out[a] < out[b]
	})
	return out
}

// unescapeMount decodes mountinfo's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for k := 0; k < len(s); k++ {
		if s[k] == '\\' && k+3 < len(s) {
			if n, err := strconv.ParseUint(s[k+1:k+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				k += 3
				continue
			}
		}
		b.WriteByte(s[k])
	}
	return b.String()
}

// gpuChoice is what the sandbox asks for: access.devices.gpu, else the
// older top-level "gpu" (empty: automatic).
func (i *Instance) gpuChoice() string {
	if g := i.Config.Access.Devices.GPU; g != "" {
		return g
	}
	return i.Config.GPU
}

// GPU resolves the choice to "nvidia", "amd" or "" (none), without failing:
// for status and the description.
func (i *Instance) GPU() string {
	switch g := i.gpuChoice(); g {
	case "none":
		return ""
	case "amd":
		return "amd"
	case "", "auto":
		if NvidiaCDI() {
			return "nvidia"
		}
		if amdHost() {
			return "amd"
		}
		return ""
	default: // "nvidia", "all", an index or a UUID of the NVIDIA spec
		return "nvidia"
	}
}

// FindGPU is the NVIDIA GPU the container gets (nil for none or AMD); an
// explicit choice the host cannot give is an error.
func (i *Instance) FindGPU() (*GPU, error) {
	switch g := i.gpuChoice(); g {
	case "none":
		return nil, nil
	case "amd":
		if !amdHost() {
			return nil, errors.New(`access.devices.gpu is "amd" but this host has no /dev/kfd (ROCm): set the gpu to "auto" or "none"`)
		}
		return nil, nil
	case "", "auto":
		return FindGPU("")
	case "nvidia":
		return FindGPU("all")
	default:
		return FindGPU(g)
	}
}

func amdHost() bool { return exists("/dev/kfd") && exists("/opt/rocm") }

// NvidiaCDI: a CDI spec for NVIDIA GPUs is installed (nvidia-ctk cdi generate).
func NvidiaCDI() bool {
	for _, p := range CDISpecPaths {
		if exists(p) {
			return true
		}
	}
	return false
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// resourceArgs: memory, processes, CPUs, /dev/shm and ulimits.
func (i *Instance) resourceArgs() []string {
	r := i.Config.Access.Resources
	var args []string
	memory := r.Memory
	// The older top-level limits (memory_bytes, cpus, pids_limit) still apply
	// where access.resources says nothing.
	if r.Pids == "" && i.Config.PidsLimit > 0 {
		r.Pids = strconv.FormatInt(i.Config.PidsLimit, 10)
	}
	if r.CPUs == "" && i.Config.CPUs > 0 {
		r.CPUs = strconv.FormatFloat(i.Config.CPUs, 'f', -1, 64)
	}
	if memory == "" && i.Config.MemoryBytes > 0 {
		memory = strconv.FormatInt(i.Config.MemoryBytes, 10)
	} else if memory == "" {
		memory = hostDefaultMemory()
	}
	if mem, _ := sizeOrMax(memory); mem > 0 {
		args = append(args, "--memory", strconv.FormatInt(mem, 10))
	}
	switch r.Pids {
	case "", "max":
		args = append(args, "--pids-limit", "-1")
	default:
		args = append(args, "--pids-limit", r.Pids)
	}
	if r.CPUs != "" && r.CPUs != "all" {
		args = append(args, "--cpus", r.CPUs)
	}
	// /dev/shm: the host's size unless set (podman's own 64M starves programs
	// that share memory or cache there; it still counts against --memory).
	shmSize := r.Shm
	if shmSize == "" {
		shmSize = hostShm()
	}
	if shm, _ := sizeOrMax(shmSize); shm > 0 {
		args = append(args, "--shm-size", strconv.FormatInt(shm, 10))
	}
	if r.Ulimits != "podman" {
		args = append(args, "--ulimit", "host")
	}
	return args
}

// deviceArgs: the GPU (NVIDIA from the host's CDI spec, see gpu.go; AMD
// through ROCm's devices) and any extra devices.
func (i *Instance) deviceArgs(gpu *GPU) []string {
	var args []string
	if gpu != nil {
		args = append(args, "--label", gpuLabel+"="+gpu.Key)
		args = append(args, gpu.args()...)
	} else if i.GPU() == "amd" {
		// /dev/kfd is the render group's: keep the host user's groups.
		args = append(args, "--label", gpuLabel+"=amd", "--device", "/dev/kfd", "--device", "/dev/dri", "--group-add", "keep-groups")
	} else {
		args = append(args, "--label", gpuLabel+"=-")
	}
	for _, d := range i.Config.Access.Devices.Extra {
		args = append(args, "--device", d)
	}
	return args
}

// writeGuestFiles writes what /etc/happy-sandbox holds: the setup script, the
// environment every way in ends with (access.env, private tools), and the
// two entry scripts. Written in place, never replaced, so a running
// container's bind mount sees the same files.
func (i *Instance) writeGuestFiles() error {
	dir := i.path("guest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	a := i.Config.Access
	var env strings.Builder
	env.WriteString("# Generated by happy-sandbox from config.json (access.env, access.home.private).\n")
	env.WriteString("# Sourced last on every way in: shell, exec, here.\n")
	if len(a.Env.Unset) > 0 {
		env.WriteString("unset " + strings.Join(a.Env.Unset, " ") + "\n")
	}
	keys := make([]string, 0, len(a.Env.Set))
	for k := range a.Env.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env.WriteString("export " + k + "=" + shellQuote(a.Env.Set[k]) + "\n")
	}
	for _, t := range a.Home.Private {
		env.WriteString("export " + privateTools[t] + "=" + stateRoot + "/" + t + "\n")
	}
	env.WriteString("export HAPPY_SANDBOX=" + shellQuote(i.Config.Name) + "\n")
	if n := i.HappyMachineName(); n != "" {
		// The Happy app's name for this machine, while it has none (ls5-1).
		env.WriteString("export HAPPY_MACHINE_NAME=" + shellQuote(n) + "\n")
	}
	if a.Resources.Ulimits != "podman" {
		// runuser's PAM session drops the soft open-files limit to 1024; the
		// container has the host's, as hard limit: take it back.
		env.WriteString("ulimit -S -n \"$(ulimit -H -n)\" 2>/dev/null\n")
	}
	if a.HostHome() {
		// The image's own tools, after the user's: used only where the host has none.
		env.WriteString(`for d in /home/dev/.local/bin /home/dev/.nvm/versions/node/*/bin; do
    [ -d "$d" ] && case ":$PATH:" in *":$d:"*) ;; *) PATH="$PATH:$d" ;; esac
done
unset d
export PATH
# happy is the image's (installed in /home/dev): its installer state too, so
# happy update finds it, not a ~/.happy-dist of the host's.
[ -d /home/dev/.happy-dist ] && export HAPPY_DIST_HOME=/home/dev/.happy-dist
`)
	}
	setup, err := guestFiles.ReadFile("guest/hsb-setup.sh")
	if err != nil {
		return err
	}
	unit, err := guestFiles.ReadFile("guest/hsb-setup.service")
	if err != nil {
		return err
	}
	files := map[string]string{
		"env.sh": env.String(),
		// Interactive: the user's own login files (which read ~/.bashrc), then env.sh.
		"bashrc": `# happy-sandbox: an interactive shell starts like a login on the host.
[ -r /etc/profile ] && . /etc/profile
for f in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
    if [ -r "$f" ]; then . "$f"; break; fi
done
unset f
. ` + GuestDir + `/env.sh
`,
		// A command: the login environment, env.sh, then the command.
		"run": `#!/bin/bash
# happy-sandbox: run a command with the user's login environment.
[ -r /etc/profile ] && . /etc/profile
for f in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
    if [ -r "$f" ]; then . "$f"; break; fi
done
unset f
. ` + GuestDir + `/env.sh
exec "$@"
`,
		"hsb-setup.sh":      string(setup),
		"hsb-setup.service": string(unit),
	}
	for name, content := range files {
		mode := os.FileMode(0o644)
		if name == "run" || name == "hsb-setup.sh" {
			mode = 0o755
		}
		if err := writeInPlace(filepath.Join(dir, name), []byte(content), mode); err != nil {
			return err
		}
	}
	return nil
}

func writeInPlace(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// EnterArgs is the podman exec argv tail that runs as the guest user in the
// login environment: an interactive shell (command empty) or a command.
// interactive: the command runs in an interactive shell (the user's
// ~/.bashrc, as in a terminal on the host).
func (i *Instance) EnterArgs(command []string, interactive bool) []string {
	u := i.User()
	env := []string{"env", "-i", "HOME=" + u.Home, "USER=" + u.Name, "LOGNAME=" + u.Name, "SHELL=" + u.Shell,
		"TERM=" + orDefault(os.Getenv("TERM"), "xterm-256color"),
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	head := append([]string{"runuser", "-u", u.Name, "--"}, env...)
	// A bare interactive bash (shellN, here N -- bash) is the shell itself: a
	// bash started from the wrapper would read ~/.bashrc after env.sh and bring
	// back what env.sh unsets (the host's *_proxy).
	if interactive && len(command) == 1 && (command[0] == "bash" || command[0] == "/bin/bash") {
		command = nil
	}
	switch {
	case len(command) == 0:
		return append(head, "/bin/bash", "--rcfile", GuestDir+"/bashrc", "-i")
	case interactive:
		return append(append(head, "/bin/bash", "--rcfile", GuestDir+"/bashrc", "-i", "-c", `exec "$0" "$@"`), command...)
	default:
		return append(append(head, GuestDir+"/run"), command...)
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Describe is a one-line summary of the access, for status.
func (a Access) Describe() string {
	prof := orDefault(a.Profile, "isolated")
	user := orDefault(a.User, "dev")
	home := orDefault(a.Home.Mode, "sandbox")
	mem := orDefault(a.Resources.Memory, hostDefaultMemory())
	gpu := orDefault(a.Devices.GPU, "auto")
	return fmt.Sprintf("%s (user %s, home %s, memory %s, gpu %s)", prof, user, home, mem, gpu)
}
