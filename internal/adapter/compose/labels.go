package compose

import (
	"encoding/json"
	"errors"
	"strings"
)

// FromLabels rebuilds a minimal Compose file for an orphan row from the compose
// labels on its containers, volumes, and networks, so the stack can be adopted
// and cleaned up after its original file is gone. It declares exactly what
// exists: unlabeled (external) resources stay out and are never removed.
func FromLabels(row map[string]any) ([]byte, error) {
	services, volumes, networks := map[string]any{}, map[string]any{}, map[string]any{}
	evidence, _ := row["evidence"].(map[string]any)
	for key, value := range evidence {
		kind, name, _ := strings.Cut(key, ":")
		object, _ := value.(map[string]any)
		labels, _ := object["labels"].(map[string]string)
		switch kind {
		case "container":
			service := labels["com.docker.compose.service"]
			image, _ := object["image"].(string)
			if service == "" || image == "" {
				return nil, errors.New("container " + name + " lacks its compose service label or image")
			}
			services[service] = map[string]any{"image": image}
		case "volume":
			if entry := labels["com.docker.compose.volume"]; entry != "" {
				volumes[entry] = map[string]any{"name": name}
			}
		case "network":
			if entry := labels["com.docker.compose.network"]; entry != "" {
				networks[entry] = map[string]any{"name": name}
			}
		}
	}
	if len(services) == 0 {
		return nil, errors.New("the project has no containers to rebuild a Compose file from; adopt it with dibs adopt --file")
	}
	// Compose drops top-level volumes and networks no service uses, and down --volumes would then leave them behind.
	// Empty lists, not nil: Compose rejects a null service volumes or networks value.
	mounts, attached := []string{}, []string{}
	for key := range volumes {
		mounts = append(mounts, key+":/dibs-orphan/"+key)
	}
	for key := range networks {
		attached = append(attached, key)
	}
	for _, service := range services {
		service.(map[string]any)["volumes"], service.(map[string]any)["networks"] = mounts, attached
	}
	return json.Marshal(map[string]any{"services": services, "volumes": volumes, "networks": networks})
}
