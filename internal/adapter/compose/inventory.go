package compose

import (
	"context"
	"encoding/json"
	"fmt"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	"strings"
)

// verifyProjectOwnership refuses an existing non-external volume or network that
// cleanup could take from someone else: one not labeled with the project, or a
// fixed-name one that another project's containers use.
func verifyProjectOwnership(ctx context.Context, ref adapterpkg.ResourceRef, config map[string]any, run adapterpkg.CommandRunner) error {
	for _, kind := range []string{"volume", "network"} {
		out, err := run(ctx, adapterpkg.DockerEnv(ref), "docker", "--context", ref.Context, kind, "ls", "--format", "{{.Name}}")
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for _, name := range strings.Fields(string(out)) {
			existing[name] = true
		}
		entries, _ := config[kind+"s"].(map[string]any)
		for key, entry := range entries {
			object, _ := entry.(map[string]any)
			if external, _ := object["external"].(bool); external {
				continue
			}
			name, _ := object["name"].(string)
			if name == "" {
				name = ref.Name + "_" + key
			}
			if !existing[name] {
				continue
			}
			out, err := run(ctx, adapterpkg.DockerEnv(ref), "docker", "--context", ref.Context, kind, "inspect", name, "--format", "{{json .Labels}}")
			if err != nil {
				return err
			}
			var labels map[string]string
			if json.Unmarshal(out, &labels) != nil || labels["com.docker.compose.project"] != ref.Name {
				return fmt.Errorf("%s %q is not labeled as part of this project; mark shared resources external", kind, name)
			}
			if name == ref.Name+"_"+key {
				continue
			}
			// The prefix keeps an unlabeled container visible as a non-empty field.
			out, err = run(ctx, adapterpkg.DockerEnv(ref), "docker", "--context", ref.Context, "ps", "-a", "--filter", kind+"="+name, "--format", `project={{.Label "com.docker.compose.project"}}`)
			if err != nil {
				return err
			}
			for _, user := range strings.Fields(string(out)) {
				if user != "project="+ref.Name {
					return fmt.Errorf("%s %q is used by containers outside this project; mark it external", kind, name)
				}
			}
		}
	}
	return nil
}
