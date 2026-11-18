# Operations

## Sweep from a host timer

Dibs does not sweep by itself. Run `dibs sweep --confirm` from cron, a systemd
timer, launchd, or your session teardown. Set the same absolute `DIBS_STATE`
and use an absolute executable path in the timer. Sweep frequency determines
how long a dead or expired lease can retain resources. Preview first to check
which resources will be deleted.

With the opt-in [lease watcher](../leases.md#lease-watcher), a lease owned by a
process is reclaimed as soon as that process exits. Keep the timer anyway, and
add a run at login or boot: it reclaims expired leases, retries refused
reclaims, and covers watchers lost to a crash or reboot.
