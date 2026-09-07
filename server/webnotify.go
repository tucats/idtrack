package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// This file implements in-app notifications for the web client: the same
// three trigger rules and per-user preferences that drive APNs push
// notifications (see notify.go and docs/NOTIFICATIONS.md) also drive a
// Server-Sent Events (SSE) stream, so a user who leaves an idtrack browser
// tab open gets a toast — and, if the tab is in the background, a desktop
// notification — without needing the native iOS/Catalyst app at all.
//
// SSE (rather than WebSockets or a shorter poll interval) was chosen because
// it is one-directional (server -> client, which is all this needs),
// requires no new Go dependency (net/http supports it natively — see
// handleNotificationStream), and degrades gracefully: if a browser's
// connection drops or is never established, the existing 30-second
// `/api/issues/changes` poll (resources/idtrack.js's pollForChanges) still
// eventually surfaces the same underlying data change, just without the
// richer per-event toast.
//
// The single biggest design constraint here is that a stream connection is
// intentionally long-lived (hours), which conflicts with two assumptions
// baked into the rest of this server: gzipHandler buffers an entire response
// body in memory until the handler returns (compress.go), and quiesce holds
// a read-lock on backupMu for a request's whole lifetime so a backup can
// safely take the write lock once every in-flight request finishes
// (backup.go) — a stream sitting in that RLock forever would permanently
// starve every future backup the moment one browser tab is open. Both are
// bypassed for this exact route; see isNotificationStreamRequest in
// middleware.go and its call sites in compress.go/backup.go.

// webNotifyEventBuffer bounds how many undelivered events a single
// subscriber (one open browser tab) is allowed to queue before publish
// starts dropping new ones for it. An in-app toast is a best-effort
// convenience — like a push notification, a dropped one is never retried —
// so a small buffer plus a non-blocking send (see publish) is preferred over
// letting one stalled tab back up the shared notify() call path that every
// other recipient's send also goes through.
const webNotifyEventBuffer = 8

// webNotifyEvent is one notification's payload as delivered to a browser
// tab over the SSE stream, JSON-encoded as the "data:" field of one SSE
// message. Category uses the same wire words as db.NotificationPrefs' JSON
// field names ("new_issue", "new_comment", "resolved") so the client's
// category handling reads the same word its Settings toggle labels use —
// see notifyCategory.webCategory() in notify.go.
type webNotifyEvent struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	IssueID  int64  `json:"issue_id"`
}

// webNotifyHub fans out webNotifyEvents to every open browser tab for a
// given user. It mirrors sessionStore's shape (an in-memory,
// process-lifetime, mutex-guarded map) but keyed by username with
// potentially many live subscribers per key — one per open tab/device, the
// same one-user-many-endpoints shape TokensForUser has for APNs device
// tokens. Like sessionStore, nothing here survives a server restart; a
// reconnecting EventSource simply re-subscribes.
type webNotifyHub struct {
	mu   sync.Mutex
	subs map[string][]chan webNotifyEvent
}

// newWebNotifyHub returns an empty, ready-to-use webNotifyHub. Constructed
// unconditionally in server.Start (unlike s.apns/s.webauthn, which are only
// built when an operator has opted into the feature) — in-app notification
// is a property of the web client itself, not something that needs its own
// server-wide on/off switch the way an external service integration does.
func newWebNotifyHub() *webNotifyHub {
	return &webNotifyHub{subs: make(map[string][]chan webNotifyEvent)}
}

// subscribe registers a new listener for username's events and returns the
// channel to receive them on plus an unsubscribe function the caller must
// defer exactly once. Mirrors the "caller owns cleanup" convention already
// used by webauthnCeremonyStore.
func (h *webNotifyHub) subscribe(username string) (<-chan webNotifyEvent, func()) {
	ch := make(chan webNotifyEvent, webNotifyEventBuffer)

	h.mu.Lock()
	h.subs[username] = append(h.subs[username], ch)
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		defer h.mu.Unlock()

		chans := h.subs[username]

		for i, c := range chans {
			if c == ch {
				h.subs[username] = append(chans[:i], chans[i+1:]...)

				break
			}
		}

		if len(h.subs[username]) == 0 {
			delete(h.subs, username)
		}

		close(ch)
	}

	return ch, unsubscribe
}

// publish delivers event to every currently-subscribed tab/device for
// username. Sends are non-blocking (select/default) so one stalled
// subscriber can never block notify()'s caller — the same fire-and-forget
// treatment sendToToken gives a slow APNs call, just with no network round
// trip to wait on in the first place. Holding h.mu for the whole call (as
// opposed to just the map read) makes this atomic with respect to
// subscribe/unsubscribe: a channel is either fully present and sent to, or
// fully removed-and-closed before publish ever looks at it — never
// half-way, which is what would risk a send on a closed channel.
func (h *webNotifyHub) publish(username string, event webNotifyEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, ch := range h.subs[username] {
		select {
		case ch <- event:
		default:
			// Subscriber's buffer is full (a backgrounded/stalled tab); drop
			// this event for it rather than blocking every other recipient's
			// delivery, including this same user's other open tabs.
		}
	}
}

// webNotifyKeepAlive is how often handleNotificationStream sends an SSE
// comment line. This serves two purposes: it keeps the connection alive
// through intermediary proxies/load balancers that would otherwise time out
// an idle connection, and it lets the server notice a dead client promptly
// via a failed Write instead of waiting indefinitely on a TCP-level failure
// that may never surface on its own.
const webNotifyKeepAlive = 25 * time.Second

// handleNotificationStream serves GET /api/notifications/stream — a
// long-lived SSE connection delivering the same in-app notification events
// described in docs/NOTIFICATIONS.md (new issue, new comment, status
// change), gated by the caller's own three notify_* preferences exactly like
// push notifications (see notify.go's notifyOne, which calls
// s.webNotify.publish alongside the APNs fan-out). One browser tab holds one
// connection; a user with several tabs/devices open simply gets several
// concurrent subscriptions.
//
// The two http.Server-level timeouts (ReadTimeout/WriteTimeout, set in
// server.go's Start) are disabled per-connection here via
// http.ResponseController, since they exist to bound slow-loris-style
// ordinary requests, not to cap how long a legitimate, otherwise-idle
// notification stream may stay open — left in place, WriteTimeout in
// particular would silently kill every stream 30 seconds after it opened.
func (s *srv) handleNotificationStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "streaming not supported", http.StatusInternalServerError)

		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})

	username := currentUser(r).Username

	events, unsubscribe := s.webNotify.subscribe(username)
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Tells a buffering reverse proxy (e.g. nginx's default proxy_buffering)
	// not to hold the response back waiting for more data — see the
	// --base-path reverse-proxy deployment story in CLAUDE.md; a stream
	// stuck silently in a proxy's buffer would defeat the whole feature.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(webNotifyKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case event := <-events:
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}

			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}

			flusher.Flush()

		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}

			flusher.Flush()
		}
	}
}
