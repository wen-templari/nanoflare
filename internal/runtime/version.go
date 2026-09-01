package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

var workerdVersionDate = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)

// CompatibilityDate asks workerd for the newest compatibility date supported
// by the configured executable.
func CompatibilityDate(executable string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("detect workerd compatibility date: %w", err)
	}
	return compatibilityDateFromVersion(string(output))
}

func compatibilityDateFromVersion(version string) (string, error) {
	date := workerdVersionDate.FindString(version)
	if date == "" {
		return "", fmt.Errorf("workerd version %q does not contain a compatibility date", version)
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", fmt.Errorf("invalid workerd compatibility date %q: %w", date, err)
	}
	return date, nil
}
