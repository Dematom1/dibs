# Operations

## Sweep from a host timer

Dibs does not sweep by itself. Run `dibs sweep --confirm` from cron, a systemd
timer, launchd, or your session teardown. Set the same absolute `DIBS_STATE`
and use an absolute executable path in the timer. Sweep frequency determines
how long a dead or expired lease can retain resources. Preview first to check
which resources will be deleted.

