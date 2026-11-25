package accounts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Secret store backends.
const (
	BackendAuto    = "auto"    // keyring if it works, else vault (default)
	BackendKeyring = "keyring" // OS keyring only
	BackendVault   = "vault"   // encrypted file in the data dir
	BackendMemory  = "memory"  // in-memory, lost on exit (tests, demos)
)

// BackendFileName records, in the data dir, which backend auto mode chose
// the first time, so a later start without a keyring (e.g. the same data
// dir used by a headless service) does not silently switch stores.
const BackendFileName = "secrets.backend"

// StoreOptions configures OpenSecretStore. The zero value means auto
// detection with default Argon2id parameters.
type StoreOptions struct {
	// Backend is BackendAuto (or ""), BackendKeyring, BackendVault or
	// BackendMemory. Integration may map it from a config/CLI flag.
	Backend string
	// VaultPath overrides <dataDir>/VaultFileName.
	VaultPath string
	// VaultKDF overrides DefaultKDFParams for new vaults.
	VaultKDF KDFParams
	// VaultPasswordFile, if set, is read at start-up to unlock the vault
	// without user interaction (OS services). Trailing newlines are
	// trimmed. Leave empty to unlock through the API/UI.
	VaultPasswordFile string
	// ProbeTimeout bounds the keyring probe (default 5s).
	ProbeTimeout time.Duration
	// Logger receives backend selection notes (never secrets).
	Logger *slog.Logger

	// probe replaces ProbeKeyring in tests.
	probe func(context.Context) error
	// keyring replaces the OS keyring backend in tests.
	keyring keyringBackend
}

// Lockable is implemented by secret stores protected by a master password
// (Vault). Use AsLockable to reach it through wrappers.
type Lockable interface {
	Locked() bool
	Initialized() bool
	Unlock(ctx context.Context, password string) error
	Lock()
	ChangePassword(ctx context.Context, oldPassword, newPassword string) error
}

// StoreStatus describes the secret store for the API / UI.
type StoreStatus struct {
	// Backend is "keyring", "vault" or "memory".
	Backend string `json:"backend"`
	// Lockable is true for the vault.
	Lockable bool `json:"lockable"`
	// Initialized is false for a vault whose master password has not been
	// chosen yet (the first Unlock creates it).
	Initialized bool `json:"initialized"`
	// Locked is true while the vault waits for its master password.
	Locked bool `json:"locked"`
	// Warning explains a degraded choice (e.g. keyring no longer
	// available), in Spanish for the UI.
	Warning string `json:"warning,omitempty"`
}

// OpenedStore is the result of OpenSecretStore.
type OpenedStore struct {
	SecretStore
	Status StoreStatus
}

// OpenSecretStore picks and opens the secret store.
//
// Auto mode (default):
//  1. if <dataDir>/secrets.vault exists, the vault is used;
//  2. else if a previous run chose the keyring (secrets.backend) or the
//     keyring probe succeeds now, the keyring is used and the choice is
//     recorded;
//  3. otherwise (headless servers, no Secret Service/D-Bus session) the
//     vault is used.
//
// If a previously chosen keyring is no longer reachable, the vault is used
// and Status.Warning says that stored secrets are unavailable.
//
// Headless behavior: the vault starts LOCKED. Accounts metadata keeps
// working, but anything that needs a secret (downloads with an account,
// checks, edits of credentials) fails with ErrLocked until the user unlocks
// it through the API/UI (Manager.Unlock; the first unlock chooses the
// master password) or VaultPasswordFile is configured.
func OpenSecretStore(ctx context.Context, opts StoreOptions, dataDir string) (*OpenedStore, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	vaultPath := opts.VaultPath
	if vaultPath == "" {
		vaultPath = filepath.Join(dataDir, VaultFileName)
	}
	probe := opts.probe
	if probe == nil {
		probe = ProbeKeyring
	}
	timeout := opts.ProbeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	newKeyring := func() *KeyringStore {
		k := NewKeyringStore()
		if opts.keyring != nil {
			k.kr = opts.keyring
		}
		return k
	}
	runProbe := func() error {
		pctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return probe(pctx)
	}

	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	switch backend {
	case BackendMemory:
		return &OpenedStore{SecretStore: NewMemoryStore(), Status: StoreStatus{Backend: BackendMemory, Initialized: true}}, nil
	case BackendKeyring:
		if err := runProbe(); err != nil {
			return nil, err
		}
		return &OpenedStore{SecretStore: newKeyring(), Status: StoreStatus{Backend: BackendKeyring, Initialized: true}}, nil
	case BackendVault:
		return openVaultStore(ctx, opts, vaultPath, "")
	case "", BackendAuto:
	default:
		return nil, fmt.Errorf("accounts: unknown secret store backend %q (auto, keyring, vault, memory)", opts.Backend)
	}

	if _, err := os.Stat(vaultPath); err == nil {
		log.Info("secret store: using existing vault", "path", vaultPath)
		return openVaultStore(ctx, opts, vaultPath, "")
	}
	marker := filepath.Join(dataDir, BackendFileName)
	prev := readMarker(marker)
	perr := runProbe()
	if perr == nil {
		if prev != BackendKeyring && dataDir != "" {
			if werr := writeFileAtomic(marker, []byte(BackendKeyring+"\n")); werr != nil {
				log.Warn("secret store: cannot record backend choice", "err", werr)
			}
		}
		log.Info("secret store: using OS keyring")
		return &OpenedStore{SecretStore: newKeyring(), Status: StoreStatus{Backend: BackendKeyring, Initialized: true}}, nil
	}
	log.Info("secret store: OS keyring unavailable, using encrypted vault", "reason", perr.Error())
	warning := ""
	if prev == BackendKeyring {
		warning = "El llavero del sistema ya no está disponible: las credenciales guardadas en él no se pueden leer. " +
			"Se usa el almacén cifrado; vuelve a introducir las credenciales o arranca con acceso al llavero."
		log.Warn("secret store: keyring chosen previously is unreachable; falling back to vault")
	}
	return openVaultStore(ctx, opts, vaultPath, warning)
}

func readMarker(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func openVaultStore(ctx context.Context, opts StoreOptions, path, warning string) (*OpenedStore, error) {
	v := NewVault(path, opts.VaultKDF)
	if opts.VaultPasswordFile != "" {
		b, err := os.ReadFile(opts.VaultPasswordFile)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("accounts: vault password file %s does not exist", opts.VaultPasswordFile)
			}
			return nil, fmt.Errorf("accounts: read vault password file: %w", err)
		}
		pw := strings.TrimRight(string(b), "\r\n")
		clear(b)
		if err := v.Unlock(ctx, pw); err != nil {
			return nil, fmt.Errorf("accounts: unlock vault with password file: %w", err)
		}
	}
	return &OpenedStore{SecretStore: v, Status: StoreStatus{Backend: BackendVault, Lockable: true, Warning: warning}}, nil
}

// AsLockable returns the Lockable behind s (also through *OpenedStore).
func AsLockable(s SecretStore) (Lockable, bool) {
	if o, ok := s.(*OpenedStore); ok {
		s = o.SecretStore
	}
	l, ok := s.(Lockable)
	return l, ok
}

// StatusOf reports the current status of s.
func StatusOf(s SecretStore) StoreStatus {
	var st StoreStatus
	if o, ok := s.(*OpenedStore); ok {
		st = o.Status
		s = o.SecretStore
	}
	switch s.(type) {
	case *Vault:
		st.Backend = BackendVault
	case *KeyringStore:
		st.Backend = BackendKeyring
	case *MemoryStore:
		st.Backend = BackendMemory
	}
	if l, ok := s.(Lockable); ok {
		st.Lockable = true
		st.Initialized = l.Initialized()
		st.Locked = l.Locked()
	} else {
		st.Initialized = true
	}
	return st
}
