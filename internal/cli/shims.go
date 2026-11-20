package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	atomicfilepkg "github.com/Dematom1/dibs/internal/atomicfile"
	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	remotepkg "github.com/Dematom1/dibs/internal/remote"
)

// shimCommand is the hidden command a shim runs when DIBS_TASK is set; it is not part of the CLI contract.
const shimCommand = "__shim"

const shimMarker = "# dibs-shim: managed by dibs setup shims"

var shimTools = []string{"docker", "kind", "k3d", "orb", "orbctl"}

func shimsEnabled() bool { return optedIn("DIBS_SHIMS", configpkg.Shims) }

// shimScript finds the real tool after the last PATH entry holding this shim, so
// each hop moves strictly later in PATH: a shim never execs itself, and stacked or
// repeated shim directories still terminate. Without
// DIBS_TASK it is a plain exec, so a human's call never reaches dibs.
func shimScript(self, tool string) string {
	return `#!/bin/sh
` + shimMarker + `
self=` + remotepkg.ShellQuote(self) + `
tool=` + tool + `
real= first= seen=
set -f; IFS=:
for dir in $PATH; do
  candidate=${dir:-.}/$tool
  if [ "$candidate" -ef "$self" ]; then seen=1 real=; continue; fi
  [ -f "$candidate" ] && [ -x "$candidate" ] || continue
  [ -n "$first" ] || first=$candidate
  [ -z "$seen" ] || [ -n "$real" ] || real=$candidate
done
unset IFS; set +f
[ -n "$seen" ] || real=$first
if [ -z "$real" ]; then
  echo "dibs shim: no $tool on PATH after $self" >&2
  exit 127
fi
if [ -n "${DIBS_TASK:-}" ]; then
  if ! command -v dibs >/dev/null 2>&1; then
    echo "dibs shim: DIBS_TASK is set but dibs is not on PATH" >&2
    exit 127
  fi
  exec dibs ` + shimCommand + ` "$tool" "$real" "$@"
fi
exec "$real" "$@"
`
}

func runSetupShims(args []string, out io.Writer, mode outputpkg.Mode) int {
	fs := flag.NewFlagSet("setup shims", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "shim directory")
	wantHelp := fs.Bool("help", false, "help")
	outputpkg.Flags(fs, &mode)
	if err := parseFlags(fs, args); err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help("setup"))
	}
	if *wantHelp {
		emitHelp(out, mode, "setup")
		return 0
	}
	if fs.NArg() != 0 {
		return outputpkg.Error(out, mode, 2, "unexpected arguments", help("setup"))
	}
	if !shimsEnabled() {
		return outputpkg.Error(out, mode, 2, shimsOff, help("setup"))
	}
	if *dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return outputpkg.Error(out, mode, 1, err.Error(), "Set HOME or pass --dir and retry dibs setup shims")
		}
		*dir = filepath.Join(configHome("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "dibs", "shims")
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return outputpkg.Error(out, mode, 1, err.Error(), "Pass an absolute --dir and retry dibs setup shims")
	}
	// Check every target first: an unmanaged file in the directory is never replaced.
	changes := map[string][]byte{}
	for _, tool := range shimTools {
		path := filepath.Join(abs, tool)
		data, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return outputpkg.Error(out, mode, 1, readErr.Error(), "Check the directory permissions and retry dibs setup shims")
		}
		if readErr == nil && !bytes.Contains(data, []byte("\n"+shimMarker+"\n")) {
			return outputpkg.Error(out, mode, 1, path+": refusing to replace an unmanaged file", "Remove the file or pass another --dir, then retry dibs setup shims")
		}
		if next := []byte(shimScript(path, tool)); !bytes.Equal(data, next) {
			changes[path] = next
		}
	}
	changed := []string{}
	for _, tool := range shimTools {
		path := filepath.Join(abs, tool)
		next, ok := changes[path]
		if !ok {
			continue
		}
		err = os.MkdirAll(abs, 0700)
		if err == nil {
			err = atomicfilepkg.Write(path, next)
		}
		if err == nil {
			err = os.Chmod(path, 0700)
		}
		if err != nil {
			return outputpkg.Error(out, mode, 1, err.Error(), "Earlier shims may be installed; fix permissions and rerun dibs setup shims")
		}
		changed = append(changed, path)
	}
	status := "installed"
	if len(changed) == 0 {
		status = "unchanged (no-op)"
	}
	hints := []string{"Put " + remotepkg.ShellQuote(abs) + " first on PATH for agent sessions; shims pass every call through unless DIBS_TASK is set"}
	outputpkg.Emit(out, mode, map[string]any{"status": status, "dir": abs, "changed": changed, "help": hints})
	return 0
}

// shimsOff names the settings that turn on dibs setup shims and shim interception.
const shimsOff = `shims are off; set DIBS_SHIMS=1 or "shims": true in the user config`

// execTool replaces this process with the real tool, keeping argv and the environment.
var execTool = func(real string, args []string) error {
	return syscall.Exec(real, append([]string{real}, args...), os.Environ())
}

// shimRoute is what a shimmed call becomes. With no adapter the call passes through.
type shimRoute struct {
	adapter, file string
	// project is the Compose project the call names; a call against a dibs compose lease passes through.
	project string
	// refusal is why a create cannot be leased exactly as asked; hint names the dibs command to run instead.
	refusal, hint string
}

func (app application) runShim(ctx context.Context, args []string, out io.Writer) int {
	tool, real, rest := args[0], args[1], args[2:]
	task := os.Getenv("DIBS_TASK")
	route := shimRoute{}
	if task != "" {
		cwd, _ := os.Getwd()
		route = classifyShim(tool, rest, os.Getenv, cwd)
	}
	if route.project != "" && app.isComposeLease(ctx, route.project) {
		route = shimRoute{}
	}
	if route.adapter == "" {
		if err := execTool(real, rest); err != nil {
			fmt.Fprintln(os.Stderr, "dibs shim: "+err.Error())
		}
		return 127
	}
	if route.refusal != "" {
		return outputpkg.Error(out, outputpkg.Auto, 2, "DIBS_TASK is set: "+route.refusal, route.hint)
	}
	create := []string{"create", "--adapter", route.adapter, "--resources", strings.Join(append([]string{tool}, rest...), " "), "--label", "task=" + task}
	if route.file != "" {
		create = append(create, "--file", route.file)
	}
	if os.Getenv("DIBS_OWNER") == "" {
		owner := app.shimOwner(ctx, task)
		if owner == "" {
			return outputpkg.Error(out, outputpkg.Auto, 2, "DIBS_TASK is set but no lease owner was found; set DIBS_OWNER or DIBS_SCOPE, or start a scope keyed by the task", "Run dibs scope start --key "+quoteAll([]string{task})[0]+" --owner KIND:ID, then retry")
		}
		create = append(create, "--owner", owner)
	}
	fmt.Fprintln(os.Stderr, "dibs shim: DIBS_TASK is set, so this create runs as: dibs "+strings.Join(quoteAll(create), " "))
	// The adapter's own tool calls must reach the real binaries, not this shim again.
	os.Unsetenv("DIBS_TASK")
	return app.runWithOutput(ctx, create, out, outputpkg.Auto)
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+-]+$`)

func quoteAll(args []string) []string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = a
		if !plainWord.MatchString(a) {
			quoted[i] = remotepkg.ShellQuote(a)
		}
	}
	return quoted
}

// shimOwner prefers DIBS_SCOPE, then the scope whose key is the task.
func (app application) shimOwner(ctx context.Context, task string) string {
	if scope := os.Getenv("DIBS_SCOPE"); scope != "" {
		return "scope:" + scope
	}
	if !scopesEnabled() || !leasepkg.ValidKey(task) {
		return ""
	}
	if v, err := app.leases.ShowScope(ctx, "", task); err == nil {
		return "scope:" + v.Scope.Lease.ID
	}
	return ""
}

func (app application) isComposeLease(ctx context.Context, project string) bool {
	id, ok := strings.CutPrefix(project, "dibs-")
	if !ok || !leasepkg.ValidID(id) {
		return false
	}
	l, exists, err := app.leases.Get(ctx, id)
	return err == nil && exists && l.ResourceRef.Adapter == "compose" && l.ResourceRef.Name == project
}

type shimOption struct{ name, value string }

// leadingOptions splits the options before the first positional argument; flags
// in valued take the next argument when written without "=".
func leadingOptions(args []string, valued map[string]bool) ([]shimOption, []string) {
	var options []shimOption
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return options, args[i+1:]
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return options, args[i:]
		}
		name, value, has := strings.Cut(a, "=")
		if !has && valued[name] && i+1 < len(args) {
			i++
			value = args[i]
		}
		options = append(options, shimOption{name, value})
	}
	return options, nil
}

func wantsHelp(options []shimOption) bool {
	for _, o := range options {
		if o.name == "-h" || o.name == "--help" {
			return true
		}
	}
	return false
}

func flagSet(names ...string) map[string]bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return set
}

var (
	dockerValued  = flagSet("--config", "-c", "--context", "-H", "--host", "-l", "--log-level", "--tlscacert", "--tlscert", "--tlskey")
	composeValued = flagSet("-f", "--file", "-p", "--project-name", "--project-directory", "--env-file", "--profile", "--ansi", "--progress", "--parallel")
	// runBoolean lists docker run's boolean long flags; any other long flag without "=" takes the next argument.
	runBoolean = flagSet("--detach", "--interactive", "--tty", "--rm", "--privileged", "--init", "--read-only", "--publish-all", "--sig-proxy", "--no-healthcheck", "--oom-kill-disable", "--disable-content-trust", "--quiet", "--help", "--use-api-socket")
)

// runShortValued are docker run's one-letter flags that take a value.
const runShortValued = "acehlmpuvw"

func classifyShim(tool string, args []string, getenv func(string) string, cwd string) shimRoute {
	switch tool {
	case "docker":
		globals, rest := leadingOptions(args, dockerValued)
		if wantsHelp(globals) || len(rest) == 0 {
			return shimRoute{}
		}
		switch {
		case rest[0] == "compose":
			return classifyCompose(len(globals) > 0, rest[1:], getenv, cwd)
		case rest[0] == "run":
			return classifyRun(rest[1:])
		case rest[0] == "container" && len(rest) > 1 && rest[1] == "run":
			return classifyRun(rest[2:])
		}
	case "kind":
		return classifyCluster(args, "create", "cluster", "kind", "kind create cluster")
	case "k3d":
		return classifyCluster(args, "cluster", "create", "k3d", "k3d cluster create")
	case "orb", "orbctl":
		if len(args) > 0 && args[0] == "create" && !wantsHelp(optionsOf(args)) {
			return shimRoute{adapter: "orb", refusal: "dibs has no OrbStack machine adapter, so " + tool + " create cannot be leased", hint: "Lease a Compose project, kind or k3d cluster with dibs create --adapter compose|kind|k3d instead"}
		}
	}
	return shimRoute{}
}

func optionsOf(args []string) []shimOption {
	var options []shimOption
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			name, value, _ := strings.Cut(a, "=")
			options = append(options, shimOption{name, value})
		}
	}
	return options
}

// classifyCluster routes a bare cluster create; dibs names the cluster, so any extra argument is refused.
func classifyCluster(args []string, first, second, adapter, verb string) shimRoute {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != first || args[i+1] != second {
			continue
		}
		if wantsHelp(optionsOf(args)) {
			return shimRoute{}
		}
		route := shimRoute{adapter: adapter}
		if len(args) != 2 {
			route.refusal = verb + " takes no names or flags under dibs, which names the cluster and its private kubeconfig"
			route.hint = "Run dibs create --adapter " + adapter + `, then eval "$(dibs env <id>)" to use its kubeconfig`
		}
		return route
	}
	return shimRoute{}
}

func classifyRun(args []string) shimRoute {
	detached := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		if strings.HasPrefix(a, "--") {
			name, value, has := strings.Cut(a, "=")
			switch {
			case name == "--help":
				return shimRoute{}
			case name == "--detach":
				detached = detached || !has || value == "true"
			case !has && !runBoolean[name]:
				i++
			}
			continue
		}
		// A one-letter group like -dit: a valued letter takes the rest of the group, or the next argument.
		for j, c := range a[1:] {
			if c == 'd' {
				detached = true
			}
			if strings.ContainsRune(runShortValued, c) {
				if j == len(a)-2 {
					i++
				}
				break
			}
		}
	}
	if !detached {
		return shimRoute{}
	}
	return shimRoute{adapter: "docker-run", refusal: "dibs has no adapter for a detached container, so docker run -d cannot be leased", hint: "Describe the container in a Compose file and run dibs create --adapter compose --file <file>, or run it in the foreground with --rm"}
}

var (
	composeFiles     = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}
	composeOverrides = []string{"compose.override.yml", "compose.override.yaml", "docker-compose.override.yml", "docker-compose.override.yaml"}
)

func classifyCompose(dockerGlobals bool, args []string, getenv func(string) string, cwd string) shimRoute {
	options, rest := leadingOptions(args, composeValued)
	if len(rest) == 0 || rest[0] != "up" {
		return shimRoute{}
	}
	upOptions, services := leadingOptions(rest[1:], nil)
	if wantsHelp(options) || wantsHelp(upOptions) {
		return shimRoute{}
	}
	route := shimRoute{adapter: "compose", project: getenv("COMPOSE_PROJECT_NAME")}
	exact := !dockerGlobals && len(services) == 0
	var files []string
	for _, o := range options {
		switch o.name {
		case "-f", "--file":
			files = append(files, o.value)
		case "-p", "--project-name":
			route.project = o.value
		default:
			exact = false
		}
	}
	for _, o := range upOptions {
		if !(o.name == "-d" && o.value == "" || o.name == "--detach" && (o.value == "" || o.value == "true")) {
			exact = false
		}
	}
	refuse := func(why string) shimRoute {
		route.refusal = why
		route.hint = "Run dibs create --adapter compose --file <file>; dibs names the project dibs-<id>, and docker compose -p dibs-<id> up <flags> then passes through"
		return route
	}
	switch {
	case route.project != "":
		return refuse("dibs names the Compose project itself, so a project name that is not a dibs lease cannot be leased")
	case !exact:
		return refuse("only docker compose [-f FILE] up [-d] can be leased exactly; services, docker or compose options, and other up flags cannot")
	case len(files) > 1:
		return refuse("dibs create leases one Compose file, not several")
	case len(files) == 1 && files[0] == "-":
		return refuse("dibs create needs a Compose file path, not standard input")
	case len(files) == 1:
		route.file = files[0]
	case getenv("COMPOSE_FILE") != "":
		separator := getenv("COMPOSE_PATH_SEPARATOR")
		if separator == "" {
			separator = string(os.PathListSeparator)
		}
		if strings.Contains(getenv("COMPOSE_FILE"), separator) {
			return refuse("dibs create leases one Compose file, not the several COMPOSE_FILE names")
		}
		route.file = getenv("COMPOSE_FILE")
	default:
		file, override := findComposeFile(cwd)
		if file == "" {
			return refuse("no Compose file found in " + cwd + " or its parents")
		}
		if override != "" {
			return refuse(override + " would be merged in by docker compose but not by dibs create")
		}
		route.file = file
	}
	if !filepath.IsAbs(route.file) {
		route.file = filepath.Join(cwd, route.file)
	}
	return route
}

// findComposeFile follows docker compose's default lookup: the first default name in the nearest directory.
func findComposeFile(dir string) (file, override string) {
	for {
		for _, name := range composeFiles {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
				for _, o := range composeOverrides {
					if _, err := os.Stat(filepath.Join(dir, o)); err == nil {
						return filepath.Join(dir, name), filepath.Join(dir, o)
					}
				}
				return filepath.Join(dir, name), ""
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}
