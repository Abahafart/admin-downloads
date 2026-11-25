package rotation

import "context"

// Admin is implemented by *Rotator and holds the management operations the
// API exposes besides rotation.Manager (type-assert the Manager to Admin;
// rotation.Static does not implement it). Commands of global egresses
// (router script, system VPN argv) are never editable through it.
type Admin interface {
	// ImportProxies adds the entries of a proxy list (persisted, passwords
	// in the SecretStore).
	ImportProxies(ctx context.Context, text string) (ImportResult, error)
	// AddWireGuard imports a wg-quick .conf as a new egress.
	AddWireGuard(ctx context.Context, name, conf string) (EgressStatus, error)
	// RemoveEgress deletes an imported egress (ErrNotImported for config ones).
	RemoveEgress(ctx context.Context, id string) error
	// TestEgress health-checks one egress now (connect + public IP).
	TestEgress(ctx context.Context, id string) (EgressStatus, error)
	// ClearQuota forgets the IP quota mark of (hoster, egressID); hoster ""
	// clears every hoster.
	ClearQuota(ctx context.Context, hoster, egressID string) error
	// Status returns the status of one egress.
	Status(egressID string) (EgressStatus, error)
}

var _ Admin = (*Rotator)(nil)
