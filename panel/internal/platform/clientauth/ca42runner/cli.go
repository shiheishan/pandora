package ca42runner

import (
	"errors"
	"regexp"
)

var attemptIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type Command struct {
	AttemptID string
}

// ParseCLI deliberately exposes exactly one caller-controlled value. All
// authority roots, paths, identities, timeouts, and execution policy are
// compiled into or obtained by the root-owned runner.
func ParseCLI(args []string) (Command, error) {
	if len(args) != 3 || args[0] != "execute" || args[1] != "--attempt-id" ||
		!attemptIDPattern.MatchString(args[2]) {
		return Command{}, errors.New("exact command execute --attempt-id TOKEN required")
	}
	return Command{AttemptID: args[2]}, nil
}
