package handover

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"
)

// Identity is a host's TLS key pair. Its ID (policy.json "peer_id") is the
// lower-hex SHA-256 of the certificate's SubjectPublicKeyInfo; peers pin it.
type Identity struct {
	Cert tls.Certificate
	ID   string
}

// PeerID of a certificate.
func PeerID(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// NewIdentity makes a P-256 key and a self-signed certificate. Nothing checks
// the certificate's names or dates: the key is what peers pin.
func NewIdentity() (*Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "chewedfeed peer"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(30, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return identityFrom(der, key)
}

func identityFrom(der []byte, key *ecdsa.PrivateKey) (*Identity, error) {
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Identity{Cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, ID: PeerID(leaf)}, nil
}

// Peer is an enrolled peer: its pinned ID and role, nothing else.
type Peer struct {
	ID             string `json:"id"`
	Role           Role   `json:"role"`
	EnrolledUnixMs int64  `json:"enrolled_unix_ms"`
}

// Store keeps this host's identity and its enrolled peers in a private
// directory (0700; files 0600, written atomically). Revoking a peer removes
// it: its key is refused from the next handshake on. Rotating replaces this
// host's key: every peer must enroll again.
type Store struct {
	dir   string
	mu    sync.RWMutex
	id    *Identity
	peers []Peer
}

const (
	identityFile = "identity.pem"
	peersFile    = "peers.json"
)

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return nil, fmt.Errorf("%s is not a directory owned by this user", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	if s.id, err = s.loadIdentity(); errors.Is(err, os.ErrNotExist) {
		if s.id, err = NewIdentity(); err == nil {
			err = s.saveIdentity()
		}
	}
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, peersFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var f struct{ Peers []Peer }
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", peersFile, err)
		}
		s.peers = f.Peers
	}
	return s, nil
}

func (s *Store) loadIdentity() (*Identity, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, identityFile))
	if err != nil {
		return nil, err
	}
	var der []byte
	var key *ecdsa.PrivateKey
	for blk, rest := pem.Decode(b); blk != nil; blk, rest = pem.Decode(rest) {
		switch blk.Type {
		case "CERTIFICATE":
			der = blk.Bytes
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
			if err != nil {
				return nil, err
			}
			var ok bool
			if key, ok = k.(*ecdsa.PrivateKey); !ok {
				return nil, errors.New("identity key is not ECDSA")
			}
		}
	}
	if der == nil || key == nil {
		return nil, errors.New("identity file incomplete")
	}
	id, err := identityFrom(der, key)
	if err != nil {
		return nil, err
	}
	if !key.PublicKey.Equal(id.Cert.Leaf.PublicKey) {
		return nil, errors.New("identity key and certificate differ")
	}
	return id, nil
}

func (s *Store) saveIdentity() error {
	key, err := x509.MarshalPKCS8PrivateKey(s.id.Cert.PrivateKey)
	if err != nil {
		return err
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.id.Cert.Certificate[0]})...)
	return writePrivate(filepath.Join(s.dir, identityFile), b)
}

func (s *Store) savePeers() error {
	b, err := json.MarshalIndent(struct {
		Peers []Peer `json:"peers"`
	}{s.peers}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(s.dir, peersFile), b)
}

func writePrivate(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Store) Identity() *Identity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

func (s *Store) Peers() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.peers)
}

// Enrolled looks up a peer by its pinned ID.
func (s *Store) Enrolled(id string) (Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := slices.IndexFunc(s.peers, func(p Peer) bool { return p.ID == id })
	if i < 0 {
		return Peer{}, false
	}
	return s.peers[i], true
}

// AddPeer records (or replaces) an enrolled peer.
func (s *Store) AddPeer(p Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == s.id.ID {
		return errors.New("cannot enroll this host's own key")
	}
	s.peers = slices.DeleteFunc(s.peers, func(q Peer) bool { return q.ID == p.ID })
	s.peers = append(s.peers, p)
	return s.savePeers()
}

// Revoke forgets a peer; its key no longer authenticates.
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.peers)
	s.peers = slices.DeleteFunc(s.peers, func(q Peer) bool { return q.ID == id })
	if len(s.peers) == n {
		return fmt.Errorf("no enrolled peer %.8s…", id)
	}
	return s.savePeers()
}

// Rotate replaces this host's key pair and forgets every peer (they pinned
// the old key and must enroll again).
func (s *Store) Rotate() error {
	id, err := NewIdentity()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, oldPeers := s.id, s.peers
	s.id, s.peers = id, nil
	if err := s.saveIdentity(); err != nil {
		s.id, s.peers = old, oldPeers
		return err
	}
	return s.savePeers()
}

// tlsBase: TLS 1.3 only, certificates on both sides, no CA: each side pins
// the other's key in VerifyConnection.
func (s *Store) tlsBase() *tls.Config {
	id := s.Identity()
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{id.Cert},
		InsecureSkipVerify: true, // no CA chain: VerifyConnection pins the key instead
	}
}

// ServerTLS accepts any client key in the handshake; Accept then admits only
// enrolled ones to the protocol (and the rest to enrollment, when it is open).
func (s *Store) ServerTLS() *tls.Config {
	c := s.tlsBase()
	c.ClientAuth = tls.RequireAnyClientCert
	c.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) != 1 {
			return errors.New("exactly one peer certificate expected")
		}
		return nil
	}
	return c
}

// ClientTLS connects only to the peer whose key has the ID peerID.
func (s *Store) ClientTLS(peerID string) *tls.Config {
	c := s.tlsBase()
	c.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) != 1 || PeerID(cs.PeerCertificates[0]) != peerID {
			return errors.New("peer key does not match the enrolled ID")
		}
		return nil
	}
	return c
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b)
}

// NewNonce is a fresh 128-bit lower-hex ID for a session or a transfer.
func NewNonce() string { return randomHex(16) }
