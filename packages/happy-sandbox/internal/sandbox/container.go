package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Podman is the engine: $HSB_PODMAN (development: tools/podman-dev), else
// the user's rootless podman.
func Podman() string {
	if p := os.Getenv("HSB_PODMAN"); p != "" {
		return p
	}
	return "podman"
}

func podman(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, Podman(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		// Podman 3.4 prints harmless errors about removed storage; keep the last real line.
		lines := strings.Split(msg, "\n")
		return out.String(), fmt.Errorf("podman %s: %v: %s", args[0], err, lines[len(lines)-1])
	}
	return out.String(), nil
}

// SELinux reports whether this host confines containers with SELinux (CentOS,
// RHEL, Fedora). Then every host directory a container may write needs a
// container label: the sandbox's own files get a private one (:Z), shares a
// shared one (:z, so several sandboxes can use the same directory).
func SELinux(ctx context.Context) bool {
	out, err := podman(ctx, "info", "--format", "{{.Host.Security.SELinuxEnabled}}")
	return err == nil && strings.TrimSpace(out) == "true"
}

type ContainerState struct {
	Exists  bool
	Running bool
	// Status is podman's word for it: running, exited, stopping, ...
	Status string
}

func (i *Instance) State(ctx context.Context) ContainerState {
	out, err := podman(ctx, "container", "inspect", "--format", "{{.State.Running}} {{.State.Status}}", i.Container())
	if err != nil {
		return ContainerState{}
	}
	running, status, _ := strings.Cut(strings.TrimSpace(out), " ")
	return ContainerState{Exists: true, Running: running == "true", Status: status}
}

// settleStopped gets a container out of "stopping": Podman 3.4 sometimes
// times out waiting for conmon's exit file (busy host) and leaves the
// container there, where it can be neither started nor stopped normally.
func (i *Instance) settleStopped(ctx context.Context) ContainerState {
	for attempt := 0; attempt < 3; attempt++ {
		st := i.State(ctx)
		if !st.Exists || (!st.Running && st.Status != "stopping") {
			return st
		}
		if attempt == 0 {
			_, _ = podman(ctx, "stop", "--time", "5", i.Container())
		} else {
			_, _ = podman(ctx, "kill", i.Container())
		}
		time.Sleep(2 * time.Second)
	}
	return i.State(ctx)
}

// Rootless reports whether podman runs rootless; the sandbox refuses otherwise.
func Rootless(ctx context.Context) error {
	out, err := podman(ctx, "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "true" {
		return errors.New("podman is not rootless; happy-sandbox runs only on rootless podman")
	}
	return nil
}

// seccompProfile writes the host's default profile plus fchmodat2 -> ENOSYS
// (tools/seccomp-profile explains why).
func (i *Instance) seccompProfile() (string, error) {
	var profile map[string]any
	data, err := os.ReadFile("/usr/share/containers/seccomp.json")
	if err != nil {
		return "", fmt.Errorf("cannot read the host's seccomp profile: %v", err)
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return "", err
	}
	syscalls, _ := profile["syscalls"].([]any)
	known := false
	for _, r := range syscalls {
		names, _ := r.(map[string]any)["names"].([]any)
		for _, n := range names {
			if n == "fchmodat2" {
				known = true
			}
		}
	}
	if !known {
		profile["syscalls"] = append(syscalls, map[string]any{"names": []string{"fchmodat2"}, "action": "SCMP_ACT_ERRNO", "errnoRet": 38})
	}
	out, _ := json.Marshal(profile)
	path := filepath.Join(i.Dir, "seccomp.json")
	return path, os.WriteFile(path, out, 0o600)
}

// createArgs: no network of its own (the TAP comes from the host), only
// NET_ADMIN + /dev/net/tun added, the host user's uid inside (keep-id), the
// identity (machine id, hostname, time zone, locale) from identity.json, the
// home and the shares as bind mounts (read-only ones stay so: no
// CAP_SYS_ADMIN), and what access grants of the host: its user and home,
// resources and devices.
func (i *Instance) createArgs(seccomp string, gpu *GPU) []string {
	c, id, a, u := i.Config, i.Identity, i.Config.Access, i.User()
	homeMode := "sandbox"
	if a.HostHome() {
		homeMode = "host"
	}
	args := []string{"create", "--name", i.Container(), "--label", "happy-sandbox=" + c.Name,
		"--label", identityLabel + "=" + id.Hash(),
		// The network link's socket directory (ServeNetwork, hsb-link).
		"-v", i.mountSpec(i.LinkDir(), LinkDirInContainer, "", privateLabel),
		"--network", "none", "--security-opt", "seccomp=" + seccomp,
		"--userns=keep-id", "--user", "0:0",
		"-e", "HSB_UID=" + strconv.Itoa(u.UID), "-e", "HSB_GID=" + strconv.Itoa(u.GID),
		"-e", "HSB_USER=" + u.Name, "-e", "HSB_GROUP=" + u.Group, "-e", "HSB_HOME=" + u.Home,
		"-e", "HSB_SHELL=" + u.Shell, "-e", "HSB_HOME_MODE=" + homeMode,
		"-e", "HSB_TIMEZONE=" + id.Timezone, "-e", "HSB_LOCALE=" + id.Locale, "-e", "TZ=" + id.Timezone,
		"-v", i.mountSpec(i.MachineIDPath(), "/etc/machine-id", "ro", privateLabel),
		// This happy-sandbox's guest files: setup, environment, entry scripts.
		"-v", i.mountSpec(i.path("guest"), GuestDir, "ro", privateLabel),
		"-v", i.mountSpec(i.path("guest", "hsb-setup.sh"), "/usr/local/lib/happy-sandbox/hsb-setup.sh", "ro", privateLabel),
		"-v", i.mountSpec(i.path("guest", "hsb-setup.service"), "/etc/systemd/system/hsb-setup.service", "ro", privateLabel),
		"--cgroupns", "private", "--ipc", "private", "--hostname", id.Hostname,
		// Not podman's /etc/hosts (it names the container, the host and the
		// host's LAN address): hsb-setup writes one with this host name only.
		// HOSTNAME too: the image's (from its build container) would win.
		"--no-hosts", "-e", "HOSTNAME=" + id.Hostname,
		"--device", "/dev/net/tun", "--cap-add", "NET_ADMIN",
		"--tmpfs", "/run", "--tmpfs", "/tmp", "--stop-signal", "SIGRTMIN+3", "--stop-timeout", "30",
		"--systemd", "always", "--pull", "never", "--http-proxy=false"}
	args = append(args, i.resourceArgs()...)
	args = append(args, i.deviceArgs(gpu)...)
	// The home: the host's own (read-write, its own path) or the private one.
	// The crontab lives in the container: hsb-setup writes our lines and the
	// installer's nightly line comes back with the next happy update.
	if a.HostHome() {
		args = append(args, "-v", i.mountSpec(u.Home, u.Home, "rw", noLabel))
	} else {
		args = append(args, "-v", i.mountSpec(i.HomeDir(), u.Home, "", privateLabel))
	}
	for _, t := range a.Home.Private {
		args = append(args, "-v", i.mountSpec(i.StateDir(t), stateRoot+"/"+t, "rw", privateLabel))
	}
	if a.HostHome() {
		// The apt hook's record (hsb-apt-record.sh) outside the shared home:
		// /home/dev is the container layer's then.
		args = append(args, "-v", i.mountSpec(i.StateDir("apt"), stateRoot+"/apt", "rw", privateLabel))
	}
	if len(a.Packages) > 0 {
		// apt's package lists outlive the container: after a recreate only the
		// packages themselves are fetched again (the lists are tens of MB).
		args = append(args, "-v", i.mountSpec(i.AptListsDir(), "/var/lib/apt/lists", "rw", privateLabel))
	}
	for _, s := range c.Shares {
		mode := "rw"
		if s.ReadOnly {
			mode = "ro"
		}
		args = append(args, "-v", i.mountSpec(s.Source, s.Target, mode, sharedLabel))
		if s.Submounts {
			for _, sub := range submounts(s.Source) {
				args = append(args, "-v", i.mountSpec(sub, s.Target+sub[len(s.Source):], mode, noLabel))
			}
		}
	}
	// Last, so nothing mounted above covers them: the sandbox may not change
	// its own gate (configuration, node, this program).
	for _, p := range a.Home.Protect {
		hp := expandHome(p)
		if t, ok := i.MapHostPath(hp); ok && exists(hp) {
			args = append(args, "-v", i.mountSpec(hp, t, "ro", noLabel))
		}
	}
	// By id: the container is from exactly the image Ensure compared.
	return append(args, i.imageID, "/sbin/init")
}

const (
	identityLabel = "happy-sandbox.identity"
	// specLabel is a hash of everything the container is created with: any
	// change (access, shares, identity, image) recreates it at the next up.
	specLabel = "happy-sandbox.spec"
)

func specHash(args []string) string {
	sum := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// gpuLabel names the GPU spec and choice a container was made with ("-":
// none); in the spec hash too, so a driver update recreates the container.
const gpuLabel = "happy-sandbox.gpu"

type labelKind int

const (
	noLabel      labelKind = iota
	privateLabel           // the sandbox's own files (:Z)
	sharedLabel            // a share, possibly used by other sandboxes (:z)
)

// mountSpec builds a `-v` value: source:target, then mode (ro/rw/"") and the
// SELinux relabel option as comma-separated options in the *third* colon
// field. The label is a mount option, not part of the target: appending it to
// the target made podman create a directory literally named "/home/dev,Z".
func (i *Instance) mountSpec(source, target, mode string, kind labelKind) string {
	opts := []string{}
	if mode != "" {
		opts = append(opts, mode)
	}
	if l := i.selinuxLabel(source, kind); l != "" {
		opts = append(opts, l)
	}
	spec := source + ":" + target
	if len(opts) > 0 {
		spec += ":" + strings.Join(opts, ",")
	}
	return spec
}

// selinuxLabel is "Z" for the sandbox's own files, "z" for a share, "" with no
// SELinux or for a share the filesystem cannot relabel (CIFS / NFS carry their
// label from the mount options instead).
func (i *Instance) selinuxLabel(source string, kind labelKind) string {
	if !i.selinux || kind == noLabel {
		return ""
	}
	if kind == privateLabel {
		return "Z"
	}
	if !relabelable(source) {
		return ""
	}
	return "z"
}

// relabelable: SELinux can label files of local filesystems only.
func relabelable(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return true // do not refuse a share over an odd statfs error
	}
	switch uint32(st.Type) {
	case 0xFF534D42, // cifs
		0x6969,     // nfs
		0x517B,     // smb
		0xFE534D42, // smb2
		0x01021997, // 9p (v9fs)
		0x0000F15F, // vboxsf
		0x65735546: // fuse
		return false
	}
	return true
}

// shareLabelOpt: the label for a share, only when its filesystem can carry one.
func (i *Instance) shareLabelOpt(source string) string {
	if !i.selinux || !relabelable(source) {
		return ""
	}
	return ",z"
}

// fillHome creates the home from the image's /home/dev the first time: into
// a staging directory, renamed into place when complete.
//
// The copy goes to a *new* directory the container creates itself
// (<staging>/home), never onto the mount point: `cp -a` ends by setting the
// destination's own timestamps, and the mount point belongs to the host user,
// which container root may not be allowed to touch (it is a sub-id, and how
// keep-id maps it differs between Podman versions and hosts).
func (i *Instance) fillHome(ctx context.Context, seccomp string) error {
	if _, err := os.Stat(i.HomeDir()); err == nil {
		return nil
	}
	staging := i.path("home.new")
	// A fill that did not finish: its files may belong to sub-ids.
	if _, err := os.Stat(staging); err == nil {
		if _, err := podman(ctx, "unshare", "rm", "-rf", staging); err != nil {
			return err
		}
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return err
	}
	owner := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	if _, err := podman(ctx, "run", "--rm", "--network", "none", "--security-opt", "seccomp="+seccomp,
		"--userns=keep-id", "--user", "0:0", "--pull", "never", "-v", i.mountSpec(staging, "/mnt", "", privateLabel),
		i.Config.Image, "sh", "-c", "cp -a /home/dev /mnt/home && chown -R "+owner+" /mnt/home"); err != nil {
		return fmt.Errorf("filling the home from the image: %v", err)
	}
	if err := os.Rename(filepath.Join(staging, "home"), i.HomeDir()); err != nil {
		return err
	}
	return os.Remove(staging)
}

// Ensure creates the container if needed and starts it. A container made
// for another identity (identity.json was edited), from an older image or
// with other limits (memory_bytes, cpus, pids_limit) or another GPU spec
// (a driver update rewrites it) is recreated: the home stays, system changes outside it are reset. It
// reports whether it did.
func (i *Instance) Ensure(ctx context.Context) (recreated bool, err error) {
	if i.Identity == nil {
		return false, errors.New("no identity")
	}
	// identity.json is the source: the mounted machine-id file follows it.
	if err := writeFile(i.MachineIDPath(), []byte(i.Identity.MachineID+"\n"), 0o644); err != nil {
		return false, err
	}
	i.selinux = SELinux(ctx)
	imageID, err := podman(ctx, "image", "inspect", "--format", "{{.Id}}", i.Config.Image)
	if err != nil {
		return false, fmt.Errorf("image %s is not installed: happy-sandbox image load FILE", i.Config.Image)
	}
	i.imageID = strings.TrimSpace(imageID)
	if err := i.Config.Access.Validate(); err != nil {
		return false, err
	}
	if err := CheckHostnameFree(i.Identity.Hostname, i.Config.Name); err != nil {
		return false, err
	}
	gpu, err := i.FindGPU()
	if err != nil {
		return false, err
	}
	if err := i.writeGuestFiles(); err != nil {
		return false, err
	}
	for _, t := range append([]string{"apt"}, i.Config.Access.Home.Private...) {
		if err := os.MkdirAll(i.StateDir(t), 0o700); err != nil {
			return false, err
		}
	}
	if len(i.Config.Access.Packages) > 0 {
		if err := os.MkdirAll(i.AptListsDir(), 0o755); err != nil {
			return false, err
		}
	}
	seccomp, err := i.seccompProfile()
	if err != nil {
		return false, err
	}
	args := i.createArgs(seccomp, gpu)
	spec := specHash(args)
	st := i.State(ctx)
	if st.Exists {
		out, _ := podman(ctx, "container", "inspect", "--format", "{{index .Config.Labels \""+specLabel+"\"}}", i.Container())
		if strings.TrimSpace(out) != spec {
			if err := i.Remove(ctx); err != nil {
				return false, err
			}
			st, recreated = ContainerState{}, true
		}
	}
	if !st.Exists {
		if !i.Config.Access.HostHome() {
			if err := i.fillHome(ctx, seccomp); err != nil {
				return false, err
			}
		}
		if err := os.MkdirAll(i.LinkDir(), 0o700); err != nil {
			return false, err
		}
		create := append([]string{args[0], "--label", specLabel + "=" + spec}, args[1:]...)
		if _, err := podman(ctx, create...); err != nil {
			if strings.Contains(err.Error(), "cgroup controller") {
				return false, fmt.Errorf("%v: a limit in config.json needs a cgroup controller this host does not delegate (%s)", err, limitsFix)
			}
			return false, err
		}
	}
	if err := i.seedClaudeSettings(); err != nil { // after fillHome: the home is there
		return recreated, err
	}
	if st.Status == "stopping" {
		st = i.settleStopped(ctx)
	}
	if !st.Running {
		if _, err := podman(ctx, "start", i.Container()); err != nil {
			return recreated, err
		}
	}
	return recreated, nil
}

// InstallHelper copies this binary in (the guest's TAP helper must match it).
func (i *Instance) InstallHelper(ctx context.Context) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := podman(ctx, "cp", self, i.Container()+":/usr/local/lib/happy-sandbox/happy-sandbox"); err != nil {
		return err
	}
	_, err = podman(ctx, "exec", "--user", "0", i.Container(), "chmod", "0755", "/usr/local/lib/happy-sandbox/happy-sandbox")
	return err
}

func (i *Instance) Stop(ctx context.Context) error {
	if st := i.State(ctx); !st.Running && st.Status != "stopping" {
		return nil
	}
	if _, err := podman(ctx, "stop", "--time", "30", i.Container()); err != nil {
		// Podman 3.4 can lose the exit file and leave it "stopping": finish the job.
		if st := i.settleStopped(ctx); st.Running || st.Status == "stopping" {
			return err
		}
	}
	return nil
}

// Remove deletes the container only: the sandbox directory (home, identity) stays.
func (i *Instance) Remove(ctx context.Context) error {
	if !i.State(ctx).Exists {
		return nil
	}
	_, err := podman(ctx, "rm", "--force", i.Container())
	return err
}

// AsUser runs a shell command in the container as the guest user with its
// login environment. runuser sets the user's groups the normal way (podman
// exec --user would add group 0 on Podman 3.4).
func (i *Instance) AsUser(ctx context.Context, command string) (string, error) {
	args := append([]string{"exec", "--user", "0", "-w", i.User().Home, i.Container()},
		i.EnterArgs([]string{"bash", "-c", command}, false)...)
	return podman(ctx, args...)
}

// Destroy removes the container and the whole sandbox directory: home,
// identity, node file, logs. Files in the home may belong to sub-ids (made
// by root or other users inside), so it removes them in podman's namespace.
func (i *Instance) Destroy(ctx context.Context) error {
	if err := i.Remove(ctx); err != nil {
		return err
	}
	if _, err := os.Stat(i.Dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_, err := podman(ctx, "unshare", "rm", "-rf", i.Dir)
	return err
}

// LoadImage installs an image archive (podman save / OCI archive) and tags it
// as the default image.
func LoadImage(ctx context.Context, file string) error {
	out, err := podman(ctx, "load", "--quiet", "--input", file)
	if err != nil {
		return err
	}
	// "Loaded image(s): NAME[,NAME…]" (Podman 3.4) / "Loaded image: NAME".
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	loaded := strings.TrimSpace(strings.Split(last[strings.Index(last, ":")+1:], ",")[0])
	if loaded == "" {
		return fmt.Errorf("podman load: no image in %q", out)
	}
	if loaded != DefaultImage {
		_, err = podman(ctx, "tag", loaded, DefaultImage)
	}
	return err
}

func ListImages(ctx context.Context) (string, error) {
	return podman(ctx, "images", "--filter", "reference=localhost/happy-sandbox*", "--format", "{{.Repository}}:{{.Tag}}  {{.ID}}  {{.Created}}  {{.Size}}")
}

// EnsurePackages installs the access.packages that are missing, as root
// through the gate (the network must be open). A recreated container starts
// from the image, so this runs at every up; with nothing missing it is one
// dpkg query. It returns what it installed.
func (i *Instance) EnsurePackages(ctx context.Context) ([]string, error) {
	pkgs := i.Config.Access.Packages
	if len(pkgs) == 0 {
		return nil, nil
	}
	names := make([]string, len(pkgs))
	for k, p := range pkgs {
		names[k], _, _ = strings.Cut(p, "=")
	}
	// One line per package: "install ok installed" when it is there.
	out, _ := podman(ctx, append([]string{"exec", "--user", "0", i.Container(),
		"dpkg-query", "-W", "-f", "${db:Status-Status} ${Package}\n"}, names...)...)
	have := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "installed" {
			have[f[1]] = true
		}
	}
	var missing []string
	for k, p := range pkgs {
		if !have[names[k]] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	// The image ships without package lists: fetch them once per container.
	script := `set -e
export DEBIAN_FRONTEND=noninteractive
[ -n "$(ls -A /var/lib/apt/lists 2>/dev/null | grep -v -e lock -e partial)" ] || apt-get update -qq
apt-get install -y -qq --no-install-recommends "$@" >/dev/null || { apt-get update -qq; apt-get install -y -qq --no-install-recommends "$@" >/dev/null; }`
	if _, err := podman(ctx, append([]string{"exec", "--user", "0", i.Container(), "bash", "-c", script, "hsb-packages"}, missing...)...); err != nil {
		return nil, fmt.Errorf("installing %s: %v", strings.Join(missing, " "), err)
	}
	return missing, nil
}
