package lease

import (
	"context"
	"errors"
	"fmt"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
	"os"
	"path/filepath"
	"slices"
	"time"
)

func (m *Manager) readInventoryStore(dir string) (*store, error) {
	s := &store{Version: 1, Leases: map[string]Lease{}, dir: dir, resources: m.options.Adapters, binary: m.options.Version}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = s.decode(data)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read lease state (kept unchanged): %w", err)
	}
	return s, nil
}
func resourceLeased(ctx context.Context, s *store, r adapterpkg.ResourceRef, run adapterpkg.CommandRunner) (bool, error) {
	id, err := claimant(ctx, s, r, nil, "", run)
	return id != "", err
}

type AdoptRequest struct {
	Owner            ownerpkg.Reference
	TTL              time.Duration
	Resource         adapterpkg.ResourceRef
	File             string
	ExcludeRemotes   []string
	ProtectLiveOwner bool
	Policy           *policypkg.Policy
	Key              string
	// Pending binds Resource.Name to this pending lease instead of a new one.
	Pending string
}

// Adopt reports true when the request was already done and nothing changed.
func (m *Manager) Adopt(ctx context.Context, r AdoptRequest) (Lease, bool, error) {
	if r.Pending == "" && r.Key != "" && r.Resource.Adapter == "compose" && r.File != "" {
		var err error
		r.File, err = filepath.Abs(r.File)
		if err != nil {
			return Lease{}, false, err
		}
	}
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Lease{}, false, err
	}
	defer s.Close()
	if r.Pending != "" {
		return m.bindPending(ctx, s, host, PendingRequest{ID: r.Pending, Adapter: r.Resource.Adapter, Handle: r.Resource.Name, File: r.File, Scope: r.Resource})
	}
	if l, done, err := keyed(s, host, r.Key, func(l Lease) bool {
		ref := l.ResourceRef
		return l.KeyedBy == "adopt" && sameOwner(l.Owner, r.Owner) && l.ProtectLiveOwner == r.ProtectLiveOwner && ref.Adapter == r.Resource.Adapter && sameAdopted(ref, r.Resource) && ref.DockerHost == r.Resource.DockerHost && (ref.Repo == "" || slices.Equal(ref.ExcludeRemotes, r.ExcludeRemotes)) && (r.Resource.Adapter != "compose" || l.PendingFile == r.File)
	}); done || err != nil {
		return l, done, err
	}
	if m.checkOwner(r.Owner, now) != ownerpkg.Alive {
		return Lease{}, false, errors.New("owner became unavailable while waiting; refresh the owner and retry dibs adopt")
	}
	l, err := newLease(r.Owner, r.TTL, "adopted existing resource", nil, now)
	if err != nil {
		return Lease{}, false, err
	}
	l.Key = r.Key
	if r.Key != "" {
		l.KeyedBy = "adopt"
	}
	l.ProtectLiveOwner, l.Policy = r.ProtectLiveOwner, r.Policy
	if r.Key != "" && r.Resource.Adapter == "compose" {
		l.PendingFile = r.File
	}
	d, ok := m.options.Adapters.Lookup(r.Resource.Adapter)
	if !ok || d.RequiresFile != (r.File != "") || (d.Discover == nil && !d.VerifiesAdoption) || (d.Terminal != nil) != (r.Resource.Terminal != nil) {
		return Lease{}, false, errors.New("unsupported adoption target or missing Compose file")
	}
	if d.Terminal != nil {
		if held, ok := sameTerminalLease(ctx, s, d, r, now); ok {
			return detached(held), true, nil
		}
	}
	var related []adapterpkg.ResourceRef
	if d.VerifiesAdoption {
		// The adapter verifies existence and duplicates against this read-only view.
		for _, id := range s.IDs() {
			if ref := s.Leases[id].ResourceRef; ref.Adapter == d.Name {
				related = append(related, ref)
			}
		}
	} else if err := m.verifyDiscovered(ctx, s, d, r.Resource); err != nil {
		return Lease{}, false, err
	}
	r.Resource.Adopted = true
	l.ResourceRef, err = d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: target(l).Metadata, SnapshotDir: filepath.Join(s.dir, "resources", l.ID), File: r.File, Existing: r.Resource, Leased: related, ExcludeRemotes: r.ExcludeRemotes})
	if err != nil {
		return Lease{}, false, err
	}
	l.Labels = attribution(l)
	if s.Version < 2 {
		s.Version = 2
	}

	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		return Lease{}, false, err
	}
	return detached(l), false, nil
}

// sameAdopted matches a keyed adoption retry: a pooled worktree by slot path and
// allocator token, a terminal by server and ID, any other resource by name.
func sameAdopted(recorded, requested adapterpkg.ResourceRef) bool {
	if requested.Path != "" {
		return recorded.Path == requested.Path && recorded.Claim == requested.Claim
	}
	if recorded.Terminal != nil || requested.Terminal != nil {
		return recorded.Name == requested.Name && recorded.Terminal != nil && requested.Terminal != nil && recorded.Terminal.Server == requested.Terminal.Server
	}
	return recorded.Name == requested.Name
}

// verifyDiscovered requires an unleased resource that discovery finds by name.
func (m *Manager) verifyDiscovered(ctx context.Context, s *store, d adapterpkg.Definition, r adapterpkg.ResourceRef) error {
	if !d.ValidName(r.Name) {
		return errors.New("unsupported adoption target or missing Compose file")
	}
	leased := refuse("already_leased", "resource is already under a lease; inspect dibs ps")
	if other, err := claimant(ctx, s, r, nil, "", adapterpkg.RunCommand); err != nil {
		return err
	} else if other != "" {
		return leased
	}
	rows, err := d.Discover(ctx, r)
	if err != nil {
		return err
	}
	found := false
	for _, row := range rows {
		if row["name"] != r.Name {
			continue
		}
		found = true
		// A resource stamped for a pending lease belongs to that lease, not to a new one.
		if other, err := claimant(ctx, s, r, rowLabels(row), "", adapterpkg.RunCommand); err != nil {
			return err
		} else if other != "" {
			return leased
		}
	}
	if !found {
		return refuse("resource_absent", "resource does not exist; adoption refused")
	}
	return nil
}

// sameTerminalLease makes an unkeyed terminal adopt retry idempotent. It matches
// only the same owner, the same terminal and server, and the shell the lease
// pinned, so a reused ID or a different owner is still refused.
func sameTerminalLease(ctx context.Context, s *store, d adapterpkg.Definition, r AdoptRequest, now time.Time) (Lease, bool) {
	for _, id := range s.IDs() {
		l := s.Leases[id]
		pin := l.ResourceRef.Terminal
		if l.Releasing != nil || pin == nil || l.ResourceRef.Adapter != r.Resource.Adapter || l.ResourceRef.Name != r.Resource.Name || pin.Server != r.Resource.Terminal.Server || !sameOwner(l.Owner, r.Owner) || !now.Before(l.Expires) {
			continue
		}
		rows, err := d.Discover(ctx, r.Resource)
		if err != nil {
			return Lease{}, false
		}
		for _, row := range rows {
			if row["name"] == r.Resource.Name && row["pid"] == pin.PID && row["start"] == pin.Start {
				return l, true
			}
		}
	}
	return Lease{}, false
}
func (m *Manager) Orphans(ctx context.Context, r adapterpkg.ResourceRef) ([]map[string]any, error) {
	rows := []map[string]any{}
	for _, name := range m.options.Adapters.CreateNames() {
		d, _ := m.options.Adapters.Lookup(name)
		if d.Discover == nil {
			continue
		}
		found, err := d.Discover(ctx, r)
		if err != nil {
			return nil, err
		}
		rows = append(rows, found...)
	}
	return m.Unleased(ctx, r, rows)
}

// Unleased filters discovered rows to those no lease in the snapshot covers, comparing within r's scope.
func (m *Manager) Unleased(ctx context.Context, r adapterpkg.ResourceRef, rows []map[string]any) ([]map[string]any, error) {
	dir := m.options.Directory
	var err error
	if dir == "" {
		dir, err = resolveStateDir(false)
	}
	if err != nil {
		return nil, err
	}
	s, err := m.readInventoryStore(dir)
	if err != nil {
		return nil, err
	}
	unleased := []map[string]any{}
	for _, row := range rows {
		r.Adapter, r.Name = row["type"].(string), row["name"].(string)
		other, err := claimant(ctx, s, r, rowLabels(row), "", adapterpkg.RunCommand)
		if err != nil {
			return nil, err
		}
		if other == "" {
			unleased = append(unleased, row)
		}
	}
	return unleased, nil
}
