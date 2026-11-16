# Output and agents

## Output for people, agents, and scripts

On a terminal, dibs prints aligned tables, relative expiry times, and readable
memory units. `dibs ps` and the no-args home view show job usage, usable capacity,
and the cores and memory kept free for you. Usage errors have one error line and
one short hint, not a help dump.

Use `--axi` or `DIBS_AXI=1` for agent output in TOON. Use `--json` for complete,
stable JSON suitable for `jq` and scripts. Both flags work before or after the
command. They are mutually exclusive, and an explicit flag overrides `DIBS_AXI`.
There is no `--human` flag.

AXI is a first-class agent view, not a debug format. Lists default to 100 compact
rows with `total` for all matching leases and `shown` when the list is truncated.
Descriptions and detail label values have 500-character previews. Truncated text
states the full character count. Help lines carry the filters and remote machine
needed to fetch the complete result. `--full` on lease commands removes these
bounds and restores complete records. **AXI with `--full` has the same fields and
data as `--json`**, which never truncates. Canonical state is never shortened.

When stdout is not a TTY and neither output flag is present, dibs emits AXI as
a safety net for agents and pipes. **Human default means terminal output**, not
redirected output. Use `--json` explicitly for scripts that need JSON.

```sh
dibs ps                              # human on a terminal
dibs --axi ps                        # bounded agent view
dibs --axi ps --full                 # complete records, same data as --json
DIBS_AXI=1 dibs ps                   # agent session environment
dibs ps --json | jq '.budget.used_cores'
```

### Install agent integrations

```sh
dibs setup hooks
dibs setup hooks --remove
```

Setup reports the files it changed. Repeating either command is a no-op.
It preserves unrelated settings and refuses malformed configs or unmanaged files
at its plugin paths. Only this explicit setup command edits agent configuration.
Restart agent sessions after installation or removal.

- Claude Code uses a managed `SessionStart` hook in `~/.claude/settings.json`.
  The hook appends `export DIBS_AXI=1` to `CLAUDE_ENV_FILE`.
- Codex uses `SessionStart` in `~/.codex/hooks.json` and enables `features.hooks`
  in `~/.codex/config.toml`. It uses `CODEX_ENV_FILE`, with `CLAUDE_ENV_FILE` as a
  compatibility fallback. Removal restores a `false` toggle changed by setup,
  or removes the toggle if setup added it. Existing enabled hooks stay enabled.
- Pi loads `~/.pi/agent/extensions/dibs-axi.ts`, which sets the environment at
  `session_start`.
- OpenCode loads `~/.config/opencode/plugins/dibs-axi.js`, which sets the
  environment through `shell.env`.

Setup respects `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `PI_CODING_AGENT_DIR`, and
`XDG_CONFIG_HOME`. Claude Code or Codex versions without an environment-file
hook receive a short instruction to use `dibs --axi` instead. Agents without
these integrations still get AXI when they capture stdout through a pipe.
No shell profile changes or executable shims are installed.
