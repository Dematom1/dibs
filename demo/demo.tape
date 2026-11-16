# Renders demo/demo.gif. Run demo/record.sh, which installs the released dibs binary
# and an empty DIBS_STATE, then runs this tape from the repository root.
# Every command and output below is real; nothing is edited or simulated.
Output demo/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1000
Set Height 520
Set Padding 16
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

Hide
Type `env -i PATH="$PATH" DIBS_STATE="$DIBS_STATE" HOME="$HOME" TERM=xterm-256color PS1='$ ' bash --norc --noprofile` Enter
Type "clear" Enter
Show

Type "# A worker leases a resource with an owner and an expiry" Enter
Sleep 2s
Type "sleep 600 & WORKER=$!" Enter
Type "dibs create --adapter none --owner pid:$WORKER --ttl 2h --resources 'demo worker' --label task=demo" Enter
Sleep 6s
Type "dibs ps --json | jq '.leases[] | {id, owner, liveness, expires}'" Enter
Sleep 5s

Type "clear" Enter
Type "# The worker dies. Its lease stays recorded, now with a dead owner" Enter
Sleep 2s
Type "kill $WORKER" Enter
Sleep 2s
Type "dibs ps --json | jq '.leases[] | {id, owner, liveness, expires}'" Enter
Sleep 4s
Type "# Sweep previews what it would reclaim, then reclaims it" Enter
Sleep 2s
Type "dibs sweep" Enter
Sleep 4s
Type "dibs sweep --confirm" Enter
Sleep 4s

Type "clear" Enter
Type "# Lease a resource for this shell, then release it when the work is done" Enter
Sleep 2s
Type "LEASE=$(dibs create --adapter none --owner pid:$$ --ttl 1h --resources 'demo shell' --quiet)" Enter
Sleep 2s
Type "dibs release $LEASE" Enter
Sleep 4s
Type "dibs release $LEASE --confirm" Enter
Sleep 4s
Type "dibs ps" Enter
Sleep 4s

Hide
Type "exit" Enter
