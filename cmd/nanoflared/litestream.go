package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/clas/nanoflare/internal/database"
)

func generatedLitestreamReplicaConfig() (database.LitestreamReplicaConfig, error) {
	if prefix := strings.TrimSpace(os.Getenv("NANOFLARE_LITESTREAM_REPLICA_URL_PREFIX")); prefix != "" {
		return database.LitestreamReplicaConfig{
			URLPrefix:       prefix,
			Endpoint:        strings.TrimSpace(os.Getenv("NANOFLARE_LITESTREAM_ENDPOINT")),
			Region:          envOrDefault("NANOFLARE_LITESTREAM_REGION", "us-east-1"),
			AccessKeyID:     firstEnv("NANOFLARE_LITESTREAM_ACCESS_KEY_ID", "LITESTREAM_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"),
			SecretAccessKey: firstEnv("NANOFLARE_LITESTREAM_SECRET_ACCESS_KEY", "LITESTREAM_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"),
			ForcePathStyle:  boolEnv("NANOFLARE_LITESTREAM_FORCE_PATH_STYLE", false),
		}, nil
	}

	minioEndpoint := strings.TrimSpace(os.Getenv("MINIO_ENDPOINT"))
	minioBucket := strings.TrimSpace(os.Getenv("MINIO_BUCKET"))
	if minioEndpoint == "" || minioBucket == "" {
		return database.LitestreamReplicaConfig{}, errors.New("set NANOFLARE_LITESTREAM_REPLICA_URL_PREFIX or MINIO_ENDPOINT and MINIO_BUCKET when enabling generated Litestream config")
	}
	return database.LitestreamReplicaConfig{
		URLPrefix:       "s3://" + minioBucket + "/litestream",
		Endpoint:        minioEndpointURL(minioEndpoint, boolEnv("MINIO_SECURE", false)),
		Region:          envOrDefault("NANOFLARE_LITESTREAM_REGION", "us-east-1"),
		AccessKeyID:     firstEnv("NANOFLARE_LITESTREAM_ACCESS_KEY_ID", "MINIO_ACCESS_KEY"),
		SecretAccessKey: firstEnv("NANOFLARE_LITESTREAM_SECRET_ACCESS_KEY", "MINIO_SECRET_KEY"),
		ForcePathStyle:  true,
	}, nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func boolEnv(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func minioEndpointURL(endpoint string, secure bool) string {
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, endpoint)
}
