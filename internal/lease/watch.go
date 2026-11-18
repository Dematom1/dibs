package lease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

// watchInterval bounds how long a watcher goes without reading the ledger, so it
// notices a released lease and an owner whose exit it could not attach to.
var watchInterval = time.Minute

// Watchable reports a lease whose owner is a process a watcher can wait on; a
// job is skipped because dibs job already waits on its child.
func Watchable(l Lease) bool {
	return (l.Owner.Kind == "pid" || l.Owner.Kind == "session") && l.Reservation == nil
}

// Watch lives exactly as long as lease id. It blocks on the owner's exit and
// wakes each interval to read the ledger, exiting once the lease is gone. An
// event only says when to look: when the owner is proven dead it runs the
// confirmed sweep for this lease alone, whose holds still apply, and exits
// without retrying, leaving a retained lease to the next sweep.
func (m *Manager) Watch(ctx context.Context, id string) (SweepResult, error) {
	dir, err := m.directory()
	if err != nil {
		return SweepResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "watch"), 0700); err != nil {
		return SweepResult{}, err
	}
	path := filepath.Join(dir, "watch", id+".lock")
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return SweepResult{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return SweepResult{}, nil // another watcher already follows this lease
		}
		return SweepResult{}, err
	}
	// shortcut: removing a held lock lets a starter that opened the old file run a second watcher; starts come once per create or adopt, so that needs two concurrent starts for one lease.
	defer os.Remove(path)
	host, err := machine()
	if err != nil {
		return SweepResult{}, err
	}
	// throttled is set after a wait ended early without proving death, such as an
	// exit event for a zombie whose ps read failed; the next wait then sleeps the interval.
	throttled := false
	for ctx.Err() == nil {
		l, ok, err := m.Get(ctx, id)
		if err != nil {
			return SweepResult{}, err
		}
		if !ok || !Watchable(l) || l.Machine != host {
			return SweepResult{}, nil
		}
		pid, _ := strconv.Atoi(l.Owner.ID)
		// Attach before checking identity, so an exit after the check still wakes the wait.
		exit := watchExit(pid)
		if m.checkOwner(l.Owner, time.Now().UTC()) == ownerpkg.Dead {
			exit.close()
			return m.Sweep(ctx, SweepRequest{Confirm: true, ID: id, OwnerDead: true})
		}
		if throttled {
			exit.close()
			exit = exitWatch{fd: -1}
		}
		began := time.Now()
		exit.wait(watchInterval)
		exit.close()
		throttled = time.Since(began) < watchInterval
	}
	return SweepResult{}, ctx.Err()
}

// exitWatch waits on one process's exit; fd < 0 means it could not attach and waits by sleeping.
type exitWatch struct{ fd int }

func (w exitWatch) close() {
	if w.fd >= 0 {
		syscall.Close(w.fd)
	}
}
