package receiptsig

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The first start makes a key, owner-only, in a directory of its own; the
// next start loads the same one.
func TestLoadOrCreateMakesAnOwnerOnlyKeyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "receipt-signing.pem")
	s, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v, want 0600", fi.Mode().Perm())
	}
	again, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.KeyID() != s.KeyID() || len(s.KeyID()) != 16 {
		t.Fatalf("key ids %q then %q", s.KeyID(), again.KeyID())
	}
	// PKCS#8, so openssl reads it.
	b, _ := os.ReadFile(path)
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		t.Fatalf("not a PKCS#8 PEM: %q", b)
	}
	if _, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err != nil {
		t.Fatal(err)
	}
}

// A key file anyone else can read is refused, as ssh refuses one.
func TestLoadOrCreateRefusesAReadableKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.pem")
	if _, err := LoadOrCreate(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v, want a refusal saying chmod 600", err)
	}
}

// A signature verifies against the published public key over the exact
// bytes, and fails if one byte changes.
func TestSignatureRoundTripAndTamper(t *testing.T) {
	s, err := LoadOrCreate(filepath.Join(t.TempDir(), "k.pem"))
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte(`{"id":"r1","costUsd":0.0012}` + "\n")
	sig := s.Sign(msg)
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature is %d bytes", len(sig))
	}
	pub := s.PublicPEM()
	if !strings.HasPrefix(string(pub), "-----BEGIN PUBLIC KEY-----") {
		t.Fatalf("public key PEM: %q", pub)
	}
	if strings.Contains(string(pub), "PRIVATE") {
		t.Fatal("public PEM carries the private key")
	}
	if err := Verify(pub, msg, sig); err != nil {
		t.Fatal(err)
	}
	tampered := []byte(strings.Replace(string(msg), "0.0012", "0.0011", 1))
	if err := Verify(pub, tampered, sig); err == nil {
		t.Fatal("a changed receipt verified")
	}
	other, _ := LoadOrCreate(filepath.Join(t.TempDir(), "other.pem"))
	if err := Verify(other.PublicPEM(), msg, sig); err == nil {
		t.Fatal("another key verified the signature")
	}
}
