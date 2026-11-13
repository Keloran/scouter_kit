package handover

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentVectors(t *testing.T) {
	var v struct {
		SecretHex          string     `json:"secret_hex"`
		HostID             string     `json:"host_id"`
		GuestID            string     `json:"guest_id"`
		GuestRole          Role       `json:"guest_role"`
		ProofHex           string     `json:"proof_hex"`
		ConfirmHex         string     `json:"confirm_hex"`
		Code               string     `json:"code"`
		Invitation         Invitation `json:"invitation"`
		InvitationText     string     `json:"invitation_text"`
		InvalidInvitations []struct{ Name, Text string }
	}
	readFixture(t, "enrollment.json", &v)
	secret, _ := hex.DecodeString(v.SecretHex)
	if got := hex.EncodeToString(EnrollProof(secret, v.HostID, v.GuestID, v.GuestRole)); got != v.ProofHex {
		t.Errorf("proof %s", got)
	}
	if got := hex.EncodeToString(EnrollConfirm(secret, v.HostID, v.GuestID, v.GuestRole)); got != v.ConfirmHex {
		t.Errorf("confirm %s", got)
	}
	if got := ConfirmationCode(secret, v.HostID, v.GuestID); got != v.Code {
		t.Errorf("code %s", got)
	}
	if got := v.Invitation.Encode(); got != v.InvitationText {
		t.Errorf("invitation text\n got  %s\n want %s", got, v.InvitationText)
	}
	before := time.UnixMilli(v.Invitation.ExpiresUnixMs - 1)
	inv, err := ParseInvitation(v.InvitationText, before)
	if err != nil || inv.Secret != v.SecretHex || inv.HostID != v.HostID {
		t.Fatalf("parse: %v %+v", err, inv)
	}
	if _, err := ParseInvitation(v.InvitationText, time.UnixMilli(v.Invitation.ExpiresUnixMs)); err == nil {
		t.Error("expired invitation accepted")
	}
	for _, c := range v.InvalidInvitations {
		if _, err := ParseInvitation(c.Text, before); err == nil {
			t.Errorf("%s accepted", c.Name)
		}
	}
	if strings.Contains(inv.String(), inv.Secret) {
		t.Error("String shows the secret")
	}
}

func TestStorePermissionsRevokeRotate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handover")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddPeer(Peer{ID: strings.Repeat("b", 64), Role: Phone}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, identityFile): 0o600, filepath.Join(dir, peersFile): 0o600} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v", path, fi.Mode(), err)
		}
	}
	again, err := OpenStore(dir)
	if err != nil || again.Identity().ID != s.Identity().ID || len(again.Peers()) != 1 {
		t.Fatalf("reload: %v", err)
	}
	if err := s.AddPeer(Peer{ID: s.Identity().ID, Role: Phone}); err == nil {
		t.Error("enrolled its own key")
	}
	if err := s.Revoke(strings.Repeat("b", 64)); err != nil || len(s.Peers()) != 0 {
		t.Fatalf("revoke: %v", err)
	}
	if err := s.Revoke(strings.Repeat("b", 64)); err == nil {
		t.Error("revoked twice")
	}
	old := s.Identity().ID
	s.AddPeer(Peer{ID: strings.Repeat("c", 64), Role: Phone})
	if err := s.Rotate(); err != nil || s.Identity().ID == old || len(s.Peers()) != 0 {
		t.Fatalf("rotate: %v", err)
	}
	if again, _ := OpenStore(dir); again.Identity().ID != s.Identity().ID {
		t.Fatal("rotated identity not saved")
	}
}

// pair is a laptop host serving on loopback and a phone guest.
type pair struct {
	laptop, phone *Store
	enroller      *Enroller
	addr          string
	accepted      chan *Conn
	acceptErrs    chan error
}

func newPair(t *testing.T) *pair {
	t.Helper()
	laptop, err := OpenStore(filepath.Join(t.TempDir(), "laptop"))
	if err != nil {
		t.Fatal(err)
	}
	phone, err := OpenStore(filepath.Join(t.TempDir(), "phone"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &pair{laptop: laptop, phone: phone, enroller: &Enroller{Store: laptop}, addr: ln.Addr().String(),
		accepted: make(chan *Conn, 8), acceptErrs: make(chan error, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ln.Close() })
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				c, err := Accept(ctx, raw, laptop, p.enroller, Options{Role: Laptop})
				if err != nil {
					p.acceptErrs <- err
					return
				}
				p.accepted <- c
			}()
		}
	}()
	return p
}

func (p *pair) enroll(t *testing.T) {
	t.Helper()
	inv := p.enroller.Invite(Laptop, []string{p.addr})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host, code, err := Enroll(ctx, p.phone, inv, Phone, DirectRoute{p.addr}.Dial)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := hex.DecodeString(inv.Secret)
	if host.ID != p.laptop.Identity().ID || code != ConfirmationCode(secret, host.ID, p.phone.Identity().ID) {
		t.Fatalf("enrolled %+v code %s", host, code)
	}
	if peer, ok := p.laptop.Enrolled(p.phone.Identity().ID); !ok || peer.Role != Phone {
		t.Fatal("laptop did not record the phone")
	}
	<-p.acceptErrs // ErrEnrolled for the enrollment connection
}

func (p *pair) dial(t *testing.T, opts Options) (*Conn, *Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opts.Role = Phone
	c, err := Dial(ctx, p.phone, DirectRoute{p.addr}, p.laptop.Identity().ID, opts)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-p.accepted:
		t.Cleanup(func() { c.Close(); s.Close() })
		return c, s
	case err := <-p.acceptErrs:
		t.Fatalf("accept: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no accept")
	}
	return nil, nil
}

func waitDone(t *testing.T, c *Conn) error {
	t.Helper()
	select {
	case <-c.Done():
		return c.Err()
	case <-time.After(10 * time.Second):
		t.Fatal("connection still open")
	}
	return nil
}

func TestEnrollConnectExchange(t *testing.T) {
	p := newPair(t)
	p.enroll(t)
	c, s := p.dial(t, Options{})
	if c.PeerRole != Laptop || s.PeerRole != Phone || c.Session == s.Session || c.PeerSession != s.Session {
		t.Fatalf("hello: %+v / %+v", c, s)
	}
	if !c.Negotiated(CapSnapshot) || !s.Negotiated(CapTelemetry) {
		t.Fatal("optional capabilities not negotiated")
	}
	tr := NewNonce()
	body := StatusBody{Role: Phone, Phase: Active, Eligible: true, Arms: Arms{true, true}, RendererReady: true, Transfer: &tr, ReadyEpoch: 1}
	if err := c.Send("status", 1, "", body); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-s.Recv():
		if m.Kind != "status" || m.Sender != p.phone.Identity().ID {
			t.Fatalf("got %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no status")
	}
	// A message this host may not send is refused before it leaves.
	if err := c.Send("status", 0, "", StatusBody{Role: Phone, Phase: Active}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid status sent: %v", err)
	}
}

func TestEnrollmentReuseExpiryAndGuessing(t *testing.T) {
	p := newPair(t)
	inv := p.enroller.Invite(Laptop, nil)
	ctx := context.Background()
	if _, _, err := Enroll(ctx, p.phone, inv, Phone, DirectRoute{p.addr}.Dial); err != nil {
		t.Fatal(err)
	}
	<-p.acceptErrs
	// Used once: a second guest with the same invitation is refused.
	other, _ := OpenStore(filepath.Join(t.TempDir(), "other"))
	if _, _, err := Enroll(ctx, other, inv, Phone, DirectRoute{p.addr}.Dial); err == nil {
		t.Fatal("invitation reused")
	}
	<-p.acceptErrs
	if _, ok := p.laptop.Enrolled(other.Identity().ID); ok {
		t.Fatal("reused invitation enrolled a peer")
	}
	// Expired: the host's clock moves past it.
	now := time.Now()
	p.enroller.Now = func() time.Time { return now }
	late := p.enroller.Invite(Laptop, nil)
	p.enroller.Now = func() time.Time { return now.Add(EnrollTTL) }
	if _, _, err := Enroll(ctx, other, late, Phone, DirectRoute{p.addr}.Dial); err == nil {
		t.Fatal("expired invitation accepted")
	}
	<-p.acceptErrs
	// Wrong secrets withdraw the open invitations after maxFailures.
	p.enroller.Now = nil
	open := p.enroller.Invite(Laptop, nil)
	wrong := open
	wrong.Secret = strings.Repeat("0", 64)
	for i := 0; i < maxFailures; i++ {
		Enroll(ctx, other, wrong, Phone, DirectRoute{p.addr}.Dial)
		<-p.acceptErrs
	}
	if _, _, err := Enroll(ctx, other, open, Phone, DirectRoute{p.addr}.Dial); err == nil {
		t.Fatal("invitation still open after guessing")
	}
}

func TestEnrollmentPinsHostKey(t *testing.T) {
	p := newPair(t)
	inv := p.enroller.Invite(Laptop, nil)
	inv.HostID = strings.Repeat("e", 64) // a different host than the one answering
	if _, _, err := Enroll(context.Background(), p.phone, inv, Phone, DirectRoute{p.addr}.Dial); err == nil || !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("connected to the wrong key: %v", err)
	}
	if len(p.phone.Peers()) != 0 {
		t.Fatal("phone recorded a host")
	}
}

// An unknown key gets no protocol data: with no invitation open it is cut
// off; with one open, a protocol hello instead of an enrollment is refused.
func TestUnauthenticatedPeerGetsNothing(t *testing.T) {
	p := newPair(t)
	stranger, _ := OpenStore(filepath.Join(t.TempDir(), "stranger"))
	stranger.AddPeer(Peer{ID: p.laptop.Identity().ID, Role: Laptop}) // it knows the laptop's key, not vice versa
	for _, open := range []bool{false, true} {
		if open {
			p.enroller.Invite(Laptop, nil)
		}
		_, err := Dial(context.Background(), stranger, DirectRoute{p.addr}, p.laptop.Identity().ID, Options{Role: Phone})
		if err == nil {
			t.Fatalf("stranger connected (invitation open: %t)", open)
		}
		if aerr := <-p.acceptErrs; !errors.Is(aerr, ErrNotEnrolled) && !strings.Contains(aerr.Error(), "enrollment refused") {
			t.Fatalf("accept: %v", aerr)
		}
	}
	if len(p.laptop.Peers()) != 0 {
		t.Fatal("stranger enrolled")
	}
}

func TestWrongPeerAndTLSVersion(t *testing.T) {
	p := newPair(t)
	p.enroll(t)
	// Pinned to another ID: refused before any data.
	p.phone.AddPeer(Peer{ID: strings.Repeat("d", 64), Role: Laptop})
	if _, err := Dial(context.Background(), p.phone, DirectRoute{p.addr}, strings.Repeat("d", 64), Options{Role: Phone}); err == nil {
		t.Fatal("connected to a key with the wrong ID")
	}
	<-p.acceptErrs
	// TLS 1.2 is refused.
	raw, err := net.Dial("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := p.phone.ClientTLS(p.laptop.Identity().ID)
	cfg.MinVersion, cfg.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if err := tls.Client(raw, cfg).Handshake(); err == nil {
		t.Fatal("TLS 1.2 accepted")
	}
	<-p.acceptErrs
}

// rawConn is the phone's TLS connection without the Conn machinery, for
// sending frames the library would never send.
func rawConn(t *testing.T, p *pair) (*tls.Conn, *Outbox) {
	t.Helper()
	raw, err := net.Dial("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(raw, p.phone.ClientTLS(p.laptop.Identity().ID))
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tc.Close() })
	return tc, &Outbox{Sender: p.phone.Identity().ID, Recipient: p.laptop.Identity().ID, Session: NewNonce()}
}

func helloFrom(o *Outbox, role Role, required []string) Message {
	m, _ := o.Next("hello", 0, "", HelloBody{Role: role, RequiredCapabilities: required, OptionalCapabilities: []string{}})
	return m
}

func TestReplayWrongSenderAndProtocolErrorsFailClosed(t *testing.T) {
	p := newPair(t)
	p.enroll(t)
	for name, attack := range map[string]func(tc *tls.Conn, o *Outbox, peerSession string){
		"replayed status": func(tc *tls.Conn, o *Outbox, ps string) {
			o.PeerSession = ps
			m, _ := o.Next("status", 0, "", StatusBody{Role: Phone, Phase: Idle})
			WriteFrame(tc, m)
			WriteFrame(tc, m)
		},
		"wrong sender": func(tc *tls.Conn, o *Outbox, ps string) {
			o.PeerSession = ps
			m, _ := o.Next("status", 0, "", StatusBody{Role: Phone, Phase: Idle})
			m.Sender = strings.Repeat("f", 64)
			WriteFrame(tc, m)
		},
		"unsolicited release": func(tc *tls.Conn, o *Outbox, ps string) {
			o.PeerSession = ps
			m, _ := o.Next("release", 1, NewNonce(), ReleaseBody{Target: Laptop, Reason: "preference"})
			WriteFrame(tc, m)
		},
		"oversized frame": func(tc *tls.Conn, o *Outbox, ps string) {
			tc.Write(binary.BigEndian.AppendUint32(nil, MaxFrameBytes+1))
		},
		"disconnect mid-frame": func(tc *tls.Conn, o *Outbox, ps string) {
			tc.Write(append(binary.BigEndian.AppendUint32(nil, 200), []byte(`{"version":1,`)...))
			tc.Close()
		},
	} {
		tc, o := rawConn(t, p)
		WriteFrame(tc, helloFrom(o, Phone, RequiredCapabilities))
		tc.SetReadDeadline(time.Now().Add(5 * time.Second))
		theirs, err := ReadFrame(tc)
		if err != nil {
			t.Fatalf("%s: laptop hello: %v", name, err)
		}
		var s *Conn
		select {
		case s = <-p.accepted:
		case err := <-p.acceptErrs:
			t.Fatalf("%s: accept: %v", name, err)
		}
		attack(tc, o, theirs.Session)
		err = waitDone(t, s)
		for m := range s.Recv() {
			if m.Kind != "status" || name != "replayed status" {
				t.Errorf("%s: delivered %s", name, m.Kind)
			}
		}
		if err == nil || errors.Is(err, ErrClosed) {
			t.Errorf("%s: closed without a reason: %v", name, err)
		}
	}
}

func TestUnsupportedProtocolRefused(t *testing.T) {
	p := newPair(t)
	p.enroll(t)
	for name, hello := range map[string]func(o *Outbox) Message{
		"version 2": func(o *Outbox) Message { m := helloFrom(o, Phone, RequiredCapabilities); m.Version = 2; return m },
		"missing required capability": func(o *Outbox) Message {
			return helloFrom(o, Phone, []string{CapTwoArm})
		},
		"unknown required capability": func(o *Outbox) Message {
			return helloFrom(o, Phone, append(RequiredCapabilities, "teleport_v1"))
		},
		"same role": func(o *Outbox) Message { return helloFrom(o, Laptop, RequiredCapabilities) },
	} {
		tc, o := rawConn(t, p)
		m := hello(o)
		b, _ := json.Marshal(m)
		tc.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...))
		select {
		case <-p.accepted:
			t.Errorf("%s: accepted", name)
		case err := <-p.acceptErrs:
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: no answer", name)
		}
	}
}

func TestDowngradeKeepsOptionalOff(t *testing.T) {
	p := newPair(t)
	p.enroll(t)
	c, s := p.dial(t, Options{Optional: []string{}})
	if c.Negotiated(CapSnapshot) || s.Negotiated(CapSnapshot) || s.Negotiated(CapTelemetry) {
		t.Fatal("optional capability used without both hellos")
	}
}

func TestRevokedWhileConnected(t *testing.T) {
	p := newPair(t)
	p.enroll(t)
	c, s := p.dial(t, Options{})
	if err := p.laptop.Revoke(p.phone.Identity().ID); err != nil {
		t.Fatal(err)
	}
	c.Send("heartbeat", 0, "", HeartbeatBody{Phase: Idle})
	if err := waitDone(t, s); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("revoked peer: %v", err)
	}
	if _, err := Dial(context.Background(), p.phone, DirectRoute{p.addr}, p.laptop.Identity().ID, Options{Role: Phone}); err == nil {
		t.Fatal("revoked peer connected again")
	}
	if err := <-p.acceptErrs; !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("accept after revoke: %v", err)
	}
}

func TestBackpressure(t *testing.T) {
	p := newPair(t)
	p.enroll(t)
	c, _ := p.dial(t, Options{}) // the laptop never reads Recv
	for i := 0; ; i++ {
		err := c.Send("heartbeat", 0, "", HeartbeatBody{Phase: Idle})
		if errors.Is(err, ErrBackpressure) {
			return
		}
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if i > 200000 {
			t.Fatal("no backpressure")
		}
	}
}

func TestLinkReconnectsWithFreshSession(t *testing.T) {
	p := newPair(t)
	p.enroll(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := make(chan [2]string, 4)
	done := make(chan error, 1)
	go func() {
		done <- Link(ctx, p.phone, DirectRoute{p.addr}, p.laptop.Identity().ID, Options{Role: Phone}, func(c *Conn) {
			sessions <- [2]string{c.Session, c.PeerSession}
		})
	}()
	first := <-sessions
	s1 := <-p.accepted
	s1.Close() // the laptop drops the connection
	var second [2]string
	select {
	case second = <-sessions:
	case <-time.After(15 * time.Second):
		t.Fatal("no reconnect")
	}
	s2 := <-p.accepted
	if second[0] == first[0] || second[1] == first[1] || s2.PeerSession != second[0] {
		t.Fatalf("reconnect reused a session: %v %v", first, second)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("link: %v", err)
	}
}
