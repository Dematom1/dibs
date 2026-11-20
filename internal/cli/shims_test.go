package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	outputpkg "github.com/Dematom1/dibs/internal/output"
)

func TestClassifyShim(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "app", "sub")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "compose.yaml"), []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	overridden := filepath.Join(dir, "over")
	for _, name := range []string{"docker-compose.yml", "docker-compose.override.yml"} {
		if err := os.MkdirAll(overridden, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(overridden, name), []byte("services: {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	type want struct{ adapter, file, project string }
	cases := []struct {
		tool, line, cwd string
		env             map[string]string
		want            want
		refused         bool
	}{
		// Reads, deletes, and other verbs pass through.
		{tool: "docker", line: "ps -a"},
		{tool: "docker", line: "compose down --volumes"},
		{tool: "docker", line: "compose -f x.yml ps"},
		{tool: "docker", line: "compose up --help"},
		{tool: "docker", line: "--help"},
		{tool: "docker", line: "run --rm -it alpine sh -c 'ls -d /'"},
		{tool: "docker", line: "run -e D=1 alpine true"},
		{tool: "docker", line: "run -edebug alpine true"},
		{tool: "docker", line: "run -v /data:/data alpine true"},
		{tool: "docker", line: "run --name d alpine ls -d"},
		{tool: "docker", line: "run --detach=false alpine true"},
		{tool: "kind", line: "get clusters"},
		{tool: "kind", line: "delete cluster --name dibs-x"},
		{tool: "kind", line: "create cluster --help"},
		{tool: "k3d", line: "cluster list"},
		{tool: "k3d", line: "cluster delete x"},
		{tool: "orb", line: "list"},
		{tool: "orbctl", line: "create --help"},
		// Creates dibs can lease exactly.
		{tool: "docker", line: "compose up -d", cwd: nested, want: want{adapter: "compose", file: filepath.Join(dir, "app", "compose.yaml")}},
		{tool: "docker", line: "compose up", cwd: dir, want: want{adapter: "compose", file: filepath.Join(dir, "x.yml")}, env: map[string]string{"COMPOSE_FILE": "x.yml"}},
		{tool: "docker", line: "compose -f /abs/c.yml up --detach", cwd: dir, want: want{adapter: "compose", file: "/abs/c.yml"}},
		{tool: "docker", line: "compose --file=c.yml up -d", cwd: dir, want: want{adapter: "compose", file: filepath.Join(dir, "c.yml")}},
		{tool: "kind", line: "create cluster", want: want{adapter: "kind"}},
		{tool: "k3d", line: "cluster create", want: want{adapter: "k3d"}},
		// Creates dibs cannot lease exactly are refused.
		{tool: "docker", line: "compose up -d --build", cwd: nested, want: want{adapter: "compose"}, refused: true},
		{tool: "docker", line: "compose up -d web", cwd: nested, want: want{adapter: "compose"}, refused: true},
		{tool: "docker", line: "--context x compose up -d", cwd: nested, want: want{adapter: "compose"}, refused: true},
		{tool: "docker", line: "compose -f a.yml -f b.yml up -d", cwd: dir, want: want{adapter: "compose"}, refused: true},
		{tool: "docker", line: "compose -f - up -d", cwd: dir, want: want{adapter: "compose"}, refused: true},
		{tool: "docker", line: "compose up -d", cwd: dir, want: want{adapter: "compose"}, env: map[string]string{"COMPOSE_FILE": "a.yml:b.yml"}, refused: true},
		{tool: "docker", line: "compose up -d", cwd: overridden, want: want{adapter: "compose"}, refused: true},
		{tool: "docker", line: "compose up -d", cwd: "/", want: want{adapter: "compose"}, refused: true},
		{tool: "docker", line: "compose -p dibs-abc up -d --build", cwd: nested, want: want{adapter: "compose", project: "dibs-abc"}, refused: true},
		{tool: "docker", line: "compose up -d", cwd: nested, env: map[string]string{"COMPOSE_PROJECT_NAME": "mine"}, want: want{adapter: "compose", project: "mine"}, refused: true},
		{tool: "docker", line: "run -d alpine sleep 1", want: want{adapter: "docker-run"}, refused: true},
		{tool: "docker", line: "run -dit alpine sh", want: want{adapter: "docker-run"}, refused: true},
		{tool: "docker", line: "run -itd alpine sh", want: want{adapter: "docker-run"}, refused: true},
		{tool: "docker", line: "--context x container run --name n --detach alpine", want: want{adapter: "docker-run"}, refused: true},
		{tool: "kind", line: "create cluster --name foo", want: want{adapter: "kind"}, refused: true},
		{tool: "kind", line: "-v 3 create cluster", want: want{adapter: "kind"}, refused: true},
		{tool: "k3d", line: "cluster create mine", want: want{adapter: "k3d"}, refused: true},
		{tool: "orb", line: "create ubuntu box", want: want{adapter: "orb"}, refused: true},
		{tool: "orbctl", line: "create ubuntu", want: want{adapter: "orb"}, refused: true},
	}
	for _, c := range cases {
		args := shellWords(c.line)
		getenv := func(name string) string { return c.env[name] }
		got := classifyShim(c.tool, args, getenv, c.cwd)
		if (shimRoute{adapter: got.adapter, file: got.file, project: got.project}) != (shimRoute{adapter: c.want.adapter, file: c.want.file, project: c.want.project}) || (got.refusal != "") != c.refused || (got.refusal != "") != (got.hint != "") {
			t.Errorf("%s %s: got %+v, want %+v refused=%v", c.tool, c.line, got, c.want, c.refused)
		}
	}
}

// shellWords splits a test command line on spaces, honoring single quotes.
func shellWords(line string) []string {
	var words []string
	for i, part := range strings.Split(line, "'") {
		if i%2 == 1 {
			words = append(words, part)
		} else {
			words = append(words, strings.Fields(part)...)
		}
	}
	return words
}

func installTestShims(t *testing.T) string {
	t.Helper()
	t.Setenv("DIBS_SHIMS", "1")
	dir := filepath.Join(t.TempDir(), "shims")
	var out bytes.Buffer
	if code := runWithOutput(context.Background(), []string{"setup", "shims", "--dir", dir}, &out, outputpkg.JSON); code != 0 {
		t.Fatalf("setup shims exited %d: %s", code, out.String())
	}
	return dir
}

func TestSetupShims(t *testing.T) {
	home := hookHome(t)
	t.Setenv("XDG_DATA_HOME", "")
	var out bytes.Buffer
	if code := runWithOutput(context.Background(), []string{"setup", "shims"}, &out, outputpkg.JSON); code != 2 || !strings.Contains(out.String(), "shims are off") {
		t.Fatalf("setup shims without the opt-in exited %d: %s", code, out.String())
	}
	if files := hookFiles(t, home); len(files) != 0 {
		t.Fatalf("refused setup wrote %v", files)
	}
	t.Setenv("DIBS_SHIMS", "1")
	for i, status := range []string{`"status":"installed"`, `"status":"unchanged (no-op)"`} {
		out.Reset()
		if code := runWithOutput(context.Background(), []string{"setup", "shims"}, &out, outputpkg.JSON); code != 0 || !strings.Contains(out.String(), status) {
			t.Fatalf("run %d exited %d: %s", i, code, out.String())
		}
	}
	dir := filepath.Join(home, ".local", "share", "dibs", "shims")
	for _, tool := range shimTools {
		info, err := os.Stat(filepath.Join(dir, tool))
		if err != nil || info.Mode().Perm()&0100 == 0 {
			t.Fatalf("%s shim missing or not executable: %v", tool, err)
		}
	}
	other := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "kind"), []byte("#!/bin/sh\necho mine\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runWithOutput(context.Background(), []string{"setup", "shims", "--dir", other}, &out, outputpkg.JSON); code != 1 || !strings.Contains(out.String(), "unmanaged") {
		t.Fatalf("unmanaged file not refused: %d %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(other, "docker")); err == nil {
		t.Fatal("a refused setup wrote another shim")
	}
}

// fakeTool writes a tool that prints its arguments one per line and exits 7.
func fakeTool(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho " + name + " from " + dir + "\nfor a in \"$@\"; do printf '[%s]\\n' \"$a\"; done\nexit 7\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestShimPassesThroughWithoutTask(t *testing.T) {
	shims := installTestShims(t)
	second := installTestShims(t)
	real := filepath.Join(t.TempDir(), "real")
	for _, tool := range shimTools {
		fakeTool(t, real, tool)
	}
	args := []string{"compose", "up", "-d", "a b", `"q"`, "$HOME", "", "*"}
	// Stacked and repeated shim directories still reach the real tool once.
	for _, path := range []string{shims + ":" + real, shims + ":" + second + ":" + shims + ":" + real} {
		cmd := exec.Command(filepath.Join(shims, "docker"), args...)
		cmd.Env = append(os.Environ(), "PATH="+path+":/usr/bin:/bin", "DIBS_TASK=")
		out, err := cmd.Output()
		if code := cmd.ProcessState.ExitCode(); code != 7 {
			t.Fatalf("PATH %s: exit %d (%v): %s", path, code, err, out)
		}
		want := "docker from " + real + "\n[compose]\n[up]\n[-d]\n[a b]\n[\"q\"]\n[$HOME]\n[]\n[*]\n"
		if string(out) != want {
			t.Fatalf("PATH %s: argv changed:\n%s\nwant:\n%s", path, out, want)
		}
	}
	cmd := exec.Command(filepath.Join(shims, "kind"), "version")
	cmd.Env = append(os.Environ(), "PATH="+shims+":/usr/bin:/bin")
	out, _ := cmd.CombinedOutput()
	if code := cmd.ProcessState.ExitCode(); code != 127 || !strings.Contains(string(out), "no kind on PATH") {
		t.Fatalf("missing real tool: exit %d: %s", code, out)
	}
}

func TestShimHandsTaskCallsToDibs(t *testing.T) {
	shims := installTestShims(t)
	real := filepath.Join(t.TempDir(), "real")
	fakeTool(t, real, "k3d")
	fakeTool(t, real, "dibs")
	cmd := exec.Command(filepath.Join(shims, "k3d"), "cluster", "create", "a b")
	cmd.Env = append(os.Environ(), "PATH="+shims+":"+real+":/usr/bin:/bin", "DIBS_TASK=t1")
	out, _ := cmd.Output()
	want := "dibs from " + real + "\n[__shim]\n[k3d]\n[" + filepath.Join(real, "k3d") + "]\n[cluster]\n[create]\n[a b]\n"
	if string(out) != want || cmd.ProcessState.ExitCode() != 7 {
		t.Fatalf("handoff to dibs:\n%s\nwant:\n%s", out, want)
	}
}

func TestRunShim(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	var execed []string
	original := execTool
	t.Cleanup(func() { execTool = original })
	execTool = func(real string, args []string) error {
		execed = append([]string{real}, args...)
		return nil
	}
	shim := func(env map[string]string, args ...string) (int, string) {
		t.Helper()
		for _, name := range []string{"DIBS_TASK", "DIBS_SHIMS", "DIBS_OWNER", "DIBS_SCOPE", "DIBS_SCOPES"} {
			t.Setenv(name, env[name])
			if env[name] == "" {
				os.Unsetenv(name)
			}
		}
		execed = nil
		var out bytes.Buffer
		code := run(context.Background(), append([]string{shimCommand}, args...), &out)
		return code, out.String()
	}
	on := map[string]string{"DIBS_TASK": "t1", "DIBS_SHIMS": "1"}
	// No task, or the opt-in off, is a pure passthrough even for a create verb.
	for _, env := range []map[string]string{{"DIBS_SHIMS": "1"}, {"DIBS_TASK": "t1"}, {"DIBS_TASK": "t1", "DIBS_SHIMS": "0"}} {
		shim(env, "kind", "/real/kind", "create", "cluster", "--name", "a b")
		if strings.Join(execed, "|") != "/real/kind|create|cluster|--name|a b" {
			t.Fatalf("env %v: execed %q", env, execed)
		}
	}
	shim(on, "kind", "/real/kind", "get", "clusters")
	if strings.Join(execed, "|") != "/real/kind|get|clusters" {
		t.Fatalf("read did not pass through: %q", execed)
	}
	code, out := shim(on, "docker", "/real/docker", "run", "-d", "alpine")
	if code != 2 || execed != nil || !strings.Contains(out, "dibs create --adapter compose") {
		t.Fatalf("docker run -d: exit %d execed %q: %s", code, execed, out)
	}
	code, out = shim(on, "kind", "/real/kind", "create", "cluster")
	if code != 2 || execed != nil || !strings.Contains(out, "dibs scope start --key t1") {
		t.Fatalf("ownerless create: exit %d execed %q: %s", code, execed, out)
	}
	// A project that is not a dibs compose lease never passes through.
	code, _ = shim(on, "docker", "/real/docker", "compose", "-p", "dibs-"+strings.Repeat("a", 32), "up", "-d", "--build")
	if code != 2 || execed != nil {
		t.Fatalf("unleased dibs- project: exit %d execed %q", code, execed)
	}
}
