package credentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// envelope.go — MU-015 part 2: envelope encryption with a versioned
// per-workspace data key under a wrapping key that can come from a production
// KMS.
//
// WHAT THE DERIVED-KEY DESIGN COULD NOT DO. Until now every credential for one
// (workspace, agent) was encrypted under a key derived by HKDF from a machine
// secret. That is a real tenant boundary — two workspaces' ciphertext is
// unreadable to each other, and a query that escapes its workspace predicate
// still yields bytes the reader cannot open. What it cannot do is ROTATE. The
// key is a pure function of the master secret, so changing the key means
// changing the master secret, and changing the master secret makes every
// existing credential in every workspace permanently unreadable. A design
// whose only rotation is total data loss has no rotation.
//
// Envelope encryption separates the two jobs. A random per-workspace DATA key
// (DEK) encrypts the credentials; a WRAPPING key (KEK) encrypts the DEK.
// Rotating means minting a new DEK version and leaving the old one wrapped and
// readable, so ciphertext written under version 1 stays decryptable while new
// writes use version 2. Rotating the KEK means re-wrapping a handful of DEKs
// rather than re-encrypting every credential.
//
// THE VERSION LIVES IN A COLUMN, NOT IN A MAGIC PREFIX. The obvious
// alternative is a header on the ciphertext, and it has a real flaw: legacy
// blobs begin with a random 12-byte nonce, so one in four billion starts with
// whatever magic you chose, and that one decrypts as the wrong format. A
// column is unambiguous — `key_version = 0` means "encrypted the old way" and
// nothing can accidentally look like it.
//
// PRODUCT INVARIANT 7. Version 0 rows keep working, decrypted by the derived
// key exactly as before, forever. An existing installation upgrades and
// notices nothing; its credentials migrate to an envelope only when they are
// next written, or when somebody asks for a rotation.

// KeyWrapper wraps and unwraps a workspace's data key.
//
// SEPARATE from KMSProvider because the two are different operations with
// different homes in a production deployment. `DeriveKey` computes a key
// locally from a secret this process holds; `WrapKey` asks a key service to
// encrypt something under a key this process never sees. A cloud KMS
// implements the second and cannot implement the first — which is exactly why
// the derived-key design could not be backed by one.
type KeyWrapper interface {
	// WrapKey encrypts a data key for one workspace.
	WrapKey(ctx context.Context, workspaceID string, dataKey []byte) ([]byte, error)
	// UnwrapKey reverses WrapKey.
	UnwrapKey(ctx context.Context, workspaceID string, wrapped []byte) ([]byte, error)
}

// wrapInfo is the derivation label for LocalKMS's wrapping key.
//
// It contains a byte no agent id can hold, so a wrapping key can never collide
// with the derived key of an agent literally named "wrap" — a collision would
// mean the key that protects every DEK in a workspace is the same key that
// protects one agent's credentials, and compromising that agent would unwrap
// the workspace.
const wrapAgentSentinel = "\x00wrap"

// WrapKey implements KeyWrapper for the local, machine-secret KMS.
//
// AES-256-GCM with the same shape as the credential encryption, because the
// operation is the same: seal 32 bytes under a key this process holds. The
// workspace is bound as ADDITIONAL AUTHENTICATED DATA, so a wrapped DEK moved
// from one workspace's row to another's fails to open rather than silently
// decrypting that tenant's credentials under a key they do not own.
func (k *LocalKMS) WrapKey(ctx context.Context, workspaceID string, dataKey []byte) ([]byte, error) {
	kek, err := k.DeriveKey(ctx, workspaceID, wrapAgentSentinel)
	if err != nil {
		return nil, err
	}
	return sealWithAAD(kek, dataKey, []byte(wsroot.Normalize(workspaceID)))
}

func (k *LocalKMS) UnwrapKey(ctx context.Context, workspaceID string, wrapped []byte) ([]byte, error) {
	kek, err := k.DeriveKey(ctx, workspaceID, wrapAgentSentinel)
	if err != nil {
		return nil, err
	}
	return openWithAAD(kek, wrapped, []byte(wsroot.Normalize(workspaceID)))
}

// WrapKey implements KeyWrapper for the passthrough KMS used in tests and by
// deployments supplying their own key material.
func (p *PassthroughKMS) WrapKey(ctx context.Context, workspaceID string, dataKey []byte) ([]byte, error) {
	kek, err := p.DeriveKey(ctx, workspaceID, wrapAgentSentinel)
	if err != nil {
		return nil, err
	}
	return sealWithAAD(kek, dataKey, []byte(wsroot.Normalize(workspaceID)))
}

func (p *PassthroughKMS) UnwrapKey(ctx context.Context, workspaceID string, wrapped []byte) ([]byte, error) {
	kek, err := p.DeriveKey(ctx, workspaceID, wrapAgentSentinel)
	if err != nil {
		return nil, err
	}
	return openWithAAD(kek, wrapped, []byte(wsroot.Normalize(workspaceID)))
}

// legacyKeyVersion marks ciphertext written before envelope encryption.
//
// Zero rather than a sentinel like -1 so an existing row, whose column is
// added with DEFAULT 0, is already correctly labelled — the migration adds a
// column and does not have to rewrite a single row.
const legacyKeyVersion = 0

const dataKeySchema = `
CREATE TABLE IF NOT EXISTS workspace_data_keys (
    workspace_id TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    wrapped      BLOB    NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retired_at   TIMESTAMP,
    PRIMARY KEY (workspace_id, version)
)`

// ErrNoDataKey reports a version that is not in the table.
//
// Distinct from a decryption failure: "the key this row names does not exist"
// is a catalog problem an operator can act on, and "the key exists and does
// not open this" is corruption or tampering. Collapsing them would send
// somebody looking for the wrong thing.
var ErrNoDataKey = errors.New("credentials: no data key for that version")

// dataKeys manages one deployment's wrapped per-workspace data keys.
type dataKeys struct {
	db      *sql.DB
	wrapper KeyWrapper
}

func ensureDataKeySchema(db *sql.DB) error {
	_, err := db.Exec(dataKeySchema)
	return err
}

// active returns the workspace's current data key, minting one on first use.
//
// The mint is an INSERT that tolerates a conflict, so two processes racing to
// create a workspace's first key end with one key rather than two — and the
// loser reads the winner's rather than failing. Two keys would not be a
// security problem, but the row written under the losing key would be
// unreadable by every other process.
func (d *dataKeys) active(ctx context.Context, workspaceID string) (int, []byte, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	version, wrapped, err := d.newest(ctx, workspaceID)
	if err == nil {
		key, unwrapErr := d.wrapper.UnwrapKey(ctx, workspaceID, wrapped)
		if unwrapErr != nil {
			return 0, nil, fmt.Errorf("credentials: unwrap data key v%d: %w", version, unwrapErr)
		}
		return version, key, nil
	}
	if !errors.Is(err, ErrNoDataKey) {
		return 0, nil, err
	}
	return d.mint(ctx, workspaceID, 1)
}

// mint creates a new data key version.
func (d *dataKeys) mint(ctx context.Context, workspaceID string, version int) (int, []byte, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return 0, nil, fmt.Errorf("credentials: generate data key: %w", err)
	}
	wrapped, err := d.wrapper.WrapKey(ctx, workspaceID, key)
	if err != nil {
		return 0, nil, fmt.Errorf("credentials: wrap data key: %w", err)
	}
	result, err := d.db.ExecContext(ctx,
		`INSERT INTO workspace_data_keys (workspace_id, version, wrapped, created_at)
		 VALUES (?, ?, ?, ?) ON CONFLICT(workspace_id, version) DO NOTHING`,
		workspaceID, version, wrapped, time.Now().UTC())
	if err != nil {
		return 0, nil, fmt.Errorf("credentials: store data key: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		// Another process minted this version first. Read theirs — a row
		// written under the key we just discarded would be unreadable
		// everywhere else.
		return d.at(ctx, workspaceID, version)
	}
	return version, key, nil
}

// at returns one specific version's unwrapped key.
func (d *dataKeys) at(ctx context.Context, workspaceID string, version int) (int, []byte, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	var wrapped []byte
	err := d.db.QueryRowContext(ctx,
		`SELECT wrapped FROM workspace_data_keys WHERE workspace_id = ? AND version = ?`,
		workspaceID, version).Scan(&wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, fmt.Errorf("%w: workspace %q version %d", ErrNoDataKey, workspaceID, version)
	}
	if err != nil {
		return 0, nil, err
	}
	key, err := d.wrapper.UnwrapKey(ctx, workspaceID, wrapped)
	if err != nil {
		return 0, nil, fmt.Errorf("credentials: unwrap data key v%d: %w", version, err)
	}
	return version, key, nil
}

// newest returns the highest non-retired version, still wrapped.
//
// RETIRED KEYS ARE STILL READABLE, which is the point of retiring rather than
// deleting: a retired key stops being used for new writes and keeps opening
// everything already written under it. Deleting it would make rotation a data
// loss event, which is the failure the derived-key design had.
//
// The `retired_at IS NULL` clause is REDUNDANT TODAY and stated as such rather
// than defended: `rotate` retires version N and immediately mints N+1, so the
// highest version is always the un-retired one and the ORDER BY alone would
// pick it. Mutation testing confirms removing the clause breaks nothing. It
// stays for the operation that does not exist yet — retiring a workspace's key
// WITHOUT a replacement, which a compromise response wants — where the
// ordering alone would keep handing out the key an operator just disabled.
func (d *dataKeys) newest(ctx context.Context, workspaceID string) (int, []byte, error) {
	var version int
	var wrapped []byte
	err := d.db.QueryRowContext(ctx,
		`SELECT version, wrapped FROM workspace_data_keys
		 WHERE workspace_id = ? AND retired_at IS NULL
		 ORDER BY version DESC LIMIT 1`, wsroot.Normalize(workspaceID)).Scan(&version, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, fmt.Errorf("%w: workspace %q", ErrNoDataKey, workspaceID)
	}
	if err != nil {
		return 0, nil, err
	}
	return version, wrapped, nil
}

// rotate retires the current version and mints the next.
//
// Retire-then-mint, in a transaction, so there is no instant at which two
// versions are un-retired. Two un-retired versions would make `newest`
// arbitrary between them, and a credential written under one would be opened
// with the other on a different process.
func (d *dataKeys) rotate(ctx context.Context, workspaceID string) (int, []byte, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	current, _, err := d.newest(ctx, workspaceID)
	switch {
	case errors.Is(err, ErrNoDataKey):
		// Rotating a workspace with no key mints its first. That is the same
		// end state as an ordinary first write, and refusing would make
		// "rotate everything" fail on any workspace that has never stored a
		// credential.
		return d.mint(ctx, workspaceID, 1)
	case err != nil:
		return 0, nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspace_data_keys SET retired_at = ? WHERE workspace_id = ? AND version = ? AND retired_at IS NULL`,
		time.Now().UTC(), workspaceID, current); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return d.mint(ctx, workspaceID, current+1)
}

// --- AEAD with additional authenticated data ---

// sealWithAAD encrypts plaintext, binding aad to the ciphertext.
//
// THE AAD IS THE POINT of this pair existing beside the plain encrypt/decrypt.
// Without it a ciphertext blob is portable: a database write that swaps two
// rows' ciphertext produces two credentials that decrypt fine and hold each
// other's values. Binding (workspace, agent, key) means a blob only opens in
// the row it was written for, so a swap is a loud failure instead of a silent
// substitution — which for a credential means an agent quietly authenticating
// as something else.
func sealWithAAD(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	out := make([]byte, len(nonce)+len(sealed))
	copy(out, nonce)
	copy(out[len(nonce):], sealed)
	return out, nil
}

func openWithAAD(key, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("credentials: ciphertext too short")
	}
	nonce, sealed := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, sealed, aad)
}

// credentialAAD binds a ciphertext to the row it belongs in.
//
// The separator is a NUL, which none of the three components can contain: a
// naive concatenation would let ("ws", "a", "bc") and ("ws", "ab", "c") produce
// the same AAD, so a credential could be moved between two agents whose names
// happen to line up.
func credentialAAD(workspaceID, agentID, key string) []byte {
	return []byte(wsroot.Normalize(workspaceID) + "\x00" + agentID + "\x00" + key)
}
