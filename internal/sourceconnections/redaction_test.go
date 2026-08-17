package sourceconnections

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/githubapp"
)

func TestExportedCredentialTypesRedactGenericSerializationAndLogging(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name      string
		value     any
		sentinels []string
	}{
		{"provider device", githubapp.DeviceAuthorization{DeviceCode: "device-sentinel", UserCode: "user-sentinel", ExpiresIn: time.Minute}, []string{"device-sentinel", "user-sentinel"}},
		{"provider tokens", githubapp.TokenBundle{AccessToken: "access-sentinel", RefreshToken: "refresh-sentinel"}, []string{"access-sentinel", "refresh-sentinel"}},
		{"device start", DeviceStart{ConnectionID: "11111111111111111111111111111111", UserCode: "user-sentinel", ExpiresAt: now}, []string{"user-sentinel"}},
		{"token bundle", TokenBundle{Generation: 1, AccessToken: "access-sentinel", RefreshToken: "refresh-sentinel"}, []string{"access-sentinel", "refresh-sentinel"}},
		{"token exchange", TokenExchange{AccessToken: "access-sentinel", RefreshToken: "refresh-sentinel"}, []string{"access-sentinel", "refresh-sentinel"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			logger.Info("value", "value", test.value)
			rendered := string(encoded) + fmt.Sprintf("%v %#v", test.value, test.value) + logs.String()
			for _, sentinel := range test.sentinels {
				if strings.Contains(rendered, sentinel) {
					t.Fatalf("generic rendering exposed %q: %s", sentinel, rendered)
				}
			}
		})
	}
}
