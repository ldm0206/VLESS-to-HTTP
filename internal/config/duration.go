package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that reads and writes as "30s" in both YAML and
// JSON, so the config file stays hand-editable.
type Duration time.Duration

// D returns the value as a standard time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalYAML writes the duration as a human-readable string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// UnmarshalYAML accepts "30s", "5m", or a bare number of seconds.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw any
	if err := value.Decode(&raw); err != nil {
		return err
	}
	return d.set(raw)
}

// MarshalJSON writes the duration as a human-readable string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts "30s", "5m", or a bare number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	return d.set(raw)
}

func (d *Duration) set(raw any) error {
	switch v := raw.(type) {
	case nil:
		*d = 0
		return nil
	case string:
		if v == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", v, err)
		}
		*d = Duration(parsed)
		return nil
	case int:
		*d = Duration(time.Duration(v) * time.Second)
		return nil
	case int64:
		*d = Duration(time.Duration(v) * time.Second)
		return nil
	case float64:
		*d = Duration(time.Duration(v * float64(time.Second)))
		return nil
	default:
		// YAML may hand us a quoted number.
		if s, ok := raw.(string); ok {
			if n, err := strconv.Atoi(s); err == nil {
				*d = Duration(time.Duration(n) * time.Second)
				return nil
			}
		}
		return fmt.Errorf("invalid duration value %v", raw)
	}
}
