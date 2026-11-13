package handover

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Enrollment (policy.json "enrollment"): the host shows a single-use
// invitation out of band (a QR code); it holds a 256-bit secret, the host's
// key ID and an expiry 300 s ahead. The guest connects with TLS pinned to that
// key ID and proves it knows the secret with an HMAC bound to both keys; the
// host answers with its own HMAC, so each side knows the other saw the same
// invitation. Both may show ConfirmationCode for a visual check.
const (
	EnrollTTL     = 300 * time.Second
	InvitePrefix  = "CFE1."
	maxEnrollSize = 1024
	maxFailures   = 10 // wrong proofs before every open invitation is withdrawn
)

var (
	ErrNoInvitation = errors.New("no valid invitation for this proof")
	hexID           = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Invitation is the out-of-band payload. Endpoints are host:port hints for the
// first connection; they are not secret. Never log an Invitation: String hides
// the secret.
type Invitation struct {
	Version       int      `json:"v"`
	HostID        string   `json:"id"`
	HostRole      Role     `json:"role"`
	Secret        string   `json:"secret"` // 64 lower-hex characters
	ExpiresUnixMs int64    `json:"exp"`
	Endpoints     []string `json:"ep,omitempty"`
}

func (i Invitation) String() string {
	return fmt.Sprintf("invitation from %s %.8s… until %s", i.HostRole, i.HostID, time.UnixMilli(i.ExpiresUnixMs).Format(time.RFC3339))
}

// Encode is the text for the QR code.
func (i Invitation) Encode() string {
	b, _ := json.Marshal(i)
	return InvitePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseInvitation decodes and checks an invitation, including its expiry.
func ParseInvitation(text string, now time.Time) (Invitation, error) {
	var i Invitation
	raw, ok := strings.CutPrefix(strings.TrimSpace(text), InvitePrefix)
	if !ok || len(raw) > 2048 {
		return i, errors.New("not a Chewedfeed invitation")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return i, errors.New("damaged invitation")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&i); err != nil {
		return Invitation{}, errors.New("damaged invitation")
	}
	switch {
	case i.Version != 1:
		return Invitation{}, errors.New("unsupported invitation version")
	case !hexID.MatchString(i.HostID) || !hexID.MatchString(i.Secret):
		return Invitation{}, errors.New("damaged invitation")
	case i.HostRole != Laptop && i.HostRole != Phone:
		return Invitation{}, errors.New("damaged invitation")
	case len(i.Endpoints) > 8:
		return Invitation{}, errors.New("damaged invitation")
	case now.UnixMilli() >= i.ExpiresUnixMs:
		return Invitation{}, errors.New("invitation expired")
	}
	return i, nil
}

func enrollMAC(secret []byte, label string, parts ...string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("chewedfeed-enroll-v1/" + label))
	for _, p := range parts {
		m.Write([]byte("\n" + p))
	}
	return m.Sum(nil)
}

// EnrollProof is the guest's proof that it read the invitation.
func EnrollProof(secret []byte, hostID, guestID string, guestRole Role) []byte {
	return enrollMAC(secret, "request", hostID, guestID, string(guestRole))
}

// EnrollConfirm is the host's answer to a valid proof.
func EnrollConfirm(secret []byte, hostID, guestID string, guestRole Role) []byte {
	return enrollMAC(secret, "confirm", hostID, guestID, string(guestRole))
}

// ConfirmationCode is six digits both sides can show after enrolling.
func ConfirmationCode(secret []byte, hostID, guestID string) string {
	m := enrollMAC(secret, "code", hostID, guestID)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(m[:4])%1000000)
}

type enrollRequest struct {
	Enroll int    `json:"enroll"`
	Role   Role   `json:"role"`
	Proof  string `json:"proof"`
}

type enrollResponse struct {
	Enrolled int    `json:"enrolled"`
	Confirm  string `json:"confirm"`
}

func writeSmallFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...))
	return err
}

func readSmallFrame(r io.Reader, v any) error {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > maxEnrollSize {
		return invalid("enrollment frame length %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	if _, err := strictJSON(b); err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("enrollment message: %v", err)
	}
	return nil
}

// Enroller is the host's side: it issues invitations and admits the guests
// that prove one. Each invitation works once and only until it expires.
type Enroller struct {
	Store *Store
	Now   func() time.Time // nil: time.Now

	mu       sync.Mutex
	open     []openInvite
	failures int
}

type openInvite struct {
	secret  []byte
	expires time.Time
}

func (e *Enroller) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Invite opens a new invitation.
func (e *Enroller) Invite(role Role, endpoints []string) Invitation {
	secret := randomHex(32)
	raw, _ := hex.DecodeString(secret)
	exp := e.now().Add(EnrollTTL)
	e.mu.Lock()
	e.open = append(e.open, openInvite{raw, exp})
	e.mu.Unlock()
	return Invitation{Version: 1, HostID: e.Store.Identity().ID, HostRole: role, Secret: secret,
		ExpiresUnixMs: exp.UnixMilli(), Endpoints: endpoints}
}

// Pending reports whether an unexpired invitation is open.
func (e *Enroller) Pending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expire()
	return len(e.open) > 0
}

func (e *Enroller) expire() {
	now := e.now()
	kept := e.open[:0]
	for _, inv := range e.open {
		if now.Before(inv.expires) {
			kept = append(kept, inv)
		}
	}
	e.open = kept
}

// admit checks a guest's proof against the open invitations, consumes the
// matching one, enrolls the guest and returns the host's confirmation.
func (e *Enroller) admit(guestID string, req enrollRequest) ([]byte, error) {
	proof, err := hex.DecodeString(req.Proof)
	if err != nil || len(proof) != sha256.Size || req.Enroll != 1 || (req.Role != Laptop && req.Role != Phone) {
		return nil, invalid("enrollment request")
	}
	hostID := e.Store.Identity().ID
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expire()
	for i, inv := range e.open {
		if hmac.Equal(proof, EnrollProof(inv.secret, hostID, guestID, req.Role)) {
			e.open = append(e.open[:i], e.open[i+1:]...) // single use
			if err := e.Store.AddPeer(Peer{ID: guestID, Role: req.Role, EnrolledUnixMs: e.now().UnixMilli()}); err != nil {
				return nil, err
			}
			return EnrollConfirm(inv.secret, hostID, guestID, req.Role), nil
		}
	}
	if e.failures++; e.failures >= maxFailures {
		e.open, e.failures = nil, 0 // someone is guessing: withdraw everything
	}
	return nil, ErrNoInvitation
}

// serveEnroll runs on a TLS connection from an unknown key: one request, one answer.
func (e *Enroller) serveEnroll(c *tls.Conn) error {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	guestID := PeerID(c.ConnectionState().PeerCertificates[0])
	var req enrollRequest
	if err := readSmallFrame(c, &req); err != nil {
		return err
	}
	confirm, err := e.admit(guestID, req)
	if err != nil {
		return err
	}
	return writeSmallFrame(c, enrollResponse{Enrolled: 1, Confirm: hex.EncodeToString(confirm)})
}

// Enroll is the guest's side: connect to the invitation's host (dial gives the
// TCP or relay connection), prove the invitation, check the host's answer and
// record the host. It returns the host and the confirmation code to show.
func Enroll(ctx context.Context, store *Store, inv Invitation, guestRole Role, dial func(context.Context) (net.Conn, error)) (Peer, string, error) {
	if _, err := ParseInvitation(inv.Encode(), time.Now()); err != nil {
		return Peer{}, "", err
	}
	secret, _ := hex.DecodeString(inv.Secret)
	raw, err := dial(ctx)
	if err != nil {
		return Peer{}, "", err
	}
	c := tls.Client(raw, store.ClientTLS(inv.HostID))
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	} else {
		c.SetDeadline(time.Now().Add(15 * time.Second))
	}
	if err := c.HandshakeContext(ctx); err != nil {
		return Peer{}, "", fmt.Errorf("enrollment handshake: %w", err)
	}
	guestID := store.Identity().ID
	proof := EnrollProof(secret, inv.HostID, guestID, guestRole)
	if err := writeSmallFrame(c, enrollRequest{Enroll: 1, Role: guestRole, Proof: hex.EncodeToString(proof)}); err != nil {
		return Peer{}, "", err
	}
	var resp enrollResponse
	if err := readSmallFrame(c, &resp); err != nil {
		return Peer{}, "", errors.New("the host refused the invitation (used, expired or withdrawn)")
	}
	got, err := hex.DecodeString(resp.Confirm)
	if err != nil || resp.Enrolled != 1 || !hmac.Equal(got, EnrollConfirm(secret, inv.HostID, guestID, guestRole)) {
		return Peer{}, "", errors.New("the host's confirmation does not match the invitation")
	}
	host := Peer{ID: inv.HostID, Role: inv.HostRole, EnrolledUnixMs: time.Now().UnixMilli()}
	if err := store.AddPeer(host); err != nil {
		return Peer{}, "", err
	}
	return host, ConfirmationCode(secret, inv.HostID, guestID), nil
}
