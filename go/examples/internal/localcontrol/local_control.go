//go:build linux

// Package localcontrol is Chewedfeed's private control channel: a Unix-domain
// socket in the user's runtime directory that answers status, release, resume
// and pin requests from the same user only. Each connection carries one JSON
// request line and gets one JSON response line. Nothing listens on TCP.
package localcontrol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Commands.
const (
	CmdStatus  = "status"
	CmdRelease = "release" // let the glasses go and stay released until resume
	CmdResume  = "resume"
	CmdPin     = "pin" // Request.Pin: prefer this host until unpinned (phone pin, per the contract)
)

// Results. Pending: the transition was started but had not finished when the
// request's time ran out; it goes on, and status shows where it got to.
const (
	ResultDone    = "done"
	ResultPending = "pending"
	ResultRefused = "refused" // Response.Error says why; nothing changed
	ResultError   = "error"
)

const (
	maxRequest     = 1024
	ioTimeout      = 2 * time.Second
	CommandTimeout = 10 * time.Second // a release joins workers and deinitializes (up to its 5 s deadline)
)

type Request struct {
	Command string `json:"command"`
	Pin     bool   `json:"pin,omitempty"`
}

// Status is what a local client may see: no addresses, keys, peer IDs or tokens.
type Status struct {
	Role     string `json:"role"`      // "laptop"
	Owner    string `json:"owner"`     // "this", "peer", "none" or "unknown"
	Phase    string `json:"phase"`     // the ownership phase (handover contract)
	Eligible bool   `json:"eligible"`  // this host may own the glasses now
	Released bool   `json:"released"`  // released by request; stays so until resume
	Pinned   bool   `json:"pinned"`    // pin preference
	Reason   string `json:"reason"`    // why the last transition happened, in words
	Left     bool   `json:"left_arm"`  // connected and ready
	Right    bool   `json:"right_arm"` // connected and ready
}

type Response struct {
	Result string  `json:"result"`
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
}

// Controller is the dashboard side. Release, Resume and Pin start the change
// and wait for it until ctx ends: done=true only once it really happened (for
// a release: workers joined and both arms down); done=false with a nil error
// means it is still in progress. An error means it was refused.
type Controller interface {
	Status() Status
	Release(ctx context.Context) (done bool, err error)
	Resume(ctx context.Context) (done bool, err error)
	Pin(ctx context.Context, on bool) (done bool, err error)
}

// DefaultPath is $XDG_RUNTIME_DIR/chewedfeed/control.sock, or, without a
// runtime directory, chewedfeed-UID/control.sock in the temporary directory.
func DefaultPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, "chewedfeed", "control.sock")
	}
	return filepath.Join(os.TempDir(), "chewedfeed-"+strconv.Itoa(os.Getuid()), "control.sock")
}

// Server owns the socket, and a lock file next to it for as long as it runs:
// a second instance fails to take the lock and never touches the socket.
type Server struct {
	path    string
	ln      *net.UnixListener
	lock    *os.File
	ctrl    Controller
	timeout time.Duration
	wg      sync.WaitGroup
	once    sync.Once
	closed  chan struct{}
}

// ErrRunning: another process holds the socket.
var ErrRunning = errors.New("another chewedfeed is already running")

// Listen creates the private directory (0700, owned by this user), takes the
// instance lock, removes a stale socket left by a dead process, and listens.
func Listen(path string, ctrl Controller) (*Server, error) {
	dir := filepath.Dir(path)
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (control socket %s)", ErrRunning, path)
		}
		return nil, err
	}
	// The lock is ours, so a socket here belongs to a process that is gone.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		lock.Close()
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		lock.Close()
		return nil, err
	}
	// The directory already keeps others out; the socket's own mode says the same.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		lock.Close()
		return nil, err
	}
	ln.SetUnlinkOnClose(false) // Close removes it while still holding the lock
	return &Server{path: path, ln: ln, lock: lock, ctrl: ctrl, timeout: CommandTimeout, closed: make(chan struct{})}, nil
}

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("control directory %s is not a directory owned by this user", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("control directory %s is open to other users: %w", dir, err)
		}
	}
	return nil
}

func (s *Server) Path() string { return s.path }

// Serve answers requests until Close. Requests are handled concurrently; the
// controller serializes the changes.
func (s *Server) Serve() error {
	for {
		c, err := s.ln.AcceptUnix()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

// Close stops accepting, waits for the requests in progress, removes the
// socket and releases the lock. Safe to call more than once.
func (s *Server) Close() error {
	s.once.Do(func() {
		close(s.closed)
		s.ln.Close()
		s.wg.Wait()
		os.Remove(s.path)
		s.lock.Close() // releases the flock; the lock file stays (removing it would race a new instance)
	})
	return nil
}

func (s *Server) handle(c *net.UnixConn) {
	defer c.Close()
	if !sameUser(c) {
		return // not even an error message for another user
	}
	c.SetReadDeadline(time.Now().Add(ioTimeout))
	resp := s.answer(c)
	c.SetWriteDeadline(time.Now().Add(ioTimeout))
	b, _ := json.Marshal(resp)
	c.Write(append(b, '\n'))
}

func (s *Server) answer(r io.Reader) Response {
	line, err := bufio.NewReaderSize(io.LimitReader(r, maxRequest+1), maxRequest+1).ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxRequest {
			return Response{Result: ResultError, Error: "request too long"}
		}
		return Response{Result: ResultError, Error: "incomplete request"}
	}
	var req Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Response{Result: ResultError, Error: "malformed request"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	var done bool
	switch req.Command {
	case CmdStatus:
		st := s.ctrl.Status()
		return Response{Result: ResultDone, Status: &st}
	case CmdRelease:
		done, err = s.ctrl.Release(ctx)
	case CmdResume:
		done, err = s.ctrl.Resume(ctx)
	case CmdPin:
		done, err = s.ctrl.Pin(ctx, req.Pin)
	default:
		return Response{Result: ResultError, Error: "unknown command"}
	}
	st := s.ctrl.Status()
	switch {
	case err != nil:
		return Response{Result: ResultRefused, Error: err.Error(), Status: &st}
	case !done:
		return Response{Result: ResultPending, Status: &st}
	}
	return Response{Result: ResultDone, Status: &st}
}

func sameUser(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return false
	}
	return int(cred.Uid) == os.Getuid()
}

// Do sends one request and returns the response. A request whose time runs
// out on the client side may still be carried out by the server.
func Do(ctx context.Context, path string, req Request) (Response, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, fmt.Errorf("chewedfeed is not running (no control socket at %s)", path)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(CommandTimeout + 2*ioTimeout))
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Now()) }) // ctx.Err() is set by then
	defer stop()
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReaderSize(io.LimitReader(c, 64<<10), 4096).ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("no answer from chewedfeed: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, errors.New("unreadable answer from chewedfeed")
	}
	return resp, nil
}
