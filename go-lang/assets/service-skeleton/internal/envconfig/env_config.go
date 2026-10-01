// Package envconfig reads typed settings from environment variables.
package envconfig

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// String returns $key or def when unset or empty.
func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Bool returns $key parsed by strconv.ParseBool, or def when unset.
// An unparsable value is an error so typos like "ture" fail at startup.
func Bool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q: %w", key, v, err)
	}
	return b, nil
}

// Duration returns $key parsed by time.ParseDuration ("500ms", "15s"), or def.
func Duration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, v, err)
	}
	return d, nil
}

// Int returns $key parsed as a base-10 integer, or def when unset.
func Int(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, v, err)
	}
	return n, nil
}
