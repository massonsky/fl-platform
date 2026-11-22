package inference

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestSecretBindingAndRotation(t *testing.T) {
	k := Keyring{Current: "v1", Keys: map[string]string{"v1": strings.Repeat("ab", 32)}}
	data, version, err := k.Seal("owner", "provider", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "test-token") {
		t.Fatal("plaintext stored")
	}
	for _, pair := range [][2]string{{"other", "provider"}, {"owner", "other"}} {
		if _, err = k.Open(pair[0], pair[1], version, data); err == nil {
			t.Fatal("cross-owner ciphertext accepted")
		}
	}
	k.Keys["v2"] = strings.Repeat("cd", 32)
	k.Current = "v2"
	if plain, err := k.Open("owner", "provider", version, data); err != nil || plain != "test-token" {
		t.Fatal("old key unreadable")
	}
	data[len(data)-1] ^= 1
	if _, err = k.Open("owner", "provider", version, data); err == nil {
		t.Fatal("tampering accepted")
	}
	if _, err = ParseKeyring(`{"current":"missing","keys":{"v1":"ab"}}`); err == nil {
		t.Fatal("invalid ring accepted")
	}
}

func TestEndpointPolicy(t *testing.T) {
	p := Policy{Allowed: []string{"https://models.example/v1"}}
	for _, raw := range []string{"http://models.example/v1", "https://evil.example/v1", "https://u:p@models.example/v1", "https://models.example/v1?x=1", "https://models.example:444/v1", "https://models.example/v1#x", "https://models.example/v1/../v1", "https://models.example/v%31"} {
		if _, err := p.Validate(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if value, err := p.Validate("https://models.example/v1/"); err != nil || value != "https://models.example/v1" {
		t.Fatal(value, err)
	}
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::7f00:1", "2002:7f00:1::"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("unsafe IP %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("public IP rejected %s", raw)
		}
	}
	if _, err := safeDial(context.Background(), "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("loopback dial allowed")
	}
	if err := newClient().CheckRedirect(nil, nil); err == nil {
		t.Fatal("redirect allowed")
	}
}

func TestModelsProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"valid", `{"data":[{"id":"z"},{"id":"a"},{"id":"z"}]}`, 200, ""},
		{"empty", `{"data":[]}`, 200, ""},
		{"missing", `{}`, 200, "INVALID_PROVIDER_RESPONSE"},
		{"badID", `{"data":[{"id":""}]}`, 200, "INVALID_PROVIDER_RESPONSE"},
		{"large", strings.Repeat("x", (1<<20)+1), 200, "INVALID_PROVIDER_RESPONSE"},
		{"auth", "upstream-secret-must-not-leak", 401, "PROVIDER_AUTH_FAILED"},
		{"throttle", "", 429, "PROVIDER_RATE_LIMITED"},
		{"server", "", 503, "PROVIDER_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("invalid upstream request")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			items, err := fetchModels(context.Background(), server.Client(), server.URL+"/v1", "test-token")
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "valid" && (len(items) != 2 || items[0].ID != "a") {
					t.Fatal(items)
				}
				return
			}
			var f Failure
			if !errors.As(err, &f) || f.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
}
