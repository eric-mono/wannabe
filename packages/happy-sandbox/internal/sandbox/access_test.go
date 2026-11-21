package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSizeOrMax(t *testing.T) {
	for in, want := range map[string]int64{"": -1, "max": -1, "512M": 512 << 20, "4G": 4 << 30, "112g": 112 << 30, "1.5G": 3 << 29, "1024": 1024, "2T": 2 << 40, "16GB": 16 << 30} {
		if got, err := sizeOrMax(in); err != nil || got != want {
			t.Errorf("sizeOrMax(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"lots", "-1G", "0", "G"} {
		if _, err := sizeOrMax(bad); err == nil {
			t.Errorf("sizeOrMax(%q) accepted", bad)
		}
	}
}

func TestAccessValidate(t *testing.T) {
	if err := (Access{}).Validate(); err != nil {
		t.Fatalf("the isolated default must be valid: %v", err)
	}
	if err := HostPreset().Validate(); err != nil {
		t.Fatalf("the host preset must be valid: %v", err)
	}
	if err := (Access{Packages: []string{"openssh-client", "g++", "libc6=2.39-0ubuntu8", "python3.12-venv"}}).Validate(); err != nil {
		t.Fatalf("package names: %v", err)
	}
	bad := map[string]Access{
		"host home without host user": {Home: HomeSpec{Mode: "host"}},
		"unknown private tool":        {Home: HomeSpec{Private: []string{"vim"}}},
		"relative protect":            {Home: HomeSpec{Protect: []string{"x/y"}}},
		"bad env name":                {Env: EnvSpec{Unset: []string{"A-B"}}},
		"bad memory":                  {Resources: Resources{Memory: "lots"}},
		"shm max":                     {Resources: Resources{Shm: "max"}},
		"tiny pids":                   {Resources: Resources{Pids: "3"}},
		"bad cpus":                    {Resources: Resources{CPUs: "-2"}},
		"bad gpu":                     {Devices: Devices{GPU: "intel;arc"}},
		"device outside /dev":         {Devices: Devices{Extra: []string{"/etc/passwd"}}},
		"bad user":                    {User: "root"},
		"package option":              {Packages: []string{"-o=x"}},
		"package path":                {Packages: []string{"../x.deb"}},
	}
	for name, a := range bad {
		if a.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDefaultMemoryLeavesTheHostSome(t *testing.T) {
	mem, err := sizeOrMax(hostDefaultMemory())
	if err != nil {
		t.Fatal(err)
	}
	if total := hostMemory(); total > 4<<30 && (mem <= 0 || mem > total-4<<30) {
		t.Fatalf("memory %s of %d: the host must keep at least 4 GiB", hostDefaultMemory(), total)
	}
}

func TestResourceArgs(t *testing.T) {
	i := &Instance{}
	mem, _ := sizeOrMax(hostDefaultMemory())
	shm, _ := sizeOrMax(hostShm())
	want := fmt.Sprintf("--memory %d --pids-limit -1 --shm-size %d --ulimit host", mem, shm)
	if got := strings.Join(i.resourceArgs(), " "); got != want {
		t.Fatalf("defaults (the host's): %q, want %q", got, want)
	}
	i.Config.MemoryBytes = 8 << 30
	if got := strings.Join(i.resourceArgs(), " "); !strings.Contains(got, "--memory 8589934592") {
		t.Fatalf("legacy memory_bytes: %q", got)
	}
	i.Config.Access.Resources = Resources{Memory: "max", Pids: "4096", CPUs: "8", Shm: "16G", Ulimits: "podman"}
	got := strings.Join(i.resourceArgs(), " ")
	if strings.Contains(got, "--memory") || strings.Contains(got, "--ulimit") {
		t.Fatalf("max memory and podman ulimits must set nothing: %q", got)
	}
	for _, want := range []string{"--pids-limit 4096", "--cpus 8", "--shm-size 17179869184"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
}

func TestCheckShareTargets(t *testing.T) {
	// Not under /tmp: a same-path share there is refused (the image's /tmp).
	src, err := os.MkdirTemp(".", "share-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(src) })
	src, _ = filepath.Abs(src)
	s, err := CheckShare(Share{Source: src}, nil)
	if err != nil || s.Target != s.Source {
		t.Fatalf("an empty target is the source's path: %+v, %v", s, err)
	}
	for _, ok := range []string{"/workspace/x", "/mnt/disk", "/data", "/opt/models", "/srv/a", "/home/someone/proj"} {
		if _, err := CheckShare(Share{Source: src, Target: ok}, nil); err != nil {
			t.Errorf("target %s refused: %v", ok, err)
		}
	}
	if _, err := CheckShare(Share{Source: t.TempDir()}, nil); err == nil {
		t.Error("a same-path share under /tmp accepted")
	}
	for _, bad := range []string{"/", "/home", "/usr", "/usr/local/x", "/etc/x", "/proc", "/var/lib/x", "/tmp/x", "relative", "/mnt/../etc"} {
		if _, err := CheckShare(Share{Source: src, Target: bad}, nil); err == nil {
			t.Errorf("target %s accepted", bad)
		}
	}
	if _, err := CheckShare(Share{Source: src, Target: "/mnt/a/b"}, []Share{{Target: "/mnt/a"}}); err == nil {
		t.Error("overlapping targets accepted")
	}
}

func TestMapHostPath(t *testing.T) {
	home := os.Getenv("HOME")
	i := &Instance{Config: Config{Shares: []Share{
		{Source: "/mnt", Target: "/mnt"},
		{Source: "/srv/proj", Target: "/workspace/proj"},
	}}}
	cases := map[string]string{"/mnt/a/b": "/mnt/a/b", "/mnt": "/mnt", "/srv/proj/x": "/workspace/proj/x"}
	for in, want := range cases {
		if got, ok := i.MapHostPath(in); !ok || got != want {
			t.Errorf("MapHostPath(%s) = %s, %v; want %s", in, got, ok, want)
		}
	}
	if _, ok := i.MapHostPath("/srv/projects"); ok {
		t.Error("a sibling with the same prefix must not map")
	}
	if _, ok := i.MapHostPath(filepath.Join(home, "x")); ok {
		t.Error("the host home maps only in home mode host")
	}
	i.Config.Access = HostPreset()
	if got, ok := i.MapHostPath(filepath.Join(home, "x")); !ok || got != filepath.Join(home, "x") {
		t.Errorf("home mode host: %s, %v", got, ok)
	}
}

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/mnt/my\040disk`); got != "/mnt/my disk" {
		t.Fatalf("got %q", got)
	}
}

func TestHappyHome(t *testing.T) {
	i := &Instance{}
	if got := i.HappyHome(); got != "/home/dev/.happy" {
		t.Fatalf("isolated: %q", got)
	}
	i.Config.Access = HostPreset()
	if got := i.HappyHome(); got != "/var/lib/happy-sandbox/happy" {
		t.Fatalf("host preset: %q", got)
	}
}

func TestEnterArgs(t *testing.T) {
	i := &Instance{}
	got := i.EnterArgs(nil, true)
	if !slices.Contains(got, "--rcfile") || got[0] != "runuser" || got[2] != "dev" {
		t.Fatalf("shell: %q", got)
	}
	got = i.EnterArgs([]string{"claude", "-c"}, false)
	if n := len(got); got[n-3] != GuestDir+"/run" || got[n-2] != "claude" || got[n-1] != "-c" {
		t.Fatalf("command: %q", got)
	}
	got = i.EnterArgs([]string{"claude"}, true)
	if got[len(got)-1] != "claude" || !slices.Contains(got, "-i") {
		t.Fatalf("interactive command: %q", got)
	}
	// shellN runs `here N -- bash`: that is the rcfile shell, not a bash
	// started from it (which would read ~/.bashrc after env.sh).
	if got := i.EnterArgs([]string{"bash"}, true); !slices.Equal(got, i.EnterArgs(nil, true)) {
		t.Fatalf("bare bash: %q", got)
	}
	if got := i.EnterArgs([]string{"bash"}, false); slices.Contains(got, "--rcfile") {
		t.Fatalf("bash without a terminal: %q", got)
	}
}

func TestGuestFilesCarryTheEnvironment(t *testing.T) {
	i := &Instance{Dir: t.TempDir(), Config: Config{Name: "t", Access: HostPreset()}}
	i.Config.Access.Env.Set = map[string]string{"A": "it's"}
	if err := i.writeGuestFiles(); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(i.Dir, "guest", "env.sh"))
	for _, want := range []string{"unset http_proxy", "export A='it'\\''s'", "export CLAUDE_CONFIG_DIR=/var/lib/happy-sandbox/claude", "export CODEX_HOME=/var/lib/happy-sandbox/codex", "export HAPPY_HOME_DIR=/var/lib/happy-sandbox/happy", "HAPPY_DIST_HOME=/home/dev/.happy-dist"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("env.sh lacks %q:\n%s", want, env)
		}
	}
	for _, f := range []string{"bashrc", "run", "hsb-setup.sh", "hsb-setup.service"} {
		if _, err := os.Stat(filepath.Join(i.Dir, "guest", f)); err != nil {
			t.Error(err)
		}
	}
}

func TestDeviceArgs(t *testing.T) {
	i := &Instance{}
	i.Config.Access.Devices = Devices{GPU: "none", Extra: []string{"/dev/ttyUSB0"}}
	if got := strings.Join(i.deviceArgs(nil), " "); got != "--label happy-sandbox.gpu=- --device /dev/ttyUSB0" {
		t.Fatalf("none: %q", got)
	}
	g := &GPU{Devices: [][2]string{{"/dev/null", "/dev/nvidia0"}}, Env: []string{"NVIDIA_VISIBLE_DEVICES=void"}, Key: "k1"}
	i.Config.Access.Devices = Devices{GPU: "nvidia"}
	if got := strings.Join(i.deviceArgs(g), " "); got != "--label happy-sandbox.gpu=k1 --device /dev/null:/dev/nvidia0 -e NVIDIA_VISIBLE_DEVICES=void" {
		t.Fatalf("nvidia: %q", got)
	}
	i.Config.Access.Devices = Devices{GPU: "amd"}
	if got := strings.Join(i.deviceArgs(nil), " "); got != "--label happy-sandbox.gpu=amd --device /dev/kfd --device /dev/dri --group-add keep-groups" {
		t.Fatalf("amd: %q", got)
	}
	// The older top-level "gpu" applies when access says nothing.
	i.Config.Access.Devices = Devices{}
	i.Config.GPU = "none"
	if i.GPU() != "" {
		t.Fatalf("legacy none: %q", i.GPU())
	}
	if g, err := i.FindGPU(); g != nil || err != nil {
		t.Fatalf("legacy none: %v %v", g, err)
	}
	i.Config.GPU = ""
	i.Config.Access.Devices = Devices{GPU: "auto"}
	want := ""
	if NvidiaCDI() {
		want = "nvidia"
	} else if amdHost() {
		want = "amd"
	}
	if i.GPU() != want {
		t.Fatalf("auto: %q, want %q", i.GPU(), want)
	}
	if err := (Access{Devices: Devices{GPU: "GPU-1234-abcd"}}).Validate(); err != nil {
		t.Fatalf("a GPU UUID: %v", err)
	}
	if err := (Access{Devices: Devices{GPU: "0;rm"}}).Validate(); err == nil {
		t.Fatal("a bad gpu passed")
	}
}
