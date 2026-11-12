package cli

import (
	"os"
	"strconv"

	configpkg "github.com/Dematom1/dibs/internal/config"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func newOwners() ownerpkg.Registry {
	owners := []ownerpkg.Definition{ownerpkg.PID(), ownerpkg.Session(), ownerpkg.Heartbeat()}
	if prOwnerEnabled() {
		owners = append(owners, ownerpkg.PR())
	}
	return ownerpkg.NewRegistry(owners...)
}

// DIBS_PR_OWNER overrides the user config either way and has no BERTH_* alias.
func prOwnerEnabled() bool { return optedIn("DIBS_PR_OWNER", configpkg.PROwner) }

// optedIn lets a set DIBS_* variable override the user config either way.
func optedIn(variable string, config func() bool) bool {
	if value, ok := os.LookupEnv(variable); ok {
		enabled, err := strconv.ParseBool(value)
		return err == nil && enabled
	}
	return config()
}
