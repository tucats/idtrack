package server

import (
	"encoding/base64"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/tucats/idtrack/db"
)

// This file implements passkey (Touch ID / Face ID / Windows Hello / security
// key) login via the WebAuthn protocol, as an alternative to the password
// login in auth_handlers.go — not a second factor and not a replacement.
// Everything here is only reachable when webauthnEnabled is true (see
// server.go, where the six routes below are conditionally registered at
// all); the server package has no notion of a "disabled" response for these
// endpoints because on a build/config where the feature is off, the routes
// simply do not exist on the mux.
//
// Two ceremonies, four endpoints:
//
//  1. Registration (adding a passkey to an already-logged-in account):
//     POST /api/webauthn/register/begin, then POST .../register/finish.
//     Both run behind s.auth() — the caller is already a known *db.User.
//  2. Login (using a passkey to establish a brand new session):
//     POST /api/webauthn/login/begin, then POST .../login/finish. Both are
//     public, like /api/login — there is no session yet.
//
// Registration asks for a discoverable (resident) credential, so login uses
// go-webauthn's "passkey"/discoverable flow: the browser's "Sign in with a
// passkey" prompt lets the user pick a credential without the server having
// asked for a username first, and the server discovers who is logging in
// from the credential's own embedded user handle (see
// webauthnDiscoverableUserHandler below), not from anything the client
// claims. Two management endpoints round it out: GET .../credentials to list
// a user's own passkeys, and DELETE .../credentials/{id} to remove one.
//
// SessionData handoff: go-webauthn's Finish/Validate calls read the raw
// WebAuthn response JSON directly from an *http.Request body — see
// FinishRegistration and FinishPasskeyLogin below — which means the request
// body on every "finish" call must be exactly that JSON, unmodified, with no
// room for extra fields idtrack itself needs (a chosen passkey label on
// register/finish; whether to request a 30-day session on login/finish; the
// ceremony ID needed to look up the matching *webauthn.SessionData on
// login/finish, since the caller isn't authenticated yet and can't be looked
// up by username the way register/finish is). All of that travels as query
// parameters instead, alongside the untouched body.

// webauthnCeremonyTTL bounds how long a begun-but-unfinished ceremony's
// challenge stays valid. This is deliberately generous compared to a
// browser's own ~60s WebAuthn UI timeout — it exists as a server-side
// backstop against a stale entry lingering forever, not as the ceremony's
// real time budget.
const webauthnCeremonyTTL = 5 * time.Minute

// webauthnCeremonyEntry is one in-flight ceremony's saved state.
type webauthnCeremonyEntry struct {
	session   *webauthn.SessionData
	expiresAt time.Time
}

// webauthnCeremonyStore is a short-lived, in-memory table of in-progress
// WebAuthn ceremonies, mirroring the shape of sessionStore in sessions.go
// but keyed by whatever the caller chooses to use to find its way back:
// registration ceremonies are keyed by username (only one in-flight
// registration per user at a time — a second "Add a passkey" click simply
// overwrites the first), while login ceremonies are keyed by a random
// ceremony ID handed to the client in the register/begin — sorry, login/begin
// — response, since the identity of whoever is completing a discoverable
// login is not known until the ceremony finishes. Entries are single-use:
// take() both returns and removes the entry, exactly like sessionStore's
// lookup-then-expire behavior for session tokens.
type webauthnCeremonyStore struct {
	mu    sync.Mutex
	items map[string]*webauthnCeremonyEntry
}

func newWebAuthnCeremonyStore() *webauthnCeremonyStore {
	return &webauthnCeremonyStore{items: make(map[string]*webauthnCeremonyEntry)}
}

// put saves session under key, replacing any prior entry for that key.
func (cs *webauthnCeremonyStore) put(key string, session *webauthn.SessionData) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.items[key] = &webauthnCeremonyEntry{session: session, expiresAt: time.Now().Add(webauthnCeremonyTTL)}
}

// take returns and removes the entry for key. ok is false if the key was
// never used, was already consumed by a prior take, or has expired.
func (cs *webauthnCeremonyStore) take(key string) (*webauthn.SessionData, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	e, ok := cs.items[key]
	delete(cs.items, key)

	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}

	return e.session, true
}

// webauthnUser adapts a *db.User plus its registered passkeys to the
// webauthn.User interface the go-webauthn library requires for both
// registration and login ceremonies.
//
// WebAuthnID (the "user handle") is the value the authenticator echoes back
// on a discoverable login so the server can identify who is signing in
// before it has any other clue. The library's own docs recommend a fully
// random 64-byte value scoped per (relying-party, application-user) pair;
// idtrack instead uses the username's raw bytes directly. That is a
// deliberate simplification that fits this specific tool: idtrack is always
// a single relying party (one RP ID configured at a time — see
// commands.Default's --webauthn-rp-id), usernames are already the stable,
// unique, un-renameable primary key for a user everywhere else in this
// codebase (db.User.Username, the users table's PRIMARY KEY), and the
// WebAuthn spec only requires the handle to be an opaque byte sequence of at
// most 64 bytes, not that it be random — usernames comfortably fit that.
// This also means webauthnDiscoverableUserHandler below can resolve straight
// to db.FindUser(userHandle) with no separate handle-to-user mapping table.
type webauthnUser struct {
	user  *db.User
	creds []db.WebAuthnCredential
}

func (u *webauthnUser) WebAuthnID() []byte { return []byte(u.user.Username) }

func (u *webauthnUser) WebAuthnName() string { return u.user.Username }

func (u *webauthnUser) WebAuthnDisplayName() string {
	if u.user.DisplayName != "" {
		return u.user.DisplayName
	}

	return u.user.Username
}

// WebAuthnCredentials converts each stored db.WebAuthnCredential into the
// library's own webauthn.Credential shape. See db.WebAuthnCredential's doc
// comment for why only a subset of the library's Credential fields are
// persisted — everything reconstructed here (ID, PublicKey, Transport, the
// sign counter, and Flags) is exactly what FinishRegistration/
// FinishPasskeyLogin need to verify a signature, detect a cloned
// authenticator, and pass the BackupEligible consistency check (Flags must
// round-trip correctly here — see the long comment on
// db.WebAuthnCredential.Flags for what breaks if it doesn't); the fields
// left at their zero value (AttestationType, AAGUID, ...) only matter for
// multi-tenant trust/attestation decisions idtrack does not make.
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.creds))

	for _, c := range u.creds {
		idBytes, err := base64.RawURLEncoding.DecodeString(c.ID)
		if err != nil {
			continue // a corrupt row should not break every other credential's login
		}

		var transports []protocol.AuthenticatorTransport

		for _, t := range db.ParseTransports(c.Transports) {
			transports = append(transports, protocol.AuthenticatorTransport(t))
		}

		out = append(out, webauthn.Credential{
			ID:            idBytes,
			PublicKey:     c.PublicKey,
			Transport:     transports,
			Flags:         webauthn.CredentialFlagsFromMsgpByte(c.Flags),
			Authenticator: webauthn.Authenticator{SignCount: c.SignCount},
		})
	}

	return out
}

// webauthnDiscoverableUserHandler resolves a webauthn.User from the
// userHandle an authenticator returns during a discoverable login — see
// webauthnUser's doc comment above for why that handle is simply the
// username's raw bytes here. rawID (the specific credential ID used) is
// unused: FinishPasskeyLogin itself verifies that the credential it receives
// belongs to the user this handler returns.
func (s *srv) webauthnDiscoverableUserHandler(rawID, userHandle []byte) (webauthn.User, error) {
	username := string(userHandle)

	user, err := db.FindUser(s.database, username)
	if err != nil {
		return nil, err
	}

	if user == nil {
		return nil, errUnknownWebAuthnUser
	}

	creds, err := db.ListCredentials(s.database, username)
	if err != nil {
		return nil, err
	}

	return &webauthnUser{user: user, creds: creds}, nil
}

var errUnknownWebAuthnUser = &webauthnError{"unknown user"}

// webauthnError is a minimal error type so webauthnDiscoverableUserHandler
// does not need to import the "errors" or "fmt" packages for one static
// message.
type webauthnError struct{ msg string }

func (e *webauthnError) Error() string { return e.msg }

// handleWebAuthnRegisterBegin serves POST /api/webauthn/register/begin.
// Authenticated (s.auth()) — this adds a passkey to the caller's own
// already-logged-in account, so the user is never in question here the way
// it is during login.
//
// The caller's existing passkeys are passed as exclusions so the
// authenticator prompt does not offer to "re-register" a credential that
// already exists for this account.
//
// Response (200 OK): a protocol.CredentialCreation — the frontend passes its
// "publicKey" field, after converting the base64url-encoded byte fields to
// ArrayBuffers, directly to navigator.credentials.create().
func (s *srv) handleWebAuthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)

	creds, err := db.ListCredentials(s.database, user.Username)
	if err != nil {
		internalError(w, err)

		return
	}

	wu := &webauthnUser{user: user, creds: creds}

	existing := wu.WebAuthnCredentials()
	exclusions := make([]protocol.CredentialDescriptor, 0, len(existing))

	for _, c := range existing {
		exclusions = append(exclusions, c.Descriptor())
	}

	creation, session, err := s.webauthn.BeginRegistration(wu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(exclusions),
	)
	if err != nil {
		internalError(w, err)

		return
	}

	s.registerCeremonies.put(user.Username, session)

	jsonResponse(w, http.StatusOK, creation)
}

// handleWebAuthnRegisterFinish serves POST /api/webauthn/register/finish.
// Authenticated. The request body must be exactly the JSON produced by
// serializing the browser's navigator.credentials.create() result (see the
// SessionData-handoff note at the top of this file); the passkey's
// user-chosen label travels separately as the "name" query parameter,
// e.g. POST .../register/finish?name=MacBook%20Touch%20ID.
//
// Response (201 Created): {"id", "name"} for the newly stored credential.
// Errors: 400 if there is no matching in-flight ceremony for this user (it
// expired, or register/begin was never called) or the authenticator's
// response fails verification.
func (s *srv) handleWebAuthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)

	session, ok := s.registerCeremonies.take(user.Username)
	if !ok {
		jsonError(w, "registration ceremony expired or not found — try again", http.StatusBadRequest)

		return
	}

	creds, err := db.ListCredentials(s.database, user.Username)
	if err != nil {
		internalError(w, err)

		return
	}

	wu := &webauthnUser{user: user, creds: creds}

	cred, err := s.webauthn.FinishRegistration(wu, *session, r)
	if err != nil {
		jsonError(w, "passkey registration failed: "+err.Error(), http.StatusBadRequest)

		return
	}

	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}

	if len(name) > 80 {
		name = name[:80]
	}

	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}

	idStr := base64.RawURLEncoding.EncodeToString(cred.ID)

	if err := db.AddCredential(s.database, idStr, user.Username, cred.PublicKey, cred.Authenticator.SignCount, strings.Join(transports, ","), cred.Flags.MsgpByte(), name); err != nil {
		internalError(w, err)

		return
	}

	log.Printf("passkey %q registered for %s", name, user.Username)

	jsonResponse(w, http.StatusCreated, map[string]string{"id": idStr, "name": name})
}

// handleWebAuthnLoginBegin serves POST /api/webauthn/login/begin. Public —
// no session exists yet, and (this being a discoverable/passkey login) the
// server does not even know who is logging in until login/finish.
//
// Response (200 OK): a protocol.CredentialAssertion (as "publicKey", for
// navigator.credentials.get()) plus a "ceremony_id" the client must echo
// back as a query parameter on login/finish — see the SessionData-handoff
// note at the top of this file for why that can't travel in the request
// body the way it does for register/finish's "name".
func (s *srv) handleWebAuthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	assertion, session, err := s.webauthn.BeginDiscoverableLogin()
	if err != nil {
		internalError(w, err)

		return
	}

	ceremonyID := uuid.New().String()
	s.loginCeremonies.put(ceremonyID, session)

	jsonResponse(w, http.StatusOK, struct {
		*protocol.CredentialAssertion
		CeremonyID string `json:"ceremony_id"`
	}{assertion, ceremonyID})
}

// handleWebAuthnLoginFinish serves POST /api/webauthn/login/finish. Public.
// The request body must be exactly the JSON produced by serializing the
// browser's navigator.credentials.get() result. Two query parameters carry
// what would otherwise be body fields (see the SessionData-handoff note at
// the top of this file): "ceremony" (required — the ID returned by
// login/begin) and "keep" ("true" to request the same 30-day session
// handleLogin grants for "keep me logged in").
//
// On success this establishes a brand new session exactly as handleLogin
// does — see finishLogin in auth_handlers.go, which both handlers share for
// that final step.
//
// Rate limiting mirrors handleLogin: s.loginLimiter is checked before doing
// any verification work, recordFailure only follows an actual failed
// assertion (not a malformed request or an already-expired ceremony), and a
// successful login clears the IP's failure history.
func (s *srv) handleWebAuthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	ceremonyID := r.URL.Query().Get("ceremony")
	if ceremonyID == "" {
		jsonError(w, "missing ceremony id", http.StatusBadRequest)

		return
	}

	session, ok := s.loginCeremonies.take(ceremonyID)
	if !ok {
		jsonError(w, "login ceremony expired or not found — try again", http.StatusBadRequest)

		return
	}

	ip := clientIP(r)
	if !s.loginLimiter.allow(ip) {
		w.Header().Set("Retry-After", "60")
		jsonError(w, "too many failed login attempts — try again later", http.StatusTooManyRequests)

		return
	}

	authedUser, cred, err := s.webauthn.FinishPasskeyLogin(s.webauthnDiscoverableUserHandler, *session, r)
	if err != nil {
		s.loginLimiter.recordFailure(ip)
		// The client-facing message stays generic (matches handleLogin's
		// "invalid credentials" — don't hand an attacker a diagnostic), but
		// the real reason is worth having in the log for the operator, since
		// there is otherwise no way to tell a genuine verification failure
		// apart from a config problem (RP ID/origin mismatch, stale/replayed
		// assertion, etc.) from the outside.
		log.Printf("passkey login failed: %v", err)
		jsonError(w, "passkey login failed", http.StatusUnauthorized)

		return
	}

	s.loginLimiter.clear(ip)

	// Persist the updated clone-detection counter and flags byte (see
	// db.WebAuthnCredential.Flags — ValidatePasskeyLogin refreshes
	// cred.Flags from the live assertion before returning it, e.g. picking
	// up a BackupState change, and that refreshed value must round-trip
	// back to storage or the *next* login's BackupEligible consistency
	// check has nothing current to compare against) and last-used
	// timestamp before issuing the session — a failure here is logged but
	// does not fail the login, matching how a legacy-hash upgrade failure
	// in handleLogin does not fail that login either: the user already
	// proved who they are, and losing this bookkeeping update is not worth
	// rejecting a legitimate authentication over.
	idStr := base64.RawURLEncoding.EncodeToString(cred.ID)
	if err := db.UpdateCredentialUsage(s.database, idStr, cred.Authenticator.SignCount, cred.Flags.MsgpByte()); err != nil {
		log.Printf("updating passkey sign count for %s: %v", idStr, err)
	}

	wu, ok := authedUser.(*webauthnUser)
	if !ok {
		internalError(w, errUnknownWebAuthnUser)

		return
	}

	keepLoggedIn := r.URL.Query().Get("keep") == "true"

	finishLogin(w, s, wu.user, keepLoggedIn, "passkey login")
}

// handleWebAuthnListCredentials serves GET /api/webauthn/credentials.
// Authenticated; always scoped to the caller's own passkeys.
//
// Response (200 OK): a JSON array of {"id", "name", "created_at",
// "last_used_at"} — db.WebAuthnCredential's json tags omit every
// security-sensitive field (the public key, sign counter, owning username),
// so this list is safe to serialize directly.
func (s *srv) handleWebAuthnListCredentials(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)

	creds, err := db.ListCredentials(s.database, user.Username)
	if err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, creds)
}

// handleWebAuthnDeleteCredential serves DELETE /api/webauthn/credentials/{id}.
// Authenticated; db.DeleteCredential scopes the delete to the caller's own
// username, so this can never remove another user's passkey — there is no
// admin override here by design (see idtrack user passkeys in
// commands/users.go for the admin escape hatch when a user has lost their
// device and cannot reach Settings themselves).
//
// Response (200 OK): {"ok": true}, whether or not a matching row existed —
// deleting an already-gone credential is not an error, matching
// handleLogout's same "always succeeds" shape.
func (s *srv) handleWebAuthnDeleteCredential(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := r.PathValue("id")

	if err := db.DeleteCredential(s.database, user.Username, id); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
