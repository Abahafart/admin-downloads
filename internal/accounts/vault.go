package accounts

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// VaultFileName is the vault file inside config.DataDir.
const VaultFileName = "secrets.vault"

// MinMasterPasswordLen is the minimum length (in characters) of a new
// master password.
const MinMasterPasswordLen = 8

// Vault errors.
var (
	// ErrWrongPassword means the master password does not open the vault.
	ErrWrongPassword = errors.New("accounts: wrong master password")
	// ErrVaultCorrupted means the vault file is malformed or was modified
	// (authentication failed although the password is right).
	ErrVaultCorrupted = errors.New("accounts: vault file is corrupted or was tampered with")
	// ErrWeakPassword means a new master password is too short.
	ErrWeakPassword = fmt.Errorf("accounts: the master password must have at least %d characters", MinMasterPasswordLen)
)

const (
	vaultFormat  = "admindescargas-vault"
	vaultVersion = 1
	kdfArgon2id  = "argon2id"
	saltLen      = 16
	// Bounds checked when reading a file, so a tampered header cannot make
	// Unlock allocate absurd amounts of memory or spin forever.
	maxKDFTime      = 64
	maxKDFMemoryKiB = 4 << 20 // 4 GiB
)

// KDFParams are the Argon2id parameters used for new vaults (existing files
// keep the parameters stored in their header until ChangePassword).
type KDFParams struct {
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

// DefaultKDFParams follows the RFC 9106 "second recommended" profile
// (t=3, m=64 MiB) with up to 4 lanes.
func DefaultKDFParams() KDFParams {
	return KDFParams{Time: 3, MemoryKiB: 64 * 1024, Threads: uint8(min(4, max(1, runtime.NumCPU())))}
}

// vaultFile is the on-disk JSON document. Everything but Ciphertext is
// public; the header fields are bound to the ciphertext as GCM additional
// data and to the verifier.
type vaultFile struct {
	Format     string  `json:"format"`
	Version    int     `json:"version"`
	KDF        vaultKD `json:"kdf"`
	Verifier   []byte  `json:"verifier"` // HMAC-SHA256(macKey, aad)
	Nonce      []byte  `json:"nonce"`
	Ciphertext []byte  `json:"ciphertext"`
}

type vaultKD struct {
	Name string `json:"name"`
	KDFParams
	Salt []byte `json:"salt"`
}

type vaultPayload struct {
	Secrets map[string]Secret `json:"secrets"`
}

func (f *vaultFile) aad() []byte {
	return fmt.Appendf(nil, "%s|v%d|%s|t=%d|m=%d|p=%d|%x",
		f.Format, f.Version, f.KDF.Name, f.KDF.Time, f.KDF.MemoryKiB, f.KDF.Threads, f.KDF.Salt)
}

func (f *vaultFile) validate() error {
	if f.Format != vaultFormat || f.Version != vaultVersion || f.KDF.Name != kdfArgon2id {
		return fmt.Errorf("%w: unsupported format", ErrVaultCorrupted)
	}
	k := f.KDF
	if len(k.Salt) < saltLen || k.Time < 1 || k.Time > maxKDFTime || k.MemoryKiB < 8 || k.MemoryKiB > maxKDFMemoryKiB || k.Threads < 1 {
		return fmt.Errorf("%w: invalid key derivation parameters", ErrVaultCorrupted)
	}
	if len(f.Verifier) != sha256.Size || len(f.Nonce) != 12 || len(f.Ciphertext) < 16 {
		return fmt.Errorf("%w: truncated", ErrVaultCorrupted)
	}
	return nil
}

// deriveKeys returns (encryption key, verifier key).
func deriveKeys(password string, kd vaultKD) ([]byte, []byte) {
	k := argon2.IDKey([]byte(password), kd.Salt, kd.Time, kd.MemoryKiB, kd.Threads, 64)
	return k[:32], k[32:]
}

func verifier(macKey, aad []byte) []byte {
	m := hmac.New(sha256.New, macKey)
	m.Write([]byte("admindescargas vault verifier\x00"))
	m.Write(aad)
	return m.Sum(nil)
}

// Vault is a SecretStore kept in one file encrypted with AES-256-GCM under a
// key derived with Argon2id from a master password. It starts locked: every
// method returns ErrLocked until Unlock succeeds. Unlocking a vault whose
// file does not exist yet creates it with that password.
//
// The whole secret map is decrypted in memory while unlocked and the file is
// rewritten atomically (temp file + fsync + rename, mode 0600) on every
// change with a fresh random nonce.
type Vault struct {
	path   string
	params KDFParams

	mu      sync.RWMutex
	key     []byte  // AES key; nil while locked
	hdr     vaultKD // KDF header of the open file
	macKey  []byte
	secrets map[string]Secret
}

var _ SecretStore = (*Vault)(nil)

// NewVault returns a locked vault stored at path. Nothing is read until
// Unlock. params are used when creating the file or changing the password;
// the zero value means DefaultKDFParams.
func NewVault(path string, params KDFParams) *Vault {
	if params == (KDFParams{}) {
		params = DefaultKDFParams()
	}
	return &Vault{path: path, params: params}
}

// Backend returns "vault".
func (v *Vault) Backend() string { return BackendVault }

// Path returns the vault file path.
func (v *Vault) Path() string { return v.path }

// Initialized reports whether the vault file exists (i.e. a master password
// was already chosen).
func (v *Vault) Initialized() bool {
	_, err := os.Stat(v.path)
	return err == nil
}

// Locked reports whether the master password is still needed.
func (v *Vault) Locked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.key == nil
}

// Unlock opens the vault with the master password, or creates the vault
// file if it does not exist (the password must then have at least
// MinMasterPasswordLen characters). It returns ErrWrongPassword or
// ErrVaultCorrupted on failure. Unlocking an unlocked vault re-checks the
// password.
func (v *Vault) Unlock(ctx context.Context, password string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	raw, err := os.ReadFile(v.path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := checkNewPassword(password); err != nil {
			return err
		}
		return v.rekeyLocked(password, map[string]Secret{})
	}
	if err != nil {
		return fmt.Errorf("accounts: read vault: %w", err)
	}
	key, macKey, kd, secrets, err := openVault(raw, password)
	if err != nil {
		return err
	}
	v.setOpen(key, macKey, kd, secrets)
	return nil
}

func checkNewPassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinMasterPasswordLen {
		return ErrWeakPassword
	}
	return nil
}

func openVault(raw []byte, password string) (key, macKey []byte, kd vaultKD, secrets map[string]Secret, err error) {
	var f vaultFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, kd, nil, fmt.Errorf("%w: invalid JSON", ErrVaultCorrupted)
	}
	if err := f.validate(); err != nil {
		return nil, nil, kd, nil, err
	}
	aad := f.aad()
	key, macKey = deriveKeys(password, f.KDF)
	if !hmac.Equal(verifier(macKey, aad), f.Verifier) {
		return nil, nil, kd, nil, ErrWrongPassword
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, kd, nil, err
	}
	plain, err := gcm.Open(nil, f.Nonce, f.Ciphertext, aad)
	if err != nil {
		return nil, nil, kd, nil, ErrVaultCorrupted
	}
	defer clear(plain)
	var p vaultPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, nil, kd, nil, fmt.Errorf("%w: invalid payload", ErrVaultCorrupted)
	}
	if p.Secrets == nil {
		p.Secrets = map[string]Secret{}
	}
	return key, macKey, f.KDF, p.Secrets, nil
}

func (v *Vault) setOpen(key, macKey []byte, kd vaultKD, secrets map[string]Secret) {
	v.wipe()
	v.key, v.macKey, v.hdr, v.secrets = key, macKey, kd, secrets
}

func (v *Vault) wipe() {
	clear(v.key)
	clear(v.macKey)
	v.key, v.macKey, v.secrets = nil, nil, nil
}

// Lock forgets the key and the decrypted secrets.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.wipe()
}

// ChangePassword re-encrypts the vault under newPassword (with a new salt and
// the current KDF parameters). oldPassword must open the current file; the
// vault is left unlocked on success. If the file does not exist yet this is
// the same as Unlock(newPassword).
func (v *Vault) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkNewPassword(newPassword); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	raw, err := os.ReadFile(v.path)
	if errors.Is(err, fs.ErrNotExist) {
		return v.rekeyLocked(newPassword, map[string]Secret{})
	}
	if err != nil {
		return fmt.Errorf("accounts: read vault: %w", err)
	}
	key, macKey, _, secrets, err := openVault(raw, oldPassword)
	if err != nil {
		return err
	}
	clear(key)
	clear(macKey)
	return v.rekeyLocked(newPassword, secrets)
}

// rekeyLocked writes secrets under a fresh salt derived from password and
// keeps the vault open with it. Caller holds mu.
func (v *Vault) rekeyLocked(password string, secrets map[string]Secret) error {
	kd := vaultKD{Name: kdfArgon2id, KDFParams: v.params, Salt: make([]byte, saltLen)}
	if _, err := rand.Read(kd.Salt); err != nil {
		return fmt.Errorf("accounts: salt: %w", err)
	}
	key, macKey := deriveKeys(password, kd)
	if err := writeVault(v.path, key, macKey, kd, secrets); err != nil {
		clear(key)
		clear(macKey)
		return err
	}
	v.setOpen(key, macKey, kd, secrets)
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("accounts: aes: %w", err)
	}
	return cipher.NewGCM(block)
}

func writeVault(path string, key, macKey []byte, kd vaultKD, secrets map[string]Secret) error {
	plain, err := json.Marshal(vaultPayload{Secrets: secrets})
	if err != nil {
		return fmt.Errorf("accounts: encode vault: %w", err)
	}
	defer clear(plain)
	f := vaultFile{Format: vaultFormat, Version: vaultVersion, KDF: kd, Nonce: make([]byte, 12)}
	if _, err := rand.Read(f.Nonce); err != nil {
		return fmt.Errorf("accounts: nonce: %w", err)
	}
	aad := f.aad()
	f.Verifier = verifier(macKey, aad)
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	f.Ciphertext = gcm.Seal(nil, f.Nonce, plain, aad)
	out, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return fmt.Errorf("accounts: encode vault: %w", err)
	}
	return writeFileAtomic(path, out)
}

// writeFileAtomic writes data to a temp file in the same directory, fsyncs
// it and renames it over path, so a crash leaves either the old or the new
// file. The file is created with mode 0600.
func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // G703: vault path comes from the data dir in config, not from user input
		return fmt.Errorf("accounts: vault dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".vault-*.tmp")
	if err != nil {
		return fmt.Errorf("accounts: vault temp file: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(name) //nolint:gosec // G703: temp file created by os.CreateTemp in the vault dir
		}
	}()
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("accounts: vault chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("accounts: write vault: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("accounts: sync vault: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("accounts: close vault: %w", err)
	}
	if err := os.Rename(name, path); err != nil { //nolint:gosec // G703: vault path comes from the data dir in config
		return fmt.Errorf("accounts: replace vault: %w", err)
	}
	if runtime.GOOS != "windows" {
		if d, err := os.Open(dir); err == nil { //nolint:gosec // G703: vault path comes from the data dir in config
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}

// Get implements SecretStore.
func (v *Vault) Get(ctx context.Context, id string) (Secret, error) {
	if err := ctx.Err(); err != nil {
		return Secret{}, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.key == nil {
		return Secret{}, ErrLocked
	}
	s, ok := v.secrets[id]
	if !ok {
		return Secret{}, ErrSecretNotFound
	}
	return cloneSecret(s), nil
}

// Set implements SecretStore.
func (v *Vault) Set(ctx context.Context, id string, s Secret) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return errors.New("accounts: empty account id")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return ErrLocked
	}
	next := maps.Clone(v.secrets)
	next[id] = cloneSecret(s)
	if err := writeVault(v.path, v.key, v.macKey, v.hdr, next); err != nil {
		return err
	}
	v.secrets = next
	return nil
}

// Delete implements SecretStore.
func (v *Vault) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return ErrLocked
	}
	if _, ok := v.secrets[id]; !ok {
		return nil
	}
	next := maps.Clone(v.secrets)
	delete(next, id)
	if err := writeVault(v.path, v.key, v.macKey, v.hdr, next); err != nil {
		return err
	}
	v.secrets = next
	return nil
}
