package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
)

// KeyringService is the service name under which secrets are stored in the
// OS keyring (Secret Service on Linux, Keychain on macOS, Credential Manager
// on Windows). The keyring "user" is the Account.ID.
const KeyringService = "admindescargas"

// keyringChunkSize is the largest value written in one keyring entry.
// Windows Credential Manager refuses blobs over 2560 bytes and the macOS
// `security` command line is limited to 4096 bytes after base64 (+33%), so
// big secrets (cookie jars, OAuth tokens with id_token) are split.
const keyringChunkSize = 2000

// chunkHeaderPrefix marks an entry whose value is spread over several
// "<id>.chunk.<n>" entries: "adesc-chunked:v1:<count>:<sha256 hex>".
const chunkHeaderPrefix = "adesc-chunked:v1:"

// maxKeyringChunks bounds a corrupted header (and the secret size: ~512 KB).
const maxKeyringChunks = 256

// keyringBackend is the subset of go-keyring used here, so tests can inject
// size limits or failures.
type keyringBackend interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

// osKeyring forwards to the go-keyring package functions (which honor
// keyring.MockInit in tests).
type osKeyring struct{}

func (osKeyring) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (osKeyring) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (osKeyring) Delete(s, u string) error        { return keyring.Delete(s, u) }

// KeyringStore is a SecretStore backed by the OS keyring. Each secret is
// JSON-encoded under (KeyringService, accountID); values larger than the
// platform limits are chunked transparently.
type KeyringStore struct {
	service string
	kr      keyringBackend
	mu      sync.Mutex // serializes multi-entry (chunked) updates
}

var _ SecretStore = (*KeyringStore)(nil)

// NewKeyringStore returns a store using the OS keyring under KeyringService.
// It does not check that a keyring is reachable; use ProbeKeyring.
func NewKeyringStore() *KeyringStore {
	return &KeyringStore{service: KeyringService, kr: osKeyring{}}
}

// Backend returns "keyring".
func (k *KeyringStore) Backend() string { return BackendKeyring }

func chunkKey(id string, n int) string { return id + ".chunk." + strconv.Itoa(n) }

func checkID(id string) error {
	if id == "" || strings.Contains(id, ".chunk.") {
		return fmt.Errorf("accounts: invalid account id %q", id)
	}
	return nil
}

// Get implements SecretStore.
func (k *KeyringStore) Get(ctx context.Context, id string) (Secret, error) {
	if err := ctx.Err(); err != nil {
		return Secret{}, err
	}
	if err := checkID(id); err != nil {
		return Secret{}, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	raw, err := k.read(id)
	if err != nil {
		return Secret{}, err
	}
	var s Secret
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Secret{}, fmt.Errorf("accounts: keyring entry for %s is corrupted: %w", id, errors.New("invalid JSON"))
	}
	return s, nil
}

// read returns the full JSON value, reassembling chunks. Caller holds mu.
func (k *KeyringStore) read(id string) (string, error) {
	head, err := k.kr.Get(k.service, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", fmt.Errorf("accounts: keyring get: %w", err)
	}
	n, sum, chunked, err := parseChunkHeader(head)
	if err != nil {
		return "", fmt.Errorf("accounts: keyring entry for %s: %w", id, err)
	}
	if !chunked {
		return head, nil
	}
	var b strings.Builder
	for i := range n {
		part, err := k.kr.Get(k.service, chunkKey(id, i))
		if err != nil {
			return "", fmt.Errorf("accounts: keyring entry for %s: missing chunk %d: %w", id, i, err)
		}
		b.WriteString(part)
	}
	full := b.String()
	if got := sha256.Sum256([]byte(full)); hex.EncodeToString(got[:]) != sum {
		return "", fmt.Errorf("accounts: keyring entry for %s: chunks do not match their checksum", id)
	}
	return full, nil
}

func parseChunkHeader(v string) (n int, sum string, chunked bool, err error) {
	rest, ok := strings.CutPrefix(v, chunkHeaderPrefix)
	if !ok {
		return 0, "", false, nil
	}
	cnt, sum, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, "", false, errors.New("malformed chunk header")
	}
	n, err = strconv.Atoi(cnt)
	if err != nil || n < 1 || n > maxKeyringChunks || len(sum) != 64 {
		return 0, "", false, errors.New("malformed chunk header")
	}
	return n, sum, true, nil
}

// chunkCount returns how many chunk entries currently exist for id (0 for a
// plain or missing entry). Caller holds mu.
func (k *KeyringStore) chunkCount(id string) int {
	head, err := k.kr.Get(k.service, id)
	if err != nil {
		return 0
	}
	n, _, chunked, err := parseChunkHeader(head)
	if err != nil || !chunked {
		return 0
	}
	return n
}

// Set implements SecretStore.
func (k *KeyringStore) Set(ctx context.Context, id string, s Secret) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkID(id); err != nil {
		return err
	}
	data, err := json.Marshal(s) //nolint:gosec // G117: this is the secret store; the JSON goes to the OS keyring
	if err != nil {
		return fmt.Errorf("accounts: encode secret: %w", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	old := k.chunkCount(id)
	val := string(data)

	var newChunks int
	if len(val) <= keyringChunkSize {
		err = k.kr.Set(k.service, id, val)
		if errors.Is(err, keyring.ErrSetDataTooBig) {
			newChunks, err = k.writeChunked(id, val, keyringChunkSize/2)
		}
	} else {
		newChunks, err = k.writeChunked(id, val, keyringChunkSize)
	}
	if err != nil {
		return fmt.Errorf("accounts: keyring set: %w", err)
	}
	for i := newChunks; i < old; i++ {
		_ = k.kr.Delete(k.service, chunkKey(id, i))
	}
	return nil
}

// writeChunked writes the chunks first and the header last, so a reader
// never sees a header pointing to missing chunks of a newer value.
func (k *KeyringStore) writeChunked(id, val string, size int) (int, error) {
	parts := splitUTF8(val, size)
	if len(parts) > maxKeyringChunks {
		return 0, keyring.ErrSetDataTooBig
	}
	for i, p := range parts {
		if err := k.kr.Set(k.service, chunkKey(id, i), p); err != nil {
			return 0, err
		}
	}
	sum := sha256.Sum256([]byte(val))
	head := chunkHeaderPrefix + strconv.Itoa(len(parts)) + ":" + hex.EncodeToString(sum[:])
	if err := k.kr.Set(k.service, id, head); err != nil {
		return 0, err
	}
	return len(parts), nil
}

// splitUTF8 splits s into pieces of at most size bytes without cutting a
// UTF-8 sequence (some keyrings require valid UTF-8 strings).
func splitUTF8(s string, size int) []string {
	var out []string
	for len(s) > 0 {
		n := min(size, len(s))
		for n < len(s) && n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		if n == 0 {
			n = min(size, len(s))
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

// Delete implements SecretStore.
func (k *KeyringStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkID(id); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	n := k.chunkCount(id)
	err := k.kr.Delete(k.service, id)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("accounts: keyring delete: %w", err)
	}
	for i := range n {
		_ = k.kr.Delete(k.service, chunkKey(id, i))
	}
	return nil
}

// probeUser is the keyring entry written and removed by ProbeKeyring.
const probeUser = "__adesc_probe__"

// ProbeKeyring checks that the OS keyring works by writing, reading and
// deleting a throwaway entry. On headless Linux (no D-Bus session or no
// Secret Service provider) it fails quickly; a locked collection may show an
// unlock prompt, so the probe gives up when ctx ends (the blocked call is
// abandoned in the background).
func ProbeKeyring(ctx context.Context) error {
	return probeKeyring(ctx, osKeyring{})
}

func probeKeyring(ctx context.Context, kr keyringBackend) error {
	done := make(chan error, 1)
	go func() {
		const v = "probe"
		if err := kr.Set(KeyringService, probeUser, v); err != nil {
			done <- err
			return
		}
		got, err := kr.Get(KeyringService, probeUser)
		_ = kr.Delete(KeyringService, probeUser)
		if err == nil && got != v {
			err = errors.New("keyring returned a different value")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("accounts: keyring unavailable: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("accounts: keyring unavailable: %w", ctx.Err())
	}
}
