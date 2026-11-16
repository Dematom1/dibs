package adapter

import (
	"fmt"
	"strings"
)

// unlandedSample bounds how many paths or commits a refusal names.
const unlandedSample = 10

// UnlandedError reports work that cleanup refused to discard; the lease is retained.
type Process struct {
	Name string `json:"name"`
	PID  int    `json:"pid"`
}

type UnlandedError struct {
	Uncommitted []string
	Unpushed    []string
	Processes   []Process
}

func sample(items []string) []string { return items[:min(len(items), unlandedSample)] }

func (e *UnlandedError) Error() string {
	if len(e.Processes) > 0 {
		var names []string
		for _, process := range e.Processes {
			names = append(names, fmt.Sprintf("%s (pid %d)", process.Name, process.PID))
		}
		return "worktree owner is not quiesced; lease retained: " + strings.Join(names, ", ")
	}
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
	// Plain maps, so AXI renders each process as a name and pid row.
	processes := []map[string]any{}
	for _, p := range e.Processes {
		processes = append(processes, map[string]any{"name": p.Name, "pid": p.PID})
	}
	return map[string]any{
		"uncommitted": append([]string{}, sample(e.Uncommitted)...), "uncommitted_count": len(e.Uncommitted),
		"unpushed": append([]string{}, sample(e.Unpushed)...), "unpushed_count": len(e.Unpushed),
		"processes": processes,
	}
}
