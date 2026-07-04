package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open env file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !ok || name == "" {
			return fmt.Errorf("parse env file line %d: expected NAME=value", lineNumber)
		}
		if strings.HasPrefix(value, `"`) {
			parsed, err := strconv.Unquote(value)
			if err != nil {
				return fmt.Errorf("parse env file line %d: %w", lineNumber, err)
			}
			value = parsed
		}
		if strings.HasPrefix(value, `'`) {
			if len(value) < 2 || !strings.HasSuffix(value, `'`) {
				return fmt.Errorf("parse env file line %d: unmatched single quote", lineNumber)
			}
			value = strings.TrimSuffix(strings.TrimPrefix(value, `'`), `'`)
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set env file line %d: %w", lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	return nil
}
