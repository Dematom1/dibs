//go:build linux

package lease

import (
	"time"

	"golang.org/x/sys/unix"
)

// watchExit opens a pidfd, which becomes readable when any process exits, not only a child (Linux 5.3+).
func watchExit(pid int) exitWatch {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return exitWatch{fd: -1}
	}
	return exitWatch{fd: fd}
}

// wait returns at the exit, after d, or early on a signal; the caller reads the ledger either way.
func (w exitWatch) wait(d time.Duration) {
	if w.fd < 0 {
		time.Sleep(d)
		return
	}
	unix.Poll([]unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}, int(d.Milliseconds()))
}
