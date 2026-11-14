package handover

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LAN discovery (CF-53). The phone advertises the DNS-SD service ServiceType (RFC 6763, over multicast DNS)
// on its peer port, TXT "v=1" (this protocol). The record names no device, address, key or user: any device
// on the network can answer, so an answer is only a hint. Dial returns an untrusted connection and the pinned
// TLS keys decide (Dial in transport.go); a candidate that fails them is skipped.
const (
	ServiceType     = "_chewedfeed._tcp"
	ServiceVersion  = "1"
	BrowseTimeout   = 3 * time.Second
	queryEvery      = 700 * time.Millisecond
	candidateDialMs = 3000
)

var ErrNoRoute = errors.New("the phone is not reachable on this network")

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Browse asks the local network for ServiceType and returns host:port candidates (the answering address and
// the advertised port) heard within BrowseTimeout, on every running IPv4 multicast interface (not one picked by
// the routing table: container bridges). The query repeats: a phone dozing on Wi-Fi misses the first ones.
// ponytail: IPv4 multicast only; add ff02::fb per interface if an IPv6-only LAN matters.
func Browse(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, BrowseTimeout)
	defer cancel()
	ifs, _ := net.Interfaces()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var out []string
	for _, ifi := range ifs {
		const want = net.FlagUp | net.FlagRunning | net.FlagMulticast
		if ifi.Flags&want != want || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		c, err := net.ListenMulticastUDP("udp4", &ifi, mdnsGroup) // shares 5353 with other responders
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			for _, a := range browse(ctx, c, mdnsGroup) {
				mu.Lock()
				if !slices.Contains(out, a) {
					out = append(out, a)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

func browse(ctx context.Context, c *net.UDPConn, to *net.UDPAddr) []string {
	deadline, _ := ctx.Deadline()
	c.SetReadDeadline(deadline)
	go func() {
		for t := time.NewTicker(queryEvery); ; {
			if _, err := c.WriteToUDP(query(), to); err != nil {
				t.Stop()
				return
			}
			select {
			case <-ctx.Done():
				t.Stop()
				c.SetReadDeadline(time.Now())
				return
			case <-t.C:
			}
		}
	}()
	var out []string
	buf := make([]byte, 9000)
	for {
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			return out
		}
		for _, port := range answers(buf[:n]) {
			if a := net.JoinHostPort(from.IP.String(), strconv.Itoa(port)); !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
}

func query() []byte {
	q := make([]byte, 12, 64) // id 0, flags 0 (mDNS), one question
	q[5] = 1
	for _, l := range strings.Split(ServiceType+".local", ".") {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	return append(q, 0, 0, 12, 0, 1) // PTR, IN
}

// answers: the ports of this service's SRV records in one mDNS response whose TXT, when present, is this
// version. Malformed input gives nothing.
func answers(m []byte) []int {
	if len(m) < 12 || m[2]&0x80 == 0 { // a response
		return nil
	}
	qd, rr := int(binary.BigEndian.Uint16(m[4:])), 0
	for _, i := range []int{6, 8, 10} {
		rr += int(binary.BigEndian.Uint16(m[i:]))
	}
	off := 12
	for range qd {
		_, n, ok := name(m, off)
		if !ok || n+4 > len(m) {
			return nil
		}
		off = n + 4
	}
	suffix := "." + ServiceType + ".local"
	ports := map[string]int{}
	version := map[string]string{}
	for range rr {
		owner, n, ok := name(m, off)
		if !ok || n+10 > len(m) {
			return nil
		}
		typ, size := binary.BigEndian.Uint16(m[n:]), int(binary.BigEndian.Uint16(m[n+8:]))
		data := n + 10
		if data+size > len(m) {
			return nil
		}
		off = data + size
		if !strings.HasSuffix(strings.ToLower(owner), suffix) {
			continue
		}
		switch typ {
		case 33: // SRV: priority, weight, port, target
			if size >= 6 {
				ports[owner] = int(binary.BigEndian.Uint16(m[data+4:]))
			}
		case 16: // TXT
			version[owner] = "?"
			for t := m[data : data+size]; len(t) > 0 && int(t[0]) < len(t); t = t[1+int(t[0]):] {
				if v, ok := strings.CutPrefix(string(t[1:1+int(t[0])]), "v="); ok {
					version[owner] = v
				}
			}
		}
	}
	var out []int
	for owner, port := range ports {
		if v, ok := version[owner]; port > 0 && (!ok || v == ServiceVersion) {
			out = append(out, port)
		}
	}
	slices.Sort(out)
	return out
}

// name reads a (possibly compressed) DNS name at off: the name and the offset after it.
func name(m []byte, off int) (string, int, bool) {
	var labels []string
	end := -1
	for jumps := 0; ; {
		if off >= len(m) {
			return "", 0, false
		}
		l := int(m[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			return strings.Join(labels, "."), end, true
		case l&0xC0 == 0xC0:
			if off+1 >= len(m) || jumps > 16 {
				return "", 0, false
			}
			if end < 0 {
				end = off + 2
			}
			off, jumps = int(binary.BigEndian.Uint16(m[off:])&0x3FFF), jumps+1
		case l < 64 && off+1+l <= len(m):
			labels = append(labels, string(m[off+1:off+1+l]))
			off += 1 + l
		default:
			return "", 0, false
		}
	}
}

// DiscoveryRoute reaches the enrolled phone: the address that last authenticated, the configured endpoint,
// then what Browse finds. Confirm after a connection authenticated; a candidate that connected but did not
// is skipped until the candidates run out. One route per Link: one coordinator, whatever the address.
type DiscoveryRoute struct {
	Endpoint string                             // configured; "" to rely on discovery
	Browse   func(ctx context.Context) []string // Browse; nil: none
	Report   func(error)                        // ErrNoRoute when nothing answers, nil once reachable again

	dial func(ctx context.Context, addr string) (net.Conn, error) // tests

	mu       sync.Mutex
	good     string
	pending  string
	bad      map[string]bool
	reported bool
}

// Confirm: the connection last dialled authenticated as the enrolled peer.
func (r *DiscoveryRoute) Confirm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != "" {
		r.good, r.pending = r.pending, ""
	}
	delete(r.bad, r.good)
}

func (r *DiscoveryRoute) Dial(ctx context.Context) (net.Conn, error) {
	r.mu.Lock()
	if r.bad == nil {
		r.bad = map[string]bool{}
	}
	if r.pending != "" { // dialled last time and never authenticated: someone else at that address
		r.bad[r.pending] = true
		if r.pending == r.good {
			r.good = ""
		}
		r.pending = ""
	}
	known := []string{r.good, r.Endpoint}
	r.mu.Unlock()

	tried := map[string]bool{}
	try := func(list []string) net.Conn {
		for _, a := range list {
			r.mu.Lock()
			skip := a == "" || tried[a] || r.bad[a]
			r.mu.Unlock()
			if skip || ctx.Err() != nil {
				continue
			}
			tried[a] = true
			if c, err := r.dialOne(ctx, a); err == nil {
				r.mu.Lock()
				r.pending = a
				r.mu.Unlock()
				r.report(nil)
				return c
			}
		}
		return nil
	}
	if c := try(known); c != nil {
		return c, nil
	}
	if r.Browse != nil {
		if c := try(r.Browse(ctx)); c != nil {
			return c, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	clear(r.bad) // the network may have changed: next time, everything is worth a try again
	r.mu.Unlock()
	r.report(ErrNoRoute)
	return nil, ErrNoRoute
}

func (r *DiscoveryRoute) dialOne(ctx context.Context, addr string) (net.Conn, error) {
	if r.dial != nil {
		return r.dial(ctx, addr)
	}
	d := net.Dialer{Timeout: time.Duration(candidateDialMs+rand.IntN(200)) * time.Millisecond}
	return d.DialContext(ctx, "tcp", addr)
}

// report calls Report on a change only.
func (r *DiscoveryRoute) report(err error) {
	r.mu.Lock()
	changed := r.reported != (err != nil)
	r.reported = err != nil
	r.mu.Unlock()
	if changed && r.Report != nil {
		r.Report(err)
	}
}
