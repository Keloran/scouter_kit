package handover

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"slices"
	"sync"
	"time"
)

// Transport (policy.json "timers_ms", "ordering"): one authenticated TLS
// connection per peer at a time. Each connection starts with both hellos and
// has its own fresh session nonce and watermarks; a reconnect never replays
// anything, the caller reconciles from the peer's new status instead.
const (
	HeartbeatInterval = 3000 * time.Millisecond
	PeerSilence       = PeerSuspectMs * time.Millisecond // no frame for this long: the connection is dead
	SendQueue         = 32
	writeTimeout      = 5 * time.Second
	helloTimeout      = 10 * time.Second
)

var (
	ErrBackpressure = errors.New("peer send queue full")
	ErrClosed       = errors.New("peer connection closed")
	ErrNotEnrolled  = errors.New("peer key is not enrolled")
	ErrEnrolled     = errors.New("connection used for enrollment") // Accept: a guest enrolled; no protocol connection
)

// Route reaches a peer: a direct endpoint, or a relay (CF-54). The connection
// it returns is untrusted; TLS with pinned keys runs over it.
type Route interface {
	Dial(ctx context.Context) (net.Conn, error)
}

// DirectRoute is a TCP endpoint, host:port.
type DirectRoute struct{ Addr string }

func (r DirectRoute) Dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "tcp", r.Addr)
}

// Options for one side of a connection.
type Options struct {
	Role      Role
	Optional  []string             // optional capabilities offered; nil: all known
	Heartbeat func() HeartbeatBody // what heartbeats carry; nil: idle
	Retired   []string             // the peer's sessions from earlier connections, refused if reused
}

// Conn is one authenticated, handshaken peer connection.
type Conn struct {
	PeerID      string
	PeerRole    Role
	Session     string // ours
	PeerSession string // theirs

	tc     *tls.Conn
	store  *Store
	stream *Stream
	out    *Outbox
	opts   Options

	sendMu sync.Mutex
	queue  chan Message
	recv   chan Message

	done      chan struct{}
	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
}

// Dial connects to an enrolled peer through route.
func Dial(ctx context.Context, store *Store, route Route, peerID string, opts Options) (*Conn, error) {
	if _, ok := store.Enrolled(peerID); !ok {
		return nil, ErrNotEnrolled
	}
	raw, err := route.Dial(ctx)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(raw, store.ClientTLS(peerID))
	if err := handshake(ctx, tc); err != nil {
		return nil, err
	}
	return startConn(ctx, tc, store, peerID, opts)
}

// Accept runs the server side on a new connection. An enrolled key gets a
// protocol connection. An unknown key may only enroll, while enroller has an
// open invitation (ErrEnrolled on success); otherwise it is refused. Either
// way an unknown key never sees a protocol message.
func Accept(ctx context.Context, raw net.Conn, store *Store, enroller *Enroller, opts Options) (*Conn, error) {
	tc := tls.Server(raw, store.ServerTLS())
	if err := handshake(ctx, tc); err != nil {
		return nil, err
	}
	peerID := PeerID(tc.ConnectionState().PeerCertificates[0])
	if _, ok := store.Enrolled(peerID); ok {
		return startConn(ctx, tc, store, peerID, opts)
	}
	defer tc.Close()
	if enroller == nil || !enroller.Pending() {
		return nil, ErrNotEnrolled
	}
	if err := enroller.serveEnroll(tc); err != nil {
		return nil, fmt.Errorf("enrollment refused: %w", err)
	}
	return nil, ErrEnrolled
}

func handshake(ctx context.Context, tc *tls.Conn) error {
	tc.SetDeadline(time.Now().Add(helloTimeout))
	if err := tc.HandshakeContext(ctx); err != nil {
		tc.Close()
		return fmt.Errorf("TLS: %w", err)
	}
	return nil
}

// startConn exchanges hellos, then runs the reader, writer and heartbeat.
func startConn(ctx context.Context, tc *tls.Conn, store *Store, peerID string, opts Options) (*Conn, error) {
	ourID := store.Identity().ID
	c := &Conn{PeerID: peerID, Session: NewNonce(), tc: tc, store: store, opts: opts,
		queue: make(chan Message, SendQueue), recv: make(chan Message, SendQueue), done: make(chan struct{})}
	optional := opts.Optional
	if optional == nil {
		optional = OptionalCapabilities
	}
	// What we advertise is all we accept: an optional payload needs both hellos.
	c.stream = NewStream(peerID, ourID, c.Session, opts.Retired, "", append(slices.Clone(RequiredCapabilities), optional...))
	c.out = &Outbox{Sender: ourID, Recipient: peerID, Session: c.Session}
	hello, err := c.out.Next("hello", 0, "", HelloBody{Role: opts.Role, RequiredCapabilities: RequiredCapabilities, OptionalCapabilities: optional})
	if err != nil {
		tc.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { tc.SetDeadline(time.Now()) })
	defer stop()
	tc.SetDeadline(time.Now().Add(helloTimeout))
	errc := make(chan error, 1)
	go func() { errc <- WriteFrame(tc, hello) }()
	m, rerr := ReadFrame(tc)
	if rerr == nil {
		rerr = c.stream.Receive(m)
	}
	if werr := <-errc; rerr == nil {
		rerr = werr
	}
	if rerr == nil && c.stream.Role() == opts.Role {
		rerr = invalid("peer claims this host's role")
	}
	if rerr != nil {
		tc.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("hello: %w", rerr)
	}
	tc.SetDeadline(time.Time{})
	c.PeerRole, c.PeerSession = c.stream.Role(), m.Session
	c.out.PeerSession = m.Session
	go c.reader()
	go c.writer()
	go c.heartbeat()
	return c, nil
}

// Recv delivers the peer's messages after hello, already checked. It is
// closed when the connection ends.
func (c *Conn) Recv() <-chan Message { return c.recv }

// Done is closed when the connection ends; Err says why.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Negotiated: both hellos listed this optional capability.
func (c *Conn) Negotiated(capability string) bool { return c.stream.Negotiated(capability) }

func (c *Conn) Close() { c.fail(ErrClosed) }

func (c *Conn) fail(err error) {
	c.closeOnce.Do(func() {
		c.errMu.Lock()
		c.err = err
		c.errMu.Unlock()
		close(c.done)
		c.tc.Close()
	})
}

// Send queues one message. It never blocks: a full queue means the peer is
// not keeping up (ErrBackpressure); a message this host may not send fails
// the same checks the peer would apply.
func (c *Conn) Send(kind string, epoch uint64, transfer string, body any) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	if len(c.queue) == cap(c.queue) {
		return ErrBackpressure
	}
	m, err := c.out.Next(kind, epoch, transfer, body)
	if err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err == nil {
		_, err = DecodeMessage(b) // the checks the peer applies
	}
	if err != nil {
		return err
	}
	c.queue <- m // room checked above, under sendMu
	return nil
}

// Expect records a transfer this host requested, so the peer's release and
// released for it are accepted.
func (c *Conn) Expect(transfer string) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.stream.Expect(transfer)
}

func (c *Conn) reader() {
	defer close(c.recv)
	for {
		c.tc.SetReadDeadline(time.Now().Add(PeerSilence))
		m, err := ReadFrame(c.tc)
		if err != nil {
			c.fail(fmt.Errorf("read: %w", err))
			return
		}
		if _, ok := c.store.Enrolled(c.PeerID); !ok {
			c.fail(ErrNotEnrolled) // revoked while connected
			return
		}
		c.sendMu.Lock()
		err = c.stream.Receive(m)
		c.sendMu.Unlock()
		if err != nil {
			c.fail(fmt.Errorf("protocol: %w", err)) // fail closed: no partial trust
			return
		}
		select {
		case c.recv <- m:
		case <-c.done:
			return
		}
	}
}

func (c *Conn) writer() {
	for {
		select {
		case <-c.done:
			return
		case m := <-c.queue:
			c.tc.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := WriteFrame(c.tc, m); err != nil {
				c.fail(fmt.Errorf("write: %w", err))
				return
			}
		}
	}
}

func (c *Conn) heartbeat() {
	t := time.NewTicker(HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			body := HeartbeatBody{Phase: Idle}
			if c.opts.Heartbeat != nil {
				body = c.opts.Heartbeat()
			}
			// ready_epoch may not run ahead of the envelope epoch.
			if err := c.Send("heartbeat", body.ReadyEpoch, "", body); errors.Is(err, ErrClosed) {
				return
			}
		}
	}
}

// Link keeps an enrolled peer connected through a route: it dials, hands each
// connection to onConn, waits for it to end and dials again after a backoff
// (RetryDelayMs with jitter, reset after a connection that lasted). Each
// connection is a new session; the peer's earlier sessions are refused.
func Link(ctx context.Context, store *Store, route Route, peerID string, opts Options, onConn func(*Conn)) error {
	failures := 0
	retired := slices.Clone(opts.Retired)
	for {
		o := opts
		o.Retired = retired
		c, err := Dial(ctx, store, route, peerID, o)
		if errors.Is(err, ErrNotEnrolled) {
			return err // revoked: stop
		}
		if err == nil {
			began := time.Now()
			onConn(c)
			select {
			case <-c.Done():
			case <-ctx.Done():
				c.Close()
			}
			retired = append(retired, c.PeerSession)
			if time.Since(began) > PeerSilence {
				failures = 0
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := time.Duration(RetryDelayMs(failures, rand.Float64())) * time.Millisecond
		failures++
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
