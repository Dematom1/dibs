package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symlinked spelling of a slot, or a slot whose directory is already gone,
// still matches the real working directory lsof reports.
func TestWorkInComparesRealPaths(t *testing.T) {
	real := t.TempDir()
	slot := filepath.Join(real, "pool", "3")
	if err := os.MkdirAll(filepath.Join(slot, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	dirs := []string{Canonical(filepath.Join(slot, "repo"))}
	for place, want := range map[string]bool{
		filepath.Join(link, "pool", "3"):         true,
		filepath.Join(link, "pool", "3", "repo"): true,
		filepath.Join(link, "pool", "30"):        false,
		"/":                                      true,
		filepath.Join(link, "pool", "3", "gone"): false,
		filepath.Join(link, "pool"):              true,
	} {
		if got := WorkIn(dirs, []string{place}) != ""; got != want {
			t.Errorf("%s: live=%v, want %v", place, got, want)
		}
	}
	gone := filepath.Join(link, "pool", "9", "deleted")
	if got := Canonical(gone); got != filepath.Join(Canonical(real), "pool", "9", "deleted") {
		t.Fatalf("missing path canonical form %s", got)
	}
}

func TestLiveWorkReadsLsof(t *testing.T) {
	dir := t.TempDir()
	lsof := func(out string, err error) CommandRunner {
		return func(_ context.Context, _ []string, name string, _ ...string) ([]byte, error) {
			if name != "lsof" {
				t.Fatalf("unexpected tool %s", name)
			}
			return []byte(out), err
		}
	}
	busy, err := LiveWork(context.Background(), lsof("p1\nfcwd\nn/\np2\nfcwd\nn"+dir+"/sub\n", nil), []string{dir})
	if err != nil || busy.Busy == "" {
		t.Fatalf("process in a watched dir not busy: %v %v", busy, err)
	}
	idle, err := LiveWork(context.Background(), lsof("p1\nfcwd\nn/\n", nil), []string{dir})
	if err != nil || idle.Busy != "" {
		t.Fatalf("no process in the watched dir: %v %v", idle, err)
	}
	for _, runner := range []CommandRunner{lsof("", errors.New("exit 1")), lsof("p1\n", nil)} {
		if _, err := LiveWork(context.Background(), runner, []string{dir}); err == nil {
			t.Fatal("unreadable process list must be unknown, not idle")
		}
	}
}

// Under the C locale lsof prints a non-ASCII name byte as \xHH; the slot must still match.
func TestProcessDirsDecodesLsofEscapes(t *testing.T) {
	slot := filepath.Join(t.TempDir(), "caf\u00e9")
	if err := os.MkdirAll(filepath.Join(slot, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(filepath.Join(slot, "repo"), "\u00e9", `\xc3\xa9`)
	run := func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte("p1\nfcwd\nn" + escaped + "\n"), nil
	}
	busy, err := LiveWork(context.Background(), run, []string{slot})
	if err != nil || busy.Busy == "" {
		t.Fatalf("escaped lsof name hid live work in %s: %v %v", slot, busy, err)
	}
	if got := unescapeLsof(`/a\tb^@c\x41\q`); got != "/a\tb\x00cA\\q" {
		t.Fatalf("unescape: %q", got)
	}
}
