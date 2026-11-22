package inference

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Keyring permits reading old ciphertext after changing the active wrapping key.
// Its JSON belongs in a server secret file, never the database or API response.
type Keyring struct {
	Current string            `json:"current"`
	Keys    map[string]string `json:"keys"`
}

func ParseKeyring(raw string) (Keyring, error) {
	var k Keyring
	if json.Unmarshal([]byte(raw), &k) != nil || k.Current == "" || len(k.Keys) == 0 {
		return k, errors.New("invalid AI keyring")
	}
	for version := range k.Keys {
		if _, err := k.aead(version); err != nil {
			return k, err
		}
	}
	_, err := k.aead(k.Current)
	return k, err
}

func (k Keyring) aead(version string) (cipher.AEAD, error) {
	key, err := hex.DecodeString(k.Keys[version])
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid AI wrapping key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (k Keyring) Seal(owner, id, secret string) ([]byte, string, error) {
	aead, err := k.aead(k.Current)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, "", err
	}
	return aead.Seal(nonce, nonce, []byte(secret), []byte(owner+"/"+id)), k.Current, nil
}

func (k Keyring) Open(owner, id, version string, data []byte) (string, error) {
	aead, err := k.aead(version)
	if err != nil {
		return "", err
	}
	if len(data) < aead.NonceSize() {
		return "", errors.New("invalid AI ciphertext")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(owner+"/"+id))
	return string(plain), err
}
