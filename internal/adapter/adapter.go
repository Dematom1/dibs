package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResourceRef captures the cleanup scope before a resource is created.

type ResourceRef struct {
	Adopted    bool   `json:"adopted,omitempty"`
	Adapter    string `json:"adapter"`
	Name       string `json:"name,omitempty"`
	Context    string `json:"docker_context,omitempty"`
	DockerHost string `json:"docker_host,omitempty"`
	File       string `json:"file,omitempty"`
	// Pooled-worktree identity: the pinned repository and pool root, the slot,
	// and the allocator's own exclusion token and holder label.
	Repo           string   `json:"repo,omitempty"`
	Root           string   `json:"treehouse_root,omitempty"`
	Path           string   `json:"path,omitempty"`
	Claim          string   `json:"treehouse_lease_id,omitempty"`
	Holder         string   `json:"treehouse_lease_holder,omitempty"`
	ExcludeRemotes []string `json:"landed_exclude_remotes,omitempty"`
}
type Metadata struct {
	LeaseID string
	Machine string
	Owner   string
	Expires time.Time
	Labels  map[string]string
}
type Target struct {
	Ref      ResourceRef
	Metadata Metadata
	// DiscardUnlanded is set only by an explicit release; sweep never sets it.
	DiscardUnlanded bool
}
type PrepareRequest struct {
	Existing    ResourceRef
	Metadata    Metadata
	SnapshotDir string
	File        string
	// Repo and ExcludeRemotes are caller inputs for pooled-worktree leases.
	Repo           string
	ExcludeRemotes []string
	// Leased is a read-only view of the same adapter's recorded resources, for
	// adapters that verify adoption themselves.
	Leased []ResourceRef
}
type Environment struct {
	Command []string
	Exports map[string]string
}

// Create returns the identity to record, which may name what only creation
// learned (such as an allocator token); the lease manager records it, never
// the adapter. Destroy retains each built-in's existing absence verification.
// A separate VerifyGone contract belongs to the later extension-contract change.
type Adapter interface {
	Create(context.Context, Target) (ResourceRef, error)
	Destroy(context.Context, Target) error
}
type Definition struct {
	Discover      func(context.Context, ResourceRef) ([]map[string]any, error)
	Name          string
	Adapter       Adapter
	CreateAllowed bool
	RequiresFile  bool
	// OptIn names the setting that enables an optional built-in that is off.
	OptIn string
	// VerifiesAdoption marks an adapter whose Prepare verifies an existing
	// resource and its duplicates itself, without Docker discovery.
	VerifiesAdoption bool
	// CleanupOutsideLock runs Destroy without holding the host-wide state lock,
	// for slow cleanup that is conditional and idempotent.
	CleanupOutsideLock bool
	Prepare            func(context.Context, PrepareRequest) (ResourceRef, error)
	Validate           func(string, Metadata, ResourceRef) error
	Env                func(ResourceRef) Environment
}

type Registry struct{ definitions []Definition }

func NewRegistry(definitions ...Definition) Registry {
	seen := map[string]bool{}
	for _, d := range definitions {
		if d.Name == "" || seen[d.Name] || d.Adapter == nil || d.Prepare == nil || d.Validate == nil || d.Env == nil {
			panic("incomplete or duplicate resource adapter")
		}
		seen[d.Name] = true
	}
	return Registry{append([]Definition(nil), definitions...)}
}
func (r Registry) Lookup(name string) (Definition, bool) {
	for _, d := range r.definitions {
		if d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}
func (r Registry) CreateNames() []string {
	var names []string
	for _, d := range r.definitions {
		if d.CreateAllowed {
			names = append(names, d.Name)
		}
	}
	return names
}
func (r Registry) CheckCreate(name, file string) error {
	d, ok := r.Lookup(name)
	if ok && !d.CreateAllowed && d.OptIn != "" {
		return fmt.Errorf("the %s adapter is off; enable it with %s", name, d.OptIn)
	}
	if !ok || !d.CreateAllowed || d.RequiresFile != (file != "") {
		names := r.CreateNames()
		choices := strings.Join(names, ", ")
		if len(names) > 1 {
			choices = strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
		}
		var required []string
		for _, entry := range r.definitions {
			if entry.RequiresFile {
				required = append(required, entry.Name)
			}
		}
		return fmt.Errorf("use --adapter %s; --file is required only for %s", choices, strings.Join(required, ", "))
	}
	return nil
}
func (r Registry) Validate(dir string, t Target) error {
	d, ok := r.Lookup(t.Ref.Adapter)
	if !ok {
		return errors.New("unsupported resource adapter")
	}
	return d.Validate(dir, t.Metadata, t.Ref)
}
