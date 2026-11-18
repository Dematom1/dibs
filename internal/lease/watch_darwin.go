//go:build darwin

package lease

import (
	"time"

	"golang.org/x/sys/unix"
)

// watchExit registers a kqueue NOTE_EXIT filter, which works for any process this user can see.
func watchExit(pid int) exitWatch {
	kq, err := unix.Kqueue()
	if err != nil {
		return exitWatch{fd: -1}
	}
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		unix.Close(kq)
		return exitWatch{fd: -1}
	}
	return exitWatch{fd: kq}
}

// wait returns at the exit, after d, or early on a signal; the caller reads the ledger either way.
func (w exitWatch) wait(d time.Duration) {
	if w.fd < 0 {
		time.Sleep(d)
		return
	}
	timeout := unix.NsecToTimespec(int64(d))
	unix.Kevent(w.fd, nil, make([]unix.Kevent_t, 1), &timeout)
}
