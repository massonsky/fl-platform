package tenantagent

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"strings"

	"github.com/google/uuid"
)

type tokenKind string

const (
	enrollmentToken tokenKind = "fl_enroll_v1"
	machineToken    tokenKind = "fl_agent_v1"
)

// A temporary value, never a persistence/HTTP DTO. The service may reveal Value
// once, after commit, using an explicit issuance response. Default JSON and fmt
// cannot accidentally disclose it, including diagnostic %#v output.
type issuedToken struct {
	ID     string `json:"id"`
	Value  string `json:"-"`
	Digest []byte `json:"-"`
}

func (issuedToken) String() string   { return "[redacted agent token]" }
func (issuedToken) GoString() string { return "[redacted agent token]" }

func issueToken(kind tokenKind) (issuedToken, error) {
	return issueTokenFrom(kind, rand.Reader)
}

// Reader injection is private to unit tests, never configurable at runtime.
func issueTokenFrom(kind tokenKind, source io.Reader) (issuedToken, error) {
	if (kind != enrollmentToken && kind != machineToken) || source == nil {
		return issuedToken{}, invalid()
	}
	id, err := uuid.NewRandomFromReader(source)
	if err != nil {
		return issuedToken{}, unavailable()
	}
	var secret [32]byte
	if _, err := io.ReadFull(source, secret[:]); err != nil {
		return issuedToken{}, unavailable()
	}
	value := string(kind) + "." + id.String() + "." + hex.EncodeToString(secret[:])
	digest := tokenDigest(value)
	return issuedToken{ID: id.String(), Value: value, Digest: digest[:]}, nil
}

func tokenDigest(value string) [32]byte {
	return sha256.Sum256([]byte("fl/tenant-agent/identity/v1\x00" + value))
}

// Parse requires canonical bytes: no trimming, Unicode, alternate UUID spelling
// or uppercase hex aliases. Callers must not log malformed input or return it.
func parseToken(kind tokenKind, value string) (issuedToken, error) {
	if (kind != enrollmentToken && kind != machineToken) || len(value) != len(kind)+102 {
		return issuedToken{}, unauthorized()
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != string(kind) || len(parts[1]) != 36 || len(parts[2]) != 64 {
		return issuedToken{}, unauthorized()
	}
	id, err := uuid.Parse(parts[1])
	if err != nil || id == uuid.Nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != parts[1] {
		return issuedToken{}, unauthorized()
	}
	secret, err := hex.DecodeString(parts[2])
	if err != nil || hex.EncodeToString(secret) != parts[2] {
		return issuedToken{}, unauthorized()
	}
	digest := tokenDigest(value)
	return issuedToken{ID: id.String(), Value: value, Digest: digest[:]}, nil
}

func matchesDigest(value issuedToken, stored []byte) bool {
	return len(value.Digest) == sha256.Size && len(stored) == sha256.Size &&
		subtle.ConstantTimeCompare(value.Digest, stored) == 1
}
