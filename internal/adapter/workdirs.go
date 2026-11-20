package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcessDirs lists the canonical working directories of this user's
// processes; an error means liveness is unknown.
func ProcessDirs(ctx context.Context, run CommandRunner) ([]string, error) {
	out, err := run(ctx, nil, "lsof", "-nP", "-a", "-u", strconv.Itoa(os.Getuid()), "-d", "cwd", "-Fn")
	if err != nil {
		return nil, errors.New("cannot list process working directories with lsof; liveness unknown")
	}
	var dirs []string
	for line := range strings.Lines(string(out)) {
		if dir, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "n"); ok && filepath.IsAbs(dir) {
			dirs = append(dirs, Canonical(dir))
			// lsof escapes non-printable bytes but not a literal backslash or caret, so keep both
			// spellings: an extra candidate can only find more live work, never hide it.
			if decoded := unescapeLsof(dir); decoded != dir {
				dirs = append(dirs, Canonical(decoded))
			}
		}
	}
	if len(dirs) == 0 {
		return nil, errors.New("lsof listed no process working directories; liveness unknown")
	}
	return dirs, nil
}

// unescapeLsof reverses lsof's output escapes for non-printable bytes: the C
// forms \b \f \n \r \t, the hexadecimal form \xHH, and the control form ^X.
func unescapeLsof(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '\\' && i+3 < len(name) && name[i+1] == 'x' && isHex(name[i+2]) && isHex(name[i+3]):
			v, _ := strconv.ParseUint(name[i+2:i+4], 16, 8)
			b.WriteByte(byte(v))
			i += 3
		case c == '\\' && i+1 < len(name) && strings.IndexByte("bfnrt", name[i+1]) >= 0:
			b.WriteByte("\b\f\n\r\t"[strings.IndexByte("bfnrt", name[i+1])])
			i++
		case c == '^' && i+1 < len(name) && name[i+1] >= '@' && name[i+1] <= '_':
			b.WriteByte(name[i+1] - '@')
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// Canonical resolves symlinks in the longest existing prefix of an absolute
// path, so a directory that is already gone still compares by its real parent.
func Canonical(path string) string {
	path = filepath.Clean(path)
	for dir, rest := path, ""; ; {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest, dir = filepath.Join(filepath.Base(dir), rest), parent
	}
}

// WorkIn returns the first place that one of the canonical process dirs is
// inside, or "" when no process works in any of them.
func WorkIn(dirs, places []string) string {
	for _, place := range places {
		real := Canonical(place)
		// TrimSuffix keeps "/" itself a parent of every directory instead of matching only "//".
		for _, dir := range dirs {
			if dir == real || strings.HasPrefix(dir, strings.TrimSuffix(real, "/")+"/") {
				return place
			}
		}
	}
	return ""
}

// LiveWork reports whether a process of this user works in any watched place,
// as an Activity whose Busy names it; an error means liveness is unknown.
func LiveWork(ctx context.Context, run CommandRunner, watch []string) (Activity, error) {
	dirs, err := ProcessDirs(ctx, run)
	if err != nil {
		return Activity{}, err
	}
	if place := WorkIn(dirs, watch); place != "" {
		return Activity{Busy: "a live process works in " + place}, nil
	}
	return Activity{}, nil
}
