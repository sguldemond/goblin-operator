package config

import (
	"fmt"
	"os"
)

type Config struct {
	IncidentName      string
	IncidentNamespace string
	APIKey            string
	Provider          string // "anthropic" (default) | "openai"
	Model             string
}

func Load() (*Config, error) {
	name := os.Getenv("INCIDENT_NAME")
	ns := os.Getenv("INCIDENT_NAMESPACE")
	if name == "" || ns == "" {
		return nil, fmt.Errorf("INCIDENT_NAME and INCIDENT_NAMESPACE must be set")
	}

	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("LLM_API_KEY must be set")
	}

	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "anthropic"
	}
	switch provider {
	case "anthropic", "openai":
	default:
		return nil, fmt.Errorf("unknown LLM_PROVIDER %q (supported: anthropic, openai)", provider)
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
		IncidentName:      name,
		IncidentNamespace: ns,
		APIKey:            apiKey,
		Provider:          provider,
		Model:             model,
	}, nil
}
