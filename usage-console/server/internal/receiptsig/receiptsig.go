// Package receiptsig signs receipt exports (spec §5.1, §9.2) with an Ed25519
// key the control plane makes on its first start. The private key stays in
// an owner-only PEM file; only the public half ever leaves the process.
package receiptsig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Signer holds the receipt signing key.
type Signer struct {
	priv  ed25519.PrivateKey
	pub   []byte // PKIX DER
	keyID string
}

// LoadOrCreate reads the PKCS#8 PEM key at path, or makes one there (0600,
// in a 0700 directory if it has to create it). A key file group or others
// can read is refused: fix it with chmod 600, or delete it to make a new key
// (exports signed with the old one then no longer verify against the API's
// key).
func LoadOrCreate(path string) (*Signer, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return create(path)
	}
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("receipt signing key %s is readable by others (mode %v): chmod 600 it", path, fi.Mode().Perm())
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("receipt signing key %s: not a PKCS#8 PEM private key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("receipt signing key %s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("receipt signing key %s: not an Ed25519 key", path)
	}
	return newSigner(priv)
}

func create(path string) (*Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// O_EXCL: two processes starting at once don't overwrite each other's key.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreate(path)
	}
	if err != nil {
		return nil, err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return newSigner(priv)
}

func newSigner(priv ed25519.PrivateKey) (*Signer, error) {
	pub, err := x509.MarshalPKIXPublicKey(priv.Public())
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pub)
	return &Signer{priv: priv, pub: pub, keyID: hex.EncodeToString(sum[:8])}, nil
}

// KeyID names the key: the first 16 hex of the SHA-256 of its public key
// (PKIX DER), so `openssl pkey -pubin -outform DER | sha256sum` finds it.
func (s *Signer) KeyID() string { return s.keyID }

// Sign is a detached Ed25519 signature (64 raw bytes) over msg exactly.
func (s *Signer) Sign(msg []byte) []byte { return ed25519.Sign(s.priv, msg) }

// PublicPEM is the public key as a PEM "PUBLIC KEY" (what openssl reads).
func (s *Signer) PublicPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: s.pub})
}

// Verify checks sig over msg with a PEM public key, as a third party would.
func Verify(publicPEM, msg, sig []byte) error {
	blk, _ := pem.Decode(publicPEM)
	if blk == nil || blk.Type != "PUBLIC KEY" {
		return errors.New("not a PEM public key")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return errors.New("not an Ed25519 key")
	}
	if !ed25519.Verify(pub, msg, sig) {
		return errors.New("signature doesn't match")
	}
	return nil
}
