# Push Notifications — Implementation Plan

**Status: Approved — in progress.** This document is both the design proposal and the living record of implementation progress. Each phase below carries a status checkbox and, once started, notes on what was actually built and any deviations from the original plan.

**Resolved decisions** (from review of the original draft — see §8 for the questions these answer):

1. All issue state transitions get a notification (not narrowed to just →Resolved).
2. No per-device token management UI/API for now (no WebAuthn-credentials-style listing).
3. Badge counts are included (§3.1, §3.3, §3.6, §4.9) — the OS-level "show badge" toggle in the device's own Notification Settings is what actually governs whether the badge is displayed; idtrack always sends a count and lets each device honor or ignore it per Apple's own per-device Settings control.
4. No notion of "watchers" — only the issue's reporter and assignee are ever notified, and only via this iOS/Catalyst push mechanism (there is no browser/web-push counterpart in this plan).

## Table of Contents

1. [Requirements (as given)](#1-requirements-as-given)
2. [Design Decisions](#2-design-decisions)
3. [Server-Side Architecture](#3-server-side-architecture)
4. [Client-Side Architecture](#4-client-side-architecture)
5. [Prerequisites (manual, one-time)](#5-prerequisites-manual-one-time)
6. [Implementation Phases](#6-implementation-phases)
7. [Testing Plan](#7-testing-plan)
8. [Open Questions](#8-open-questions)

---

## 1. Requirements (as given)

- iOS/Catalyst app entitlements updated to request push notification capability.
- On login or app relaunch, the client acquires an APNs device token and sends it to the server. The server persists, per token: owning username, the token value, when it was created, and when it was last used to send a notification.
- The server sends a push notification when:
  1. **A new issue is created**, notifying the assignee if the assignee differs from the reporter (creator).
  2. **A new comment is added**, but only when the issue's reporter and assignee are different people. In that case, notify whichever of {reporter, assignee} did **not** author the comment (either, both, or neither — never the comment's own author).
  3. **An issue changes status**, notifying whichever of {reporter, assignee} did **not** perform the status change.
- If Apple's push service reports a token as expired/invalid, the server silently removes it from the token table.
- Client Settings gets three independent toggles: notify on new issue, on new comment, on resolution.
- During onboarding, the app explains that it would like to send notifications and asks permission. A "no" answer turns all three toggles off and prevents the client from even acquiring/sending a token.

## 2. Design Decisions

These are choices made while turning the requirements into a concrete design. Flagging them explicitly so they can be challenged before Phase 1 starts.

- **Per-user preferences live server-side**, not just on-device. A push notification is inherently server-initiated and cannot be filtered by a client after the fact — the server must know, per user and per category, whether to send. Three boolean columns are added to `users`: `notify_new_issue`, `notify_new_comment`, `notify_resolved` (default `1`/on — see below for why the default doesn't matter much in practice).
- **A device's registered token is keyed on the token itself**, not on `(username, device)`. A device token is unique per app-install-plus-device already; if a different user later logs into the same physical device, re-registering simply reassigns that same token row to the new username (`INSERT ... ON CONFLICT(token) DO UPDATE SET username=excluded.username`). This mirrors how Apple's own token semantics work (one token = one (app, device) pair) and avoids a separate composite-key/cleanup problem.
- **Notifications are sent via APNs' HTTP/2 provider API using token-based authentication** (a `.p8` signing key from the Apple Developer portal), not a legacy certificate-based connection — the token approach is Apple's current recommendation, doesn't expire yearly like certs, and works for both the app's Development and Production APNs environments with the same key.
- **No new third-party Go dependency.** `net/http`'s `http.Client` negotiates HTTP/2 automatically over TLS for `https://` URLs since Go 1.6, and the ES256 JWT APNs auth token can be hand-signed with stdlib `crypto/ecdsa` — no HTTP/2 library and no JWT library needed. This keeps to the project's established minimal-dependency posture (see CLAUDE.md's WebAuthn section for the last time this tradeoff was made explicitly) — a small `internal/apns` implementation is written by hand, in the same spirit as the hand-rolled JS minifier and multipart encoder already in this codebase.
- **Notification sends are fire-and-forget from the HTTP handler's perspective.** `handleCreateIssue`, `handleCreateComment`, and `handleUpdateIssue` launch a goroutine to do the actual APNs call(s) after the database write succeeds, so a slow or failing push never delays the API response the web/iOS client is waiting on. Errors are logged, not surfaced to the caller — identical in spirit to how `handleUpdateIssue`'s auto-comment-on-Duplicate already treats notification-adjacent side effects as best-effort.
- **One global sandbox/production flag per server instance**, not per-request/per-token environment detection. A device token does not reliably self-identify which APNs environment issued it. Since idtrack is a self-hosted, single-team internal tool, an operator's instance serves either TestFlight/App-Store builds (production) or Xcode debug builds (sandbox), not usually both at once — this is called out as an accepted limitation rather than solved generally.
- **Deep-linking a tapped notification reuses the existing `pendingIssueSelection` mechanism** built for `idtrack://` share links (see `AppState.handleIncomingURL`, commit `8cd8cb3`). The push payload carries an `issue_id`; the notification-response delegate just sets `AppState.pendingIssueSelection`, and `MainAppView`'s existing observer does the rest. No new deep-link plumbing needed.
- **The pre-permission ask is a custom screen shown before Apple's system dialog** ("soft ask" pattern) — standard iOS practice, since the system dialog can only be shown meaningfully once per install (a denial can't be re-prompted; the app must send the user to Settings instead). This satisfies "let the user know... and ask their permission" as two steps: our explanation screen, then (only on "Enable") the real `requestAuthorization` call.
- **`server.Start()`'s parameter list, already at 19 positional arguments, is not extended further with 5+ more APNs settings.** Phase 2 introduces a `server.Config` struct that `commands.Serve` populates and passes as a single argument. This is a mechanical refactor of the existing parameters plus the new ones — not a behavior change — and is called out as its own phase so it can be reviewed/reverted independently of the notification feature itself if preferred.

## 3. Server-Side Architecture

### 3.1 Schema changes (`db/db.go`)

Two purely-additive changes, following the existing `addColumnIfMissing`/`CREATE TABLE IF NOT EXISTS` migration pattern — no new migration framework:

```sql
-- Brand-new table, like webauthn_credentials/attachments: needs no backfill.
CREATE TABLE IF NOT EXISTS notification_tokens (
    token             TEXT PRIMARY KEY,   -- APNs device token, hex-encoded
    username          TEXT NOT NULL,
    created_at        TEXT NOT NULL,      -- RFC3339 UTC; set once, never updated
    last_notified_at  TEXT NOT NULL DEFAULT '',  -- RFC3339 UTC; updated after each successful send
    badge_count       INTEGER NOT NULL DEFAULT 0 -- APNs is stateless about badges; the provider must track and send the absolute number each time (see §3.6)
);
CREATE INDEX IF NOT EXISTS idx_notification_tokens_username ON notification_tokens (username);
```

```go
// New columns on the existing users table, via addColumnIfMissing:
addColumnIfMissing(db, "users", "notify_new_issue",   "INTEGER NOT NULL DEFAULT 1")
addColumnIfMissing(db, "users", "notify_new_comment",  "INTEGER NOT NULL DEFAULT 1")
addColumnIfMissing(db, "users", "notify_resolved",     "INTEGER NOT NULL DEFAULT 1")
```

These three columns default to "on" purely as a safe schema default for rows created before this feature existed (an upgraded server shouldn't retroactively silence a user who never made a choice) — in practice a fresh install/onboarding always writes an explicit value once permission is granted or denied (see §4.3), so the default is rarely observed in the wild.

Deliberately **not** added to the shared `db.User` struct (which backs `GET /api/users` — visible to every authenticated user for the assignee dropdown/`userMap`). A separate, small `db.NotificationPrefs` struct and its own get/set functions keep one user's notification choices from leaking into another user's client.

### 3.2 New `db/notifications.go`

- `RegisterToken(db, username, token string) error` — upsert by token PK (reassigns `username` and refreshes nothing else on conflict; `created_at` is only set on first insert).
- `DeleteToken(db, token string) error` — used both by the self-service unregister endpoint (sign-out) and by the APNs-feedback cleanup path.
- `TokensForUser(db, username string) ([]string, error)` — every token currently registered to a user (0, 1, or many devices).
- `TouchToken(db, token string) error` — sets `last_notified_at = now` after a successful send.
- `IncrementBadge(db, token string) (int, error)` — atomically does `badge_count = badge_count + 1` and returns the new value in one statement (`UPDATE ... RETURNING badge_count`, supported by `modernc.org/sqlite`), so the caller has the exact number to put in the APNs payload without a separate read-then-write race.
- `ResetBadge(db, token string) error` — sets `badge_count = 0`; called by the client-side "app became active" handler (§4.9).
- `GetNotificationPrefs(db, username string) (*NotificationPrefs, error)`
- `SetNotificationPrefs(db, username string, prefs NotificationPrefs) error`

```go
type NotificationPrefs struct {
    NewIssue   bool `json:"new_issue"`
    NewComment bool `json:"new_comment"`
    Resolved   bool `json:"resolved"`
}
```

### 3.3 `internal/apns` package (new)

A minimal provider-API client:

- `NewClient(keyPath, keyID, teamID, topic string, sandbox bool) (*Client, error)` — parses the `.p8` PEM (`crypto/ecdsa` + `crypto/x509`), stores the signing key.
- Internally maintains a cached ES256 JWT (APNs auth tokens are valid up to 1h; Apple asks providers not to generate a fresh one per request), regenerating it when it's within a few minutes of expiry.
- `Send(deviceToken string, payload Payload) error` — POSTs to `https://api.push.apple.com/3/device/<token>` (or `api.sandbox.push.apple.com` when `sandbox` is true) with `authorization: bearer <jwt>`, `apns-topic: <bundle id>`, `apns-push-type: alert`, `apns-priority: 10`, and a JSON body `{"aps":{"alert":{"title":...,"body":...},"sound":"default","badge":N},"issue_id":N}`. `Payload.Badge` is the absolute count from `db.IncrementBadge` (§3.2), not a delta — APNs always sets the icon to exactly the number given.
- Return value distinguishes three outcomes the caller needs: success, **permanently invalid token** (HTTP 400 `BadDeviceToken` or HTTP 410 `Unregistered` — caller should call `db.DeleteToken`), and any other transient error (logged, token kept).

### 3.4 Config plumbing

`commands/common.go`'s `defaults` struct gains:

```go
ApnsKeyPath  string `json:"apns_key_path,omitempty"`   // absolute path to the .p8 signing key
ApnsKeyID    string `json:"apns_key_id,omitempty"`
ApnsTeamID   string `json:"apns_team_id,omitempty"`
ApnsTopic    string `json:"apns_topic,omitempty"`      // bundle id, e.g. "com.tucats.idtrack"
ApnsSandbox  bool   `json:"apns_sandbox,omitempty"`     // true = talk to APNs' sandbox environment
```

New `idtrack default` flags: `--apns-key-path`, `--apns-key-id`, `--apns-team-id`, `--apns-topic`, `--apns-sandbox [true|false]`. Validated as a group the same way WebAuthn's RP ID/origin are: if any of key-path/key-id/team-id/topic is set, all four must be — `Default` errors otherwise. Push notifications are simply not sent (not an error at send time) when the group isn't configured at all — `server.Start()` treats an absent APNs config as "feature off," logging a note once at startup rather than failing to boot, since not every operator wants or can set up push notifications.

`server.Start()` signature: introduce `server.Config` (see §2) carrying every existing positional parameter plus `ApnsKeyPath/KeyID/TeamID/Topic/Sandbox`. `commands.Serve` builds one `server.Config{}` literal instead of a 24-argument call.

### 3.5 New HTTP API endpoints

| Method | Path | Auth | Body |
| --- | --- | --- | --- |
| POST | `/api/notifications/token` | yes | `{"token": "<hex>"}` — registers/reassigns this device token to the caller |
| DELETE | `/api/notifications/token/{token}` | yes | — unregisters (called on sign-out) |
| GET | `/api/notifications/prefs` | yes | — returns the caller's own `NotificationPrefs` |
| PUT | `/api/notifications/prefs` | yes | `{"new_issue","new_comment","resolved"}` — replaces the caller's prefs |
| POST | `/api/notifications/badge/reset` | yes | `{"token": "<hex>"}` — zeroes that device's server-tracked badge count |

All five follow the existing self-service pattern used by `/api/webauthn/credentials` (auth required, no admin check, operates only on `currentUser(r)` — never takes a username in the path/body). The badge-reset endpoint takes a token rather than acting on every token the user owns, since badge count is inherently per-device (one iPhone's icon shouldn't be zeroed by reading mail on an iPad).

**Deviation from the original draft**: unregister takes the token as a path parameter (`DELETE .../token/{token}`) rather than a JSON body, matching every other DELETE route in this API (none of which carry a body) instead of introducing the first body-bearing DELETE. `db.DeleteTokenForUser(username, token)` scopes the delete to the caller's own username — mirroring `db.DeleteCredential`'s ownership check for WebAuthn passkeys — so this can never remove a token belonging to a different account, even by replaying another user's token value. A separate, unscoped `db.DeleteToken(token)` remains for the server-internal APNs-feedback cleanup path (Phase 5), which only ever has the bare token string in hand.

### 3.6 Wiring notification triggers

- **`handleCreateIssue`** (`server/issues.go`): after `db.CreateIssue` succeeds, if `issue.Assignee != "" && issue.Assignee != issue.Reporter`, `go s.notify(issue.Assignee, notifyCategoryNewIssue, ...)`.
- **`handleCreateComment`** (`server/comments.go`): after `db.CreateComment` succeeds, using the already-fetched `issue`: if `issue.Reporter != issue.Assignee` (both non-blank), notify whichever of `{issue.Reporter, issue.Assignee}` is non-blank and `!= author`, deduplicated.
- **`handleUpdateIssue`** (`server/issues.go`): after `db.UpdateIssue` succeeds, if `oldStatus != newStatus`, notify whichever of `{existing.Reporter, existing.Assignee}` (pre-update values) is non-blank and `!= u.Username` (the actor), deduplicated. Fires for **every** status transition (Open↔Blocked↔Resolved↔Duplicate, not just →Resolved), gated by the single `notify_resolved` preference — per the resolved decision at the top of this document.

A shared unexported helper does the actual dedup/pref-check/send:

```go
// notify looks up each username's prefs and tokens, and — for every enabled,
// token-bearing recipient — sends a push in its own goroutine. Called with
// `go` from the three call sites above so it never blocks the HTTP response.
// Immediately before sending on each token, it calls db.IncrementBadge(token)
// to get that device's next absolute badge number for the payload.
func (s *srv) notify(usernames []string, category notifyCategory, title, body string, issueID int64)
```

## 4. Client-Side Architecture

### 4.1 Entitlements (`ios-app/IDTrackApp/IDTrackApp.entitlements`)

```xml
<key>aps-environment</key>
<string>development</string>
```

Xcode automatically rewrites this to `production` in the entitlements it actually embeds for an Archive/TestFlight build when using automatic signing — committing `development` in source is the standard, safe choice.

### 4.2 AppDelegate + notification delegate (new `PushNotifications.swift`)

The app is currently a pure SwiftUI `App` with no `UIApplicationDelegateAdaptor`. This adds the first one:

```swift
@main
struct IDTrackApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
    ...
}
```

`AppDelegate`:
- `application(_:didRegisterForRemoteNotificationsWithDeviceToken:)` — converts the `Data` token to a lowercase hex string, hands it to `AppState.shared` to POST to the server.
- `application(_:didFailToRegisterForRemoteNotificationsWithError:)` — logs only (e.g. no network, simulator without push support on older iOS).
- Sets `UNUserNotificationCenter.current().delegate = self` and implements `UNUserNotificationCenterDelegate`:
  - `willPresent` → `.banner, .sound` so a push shows even while the app is foregrounded.
  - `didReceive response` → reads `response.notification.request.content.userInfo["issue_id"]`, sets `AppState.shared?.pendingIssueSelection`.

`AppState` gains `static weak var shared: AppState?`, set once in `init()`, so the (non-SwiftUI-View) `AppDelegate` has a way to reach it — the same problem WebAuthn's biometric flow doesn't have (those run inside a View) but push delegate callbacks do.

### 4.3 Permission flow

New `NotificationPermissionView.swift` — a single explanatory screen ("We'd like to send you notifications for new issues, comments, and resolutions — you can change this anytime in Settings.") with **Enable** / **Not Now** buttons.

Shown once per install, gated by a UserDefaults flag `notificationPromptShown` (device/install-level, not tied to any one account — matches the fact that the OS permission itself is install-level). Triggered from `MainAppView` on first appearance after any successful sign-in (onboarding, fresh login, or persisted-session restore) — one code path instead of duplicating it into `OnboardingView` and `LoginView` separately.

- **Enable** tapped → `UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])`.
  - Granted → `PUT /api/notifications/prefs {true,true,true}`, then `UIApplication.shared.registerForRemoteNotifications()` on the main thread.
  - Denied (rare, but possible even after tapping Enable) → `PUT /api/notifications/prefs {false,false,false}`.
- **Not Now** tapped → the system dialog is never shown (preserves the ability to ask again later from Settings) → `PUT /api/notifications/prefs {false,false,false}`, no token acquisition at all.

### 4.4 Token (re-)registration on login/relaunch

Per the requirement, this must happen on every login *and* every relaunch, not just once at onboarding. Implementation: wherever `AppState.signIn(user:)` is called (login, onboarding, and `ContentView.boot()`'s persisted-session restore), after a successful sign-in check `UNUserNotificationCenter.current().notificationSettings().authorizationStatus == .authorized`; if so, call `registerForRemoteNotifications()` again (Apple's own guidance: token can change at any time, e.g. after restore-from-backup, so re-registering on every launch is correct and cheap — the server-side upsert makes a repeat POST of an unchanged token a no-op). If the status is `.denied` or `.notDetermined`, nothing is registered and no token is sent — satisfying "if they say no... prevents the client from even acquiring a notification token."

### 4.5 `APIClient.swift` additions

```swift
struct NotificationPrefs: Codable {
    var newIssue: Bool
    var newComment: Bool
    var resolved: Bool
    enum CodingKeys: String, CodingKey {
        case newIssue = "new_issue", newComment = "new_comment", resolved
    }
}

func registerNotificationToken(_ token: String) async throws
func unregisterNotificationToken(_ token: String) async throws  // DELETE /api/notifications/token/{token}
func getNotificationPrefs() async throws -> NotificationPrefs
func updateNotificationPrefs(_ prefs: NotificationPrefs) async throws
func resetBadge(token: String) async throws
```

### 4.6 `AppState.swift` additions

- `@Published var notificationPrefs = NotificationPrefs(newIssue: true, newComment: true, resolved: true)` — refreshed from the server right after sign-in (mirrors `refreshUsers()`/`refreshProjects()`).
- `var deviceTokenHex: String?` — the current APNs token, kept in memory (not persisted) so `signOut()` can best-effort `DELETE /api/notifications/token` with it before clearing session state, mirroring the existing cookie-jar cleanup.

### 4.7 `SettingsView.swift` — new "Notifications" section

- If OS authorization is `.denied`: a single row explaining notifications are off in iOS Settings, with a button that opens `UIApplication.openSettingsURLString` (apps cannot re-trigger the system dialog once denied).
- If OS authorization is `.notDetermined`: an "Enable Notifications" button that re-shows the same soft-ask flow from §4.3.
- If `.authorized`: three toggles — "New Issues", "New Comments", "Resolved Issues" — bound to `appState.notificationPrefs.*`, each calling `updateNotificationPrefs` on change.

### 4.8 Tap-to-open

No new code beyond §4.2's `didReceive response` handler — it sets the same `AppState.pendingIssueSelection` that `idtrack://...?issue=42` links already populate, and `MainAppView`'s existing `.onChange`/`initial: true` observer (from the `idtrack://` deep-link feature) opens the issue.

### 4.9 Badge reset

The app icon badge is cleared in two places, both cheap and redundant with each other (network reset can be delayed or fail; the local reset is instant):

- **Locally, immediately**: a `.onChange(of: scenePhase)` observer in `MainAppView` (or `IDTrackApp`) calls `UNUserNotificationCenter.current().setBadgeCount(0)` whenever the app becomes `.active`, so the icon clears the moment the user opens the app regardless of network state.
- **Server-side**: the same handler calls `appState.api.resetBadge(token: appState.deviceTokenHex)` (best-effort, `try?`) so the next push computed via `db.IncrementBadge` starts counting from 0 again instead of continuing to climb from whatever it last reached.

## 5. Prerequisites (manual, one-time)

These require access to the Apple Developer account and cannot be automated from here:

1. In the Apple Developer portal → Certificates, Identifiers & Profiles → Keys, create an **APNs Auth Key** (`.p8`). Note its **Key ID**.
2. Note the account's **Team ID** (top-right of the developer portal, or `Certificates, Identifiers & Profiles → Membership`).
3. Confirm the app's bundle identifier — already `com.tucats.idtrack` (from `IDTrackApp.xcodeproj`) — is enabled for the **Push Notifications** capability in the identifier's configuration.
4. Copy the `.p8` file to wherever the idtrack server runs, and configure it:

   ```sh
   idtrack default \
     --apns-key-path /path/to/AuthKey_XXXXXXXXXX.p8 \
     --apns-key-id XXXXXXXXXX \
     --apns-team-id YYYYYYYYYY \
     --apns-topic com.tucats.idtrack
   ```

   Add `--apns-sandbox true` only for a server instance that exclusively serves Xcode debug builds (see §2's accepted limitation).
5. In Xcode, enable the **Push Notifications** capability on the `IDTrackApp` target (this both updates the entitlements and, more importantly, is required for App Store/TestFlight provisioning to actually include the capability — the entitlements file alone is necessary but not sufficient).

## 6. Implementation Phases

Each phase is intended to be a small, reviewable, independently-buildable unit of work.

- [ ] **Phase 0 — Prerequisites.** Complete §5 above (Apple Developer key, Xcode capability). Blocks end-to-end testing of every later phase but not the code itself.
- [x] **Phase 1 — Server schema & db package.** `notification_tokens` table, `users` prefs columns, `db/notifications.go`. Implemented as designed; `IncrementBadge` uses `UPDATE ... RETURNING` (confirmed supported by `modernc.org/sqlite` v1.50.1) as a single atomic statement rather than read-then-write. Covered by `db/notifications_test.go`.
- [x] **Phase 2 — `server.Config` refactor.** `server.Start(cfg Config)` replaces the 19-argument form; `commands/serve.go` builds one `server.Config{}` literal. Added the five `Apns*` fields to `server.Config`, `srv`, and `commands/common.go`'s `defaults` struct, plus `idtrack default --apns-key-path/--apns-key-id/--apns-team-id/--apns-topic/--apns-sandbox` (validated as an all-or-nothing group, same pattern as `--webauthn-rp-id`/`--webauthn-rp-origin`) and `showDefaults` rows. No behavior change to existing flags; full test suite still green.
- [x] **Phase 3 — `internal/apns` client.** JWT signing/caching, HTTP/2 send, response classification (success / permanently-invalid via `ErrInvalidToken` / transient). No new dependency — `net/http` negotiates HTTP/2 automatically; ES256 JWT signing is hand-rolled with `crypto/ecdsa`. Covered by `internal/apns/apns_test.go`, including a test that independently re-verifies a signed auth token's signature with `ecdsa.Verify` against the same key (catching a raw-r‖s-vs-ASN.1-DER mistake that would otherwise only surface as APNs silently rejecting everything in production).
- [x] **Phase 4 — API endpoints.** `server/notifications.go`: `POST /api/notifications/token`, `DELETE /api/notifications/token/{token}` (path param, not body — see the deviation note above), `GET`/`PUT /api/notifications/prefs`, `POST /api/notifications/badge/reset`; registered in `server.go` alongside the WebAuthn/attachment routes. Added `db.DeleteTokenForUser` (ownership-scoped, for the self-service unregister endpoint) alongside the existing unscoped `db.DeleteToken` (for Phase 5's internal APNs-feedback cleanup). Covered by `server/notifications_test.go`, including that one user cannot unregister or alter another user's token/prefs.
- [x] **Phase 5 — Trigger wiring.** `server/notify.go` (`notify`/`notifyOne`/`sendToToken`, `truncateForNotification`) wired via `go s.notify(...)` into `handleCreateIssue`, `handleCreateComment`, `handleUpdateIssue`. `s.apns` is typed as a small `pushSender` interface (not `*apns.Client` directly) so tests can inject a fake and assert exactly who/what was sent, without a real or httptest-mocked APNs endpoint. `server.Start()` constructs the real `*apns.Client` when all four Apns* config fields are set (fails startup on a partial group or an unparseable key, mirroring WebAuthn's fail-fast). Covered by `server/notify_test.go`: the pure decision logic (dedup, pref-gating, badge increment, invalid-token removal) tested by calling `notify()` directly/synchronously, plus full handler-level tests for all three triggers and their negative cases (unassigned, self-assigned, reporter==assignee, comment-author exclusion, unchanged status).
- [x] **Phase 6 — Server docs.** `docs/API.md` — new "Push Notifications" endpoint section under Authenticated endpoints. `CLAUDE.md` — `notification_tokens` table + `users.notify_*` columns in the schema, migration note, `defaults.json` example, `idtrack default --apns-*` flags in both the CLI Verbs section and Usage() text, HTTP API quick-reference rows, and six new "Important Implementation Decisions" bullets covering the design end-to-end. `resources/MANUAL.md` (served at `/manual`) — `--apns-*` flag table rows plus a full "Push Notifications (iOS/Catalyst App)" subsection mirroring the existing Passkey (WebAuthn) one, explicitly scoped to the iOS app only (no web push in this design).
- [x] **Phase 7 — iOS entitlements & delegate scaffolding (plus core plumbing).** `aps-environment` entitlement; `SystemCapabilities` block added to the Xcode project so automatic signing requests the Push capability (confirmed working — a real `xcodebuild` build with this machine's actual Apple Developer signing identity produced a provisioning profile carrying `com.apple.developer.aps-environment`, so the Apple Developer portal side is already in place); `AppDelegate`/`UNUserNotificationCenterDelegate` in new `PushNotifications.swift`, wired via `@UIApplicationDelegateAdaptor` in `IDTrackApp.swift`. Consolidated with most of Phase 9's networking/state plumbing in this same commit since `AppDelegate` cannot compile without it: `AppState.shared`, `notificationPrefs`/`deviceTokenHex`, `refreshNotificationPrefs()`/`registerForPushNotificationsIfAuthorized()`/`handleDeviceToken(_:)`, `signOut()` unregistering the device token, `NotificationPrefs` model, and the five `APIClient` methods. None of this is triggered from any UI yet — that's Phase 8/9's remaining call-site wiring. Verified by building the real Xcode project for Mac Catalyst after every edit (`xcodebuild -scheme IDTrack -destination 'platform=macOS,variant=Mac Catalyst'`) — this machine has a working Xcode 27 toolchain and valid signing identity, so this is a real compiled/signed build, not a syntax-only check.
- [x] **Phase 8 — iOS permission UX.** New `NotificationPermissionView.swift` (soft-ask screen, Enable/Not Now). `AppState.handlePostSignIn()` is the single choke point called from all three post-sign-in sites (`OnboardingView.submit`, `LoginView.submit`, `ContentView.boot`'s persisted-session restore) — it refreshes prefs from the server, then either shows the one-time prompt (`showNotificationPermissionPrompt`, gated by `AppState.notificationPromptShownKey` in `UserDefaults`) or silently re-registers for push if the prompt was already answered on this install. Presented from `MainAppView` alongside the other `showX` sheets. Verified with a real Mac Catalyst build.
- [ ] **Phase 9 — iOS token/prefs plumbing.** `APIClient` additions, `AppState` additions, registration-on-login/relaunch, unregistration-on-sign-out.
- [ ] **Phase 10 — iOS Settings UI.** The three toggles + OS-permission-status row in `SettingsView`.
- [ ] **Phase 11 — Tap-to-open verification.** Confirm the existing `pendingIssueSelection` path is exercised correctly from a real push tap (should require no new code — verification only).
- [ ] **Phase 12 — End-to-end test pass.** See §7.

## 7. Testing Plan

- **Server unit tests** (`server/*_test.go`, `db/*_test.go` — this project already has good coverage of similar handlers): token register/reassign/delete, prefs get/set, the three trigger conditions (including the "reporter == assignee → no comment notification" and "don't notify the actor" edge cases), and `internal/apns`'s response-classification logic with a mocked HTTP transport (no real APNs calls in unit tests).
- **Simulated push delivery without a real device**: iOS 16+ Simulator supports `xcrun simctl push <device> com.tucats.idtrack /path/to/payload.json` for testing the *client-side* receive/tap/deep-link path without needing real APNs delivery — useful for Phases 8–11 before Phase 0's real credentials are even in place.
- **Real end-to-end test** (needs Phase 0 complete, a physical device, and a TestFlight or ad-hoc build): create an issue assigned to a second test account on a second device, confirm the push arrives, tap it, confirm it opens the right issue; repeat for the comment and status-change cases including the negative cases (comment author doesn't get notified; reporter == assignee produces no comment notification).
- **Invalid-token cleanup**: manually insert a garbage token row, trigger a notification, confirm the row is deleted after the APNs 400/410 response.

## 8. Open Questions

All four questions raised in the original draft were resolved on review — see "Resolved decisions" at the top of this document. Nothing currently open; new questions that come up during implementation get added here.
