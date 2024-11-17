package models

import (
	"errors"
)

// ValidateConfig validates the Config struct
func ValidateConfig(config *Config) error {
	// Check mandatory fields
	if config.Name == "" {
		return errors.New("missing mandatory field: name")
	}
	if config.Team == "" {
		return errors.New("missing mandatory field: team")
	}
	if config.Replicas == 0 {
		return errors.New("missing mandatory field: replicas")
	}
	if config.Type == "" {
		return errors.New("missing mandatory field: type")
	}
	if config.Ingress.Type == "" {
		return errors.New("missing mandatory field: ingress.type")
	}

	// Additional validation logic can be added here

	return nil
}
