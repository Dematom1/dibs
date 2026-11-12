package adapter

import (
	"fmt"
	"strings"
)

// unlandedSample bounds how many paths or commits a refusal names.
const unlandedSample = 10

// UnlandedError reports work that cleanup refused to discard; the lease is retained.
type UnlandedError struct {
	Uncommitted []string
	Unpushed    []string
}

func sample(items []string) []string { return items[:min(len(items), unlandedSample)] }

func (e *UnlandedError) Error() string {
	text := fmt.Sprintf("unlanded work blocks cleanup; lease retained: %d uncommitted path(s), %d commit(s) on no remote", len(e.Uncommitted), len(e.Unpushed))
	if len(e.Uncommitted) > 0 {
		text += "; uncommitted: " + strings.Join(sample(e.Uncommitted), ", ")
	}
	if len(e.Unpushed) > 0 {
		text += "; unpushed: " + strings.Join(sample(e.Unpushed), ", ")
	}
	return text
}

// View is the stable machine-readable form of the refusal.
func (e *UnlandedError) View() map[string]any {
	return map[string]any{
		"uncommitted": append([]string{}, sample(e.Uncommitted)...), "uncommitted_count": len(e.Uncommitted),
		"unpushed": append([]string{}, sample(e.Unpushed)...), "unpushed_count": len(e.Unpushed),
	}
}
