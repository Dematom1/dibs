# Jobs

## Run a local job

```sh
 dibs job --cores 2 --memory 2GiB --timeout 10m -- go test ./...
 dibs job --class suite --wait 5m -- just check
 dibs ps                           # includes each job's cores, memory and class
```

`job` reserves logical cores and memory on this machine, minus **owner room**
reserved for interactive work. Reservations coordinate cooperating dibs jobs;
they are not OS CPU or memory caps, and stack leases' resource descriptions do
not count against them. Configure a static partition if other tools such as
runner-up also schedule work on the host; dibs does not read their ledgers.
Jobs inherit the working directory and environment, and pass stdin, stdout and
stderr through unchanged. `DIBS_LEASE` identifies the job.
`--axi` and `--json` change only pre-start errors, not child output. Use noninteractive
commands. Dibs does not hand terminal foreground job control to the child.

Admission is FIFO across classes, with no backfill: a smaller request cannot
bypass an earlier larger waiter. The default wait is `30m`; `--wait 0` tries
once. A wait that exhausts returns 75 without starting user code. Dead or
expired waiters are discarded. Admission also attempts to reclaim dead or
expired job leases before counting their reservations; failed cleanup retains
the reservation. A request larger than the usable budget fails with code 2.
Queue order is the order requests acquire the state lock, not shell launch time.
The state lock is released while waiting and while a child runs.

Each job is a lease owned by the `dibs job` process, not `DIBS_OWNER` or its
invoking shell. Its deadline starts at admission and is the explicit `--timeout`
or class timeout. Job deadlines cannot be extended. On timeout or cancellation,
dibs sends TERM to the process group, then KILL after one second if needed.
Normal completion also cleans up remaining group members before releasing the
reservation. Cleanup failures go to stderr and retain the lease; they never
replace the child's exit code. If dibs itself is killed, `sweep --confirm`
reclaims the job lease on owner death or expiry. Keep a host sweep timer. A
matching live leader permits process-group cleanup; if the leader is missing or
reaped, dibs warns and releases the lease without signaling an unverifiable
group. Orphan descendants may then require manual cleanup.

Process groups are not a security boundary: programs that call `setsid` or
change groups can escape cleanup. Container/cgroup enforcement is not part of
local job admission. Cleanup verifies the recorded group leader's start identity
before signaling and retains the record if identity is unknown or reused while
the leader is present.

### Machine budget and classes

The optional config is strict JSON at `$XDG_CONFIG_HOME/dibs/config.json`, or
`~/.config/dibs/config.json`. `DIBS_CONFIG` selects a different file; an
explicitly selected missing file is an error. Unknown fields are rejected.

Without a config, totals come from the host's logical CPU count and physical
memory (`sysctl hw.memsize` on macOS, `/proc/meminfo` on Linux). Owner room is
`max(1, cores/4)` integer cores, capped at `cores-1` (zero on a single-core host),
and `max(512MiB, memory/4)` bytes, capped at half the host's memory. The default
class requests one core and up to 1GiB of usable memory, with a `1h` timeout.

For example, this gives jobs a shared 4-core/8GiB partition on a 16-core/32GiB
machine, leaving the rest for its owner and other schedulers:

```json
{
  "cores": 16,
  "memory": "32GiB",
  "room_cores": 12,
  "room_memory": "24GiB",
  "classes": {
    "default": {"cores": 1, "memory": "1GiB", "timeout": "1h"},
    "suite": {"cores": 2, "memory": "4GiB", "timeout": "30m"},
    "image": {"cores": 4, "memory": "8GiB", "timeout": "45m"}
  }
}
```

Omitted top-level fields keep host defaults. Each configured class supplies
positive `cores`, `memory`, and `timeout`; `default` remains available unless
explicitly replaced. `--cores`, `--memory`, and `--timeout` override the selected
class independently. Memory accepts integer bytes or `KiB`, `MiB`, `GiB`, `KB`,
`MB`, `GB`, and `B`; cores are positive integers. Timeouts are positive Go
durations. Config edits apply to new invocations; existing reservations still
count, and running jobs keep their original deadline. Use the same config and
state directory for every cooperating invocation on a machine.

