package cfcompat

import (
	"errors"
	"strings"
)

func ParseTagFilter(value string) (string, bool, error) {
	name, mode, ok := strings.Cut(value, ":")
	if !ok || strings.ContainsAny(name, ",&:") || ValidateTags([]string{name}) != nil || (mode != "yes" && mode != "no") {
		return "", false, errors.New("tags supports one tag:yes or tag:no filter")
	}
	return name, mode == "yes", nil
}
