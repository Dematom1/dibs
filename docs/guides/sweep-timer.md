# Operations

## Sweep from a host timer

Without the opt-in [lease watcher](../leases.md#lease-watcher), dibs sweeps
only when a command requests it. Run `dibs sweep --confirm` from cron, a
systemd timer, launchd, or your session teardown. Set the same absolute `DIBS_STATE`
and use an absolute executable path in the timer. Sweep frequency determines
how long a dead or expired lease can retain resources. Preview first to check
which resources will be deleted.

With the opt-in [lease watcher](../leases.md#lease-watcher), a lease owned by a
process gets one confirmed sweep attempt once that process's death is proven.
The lease's policy can still hold it, for example during a grace period or
under a report-only policy, and starting the watcher is best-effort. Keep the
timer anyway, and add a run at login or boot: it reclaims expired leases,
retries held or refused reclaims, and covers watchers lost to a crash or reboot.
