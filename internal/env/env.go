// Package env reads typed environment variables with fallbacks, sharing one
// truthy/falsy vocabulary across the binary and config loader.
package env

import (
	"os"
	"strconv"
)

// Or returns the value of key, or def when key is unset or empty.
func Or(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// IntOr returns key parsed as an int, or def when key is unset or unparsable.
func IntOr(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

// Bool returns key parsed as a bool from the accepted vocabulary
// (1/true/TRUE/yes, 0/false/FALSE/no), or def for anything else.
func Bool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes":
		return true
	case "0", "false", "FALSE", "no":
		return false
	}
	return def
}
