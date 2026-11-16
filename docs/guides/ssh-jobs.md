# Jobs over SSH

## Run jobs over SSH

Add a machine to your local config. Paths refer to the remote machine and must
be absolute. `host` can be an SSH config alias. `port` and `identity_file` are
optional. SSH uses batch authentication, normal host-key checking, a 15-second
connection timeout, and keepalives every 5 seconds with 3 unanswered probes.

```json
{
  "machines": {
    "lenovo": {
      "host": "lenovo",
      "user": "worker",
      "workdir": "/home/worker/src/project",
      "agent": "/home/worker/.local/bin/dibs"
    }
  }
}
```

Build a trusted binary for the remote OS and architecture, then install it:

```sh
 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/dibs-linux .
 dibs machine install lenovo --file /tmp/dibs-linux
 dibs job --machine lenovo --class suite -- go test ./...
 dibs ps --machine lenovo
 dibs sweep --machine lenovo --confirm
```

Installation streams the binary over SSH, checks its version on the remote,
and atomically replaces the configured agent path. A wrong version or
architecture fails without replacing the installed binary. Running agents
keep their executable. Every connection checks the remote version before
requesting work, and the agent checks the client's version again. Mismatches
refuse to start a child and explain how to install the matching binary.

The same dibs binary runs as `dibs agent` on the remote, once per SSH session.
It is not a daemon. The remote config decides classes, total budget, owner room,
and admission across all clients. The client does not forward its budget or
class defaults. Remote state stays on the remote machine, under that SSH user's
usual dibs state directory. `ps --machine` and `sweep --machine` use that state.
Each machine reclaims only its own leases. There is no shared cross-host ledger.

Remote jobs run in the configured workdir with the environment of a
non-interactive SSH command. Login profiles such as `~/.profile` are not read.
Make required tools available on that PATH, or invoke them by absolute path.
No source, worktree, environment variables, build tools, or credentials are
copied. Remote child stdin is `/dev/null`; SSH stdin is reserved for liveness,
so remote commands must be noninteractive. Child stdout and stderr pass through.
The private protocol frames stdout, including arbitrary bytes, and the final
exit code separately. Child exit 255 is preserved rather than mistaken for an
SSH transport failure.

The agent is the lease's `session:<holder-pid>` owner. EOF on its SSH stdin or
session cancellation kills the job's process group and releases its lease,
including while admission is waiting. An idle local stdin does not end the SSH
session. For broken-network detection on the server, configure sshd's
`ClientAliveInterval` and `ClientAliveCountMax`, for example 5 and 3. Client
keepalives alone do not bound how long the server waits for a lost client.
Remote deadlines still apply. Keep a sweep timer on each machine for agent
crashes and failed cleanup. Process-group escape limitations apply remotely too.

A missing or broken transport returns 76 with diagnostics on stderr once SSH
has started. The remote result is then unknown; do not assume the child did not
run. This code avoids reporting dibs's own 1 or 2 after a child may have started.
A completed child can itself return 76, as with any reserved job exit code.

For a Lenovo rollout, provision key-based SSH and trusted host keys, a remote
checkout and its dependencies, the matching Linux binary, and a remote dibs
config with a budget partition that leaves owner room and avoids other
schedulers. Configure an sshd keepalive policy and a per-user sweep timer.
Kubernetes-backed machines remain deferred.
