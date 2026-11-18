package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLeaseWatcherOptIn(t *testing.T) {
	var started []string
	previous := startWatcher
	startWatcher = func(id string) { started = append(started, id) }
	t.Cleanup(func() { startWatcher = previous })
	config := filepath.Join(t.TempDir(), "config.json")
	heartbeat := filepath.Join(t.TempDir(), "beat")
	if err := os.WriteFile(heartbeat, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_CONFIG", config)
	pid := "pid:" + strconv.Itoa(os.Getpid())
	for _, tc := range []struct {
		name, config, env, owner string
		setEnv, want             bool
	}{
		{name: "absent", owner: pid},
		{name: "config on", config: `{"lease_watcher":true}`, owner: pid, want: true},
		{name: "env on", env: "1", setEnv: true, owner: pid, want: true},
		{name: "env off overrides config", config: `{"lease_watcher":true}`, env: "0", setEnv: true, owner: pid},
		{name: "session owner", env: "1", setEnv: true, owner: "session:" + strconv.Itoa(os.Getpid()), want: true},
		{name: "heartbeat owner has no process to watch", env: "1", setEnv: true, owner: "heartbeat:" + heartbeat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started = nil
			t.Setenv("DIBS_STATE", t.TempDir())
			os.Remove(config)
			if tc.config != "" {
				if err := os.WriteFile(config, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("DIBS_LEASE_WATCHER", tc.env)
			if !tc.setEnv {
				os.Unsetenv("DIBS_LEASE_WATCHER")
			}
			id := invoke(t, 0, "create", "--owner", tc.owner)["record"].(map[string]any)["id"]
			if tc.want != (len(started) == 1 && started[0] == id) || len(started) > 1 {
				t.Fatalf("started watchers %v for lease %v, want watcher %v", started, id, tc.want)
			}
		})
	}
}
