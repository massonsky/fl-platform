package tenantagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func assertFailure(t *testing.T, err error, status int, code string) {
	t.Helper()
	var failure Failure
	if !errors.As(err, &failure) || failure.Status != status || failure.Code != code || err.Error() != code {
		t.Fatalf("expected safe %d/%s, got %v", status, code, err)
	}
}

func TestTokenNamespacesAndCanonicalRoundTrip(t *testing.T) {
	for _, kind := range []tokenKind{enrollmentToken, machineToken} {
		token, err := issueToken(kind)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parseToken(kind, token.Value)
		if err != nil || parsed.ID != token.ID || !matchesDigest(parsed, token.Digest) {
			t.Fatal("valid token failed round trip", err)
		}
		other := machineToken
		if kind == machineToken {
			other = enrollmentToken
		}
		_, err = parseToken(other, token.Value)
		assertFailure(t, err, 401, "UNAUTHORIZED")
		changed, err := issueToken(kind)
		if err != nil || changed.ID == token.ID || matchesDigest(changed, token.Digest) {
			t.Fatal("different issued identity matched", err)
		}
		for _, value := range []string{"", token.Value + " ", " " + token.Value, strings.ToUpper(token.Value),
			strings.Replace(token.Value, token.ID, "00000000-0000-0000-0000-000000000000", 1),
			token.Value[:len(token.Value)-1], token.Value[:len(token.Value)-1] + "z"} {
			parsed, err := parseToken(kind, value)
			assertFailure(t, err, 401, "UNAUTHORIZED")
			if parsed.ID != "" || parsed.Value != "" || parsed.Digest != nil {
				t.Fatal("rejected parse returned sensitive partial result")
			}
		}
		// A valid alternate secret/selector must still fail the stored digest.
		parts := strings.Split(token.Value, ".")
		for _, index := range []int{1, 2} {
			modified := append([]string(nil), parts...)
			modified[index] = strings.Split(changed.Value, ".")[index]
			candidate, err := parseToken(kind, strings.Join(modified, "."))
			if err != nil || matchesDigest(candidate, token.Digest) {
				t.Fatal("swapped secret/selector passed", err)
			}
		}
	}
}

func TestTokenFailureAndSafeRepresentations(t *testing.T) {
	for _, size := range []int{0, 15, 16, 47} {
		token, err := issueTokenFrom(enrollmentToken, bytes.NewReader(make([]byte, size)))
		assertFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
		if token.ID != "" || token.Value != "" || token.Digest != nil {
			t.Fatal("entropy failure leaked a partial token")
		}
	}
	_, err := issueTokenFrom("bad", bytes.NewReader(make([]byte, 48)))
	assertFailure(t, err, 400, "INVALID_REQUEST")
	token, err := issueToken(enrollmentToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{token, &token, Agent{}, CredentialMetadata{}} {
		raw, err := json.Marshal(v)
		if err != nil || bytes.Contains(raw, []byte(token.Value)) || bytes.Contains(raw, []byte("digest")) || bytes.Contains(raw, []byte("registry_slot")) {
			t.Fatal("metadata JSON leaked private fields", err)
		}
	}
	for _, printed := range []string{fmt.Sprintf("%v", token), fmt.Sprintf("%+v", token), fmt.Sprintf("%#v", token)} {
		if printed != "[redacted agent token]" {
			t.Fatal("diagnostic representation not redacted")
		}
	}
	if matchesDigest(token, nil) || matchesDigest(issuedToken{}, token.Digest) || matchesDigest(token, make([]byte, 31)) {
		t.Fatal("invalid digest length authorized")
	}
}

func TestAgentNameAndFixedPolicy(t *testing.T) {
	name, err := normalizedName("  Узел организации  ")
	if err != nil || name != "Узел организации" {
		t.Fatal("name normalization", err)
	}
	for _, invalidName := range []string{"", " \t", "node\nsecret", "node\x00", string([]byte{0xff}), strings.Repeat("я", 121)} {
		_, err = normalizedName(invalidName)
		assertFailure(t, err, 400, "INVALID_REQUEST")
	}
	if _, err = normalizedName(strings.Repeat("я", 120)); err != nil {
		t.Fatal("120-rune name rejected", err)
	}
	if IdentityLimit != 50 || IssueUnits != 20 || IssueCost != 2 || IssueWindow.Seconds() != 3600 || EnrollmentTTL.Seconds() != 900 || CredentialTTL.Hours() != 720 {
		t.Fatal("accepted policy drift")
	}
}
