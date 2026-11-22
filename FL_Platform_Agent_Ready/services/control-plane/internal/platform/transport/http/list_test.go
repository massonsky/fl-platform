package transport

import (
	"testing"
	"time"
)

func TestCursorScopeSignatureAndExpiry(t *testing.T) {
	server := Server{CursorKey: []byte("unit-test-key-not-a-deployment-secret")}
	c := cursor{Scope: "user/project", Cut: time.Now(), Offset: 50, Expires: time.Now().Add(time.Minute).Unix()}
	encoded := server.encodeCursor(c)
	got, err := server.decodeCursor(encoded, c.Scope)
	if err != nil || got.Offset != 50 {
		t.Fatal("valid cursor rejected")
	}
	if _, err = server.decodeCursor(encoded, "other/project"); err == nil {
		t.Fatal("cross-principal cursor accepted")
	}
	if _, err = server.decodeCursor("x"+encoded[1:], c.Scope); err == nil {
		t.Fatal("tampered cursor accepted")
	}
	c.Expires = time.Now().Add(-time.Minute).Unix()
	if _, err = server.decodeCursor(server.encodeCursor(c), c.Scope); err == nil {
		t.Fatal("expired cursor accepted")
	}
}
