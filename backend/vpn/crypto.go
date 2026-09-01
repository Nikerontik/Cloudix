// Package vpn implements Cloudix's own overlay network: a named, password
// protected group that lets peers find and talk to each other across the
// internet, the way people currently use RadminVPN for.
//
// It is deliberately NOT a system-wide VPN. A real one needs a virtual network
// adapter, which means a signed kernel driver on Windows and a Network
// Extension entitlement on macOS, plus administrator rights on both — none of
// which a self-signed app can ship. What this does instead is carry Cloudix's
// own traffic, which is all Cloudix actually needs: other applications and
// games are not routed through it.
//
// Security model:
//   - The password never leaves the machine. It is stretched with Argon2id into
//     a network key; only a blinded network id derived from that key is ever
//     transmitted.
//   - Knowledge of the network key is proven implicitly: it is mixed into the
//     HKDF that derives every link key, so a peer with the wrong password
//     simply cannot open the first frame.
//   - Every node has a persistent X25519 identity. Links are encrypted with
//     XChaCha20-Poly1305 over an ECDH shared secret.
//   - The host relays traffic between members but cannot read it: member to
//     member payloads are sealed under a separate key derived from those two
//     members' identities.
package vpn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const (
	// Argon2id parameters. Deliberately costly: the network key is the only
	// thing standing between an attacker who can reach the host port and the
	// network, so an offline guess should not be cheap.
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	keyLen       = 32
)

// NormalizeNetworkName makes the name case- and whitespace-insensitive so that
// "Home Net" and "home net" are the same network for everyone.
func NormalizeNetworkName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// DeriveNetworkKey stretches the shared password into the network's root key.
// The name acts as the salt, so the same password in two differently named
// networks yields unrelated keys.
func DeriveNetworkKey(name, password string) []byte {
	salt := blake2b.Sum256([]byte("cloudix-vpn-salt|" + NormalizeNetworkName(name)))
	return argon2.IDKey([]byte(password), salt[:], argonTime, argonMemory, argonThreads, keyLen)
}

// NetworkID is a blinded identifier derived from the network key. It is the
// only network-identifying value that goes on the wire: it reveals neither the
// name nor the password, and cannot be reversed without guessing both.
func NetworkID(networkKey []byte) string {
	h := blake2b.Sum256(append([]byte("cloudix-vpn-id|"), networkKey...))
	return hex.EncodeToString(h[:16])
}

// ---------------------------------------------------------------- identity ---

// Identity is a node's long-lived X25519 keypair.
type Identity struct {
	Private [32]byte
	Public  [32]byte
}

func NewIdentity() (*Identity, error) {
	var id Identity
	if _, err := io.ReadFull(rand.Reader, id.Private[:]); err != nil {
		return nil, fmt.Errorf("generate identity: %w", err)
	}
	pub, err := curve25519.X25519(id.Private[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}
	copy(id.Public[:], pub)
	return &id, nil
}

// IdentityFromSeed restores an identity from its stored private key.
func IdentityFromSeed(seed []byte) (*Identity, error) {
	if len(seed) != 32 {
		return nil, fmt.Errorf("identity seed must be 32 bytes, got %d", len(seed))
	}
	var id Identity
	copy(id.Private[:], seed)
	pub, err := curve25519.X25519(id.Private[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}
	copy(id.Public[:], pub)
	return &id, nil
}

func (i *Identity) PublicHex() string { return hex.EncodeToString(i.Public[:]) }

// Fingerprint is a short, human-comparable form of a public key. Users can read
// it out to each other to confirm nobody is being impersonated by the host.
func Fingerprint(pub []byte) string {
	h := blake2b.Sum256(append([]byte("cloudix-vpn-fp|"), pub...))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:10])
	var parts []string
	for i := 0; i < len(enc); i += 4 {
		end := i + 4
		if end > len(enc) {
			end = len(enc)
		}
		parts = append(parts, enc[i:end])
	}
	return strings.Join(parts, "-")
}

// ------------------------------------------------------------------- keys ---

// linkKey derives the symmetric key for a connection. The network key is folded
// into the HKDF salt, which is what authenticates the peer: without the right
// password the two sides derive different keys and the first sealed frame fails
// to open. Nonces from both sides make every session unique.
func linkKey(shared, networkKey, nonceA, nonceB []byte, info string) ([]byte, error) {
	salt := blake2b.Sum256(append(append([]byte("cloudix-vpn-link|"), networkKey...), append(nonceA, nonceB...)...))
	r := hkdf.New(blake256, shared, salt[:], []byte(info))
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("derive link key: %w", err)
	}
	return key, nil
}

// SharedSecret performs the X25519 exchange.
func SharedSecret(priv [32]byte, peerPub []byte) ([]byte, error) {
	if len(peerPub) != 32 {
		return nil, fmt.Errorf("peer public key must be 32 bytes, got %d", len(peerPub))
	}
	secret, err := curve25519.X25519(priv[:], peerPub)
	if err != nil {
		return nil, fmt.Errorf("x25519: %w", err)
	}
	// An all-zero result means a low-order point was supplied.
	var zero [32]byte
	if subtle.ConstantTimeCompare(secret, zero[:]) == 1 {
		return nil, fmt.Errorf("x25519: degenerate shared secret")
	}
	return secret, nil
}

// E2EKey derives the key two members use to talk through the host. Sorting the
// public keys makes both sides arrive at the same value, and the network key in
// the salt keeps it unreachable for the relaying host, which knows neither
// private key.
func E2EKey(priv [32]byte, peerPub, networkKey []byte) ([]byte, error) {
	shared, err := SharedSecret(priv, peerPub)
	if err != nil {
		return nil, err
	}
	self, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}

	lo, hi := self, peerPub
	if string(lo) > string(hi) {
		lo, hi = hi, lo
	}
	salt := blake2b.Sum256(append(append([]byte("cloudix-vpn-e2e|"), networkKey...), append(lo, hi...)...))

	r := hkdf.New(blake256, shared, salt[:], []byte("cloudix-vpn-e2e"))
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("derive e2e key: %w", err)
	}
	return key, nil
}

// ------------------------------------------------------------------ seals ---

// Seal encrypts with XChaCha20-Poly1305. Its 24-byte nonce is large enough to
// be chosen at random for every message, which removes any need to track
// counters across reconnects.
func Seal(key, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("new aead: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

func Open(key, box []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("new aead: %w", err)
	}
	if len(box) < aead.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ct := box[:aead.NonceSize()], box[aead.NonceSize():]
	out, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return out, nil
}

// blake256 adapts blake2b to the func() hash.Hash that HKDF expects.
func blake256() hash.Hash {
	h, err := blake2b.New256(nil)
	if err != nil {
		panic("blake2b.New256 with nil key cannot fail: " + err.Error())
	}
	return h
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}
