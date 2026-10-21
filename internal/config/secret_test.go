package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateSecretRequiresAtLeast32DecodedBytes(t *testing.T) {
	for _, key := range [][]byte{[]byte("x"), []byte(strings.Repeat("x", 31))} {
		if err := validateSecret("APP_SECRET", base64.StdEncoding.EncodeToString(key)); err == nil {
			t.Fatalf("accepted %d-byte decoded secret", len(key))
		}
	}
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	if err := validateSecret("APP_SECRET", key); err != nil {
		t.Fatalf("rejected 32-byte decoded secret: %v", err)
	}
}
