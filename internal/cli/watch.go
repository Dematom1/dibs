package cli

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
)

// watchCommand is the hidden command a started watcher runs; it is not part of the CLI contract.
const watchCommand = "__watch"

func leaseWatcherEnabled() bool { return optedIn("DIBS_LEASE_WATCHER", configpkg.LeaseWatcher) }

// startWatcher launches a detached watcher for one lease. A failed start only
// leaves reclaim to the next sweep, so it is not an error.
var startWatcher = func(id string) {
	bin, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(bin, watchCommand, id)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}

// watch starts the lease's watcher when the lease_watcher opt-in is on and its owner is a process.
func (app application) watch(l leasepkg.Lease) {
	if leasepkg.Watchable(l) && leaseWatcherEnabled() {
		startWatcher(l.ID)
	}
}

// runWatch is the watcher process: 0 when it exits cleanly, 1 when it could not follow or reclaim the lease.
func (app application) runWatch(ctx context.Context, id string) int {
	if !leasepkg.ValidID(id) {
		return 2
	}
	r, err := app.leases.Watch(ctx, id)
	if err != nil || len(r.Failures) > 0 {
		return 1
	}
	return 0
}
