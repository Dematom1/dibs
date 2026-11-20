package compose

import (
	"encoding/json"
	"testing"
)

// Compose down --volumes removes named volumes declared by the file; preserve every labeled resource and exclude unlabeled external resources.
func TestFromLabelsDeclaresOnlyLabeledResources(t *testing.T) {
	row := map[string]any{"evidence": map[string]any{
		"container:lab-web-1": map[string]any{"image": "alpine:3.23", "labels": map[string]string{"com.docker.compose.service": "web"}},
		"volume:lab_data":     map[string]any{"labels": map[string]string{"com.docker.compose.volume": "data"}},
		"network:lab_default": map[string]any{"labels": map[string]string{"com.docker.compose.network": "default"}},
		"volume:shared":       map[string]any{"labels": map[string]string{}},
	}}
	data, err := FromLabels(row)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["services"]["web"]["image"] != "alpine:3.23" || got["volumes"]["data"]["name"] != "lab_data" || got["networks"]["default"]["name"] != "lab_default" || len(got["volumes"]) != 1 || len(got["services"]["web"]["volumes"].([]any)) != 1 || len(got["services"]["web"]["networks"].([]any)) != 1 {
		t.Fatalf("snapshot must declare exactly the labeled resources: %s", data)
	}
	bare, err := FromLabels(map[string]any{"evidence": map[string]any{"container:c": map[string]any{"image": "alpine", "labels": map[string]string{"com.docker.compose.service": "web"}}}})
	var plain map[string]map[string]map[string]any
	if err != nil || json.Unmarshal(bare, &plain) != nil {
		t.Fatalf("%s %v", bare, err)
	}
	// Compose rejects a null service volumes or networks value, so each must be a list even when empty.
	for _, key := range []string{"volumes", "networks"} {
		if list, ok := plain["services"]["web"][key].([]any); !ok || len(list) != 0 {
			t.Fatalf("service %s must be an empty list: %s", key, bare)
		}
	}
	for _, bad := range []map[string]any{
		{"evidence": map[string]any{"volume:lab_data": map[string]any{"labels": map[string]string{"com.docker.compose.volume": "data"}}}},
		{"evidence": map[string]any{"container:x": map[string]any{"labels": map[string]string{"com.docker.compose.service": "web"}}}},
	} {
		if _, err := FromLabels(bad); err == nil {
			t.Fatalf("expected refusal for %v", bad)
		}
	}
}
