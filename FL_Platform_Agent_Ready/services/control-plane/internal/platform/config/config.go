package config

import (
	"fmt"
	"os"
	"strings"
)

func Secret(name string) (string, error) {
	directory := os.Getenv("FL_SECRETS_DIR")
	if directory == "" {
		directory = "/run/secrets"
	}
	data, err := os.ReadFile(directory + "/" + name)
	if err != nil {
		return "", fmt.Errorf("read secret %s: %w", name, err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("empty secret %s", name)
	}
	return value, nil
}
func Env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
