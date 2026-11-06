package main

import (
	"testing"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/openmeter"
)

func TestNewIdentityFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name             string
		issuer, clientID string
		dev              bool
		want             string // "dev", "gateway" or "" for an error
	}{
		{name: "nothing configured", want: ""},
		{name: "issuer without client ID", issuer: "https://kc/realms/nebari", want: ""},
		{name: "dev and issuer both set", issuer: "https://kc/realms/nebari", clientID: "console", dev: true, want: ""},
		{name: "dev mode", dev: true, want: "dev"},
		{name: "issuer and client ID", issuer: "https://kc/realms/nebari", clientID: "console", want: "gateway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, stop, err := newIdentity(tc.issuer, "", tc.clientID, "", tc.dev)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("got %T, want an error", id)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer stop()
			switch id.(type) {
			case openmeter.DevIdentity:
				if tc.want != "dev" {
					t.Fatalf("got DevIdentity, want %s", tc.want)
				}
			case openmeter.GatewayIdentity:
				if tc.want != "gateway" {
					t.Fatalf("got GatewayIdentity, want %s", tc.want)
				}
			default:
				t.Fatalf("got %T", id)
			}
		})
	}
}
