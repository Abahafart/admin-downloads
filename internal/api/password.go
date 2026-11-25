package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password hashes are Argon2id PHC strings (config server.password_hash):
//
//	$argon2id$v=19$m=<KiB>,t=<iterations>,p=<parallelism>$<salt>$<hash>
//
// salt and hash are unpadded standard base64, as produced by the reference
// implementation and most libraries.

// Default parameters of HashPassword (OWASP-compliant, ~64 MiB).
const (
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 2
	argonSaltLen = 16
	argonKeyLen  = 32
)

// Upper bounds accepted when verifying, so a hostile config cannot make a
// login allocate gigabytes.
const (
	maxArgonMemory  = 1 << 20 // 1 GiB in KiB
	maxArgonTime    = 64
	maxArgonThreads = 64
)

// ErrBadHash is returned by VerifyPassword for malformed hashes.
var ErrBadHash = errors.New("api: malformed argon2id password hash")

// HashPassword returns the Argon2id PHC string of password.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("api: empty password")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("api: random salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the PHC hash. The
// comparison is constant-time.
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, ErrBadHash
	}
	if m == 0 || m > maxArgonMemory || t == 0 || t > maxArgonTime || p == 0 || p > maxArgonThreads {
		return false, ErrBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(strings.TrimRight(parts[4], "="))
	if err != nil || len(salt) < 8 {
		return false, ErrBadHash
	}
	want, err := b64.DecodeString(strings.TrimRight(parts[5], "="))
	if err != nil || len(want) < 16 || len(want) > 128 {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want))) //nolint:gosec // G115: len(want) is checked to be <= 128 above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
