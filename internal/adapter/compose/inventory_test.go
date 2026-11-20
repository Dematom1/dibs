package compose

import (
	"context"
	"encoding/json"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	"strings"
	"testing"
)

func TestComposeAdoptionRequiresProjectLabels(t *testing.T) {
	ref := adapterpkg.ResourceRef{Adapter: "compose", Name: "lab", Context: "test"}
	config := map[string]any{"volumes": map[string]any{"data": map[string]any{"name": "lab_data"}}}
	for _, project := range []string{"", "other", "lab"} {
		runner := func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "volume ls") {
				return []byte("lab_data"), nil
			}
			if strings.Contains(strings.Join(args, " "), "inspect") {
				return json.Marshal(map[string]string{"com.docker.compose.project": project})
			}
			return nil, nil
		}
		err := verifyProjectOwnership(context.Background(), ref, config, runner)
		if (err == nil) != (project == "lab") {
			t.Fatalf("project %q: %v", project, err)
		}
	}
}

func TestFixedNamesNeedProjectOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, exists, label, users string
		ok                         bool
	}{
		{name: "absent, compose labels it on create", ok: true},
		{name: "owned and used only by the project", exists: "alteran_dev", label: "lab", users: "project=lab\nproject=lab\n", ok: true},
		{name: "unlabeled", exists: "alteran_dev"},
		{name: "another project's", exists: "alteran_dev", label: "other"},
		{name: "shared with another project", exists: "alteran_dev", label: "lab", users: "project=lab\nproject=other\n"},
		{name: "used by an unlabeled container", exists: "alteran_dev", label: "lab", users: "project=\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
				call := strings.Join(args, " ")
				switch {
				case strings.Contains(call, "compose -p lab"):
					return []byte(`{"services":{"app":{"image":"alpine","container_name":"alteran_app"}},"networks":{"dev":{"name":"alteran_dev"}}}`), nil
				case strings.Contains(call, "network ls"):
					return []byte(tc.exists), nil
				case strings.Contains(call, "network inspect"):
					return json.Marshal(map[string]string{"com.docker.compose.project": tc.label})
				case strings.Contains(call, "ps -a --filter network=alteran_dev"):
					return []byte(tc.users), nil
				}
				return nil, nil
			}
			request := adapterpkg.PrepareRequest{SnapshotDir: t.TempDir(), File: "compose.yaml", Existing: adapterpkg.ResourceRef{Name: "lab", Context: "test", DockerHost: "unix:///docker.sock", Adopted: true}}
			if _, err := prepare(context.Background(), request, runner, false); err == nil {
				t.Fatal("fixed name accepted without the opt-in")
			}
			if _, err := prepare(context.Background(), request, runner, true); (err == nil) != tc.ok {
				t.Fatalf("ok=%v: %v", tc.ok, err)
			}
		})
	}
}
