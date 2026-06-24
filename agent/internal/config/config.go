package config

import (
	"fmt"
	"os"
)

type Config struct {
	RemediationName      string
	RemediationNamespace string
	APIKey               string
	Provider             string // "anthropic" (default) | "openai"
	Model                string
}

func Load() (*Config, error) {
	name := os.Getenv("REMEDIATION_NAME")
	ns := os.Getenv("REMEDIATION_NAMESPACE")
	if name == "" || ns == "" {
		return nil, fmt.Errorf("REMEDIATION_NAME and REMEDIATION_NAMESPACE must be set")
	}

	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("LLM_API_KEY must be set")
	}

	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "anthropic"
	}
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		switch provider {
		case "openai":
			model = "gpt-4o"
		default:
			model = "claude-sonnet-4-6"
		}
	}

	return &Config{
		RemediationName:      name,
		RemediationNamespace: ns,
		APIKey:               apiKey,
		Provider:             provider,
		Model:                model,
	}, nil
}
