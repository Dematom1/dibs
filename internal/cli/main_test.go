package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the developer's own config, ledger, and DIBS_* settings out of
// every test: each test that needs an opt-in or a config sets it explicitly.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "dibs-cli-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "DIBS_") || strings.HasPrefix(name, "BERTH_") {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
