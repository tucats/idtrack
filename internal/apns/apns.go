// Package apns is a minimal client for Apple's APNs HTTP/2 provider API using
// token-based (JWT) authentication. It exists so idtrack can send push
// notifications without adding a third-party dependency: Go's net/http
// already negotiates HTTP/2 automatically for https:// URLs, and the ES256
// JWT APNs requires can be hand-signed with the standard library's
// crypto/ecdsa — see docs/NOTIFICATIONS.md §2 for the reasoning. This mirrors
// other hand-rolled pieces of idtrack (the JS minifier, the multipart
// encoder in the iOS client) rather than reaching for a general-purpose
// library to do a small, well-defined job.
package apns

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// productionHost and sandboxHost are APNs' two provider API endpoints. Which
// one a given deployment talks to is a single, deployment-wide choice (see
// Client.Sandbox / docs/NOTIFICATIONS.md's accepted limitation on mixed
// dev/TestFlight deployments) — there is no per-request or per-token
// environment selection.
const (
	productionHost = "https://api.push.apple.com"
	sandboxHost    = "https://api.sandbox.push.apple.com"
)

// authTokenLifetime is how long a signed APNs auth token is treated as valid
// before Client regenerates one. Apple allows up to one hour; this stays
// comfortably under that so a token is never rejected as expired mid-flight,
// while still amortizing the signing cost across many notifications rather
// than signing one per send (which Apple's own guidance discourages).
const authTokenLifetime = 50 * time.Minute

// ErrInvalidToken is returned by Client.Send when APNs reports the device
// token itself as permanently invalid — HTTP 410 "Unregistered" (the device
// is no longer eligible to receive notifications for this app) or HTTP 400
// "BadDeviceToken" (malformed or wrong-environment token). Callers should
// treat this as "stop sending to this token" and remove it from storage
// (see db.DeleteToken) rather than retrying. Every other failure — network
// errors, a misconfigured topic, APNs being briefly unavailable — is
// returned as a plain error instead, since the token itself may still be
// good.
var ErrInvalidToken = errors.New("apns: device token is no longer valid")

// Client sends push notifications via APNs' HTTP/2 provider API, token auth.
// A single Client is safe for concurrent use by multiple goroutines — the
// notifier (see server/notify.go) sends to several recipients' several
// devices concurrently, all through one shared Client.
type Client struct {
	keyID string
	// teamID identifies the Apple Developer team; part of the signed auth
	// token's "iss" claim.
	teamID string
	// topic is the APNs topic — the app's bundle id — sent as the
	// "apns-topic" header on every request.
	topic      string
	host       string // productionHost or sandboxHost, fixed at construction
	signingKey *ecdsa.PrivateKey
	httpClient *http.Client

	mu       sync.Mutex // guards token/tokenIssuedAt below
	token    string
	tokenIat time.Time
}

// NewClient loads and parses the .p8 auth key at keyPath (a PEM-encoded
// PKCS#8 ECDSA private key, exactly as downloaded from the Apple Developer
// portal) and returns a Client configured to sign requests as keyID/teamID
// and address them to topic. sandbox selects APNs' sandbox environment
// (Xcode debug builds) instead of production (TestFlight/App Store builds).
func NewClient(keyPath, keyID, teamID, topic string, sandbox bool) (*Client, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("apns: reading key file: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("apns: %q does not contain PEM data", keyPath)
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parsing key: %w", err)
	}

	ecKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apns: %q is not an ECDSA private key", keyPath)
	}

	host := productionHost
	if sandbox {
		host = sandboxHost
	}

	return &Client{
		keyID:      keyID,
		teamID:     teamID,
		topic:      topic,
		host:       host,
		signingKey: ecKey,
		// A short per-request timeout is appropriate here: every call site
		// (see server/notify.go) runs in its own goroutine specifically so a
		// slow or unreachable APNs never blocks an HTTP handler response, but
		// an unbounded hang would still leak goroutines under sustained
		// notification volume if APNs stopped responding entirely.
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Payload is the content of one push notification.
type Payload struct {
	Title   string
	Body    string
	Badge   int   // absolute badge count to display, not a delta — see db.IncrementBadge
	IssueID int64 // carried outside "aps" so the client can deep-link to it; see AppState.pendingIssueSelection on the iOS side
}

// Send delivers one notification to deviceToken. It returns ErrInvalidToken
// (see above) when APNs reports the token as permanently invalid, or a plain
// error for any other failure (network, misconfiguration, transient APNs
// unavailability) — callers distinguish the two with errors.Is.
func (c *Client) Send(deviceToken string, p Payload) error {
	token, err := c.authToken()
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{
				"title": p.Title,
				"body":  p.Body,
			},
			"sound": "default",
			"badge": p.Badge,
		},
		"issue_id": p.IssueID,
	})
	if err != nil {
		return fmt.Errorf("apns: encoding payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.host+"/3/device/"+deviceToken, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("apns: building request: %w", err)
	}

	req.Header.Set("authorization", "bearer "+token)
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("apns: request failed: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	var reasonBody struct {
		Reason string `json:"reason"`
	}

	json.NewDecoder(resp.Body).Decode(&reasonBody) //nolint:errcheck // best-effort; the status code alone is still meaningful without it

	// APNs' documented signal for "never send to this token again":
	// https://developer.apple.com/documentation/usernotifications/handling-notification-responses-from-apns
	if resp.StatusCode == http.StatusGone ||
		(resp.StatusCode == http.StatusBadRequest && reasonBody.Reason == "BadDeviceToken") {
		return ErrInvalidToken
	}

	return fmt.Errorf("apns: send failed: %d %s", resp.StatusCode, reasonBody.Reason)
}

// authToken returns a cached ES256 JWT if it is still within
// authTokenLifetime, otherwise signs and caches a fresh one. Apple explicitly
// asks providers not to generate a new token for every request; a token is
// valid for up to an hour and should be reused across many notifications.
func (c *Client) authToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Since(c.tokenIat) < authTokenLifetime {
		return c.token, nil
	}

	now := time.Now().UTC()

	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": c.keyID})
	if err != nil {
		return "", fmt.Errorf("apns: encoding JWT header: %w", err)
	}

	claims, err := json.Marshal(map[string]any{"iss": c.teamID, "iat": now.Unix()})
	if err != nil {
		return "", fmt.Errorf("apns: encoding JWT claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)

	hash := sha256.Sum256([]byte(signingInput))

	r, s, err := ecdsa.Sign(rand.Reader, c.signingKey, hash[:])
	if err != nil {
		return "", fmt.Errorf("apns: signing auth token: %w", err)
	}

	// JWT's ES256 signature is the raw 32-byte-r || 32-byte-s concatenation,
	// NOT the ASN.1 DER encoding ecdsa.SignASN1 would produce — big.Int's
	// FillBytes left-pads each coordinate to exactly 32 bytes (the P-256
	// field size), which is what the JWS spec (RFC 7515) requires.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	c.token = signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	c.tokenIat = now

	return c.token, nil
}
