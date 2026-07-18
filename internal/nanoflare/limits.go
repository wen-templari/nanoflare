package nanoflare

import (
	"errors"
	"fmt"
	"strings"
)

const (
	UsageLevelDefault = "default"
	UsageLevelPaid    = "paid"
)

var ErrUsageLimitExceeded = errors.New("usage limit exceeded")

type OrgLimits struct {
	Workers              *int
	KVNamespaces         *int
	ObjectStorageBuckets *int
	OAuthClients         *int
	ObjectStorageBytes   *int64
	KVStorageBytes       *int64
}

type UsageLimitError struct {
	Message string
}

func (e UsageLimitError) Error() string {
	return e.Message
}

func (e UsageLimitError) Unwrap() error {
	return ErrUsageLimitExceeded
}

func NormalizeUsageLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case UsageLevelPaid:
		return UsageLevelPaid
	default:
		return UsageLevelDefault
	}
}

func OrgLimitsForLevel(level string) OrgLimits {
	if NormalizeUsageLevel(level) == UsageLevelPaid {
		return OrgLimits{}
	}
	three := 3
	zero := 0
	objectStorageBytes := int64(500 * 1024 * 1024)
	kvStorageBytes := int64(100 * 1024 * 1024)
	return OrgLimits{
		Workers:              &three,
		KVNamespaces:         &three,
		ObjectStorageBuckets: &three,
		OAuthClients:         &zero,
		ObjectStorageBytes:   &objectStorageBytes,
		KVStorageBytes:       &kvStorageBytes,
	}
}

func usageLimitError(level, resource string, limit int) error {
	return UsageLimitError{Message: fmt.Sprintf("%s orgs are limited to %d %s", NormalizeUsageLevel(level), limit, resource)}
}

func usageByteLimitError(level, resource string, limit int64) error {
	return UsageLimitError{Message: fmt.Sprintf("%s orgs are limited to %d bytes of %s", NormalizeUsageLevel(level), limit, resource)}
}
