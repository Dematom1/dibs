package treehouseworktree

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
)

const leaseID = "0123456789abcdef0123456789abcdef"

// fake answers each tool call by its argv (without the trailing --root pair) and records every call.
type fake struct {
	answers map[string][]reply
	calls   []string
	dirs    []string
	envs    [][]string
}

type reply struct {
	out string
	err error
}

func (f *fake) run(_ context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls, f.dirs, f.envs = append(f.calls, call), append(f.dirs, dir), append(f.envs, env)
	key := strings.TrimSuffix(strings.Split(call, " --root ")[0], " ")
	queue := f.answers[key]
	if len(queue) == 0 {
		return nil, errors.New("unexpected call: " + call)
	}
	f.answers[key] = queue[1:]
	return []byte(queue[0].out), queue[0].err
}

func status(t *testing.T, slots ...slot) string {
	t.Helper()
	data, err := json.Marshal(slots)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func target(path, claim string) adapterpkg.Target {
	return adapterpkg.Target{
		Ref:      adapterpkg.ResourceRef{Adapter: Name, Repo: "/repo", Root: "/pool", Path: path, Claim: claim, Holder: holder(leaseID)},
		Metadata: adapterpkg.Metadata{LeaseID: leaseID},
	}
}

const statusCall = "treehouse status --json"
const slotPath = "/pool/app-1/3/app"

func clean() map[string][]reply {
	return map[string][]reply{
		"git -C " + slotPath + " status --porcelain --untracked-files=all":                        {{out: ""}},
		"git -C " + slotPath + " log --format=%H HEAD --not --remotes --":                         {{out: ""}},
		"treehouse return " + slotPath + " --if-lease-id tok-A --if-lease-holder dibs:" + leaseID: {{out: ""}},
	}
}

func TestCreateRecordsTokenPathAndSlot(t *testing.T) {
	f := &fake{answers: map[string][]reply{
		"treehouse get --lease --json --lease-holder dibs:" + leaseID: {{out: `{"path":"` + slotPath + `","lease_id":"tok-A","lease_holder":"dibs:` + leaseID + `"}`}},
		statusCall: {{out: status(t, slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: holder(leaseID)})}},
	}}
	ref, err := worktree{f.run}.Create(context.Background(), target("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Path != slotPath || ref.Claim != "tok-A" || ref.Name != "3" || ref.Holder != holder(leaseID) {
		t.Fatalf("ref = %+v", ref)
	}
	if err := validate("", adapterpkg.Metadata{LeaseID: leaseID}, ref); err != nil {
		t.Fatal(err)
	}
	for i, call := range f.calls {
		if f.dirs[i] != "/repo" || !strings.HasSuffix(call, "--root /pool") {
			t.Fatalf("call %q ran in %q without the pinned root", call, f.dirs[i])
		}
		if !reflect.DeepEqual(f.envs[i], env()) {
			t.Fatalf("caller environment leaked: %v", f.envs[i])
		}
	}
}

func TestCreateKeepsPendingIntentOnBadAllocation(t *testing.T) {
	for name, answer := range map[string]reply{
		"tool failure":  {err: adapterpkg.CommandError{Code: 1}},
		"not json":      {out: "/pool/app-1/3/app\n"},
		"wrong holder":  {out: `{"path":"` + slotPath + `","lease_id":"tok-A","lease_holder":"someone"}`},
		"relative path": {out: `{"path":"app","lease_id":"tok-A","lease_holder":"dibs:` + leaseID + `"}`},
		"no token":      {out: `{"path":"` + slotPath + `","lease_holder":"dibs:` + leaseID + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{answers: map[string][]reply{"treehouse get --lease --json --lease-holder dibs:" + leaseID: {answer}}}
			in := target("", "")
			ref, err := worktree{f.run}.Create(context.Background(), in)
			if err == nil || !reflect.DeepEqual(ref, in.Ref) {
				t.Fatalf("ref = %+v, err = %v", ref, err)
			}
		})
	}
}

func TestCreateToleratesStatusFailureAfterAllocation(t *testing.T) {
	f := &fake{answers: map[string][]reply{
		"treehouse get --lease --json --lease-holder dibs:" + leaseID: {{out: `{"path":"` + slotPath + `","lease_id":"tok-A","lease_holder":"dibs:` + leaseID + `"}`}},
		statusCall: {{err: errors.New("busy")}},
	}}
	ref, err := worktree{f.run}.Create(context.Background(), target("", ""))
	if err != nil || ref.Claim != "tok-A" || ref.Name != "" {
		t.Fatalf("ref = %+v, err = %v", ref, err)
	}
}

func TestDestroyReturnsExactClaimAndVerifiesGone(t *testing.T) {
	answers := clean()
	answers[statusCall] = []reply{
		{out: status(t, slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: holder(leaseID)})},
		{out: status(t, slot{Name: "3", Path: slotPath, Status: "available"})},
	}
	f := &fake{answers: answers}
	if err := (worktree{f.run}).Destroy(context.Background(), target(slotPath, "tok-A")); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 5 {
		t.Fatalf("calls = %q", f.calls)
	}
}

func TestDestroyRecoversPendingClaimByHolder(t *testing.T) {
	answers := clean()
	answers[statusCall] = []reply{
		{out: status(t, slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: holder(leaseID)})},
		{out: status(t, slot{Name: "3", Path: slotPath, Status: "available"})},
	}
	f := &fake{answers: answers}
	if err := (worktree{f.run}).Destroy(context.Background(), target("", "")); err != nil {
		t.Fatal(err)
	}
	if len(f.answers["treehouse return "+slotPath+" --if-lease-id tok-A --if-lease-holder dibs:"+leaseID]) != 0 {
		t.Fatalf("pending claim was not returned: %q", f.calls)
	}
}

func TestDestroyNeverReturnsReassignedSlot(t *testing.T) {
	// The slot now carries token B under another holder; lease A is gone and nothing is returned.
	f := &fake{answers: map[string][]reply{statusCall: {{out: status(t, slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-B", LeaseHolder: "other"})}}}}
	if err := (worktree{f.run}).Destroy(context.Background(), target(slotPath, "tok-A")); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("touched a reassigned slot: %q", f.calls)
	}
}

func TestDestroyAbsenceNeedsEvidence(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	present := t.TempDir()
	for name, tc := range map[string]struct {
		path   string
		wantOK bool
	}{
		"pending claim with no holder match": {"", true},
		"pinned slot deleted":                {missing, true},
		"pinned slot unlisted but present":   {present, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{answers: map[string][]reply{statusCall: {{out: status(t, slot{Name: "9", Path: "/elsewhere", Status: "available"})}}}}
			claim := ""
			if tc.path != "" {
				claim = "tok-A"
			}
			err := (worktree{f.run}).Destroy(context.Background(), target(tc.path, claim))
			if (err == nil) != tc.wantOK {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestDestroyRefusesUnlandedWork(t *testing.T) {
	leased := slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: holder(leaseID)}
	many := strings.Repeat("abc\n", 12)
	for name, tc := range map[string]struct {
		porcelain, log            string
		uncommitted, unpushed     int
		firstUncommitted, exclude string
	}{
		"dirty tracked and untracked": {porcelain: " M a.txt\n?? new dir/b.txt\n", uncommitted: 2, firstUncommitted: "a.txt"},
		"unpushed commits":            {log: many, unpushed: 12},
		"excluded mirror":             {log: "abc\n", unpushed: 1, exclude: "gate"},
	} {
		t.Run(name, func(t *testing.T) {
			logCall := "git -C " + slotPath + " log --format=%H HEAD --not --remotes --"
			ref := target(slotPath, "tok-A")
			if tc.exclude != "" {
				logCall = "git -C " + slotPath + " log --format=%H HEAD --not --exclude=" + tc.exclude + "/* --remotes --"
				ref.Ref.ExcludeRemotes = []string{tc.exclude}
			}
			f := &fake{answers: map[string][]reply{
				statusCall: {{out: status(t, leased)}},
				"git -C " + slotPath + " status --porcelain --untracked-files=all": {{out: tc.porcelain}},
				logCall: {{out: tc.log}},
			}}
			err := (worktree{f.run}).Destroy(context.Background(), ref)
			var unlanded *adapterpkg.UnlandedError
			if !errors.As(err, &unlanded) {
				t.Fatalf("err = %v", err)
			}
			view := unlanded.View()
			if view["uncommitted_count"] != tc.uncommitted || view["unpushed_count"] != tc.unpushed || len(view["unpushed"].([]string)) > 10 {
				t.Fatalf("view = %v", view)
			}
			if tc.firstUncommitted != "" && view["uncommitted"].([]string)[0] != tc.firstUncommitted {
				t.Fatalf("view = %v", view)
			}
			for _, call := range f.calls {
				if strings.HasPrefix(call, "treehouse return") {
					t.Fatal("returned a slot holding unlanded work")
				}
			}
		})
	}
}

func TestDestroyDiscardOverrideForcesReturn(t *testing.T) {
	f := &fake{answers: map[string][]reply{
		statusCall: {
			{out: status(t, slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: holder(leaseID)})},
			{out: status(t, slot{Name: "3", Path: slotPath, Status: "available"})},
		},
		"treehouse return " + slotPath + " --if-lease-id tok-A --if-lease-holder dibs:" + leaseID + " --force": {{out: ""}},
	}}
	ref := target(slotPath, "tok-A")
	ref.DiscardUnlanded = true
	if err := (worktree{f.run}).Destroy(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
}

func TestDestroyFailuresRetainLease(t *testing.T) {
	leased := status(t, slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: holder(leaseID)})
	returnCall := "treehouse return " + slotPath + " --if-lease-id tok-A --if-lease-holder dibs:" + leaseID
	for name, tc := range map[string]struct {
		statuses []reply
		ret      reply
		want     string
	}{
		"status unavailable":     {statuses: []reply{{err: adapterpkg.CommandError{Code: 1}}}, want: "resource operation failed"},
		"status unreadable":      {statuses: []reply{{out: "{"}}, want: "cannot read treehouse status"},
		"treehouse kept dirty":   {statuses: []reply{{out: leased}}, ret: reply{err: adapterpkg.CommandError{Code: notReturned}}, want: "uncommitted changes"},
		"return failed":          {statuses: []reply{{out: leased}}, ret: reply{err: adapterpkg.CommandError{Code: 1}}, want: "resource operation failed"},
		"claim survived":         {statuses: []reply{{out: leased}, {out: leased}}, want: "survived"},
		"verify listing failure": {statuses: []reply{{out: leased}, {err: errors.New("busy")}}, want: "busy"},
	} {
		t.Run(name, func(t *testing.T) {
			answers := clean()
			answers[statusCall] = tc.statuses
			answers[returnCall] = []reply{tc.ret}
			f := &fake{answers: answers}
			err := (worktree{f.run}).Destroy(context.Background(), target(slotPath, "tok-A"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPrepareCreatePinsRepoRootAndHolder(t *testing.T) {
	gitCall := "git -C /work/linked rev-parse --path-format=absolute --git-common-dir"
	t.Setenv("TREEHOUSE_ROOT", "pools")
	f := &fake{answers: map[string][]reply{gitCall: {{out: "/work/main/.git\n"}}}}
	ref, err := prepare(context.Background(), adapterpkg.PrepareRequest{Metadata: adapterpkg.Metadata{LeaseID: leaseID}, Repo: "/work/linked", ExcludeRemotes: []string{"gate"}}, f.run)
	if err != nil {
		t.Fatal(err)
	}
	want := adapterpkg.ResourceRef{Adapter: Name, Repo: "/work/main", Root: "/work/main/pools", Holder: holder(leaseID), ExcludeRemotes: []string{"gate"}}
	if !reflect.DeepEqual(ref, want) {
		t.Fatalf("ref = %+v", ref)
	}
	if err := validate("", adapterpkg.Metadata{LeaseID: leaseID}, ref); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TREEHOUSE_ROOT", "")
	f = &fake{answers: map[string][]reply{gitCall: {{out: "/work/bare.git\n"}}}}
	if _, err := prepare(context.Background(), adapterpkg.PrepareRequest{Metadata: adapterpkg.Metadata{LeaseID: leaseID}, Repo: "/work/linked"}, f.run); err == nil {
		t.Fatal("accepted a bare repository")
	}
	if _, err := prepare(context.Background(), adapterpkg.PrepareRequest{Repo: "/work/linked", ExcludeRemotes: []string{"-x"}}, f.run); err == nil {
		t.Fatal("accepted an option-like remote")
	}
}

func TestPrepareAdoptionVerifiesTokenAndDuplicates(t *testing.T) {
	t.Setenv("TREEHOUSE_ROOT", "")
	gitCall := "git -C " + slotPath + " rev-parse --path-format=absolute --git-common-dir"
	leased := slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: "caller"}
	for name, tc := range map[string]struct {
		listing slot
		leased  []adapterpkg.ResourceRef
		wantErr bool
	}{
		"durably leased":             {listing: leased},
		"wrong token":                {listing: slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-B"}, wantErr: true},
		"process reservation only":   {listing: slot{Name: "3", Path: slotPath, Status: "in-use", LeaseID: "tok-A"}, wantErr: true},
		"token on another path":      {listing: slot{Name: "4", Path: "/pool/app-1/4/app", Status: "leased", LeaseID: "tok-A"}, wantErr: true},
		"path already leased":        {listing: leased, leased: []adapterpkg.ResourceRef{{Path: slotPath, Claim: "tok-old"}}, wantErr: true},
		"token already leased":       {listing: leased, leased: []adapterpkg.ResourceRef{{Path: "/other", Claim: "tok-A"}}, wantErr: true},
		"pending lease holds holder": {listing: slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: holder(leaseID)}, leased: []adapterpkg.ResourceRef{{Holder: holder(leaseID)}}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{answers: map[string][]reply{gitCall: {{out: "/repo/.git\n"}}, statusCall: {{out: status(t, tc.listing)}}}}
			ref, err := prepare(context.Background(), adapterpkg.PrepareRequest{Existing: adapterpkg.ResourceRef{Adopted: true, Path: slotPath, Claim: "tok-A"}, Leased: tc.leased}, f.run)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err == nil {
				want := adapterpkg.ResourceRef{Adapter: Name, Adopted: true, Name: "3", Repo: "/repo", Path: slotPath, Claim: "tok-A"}
				if !reflect.DeepEqual(ref, want) {
					t.Fatalf("ref = %+v", ref)
				}
				if err := validate("", adapterpkg.Metadata{LeaseID: leaseID}, ref); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAdoptedReturnIsConditionalOnTokenOnly(t *testing.T) {
	f := &fake{answers: map[string][]reply{
		statusCall: {
			{out: status(t, slot{Name: "3", Path: slotPath, Status: "leased", LeaseID: "tok-A", LeaseHolder: "caller"})},
			{out: status(t, slot{Name: "3", Path: slotPath, Status: "available"})},
		},
		"git -C " + slotPath + " status --porcelain --untracked-files=all": {{out: ""}},
		"git -C " + slotPath + " log --format=%H HEAD --not --remotes --":  {{out: ""}},
		"treehouse return " + slotPath + " --if-lease-id tok-A":            {{out: ""}},
	}}
	ref := target(slotPath, "tok-A")
	ref.Ref.Adopted, ref.Ref.Holder, ref.Ref.Root = true, "", ""
	if err := (worktree{f.run}).Destroy(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsTamperedIdentity(t *testing.T) {
	good := target(slotPath, "tok-A").Ref
	metadata := adapterpkg.Metadata{LeaseID: leaseID}
	if err := validate("", metadata, good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*adapterpkg.ResourceRef){
		"relative repo":         func(r *adapterpkg.ResourceRef) { r.Repo = "repo" },
		"unclean path":          func(r *adapterpkg.ResourceRef) { r.Path = "/pool/../x" },
		"path without token":    func(r *adapterpkg.ResourceRef) { r.Claim = "" },
		"foreign holder":        func(r *adapterpkg.ResourceRef) { r.Holder = "dibs:other" },
		"docker field":          func(r *adapterpkg.ResourceRef) { r.Context = "default" },
		"adopted with holder":   func(r *adapterpkg.ResourceRef) { r.Adopted = true },
		"adopted without slot":  func(r *adapterpkg.ResourceRef) { r.Adopted, r.Holder, r.Path, r.Claim = true, "", "", "" },
		"option-like exclusion": func(r *adapterpkg.ResourceRef) { r.ExcludeRemotes = []string{"--all"} },
		"relative root":         func(r *adapterpkg.ResourceRef) { r.Root = "pool" },
	} {
		t.Run(name, func(t *testing.T) {
			ref := good
			mutate(&ref)
			if err := validate("", metadata, ref); err == nil {
				t.Fatal("accepted tampered identity")
			}
		})
	}
}

func TestDefinitionOptIn(t *testing.T) {
	off, on := New(nil, false), New(nil, true)
	if off.CreateAllowed || off.VerifiesAdoption || !on.CreateAllowed || !on.VerifiesAdoption || !off.CleanupOutsideLock || off.OptIn == "" || off.Discover != nil {
		t.Fatalf("off = %+v, on = %+v", off, on)
	}
	err := adapterpkg.NewRegistry(off).CheckCreate(Name, "")
	if err == nil || !strings.Contains(err.Error(), "DIBS_TREEHOUSE_WORKTREE") {
		t.Fatalf("err = %v", err)
	}
	if err := adapterpkg.NewRegistry(on).CheckCreate(Name, ""); err != nil {
		t.Fatal(err)
	}
}
