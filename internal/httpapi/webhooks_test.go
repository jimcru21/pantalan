package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestValidSignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	tests := []struct {
		name   string
		secret string
		header string
		want   bool
	}{
		{"matching signature", "s3cret", good, true},
		{"wrong secret", "other", good, false},
		{"missing prefix", "s3cret", good[len("sha256="):], false},
		{"not hex", "s3cret", "sha256=zz", false},
		{"no secret configured", "", good, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validSignature(tt.secret, body, tt.header); got != tt.want {
				t.Errorf("validSignature() = %v, want %v", got, tt.want)
			}
		})
	}
}
