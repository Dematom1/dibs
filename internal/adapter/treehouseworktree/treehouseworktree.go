// Package treehouseworktree leases one pooled treehouse worktree slot through
// treehouse's own durable lease token. Treehouse keeps allocation and reset;
// dibs adds owner, expiry and reclaim, and never discards unlanded work.
package treehouseworktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
)

const Name = "treehouse-worktree"

// OptIn names the settings that enable creation and adoption.
const OptIn = `DIBS_TREEHOUSE_WORKTREE=1 or "treehouse_worktree_adapter": true in the user config.json`

// notReturned is treehouse's exit status when it keeps a dirty slot rather than discard it.
const notReturned = 3

var remoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidRemote reports whether name is a plain git remote name usable as a landed-ref exclusion.
func ValidRemote(name string) bool { return remoteName.MatchString(name) }

func holder(leaseID string) string { return "dibs:" + leaseID }

// env keeps every call independent of the caller's later treehouse and git environment.
func env() []string {
	return []string{"TREEHOUSE_ROOT=", "TREEHOUSE_LEASE_HOLDER=", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
}

type process struct {
	Name string `json:"name"`
	PID  int    `json:"pid"`
}

type slot struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Status      string    `json:"status"`
	LeaseID     string    `json:"lease_id"`
	LeaseHolder string    `json:"lease_holder"`
	Processes   []process `json:"processes"`
}

type worktree struct{ run adapterpkg.DirRunner }

func (w worktree) treehouse(ctx context.Context, ref adapterpkg.ResourceRef, args ...string) ([]byte, error) {
	if ref.Root != "" {
		args = append(args, "--root", ref.Root)
	}
	return w.run(ctx, ref.Repo, env(), "treehouse", args...)
}

func (w worktree) slots(ctx context.Context, ref adapterpkg.ResourceRef) ([]slot, error) {
	out, err := w.treehouse(ctx, ref, "status", "--json")
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	trimmed := strings.TrimSpace(string(out))
	if len(trimmed) == 0 || trimmed[0] != '[' || json.Unmarshal(out, &raw) != nil {
		return nil, errors.New("cannot read treehouse status --json; lease retained")
	}
	slots := make([]slot, 0, len(raw))
	for _, entry := range raw {
		var fields map[string]json.RawMessage
		var s slot
		if json.Unmarshal(entry, &fields) != nil || fields == nil ||
			json.Unmarshal(fields["path"], &s.Path) != nil || s.Path == "" ||
			json.Unmarshal(fields["status"], &s.Status) != nil || s.Status == "" ||
			len(fields["lease_id"]) == 0 || fields["lease_id"][0] != '"' || json.Unmarshal(fields["lease_id"], &s.LeaseID) != nil ||
			len(fields["lease_holder"]) == 0 || fields["lease_holder"][0] != '"' || json.Unmarshal(fields["lease_holder"], &s.LeaseHolder) != nil ||
			len(fields["processes"]) == 0 || fields["processes"][0] != '[' ||
			json.Unmarshal(fields["processes"], &s.Processes) != nil {
			return nil, errors.New("cannot read treehouse status --json; lease retained")
		}
		if err := json.Unmarshal(entry, &s); err != nil {
			return nil, errors.New("cannot read treehouse status --json; lease retained")
		}
		slots = append(slots, s)
	}
	return slots, nil
}

// claimed finds the slot carrying this lease's claim: its token, or, before the
// token is recorded (a crash right after allocation), its unique holder label.
func claimed(slots []slot, ref adapterpkg.ResourceRef) (slot, bool) {
	for _, s := range slots {
		if (ref.Claim != "" && s.LeaseID == ref.Claim) || (ref.Claim == "" && ref.Holder != "" && s.LeaseHolder == ref.Holder) {
			return s, true
		}
	}
	return slot{}, false
}

func (w worktree) Create(ctx context.Context, t adapterpkg.Target) (adapterpkg.ResourceRef, error) {
	out, err := w.treehouse(ctx, t.Ref, "get", "--lease", "--json", "--lease-holder", t.Ref.Holder)
	if err != nil {
		return t.Ref, err
	}
	var got slot
	if json.Unmarshal(out, &got) != nil || !filepath.IsAbs(got.Path) || got.LeaseID == "" || got.LeaseHolder != t.Ref.Holder {
		return t.Ref, errors.New("treehouse returned an unreadable allocation; the lease is retained and release finds the slot by its holder")
	}
	ref := t.Ref
	ref.Path, ref.Claim = filepath.Clean(got.Path), got.LeaseID
	// The slot name is for display only; the token and path are the identity.
	if slots, err := w.slots(ctx, ref); err == nil {
		if s, ok := claimed(slots, ref); ok {
			ref.Name = s.Name
		}
	}
	return ref, nil
}

func (w worktree) Destroy(ctx context.Context, t adapterpkg.Target) error {
	slots, err := w.slots(ctx, t.Ref)
	if err != nil {
		return err
	}
	s, held := claimed(slots, t.Ref)
	if !held {
		return gone(slots, t.Ref)
	}
	if len(s.Processes) != 0 {
		return processBlocker(s.Processes)
	}
	var checkedHead string
	if !t.DiscardUnlanded {
		checkedHead, err = w.landed(ctx, s.Path, t.Ref.ExcludeRemotes)
		if err != nil {
			return err
		}
	}
	latest, err := w.slots(ctx, t.Ref)
	if err != nil {
		return err
	}
	current, held := claimed(latest, t.Ref)
	if !held || current.LeaseID != s.LeaseID || filepath.Clean(current.Path) != filepath.Clean(s.Path) || len(current.Processes) != 0 {
		if held && len(current.Processes) != 0 {
			return processBlocker(current.Processes)
		}
		return errors.New("worktree claim or process set changed during cleanup; lease retained")
	}
	if !t.DiscardUnlanded {
		head, err := w.head(ctx, current.Path)
		if err != nil {
			return err
		}
		if head != checkedHead {
			return errors.New("worktree HEAD changed during cleanup; lease retained")
		}
	}
	s = current
	// Conditional on the exact claim, so a stale lease never resets a slot reassigned since.
	args := []string{"return", s.Path, "--if-lease-id", s.LeaseID}
	if t.Ref.Holder != "" {
		args = append(args, "--if-lease-holder", t.Ref.Holder)
	}
	if t.DiscardUnlanded {
		args = append(args, "--force")
	}
	// Final verification and return are separate processes; see treehouse issue 216 for atomic return-if-clean.
	if _, err := w.treehouse(ctx, t.Ref, args...); err != nil {
		var failed adapterpkg.CommandError
		if errors.As(err, &failed) && failed.Code == notReturned {
			return errors.New("treehouse kept the slot because it has uncommitted changes; lease retained")
		}
		return err
	}
	slots, err = w.slots(ctx, t.Ref)
	if err != nil {
		return err
	}
	if _, held := claimed(slots, t.Ref); held {
		return errors.New("worktree claim survived treehouse return; lease retained, retry dibs release")
	}
	return nil
}

// exists reports whether the pool still carries the lease's claim; absence
// needs the same evidence Destroy requires, and anything else is unknown.
func (w worktree) exists(ctx context.Context, t adapterpkg.Target) (bool, error) {
	slots, err := w.slots(ctx, t.Ref)
	if err != nil {
		return false, err
	}
	if _, held := claimed(slots, t.Ref); held {
		return true, nil
	}
	return false, gone(slots, t.Ref)
}

// gone accepts absence only with evidence: no slot carries the claim, and the
// pinned slot is either listed without it or no longer exists. A pending claim
// with no recorded path relies on its holder label being absent from the pool.
func gone(slots []slot, ref adapterpkg.ResourceRef) error {
	if ref.Path == "" {
		return nil
	}
	for _, s := range slots {
		if filepath.Clean(s.Path) == ref.Path {
			return nil
		}
	}
	if _, err := os.Stat(ref.Path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("treehouse status does not list the pinned slot %s; cannot verify its claim is gone, lease retained", ref.Path)
}

// landed refuses uncommitted changes and commits that no remote-tracking ref
// (other than excluded remotes) contains.
func processBlocker(processes []process) error {
	blockers := make([]adapterpkg.Process, 0, len(processes))
	for _, p := range processes {
		blockers = append(blockers, adapterpkg.Process{Name: p.Name, PID: p.PID})
	}
	return &adapterpkg.UnlandedError{Processes: blockers}
}

func (w worktree) landed(ctx context.Context, path string, exclude []string) (string, error) {
	out, err := w.run(ctx, "", env(), "git", "-C", path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	unlanded := &adapterpkg.UnlandedError{}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) > 3 {
			unlanded.Uncommitted = append(unlanded.Uncommitted, line[3:])
		}
	}
	args := []string{"-C", path, "log", "--format=%H", "HEAD", "--not"}
	for _, remote := range exclude {
		args = append(args, "--exclude="+remote+"/*")
	}
	out, err = w.run(ctx, "", env(), "git", append(args, "--remotes", "--")...)
	if err != nil {
		return "", err
	}
	unlanded.Unpushed = strings.Fields(string(out))
	if len(unlanded.Uncommitted) > 0 || len(unlanded.Unpushed) > 0 {
		return "", unlanded
	}
	head, err := w.head(ctx, path)
	return head, err
}

func (w worktree) head(ctx context.Context, path string) (string, error) {
	out, err := w.run(ctx, "", env(), "git", "-C", path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// mainCheckout pins the repository's main checkout, so cleanup survives the
// removal of whichever linked worktree the caller stood in.
func mainCheckout(ctx context.Context, dir string, run adapterpkg.DirRunner) (string, error) {
	out, err := run(ctx, "", env(), "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := filepath.Clean(strings.TrimSpace(string(out)))
	if !filepath.IsAbs(common) || filepath.Base(common) != ".git" {
		return "", errors.New("--repo must name a non-bare git checkout")
	}
	return filepath.Dir(common), nil
}

func prepare(ctx context.Context, r adapterpkg.PrepareRequest, run adapterpkg.DirRunner) (adapterpkg.ResourceRef, error) {
	ref := adapterpkg.ResourceRef{Adapter: Name, ExcludeRemotes: r.ExcludeRemotes}
	for _, remote := range ref.ExcludeRemotes {
		if !ValidRemote(remote) {
			return ref, fmt.Errorf("invalid --landed-exclude-remote %q", remote)
		}
	}
	dir := r.Repo
	if r.Existing.Adopted {
		dir = r.Existing.Path
	}
	repo, err := mainCheckout(ctx, dir, run)
	if err != nil {
		return ref, err
	}
	ref.Repo = repo
	if root := os.Getenv("TREEHOUSE_ROOT"); root != "" {
		if !filepath.IsAbs(root) {
			root = filepath.Join(repo, root) // treehouse resolves relative roots from the repository root
		}
		ref.Root = filepath.Clean(root)
	}
	if !r.Existing.Adopted {
		ref.Holder = holder(r.Metadata.LeaseID)
		return ref, nil
	}
	ref.Adopted, ref.Path, ref.Claim = true, r.Existing.Path, r.Existing.Claim
	slots, err := worktree{run}.slots(ctx, ref)
	if err != nil {
		return ref, err
	}
	s, ok := claimed(slots, ref)
	if !ok || filepath.Clean(s.Path) != ref.Path || s.Status != "leased" {
		return ref, errors.New("slot is not durably leased under that treehouse lease id; run treehouse lease <name> --json and adopt with its lease_id")
	}
	for _, other := range r.Leased {
		if other.Path == ref.Path || other.Claim == ref.Claim || (other.Holder != "" && other.Holder == s.LeaseHolder) {
			return ref, errors.New("slot is already under a lease; inspect dibs ps")
		}
	}
	ref.Name = s.Name
	return ref, nil
}

func validate(_ string, metadata adapterpkg.Metadata, ref adapterpkg.ResourceRef) error {
	clean := func(path string) bool { return filepath.IsAbs(path) && filepath.Clean(path) == path }
	ok := ref.Adapter == Name && clean(ref.Repo) && (ref.Root == "" || clean(ref.Root)) &&
		(ref.Path == "") == (ref.Claim == "") && (ref.Path == "" || clean(ref.Path)) && ref.Context == "" && ref.DockerHost == "" && ref.File == ""
	if ref.Adopted {
		ok = ok && ref.Path != "" && ref.Holder == ""
	} else {
		ok = ok && ref.Holder == holder(metadata.LeaseID)
	}
	for _, remote := range ref.ExcludeRemotes {
		ok = ok && ValidRemote(remote)
	}
	if !ok {
		return errors.New("invalid treehouse worktree identity")
	}
	return nil
}

// New always registers the definition so recorded leases stay readable and
// releasable; enabled gates only creation and adoption.
func New(run adapterpkg.DirRunner, enabled bool) adapterpkg.Definition {
	w := worktree{run}
	return adapterpkg.Definition{Name: Name, Adapter: w, CreateAllowed: enabled, VerifiesAdoption: enabled, CleanupOutsideLock: true, OptIn: OptIn, HostScoped: true, Exists: w.exists,
		ResolveRepo: func(ctx context.Context, repo string) (string, error) { return mainCheckout(ctx, repo, run) },
		Prepare: func(ctx context.Context, r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
			return prepare(ctx, r, run)
		},
		Validate: validate,
		Env:      func(adapterpkg.ResourceRef) adapterpkg.Environment { return adapterpkg.Environment{} },
	}
}
