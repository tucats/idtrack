package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestKey generates a fresh P-256 key, PEM/PKCS#8-encodes it exactly
// like a downloaded .p8 file, and writes it to a temp file for NewClient. It
// also returns the key itself so tests can independently verify signatures
// produced against it.
func writeTestKey(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling test key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("writing test key: %v", err)
	}

	return path, key
}

func TestNewClient_ParsesKeyAndSelectsHost(t *testing.T) {
	keyPath, _ := writeTestKey(t)

	c, err := NewClient(keyPath, "KEYID123", "TEAMID456", "com.tucats.idtrack", false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.host != productionHost {
		t.Errorf("host: got %q, want production", c.host)
	}

	c, err = NewClient(keyPath, "KEYID123", "TEAMID456", "com.tucats.idtrack", true)
	if err != nil {
		t.Fatalf("NewClient (sandbox): %v", err)
	}

	if c.host != sandboxHost {
		t.Errorf("host: got %q, want sandbox", c.host)
	}
}

func TestNewClient_RejectsMissingFile(t *testing.T) {
	if _, err := NewClient("/nonexistent/key.p8", "k", "t", "topic", false); err == nil {
		t.Error("expected an error for a missing key file")
	}
}

func TestNewClient_RejectsNonPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notakey.p8")
	os.WriteFile(path, []byte("not a pem file"), 0600)

	if _, err := NewClient(path, "k", "t", "topic", false); err == nil {
		t.Error("expected an error for non-PEM content")
	}
}

// TestAuthToken_SignatureVerifies independently confirms the hand-rolled
// ES256 signing in authToken (raw r||s concatenation, not ASN.1 DER) is
// actually correct by verifying it with the standard library's own
// ecdsa.Verify against the same key — a mistake here would otherwise only
// surface as APNs silently rejecting every notification in production.
func TestAuthToken_SignatureVerifies(t *testing.T) {
	keyPath, key := writeTestKey(t)

	c, err := NewClient(keyPath, "KEYID123", "TEAMID456", "com.tucats.idtrack", false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	tok, err := c.authToken()
	if err != nil {
		t.Fatalf("authToken: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts", len(parts))
	}

	signingInput := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signingInput))

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}

	if len(sig) != 64 {
		t.Fatalf("expected a 64-byte raw signature, got %d bytes", len(sig))
	}

	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])

	if !ecdsa.Verify(&key.PublicKey, hash[:], r, s) {
		t.Error("signature does not verify against the signing key's public half")
	}
}

func TestAuthToken_CachesAndHasThreeParts(t *testing.T) {
	keyPath, _ := writeTestKey(t)

	c, err := NewClient(keyPath, "KEYID123", "TEAMID456", "com.tucats.idtrack", false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	tok1, err := c.authToken()
	if err != nil {
		t.Fatalf("authToken: %v", err)
	}

	if parts := strings.Split(tok1, "."); len(parts) != 3 {
		t.Errorf("expected a 3-part JWT, got %d parts", len(parts))
	}

	tok2, err := c.authToken()
	if err != nil {
		t.Fatalf("authToken (second call): %v", err)
	}

	if tok1 != tok2 {
		t.Error("expected the cached token to be reused within its lifetime")
	}

	// Force expiry and confirm a fresh token is signed.
	c.tokenIat = time.Now().Add(-2 * authTokenLifetime)

	tok3, err := c.authToken()
	if err != nil {
		t.Fatalf("authToken (after expiry): %v", err)
	}

	if tok3 == tok1 {
		t.Error("expected a fresh token after the cached one expired")
	}
}

func TestSend_Success(t *testing.T) {
	keyPath, _ := writeTestKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/3/device/abc123") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		if r.Header.Get("apns-topic") != "com.tucats.idtrack" {
			t.Errorf("apns-topic: got %q", r.Header.Get("apns-topic"))
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := NewClient(keyPath, "KEYID123", "TEAMID456", "com.tucats.idtrack", false)
	c.host = srv.URL

	if err := c.Send("abc123", Payload{Title: "Hi", Body: "There", Badge: 1, IssueID: 42}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestSend_Unregistered_ReturnsErrInvalidToken(t *testing.T) {
	keyPath, _ := writeTestKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		w.Write([]byte(`{"reason":"Unregistered"}`))
	}))
	defer srv.Close()

	c, _ := NewClient(keyPath, "KEYID123", "TEAMID456", "com.tucats.idtrack", false)
	c.host = srv.URL

	err := c.Send("abc123", Payload{})
	if err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestSend_BadDeviceToken_ReturnsErrInvalidToken(t *testing.T) {
	keyPath, _ := writeTestKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"reason":"BadDeviceToken"}`))
	}))
	defer srv.Close()

	c, _ := NewClient(keyPath, "KEYID123", "TEAMID456", "com.tucats.idtrack", false)
	c.host = srv.URL

	err := c.Send("abc123", Payload{})
	if err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestSend_OtherBadRequest_IsTransient(t *testing.T) {
	keyPath, _ := writeTestKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"reason":"MissingTopic"}`))
	}))
	defer srv.Close()

	c, _ := NewClient(keyPath, "KEYID123", "TEAMID456", "com.tucats.idtrack", false)
	c.host = srv.URL

	err := c.Send("abc123", Payload{})
	if err == nil || err == ErrInvalidToken {
		t.Errorf("expected a plain transient error, got %v", err)
	}
}
