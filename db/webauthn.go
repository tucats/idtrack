package db

import (
	"database/sql"
	"strings"
	"time"
)

// WebAuthnCredential represents a row in the webauthn_credentials table: one
// registered passkey (Touch ID, Face ID, Windows Hello, or a roaming
// authenticator such as a security key or phone) belonging to a user. A user
// may have zero or more of these; they are an alternative to, not a
// replacement for, the password stored on the users table.
//
// The fields here are deliberately a subset of what the go-webauthn library's
// own Credential struct carries (see server/webauthn.go, which converts
// between the two) — idtrack is a single relying party with one configured
// RP ID at a time, so per-credential attestation metadata that only matters
// for multi-tenant trust decisions (AAGUID, attestation type/format) is not
// persisted; only what is needed to verify a future assertion (the public
// key, the clone-detection sign counter, and the packed backup-eligible/
// backup-state/user-verified flag byte — see Flags below) and to let a user
// manage their own list (a label and two timestamps).
type WebAuthnCredential struct {
	ID         string `json:"id"` // base64url credential ID
	Username   string `json:"-"`  // owning user; not surfaced in the credential-list API response
	PublicKey  []byte `json:"-"`  // COSE public key; never exposed over the API
	SignCount  uint32 `json:"-"`  // clone-detection counter; updated on every successful login
	Transports string `json:"-"`  // comma-separated protocol.AuthenticatorTransport values
	// Flags is the single-byte packed form of webauthn.CredentialFlags
	// (webauthn.CredentialFlags.MsgpByte()/CredentialFlagsFromMsgpByte()) —
	// UserPresent, UserVerified, BackupEligible, BackupState. This MUST be
	// persisted: FinishPasskeyLogin compares the stored BackupEligible bit
	// against the live assertion's and rejects the login outright
	// ("Backup Eligible flag inconsistency detected during login
	// validation") if they don't match, which they never will if this field
	// is left at its zero value while a real authenticator (e.g. an iCloud
	// Keychain-synced passkey) reports BackupEligible=true. Written once at
	// registration (handleWebAuthnRegisterFinish) and refreshed on every
	// successful login (handleWebAuthnLoginFinish) via UpdateCredentialUsage,
	// per the library's own storage guidance for this field.
	Flags      byte   `json:"-"`
	Name       string `json:"name"` // user-supplied label, e.g. "MacBook Touch ID"
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// AddCredential inserts a newly-registered passkey. id is the base64url
// credential ID; publicKey is the COSE-encoded public key bytes; transports
// is a comma-separated list (may be empty — not every authenticator reports
// its transports); flags is the packed CredentialFlags byte (see
// WebAuthnCredential.Flags).
func AddCredential(database *sql.DB, id, username string, publicKey []byte, signCount uint32, transports string, flags byte, name string) error {
	_, err := database.Exec(
		`INSERT INTO webauthn_credentials (id, username, public_key, sign_count, transports, flags, name, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, username, publicKey, signCount, transports, flags, name, time.Now().UTC().Format(time.RFC3339),
	)

	return err
}

// ListCredentials returns every passkey registered to username, ordered by
// creation time. Used both by the discoverable-login handler (to assemble
// the webauthn.User adapter's credential list) and by the Settings
// "Passkeys" list.
func ListCredentials(database *sql.DB, username string) ([]WebAuthnCredential, error) {
	rows, err := database.Query(
		`SELECT id, username, public_key, sign_count, transports, flags, name, created_at, last_used_at
		 FROM webauthn_credentials WHERE username = ? ORDER BY created_at`, username,
	)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var creds []WebAuthnCredential

	for rows.Next() {
		var c WebAuthnCredential

		if err := rows.Scan(&c.ID, &c.Username, &c.PublicKey, &c.SignCount, &c.Transports, &c.Flags, &c.Name, &c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}

		creds = append(creds, c)
	}

	if creds == nil {
		creds = []WebAuthnCredential{}
	}

	return creds, rows.Err()
}

// UpdateCredentialUsage writes back the sign counter, packed flags byte, and
// last-used timestamp for one credential. The go-webauthn library requires
// both the sign counter and the flags (see WebAuthnCredential.Flags) to be
// persisted after every successful assertion — the former so the next login
// can detect a cloned authenticator, the latter so the next login's
// BackupEligible consistency check has the current value to compare against.
func UpdateCredentialUsage(database *sql.DB, id string, signCount uint32, flags byte) error {
	_, err := database.Exec(
		`UPDATE webauthn_credentials SET sign_count = ?, flags = ?, last_used_at = ? WHERE id = ?`,
		signCount, flags, time.Now().UTC().Format(time.RFC3339), id,
	)

	return err
}

// DeleteCredential removes one credential, scoped to owner (the caller must
// pass the requesting user's own username) so a user can only ever delete
// their own passkeys via the self-service Settings UI. The admin CLI path
// (idtrack user passkeys <username> revoke <id>) calls this with the target
// user's username already known, achieving the same scoping.
func DeleteCredential(database *sql.DB, owner, id string) error {
	_, err := database.Exec(`DELETE FROM webauthn_credentials WHERE id = ? AND username = ?`, id, owner)

	return err
}

// ParseTransports splits the stored comma-separated transports string back
// into a slice. Mirrors ParseTeams/parseDependentIssues elsewhere in this
// package, which use the same comma-separated-string-column convention for
// small sets where SQLite has no native array type.
func ParseTransports(s string) []string {
	if s == "" {
		return nil
	}

	return strings.Split(s, ",")
}
