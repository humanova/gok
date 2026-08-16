package mapcuration

import (
	"fmt"
	"strings"
)

type RequiredPath struct {
	Flag  string
	Value string
}

// Rejects missing input artifacts before a stage starts work.
func RequirePaths(paths ...RequiredPath) error {
	for _, path := range paths {
		if strings.TrimSpace(path.Value) == "" {
			return fmt.Errorf("%s is required", path.Flag)
		}
	}
	return nil
}
