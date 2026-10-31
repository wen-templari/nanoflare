package config

import (
	"log"
	"os"
	"strconv"
	"strings"
)

// EnvBoolOrDefault reads a boolean startup setting consistently in both daemons.
func EnvBoolOrDefault(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Fatalf("%s must be a boolean: %v", name, err)
	}
	return parsed
}
