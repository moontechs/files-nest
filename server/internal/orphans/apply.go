package orphans

import (
	"fmt"
	"os"
)

// Apply removes each candidate file from disk. It is best-effort: a failed
// os.Remove appends an error to Result.Errors and processing continues with
// the remaining candidates. Successful removals are appended to
// Result.Removed, and the input candidates are preserved in
// Result.Candidates so callers can see the full attempt set.
func Apply(candidates []Candidate) Result {
	var result Result

	result.Candidates = candidates

	for _, candidate := range candidates {
		err := os.Remove(candidate.Path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("remove %s: %w", candidate.Path, err))

			continue
		}

		result.Removed = append(result.Removed, candidate)
	}

	return result
}
