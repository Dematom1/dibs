package cli

import (
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	composepkg "github.com/Dematom1/dibs/internal/adapter/compose"
	k3dpkg "github.com/Dematom1/dibs/internal/adapter/k3d"
	kindpkg "github.com/Dematom1/dibs/internal/adapter/kind"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	treehousepkg "github.com/Dematom1/dibs/internal/adapter/treehouseworktree"
	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
)

func newAdapters() adapterpkg.Registry {
	// DIBS_TREEHOUSE_WORKTREE has no BERTH_* alias.
	treehouse := treehousepkg.New(adapterpkg.RunCommandIn, optedIn("DIBS_TREEHOUSE_WORKTREE", configpkg.TreehouseWorktree))
	return adapterpkg.NewRegistry(nonepkg.New(), kindpkg.New(adapterpkg.RunCommand), k3dpkg.New(adapterpkg.RunCommand), composepkg.New(adapterpkg.RunCommand), treehouse)
}

func kubeconfigFor(r adapterpkg.Registry, l leasepkg.Lease) string {
	d, ok := r.Lookup(l.ResourceRef.Adapter)
	if !ok {
		return ""
	}
	return d.Env(l.ResourceRef).Exports["KUBECONFIG"]
}
