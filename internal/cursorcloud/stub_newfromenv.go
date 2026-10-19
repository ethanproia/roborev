package cursorcloud

import (
	"fmt"
	"os"
	"strings"
)

// NewFromEnv builds a Client from CURSOR_API_KEY.
// replaced by slice 4
func NewFromEnv() (Client, error) {
	if strings.TrimSpace(os.Getenv(EnvAPIKey)) == "" {
		return nil, fmt.Errorf("cursorcloud: %s is required (replaced by slice 4)", EnvAPIKey)
	}
	// The value of CURSOR_API_KEY is never included in this error.
	return nil, fmt.Errorf("cursorcloud: NewFromEnv is not implemented (replaced by slice 4); %s is set", EnvAPIKey)
}
