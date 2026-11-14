package handover

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

// response builds an mDNS answer: SRV (and TXT when txt != "") for instance under service.
func response(service, instance string, port uint16, txt string) []byte {
	m := []byte{0, 0, 0x84, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	rrs := 0
	owner := func() []byte {
		var b []byte
		for _, l := range strings.Split(instance+"."+service+".local", ".") {
			b = append(append(b, byte(len(l))), l...)
		}
		return append(b, 0)
	}
	rr := func(typ uint16, data []byte) {
		m = append(m, owner()...)
		m = binary.BigEndian.AppendUint16(m, typ)
		m = append(m, 0x80, 1, 0, 0, 0, 120)
		m = binary.BigEndian.AppendUint16(m, uint16(len(data)))
		m = append(m, data...)
		rrs++
	}
	srv := []byte{0, 0, 0, 0}
	srv = binary.BigEndian.AppendUint16(srv, port)
	rr(33, append(srv, 5, 'p', 'h', 'o', 'n', 'e', 5, 'l', 'o', 'c', 'a', 'l', 0))
	if txt != "" {
		rr(16, append([]byte{byte(len(txt))}, txt...))
	}
	binary.BigEndian.PutUint16(m[6:], uint16(rrs))
	return m
}

func TestAnswers(t *testing.T) {
	if got := answers(response(ServiceType, "chewedfeed", 47613, "v=1")); !slices.Equal(got, []int{47613}) {
		t.Fatalf("ours: %v", got)
	}
	if got := answers(response(ServiceType, "x", 1234, "")); !slices.Equal(got, []int{1234}) {
		t.Fatalf("no TXT: %v", got)
	}
	for name, m := range map[string][]byte{
		"other version": response(ServiceType, "x", 1234, "v=2"),
		"other service": response("_other._tcp", "x", 1234, "v=1"),
		"a query":       append([]byte{0, 0, 0}, response(ServiceType, "x", 1, "")[3:]...),
		"short":         {1, 2, 3},
		"truncated":     response(ServiceType, "x", 1234, "v=1")[:40],
		"pointer loop":  {0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0xC0, 12},
	} {
		if got := answers(m); len(got) != 0 {
			t.Fatalf("%s: %v", name, got)
		}
	}
	// Compressed names are followed.
	if _, end, ok := name([]byte{1, 'a', 0, 1, 'b', 0xC0, 0}, 3); !ok || end != 7 {
		t.Fatal("compressed name")
	}
}

// A fake responder on loopback: browse sends the query and collects candidates from every answer.
func TestBrowseLoopback(t *testing.T) {
	responder, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer responder.Close()
	go func() {
		buf := make([]byte, 512)
		n, from, err := responder.ReadFromUDP(buf)
		if err != nil || !slices.Equal(buf[:n], query()) {
			return
		}
		responder.WriteToUDP(response(ServiceType, "chewedfeed", 47613, "v=1"), from)
		responder.WriteToUDP(response(ServiceType, "chewedfeed", 47613, "v=1"), from) // repeated: once
		responder.WriteToUDP(response(ServiceType, "evil", 9, "v=9"), from)
	}()
	c, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	got := browse(ctx, c, responder.LocalAddr().(*net.UDPAddr))
	if !slices.Equal(got, []string{"127.0.0.1:47613"}) {
		t.Fatalf("candidates %v", got)
	}
}

type fakeNet struct {
	up      map[string]bool // addresses that accept TCP
	found   []string        // what discovery answers
	dialled []string
}

func (f *fakeNet) route(endpoint string) *DiscoveryRoute {
	r := &DiscoveryRoute{Endpoint: endpoint, Browse: func(context.Context) []string { return f.found }}
	r.dial = func(_ context.Context, a string) (net.Conn, error) {
		f.dialled = append(f.dialled, a)
		if !f.up[a] {
			return nil, errors.New("refused")
		}
		c, s := net.Pipe()
		s.Close()
		return c, nil
	}
	return r
}

func TestDiscoveryRoute(t *testing.T) {
	ctx := context.Background()
	// The configured endpoint answers: no discovery needed.
	f := &fakeNet{up: map[string]bool{"a:1": true}, found: []string{"b:1"}}
	r := f.route("a:1")
	if _, err := r.Dial(ctx); err != nil || !slices.Equal(f.dialled, []string{"a:1"}) {
		t.Fatalf("configured: %v %v", f.dialled, err)
	}
	r.Confirm()

	// The phone moved (stale endpoint, network change): discovery finds it, and it is remembered.
	f.up, f.found, f.dialled = map[string]bool{"b:1": true}, []string{"b:1"}, nil
	if _, err := r.Dial(ctx); err != nil || !slices.Equal(f.dialled, []string{"a:1", "b:1"}) {
		t.Fatalf("moved: %v %v", f.dialled, err)
	}
	r.Confirm()
	f.dialled = nil
	r.Dial(ctx)
	if f.dialled[0] != "b:1" {
		t.Fatalf("remembered: %v", f.dialled)
	}
	r.Confirm()

	// A device that answers discovery but does not authenticate (no Confirm) is skipped next time.
	f2 := &fakeNet{up: map[string]bool{"evil:1": true, "phone:1": true}, found: []string{"evil:1", "phone:1"}}
	r2 := f2.route("")
	r2.Dial(ctx) // evil:1, never confirmed
	f2.dialled = nil
	if _, err := r2.Dial(ctx); err != nil || !slices.Equal(f2.dialled, []string{"phone:1"}) {
		t.Fatalf("hostile candidate: %v %v", f2.dialled, err)
	}
}

func TestDiscoveryRouteUnreachable(t *testing.T) {
	var reports []error
	f := &fakeNet{up: map[string]bool{}}
	r := f.route("a:1")
	r.Report = func(err error) { reports = append(reports, err) }
	for range 2 {
		if _, err := r.Dial(context.Background()); !errors.Is(err, ErrNoRoute) {
			t.Fatal(err)
		}
	}
	f.up["a:1"] = true
	r.Dial(context.Background())
	if len(reports) != 2 || !errors.Is(reports[0], ErrNoRoute) || reports[1] != nil {
		t.Fatalf("reported once each way: %v", reports)
	}
	// Multicast blocked (nothing found) and no endpoint: unreachable, never a dial of nothing.
	f2 := &fakeNet{}
	if _, err := f2.route("").Dial(context.Background()); !errors.Is(err, ErrNoRoute) || len(f2.dialled) != 0 {
		t.Fatal(err, f2.dialled)
	}
}
