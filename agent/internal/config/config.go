package config

import (
	"fmt"
	"os"
)

type Config struct {
	RemediationName      string
	RemediationNamespace string
	APIKey               string
}

func Load() (*Config, error) {
	name := os.Getenv("REMEDIATION_NAME")
	ns := os.Getenv("REMEDIATION_NAMESPACE")
	apiKey := os.Getenv("API_KEY")
	if name == "" || ns == "" {
		return nil, fmt.Errorf("REMEDIATION_NAME and REMEDIATION_NAMESPACE must be set")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API_KEY must be set")
	}
	return &Config{
		RemediationName:      name,
		RemediationNamespace: ns,
		APIKey:               apiKey,
	}, nil
}
