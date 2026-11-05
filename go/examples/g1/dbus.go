package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

// A small D-Bus client (the wire protocol of the D-Bus specification):
// enough to monitor the session bus and to serve one interface on it.

const (
	msgCall   = 1
	msgReturn = 2
	msgError  = 3
	msgSignal = 4

	flagNoReply = 1
)

// message is a D-Bus message; Body holds the decoded values in signature
// order (see decode).
type message struct {
	Type                               byte
	Flags                              byte
	Serial, ReplySerial                uint32
	Path, Interface, Member, ErrorName string
	Destination, Sender, Signature     string
	Body                               []any
	order                              binary.ByteOrder
}

// variant is a value of type v.
type variant struct {
	Sig   string
	Value any
}

type objectPath string
type signature string

type bus struct {
	conn   net.Conn
	r      *bufio.Reader
	mu     sync.Mutex
	serial uint32
	name   string
}

// dialBus connects to a bus address such as unix:path=/run/user/1000/bus
// (the session bus: DBUS_SESSION_BUS_ADDRESS) and says Hello.
func dialBus(address string) (*bus, error) {
	if address == "" {
		address = os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	}
	if address == "" && os.Getenv("XDG_RUNTIME_DIR") == "" {
		return nil, errors.New("DBUS_SESSION_BUS_ADDRESS and XDG_RUNTIME_DIR are not set (log in, or pass -bus ADDRESS)")
	}
	if address == "" {
		address = "unix:path=" + os.Getenv("XDG_RUNTIME_DIR") + "/bus"
	}
	var errs []error
	for _, a := range strings.Split(address, ";") {
		c, err := dialOne(a)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("D-Bus %s: %w", address, errors.Join(errs...))
}

func dialOne(address string) (*bus, error) {
	kind, rest, _ := strings.Cut(address, ":")
	if kind != "unix" {
		return nil, fmt.Errorf("transport %q not supported", kind)
	}
	var path string
	for _, kv := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "path":
			path = unescape(v)
		case "abstract":
			path = "@" + unescape(v)
		}
	}
	if path == "" {
		return nil, fmt.Errorf("no path in %q", address)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	b := &bus{conn: conn, r: bufio.NewReader(conn)}
	if err := b.auth(); err != nil {
		conn.Close()
		return nil, err
	}
	reply, err := b.call("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Hello", "")
	if err != nil {
		conn.Close()
		return nil, err
	}
	b.name, _ = reply.arg(0).(string)
	return b, nil
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// auth is SASL EXTERNAL with our uid.
func (b *bus) auth() error {
	uid := hex.EncodeToString([]byte(strconv.Itoa(os.Getuid())))
	if _, err := io.WriteString(b.conn, "\x00AUTH EXTERNAL "+uid+"\r\n"); err != nil {
		return err
	}
	line, err := b.r.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "OK ") {
		return fmt.Errorf("authentication refused: %q", strings.TrimSpace(line))
	}
	_, err = io.WriteString(b.conn, "BEGIN\r\n")
	return err
}

func (b *bus) Close() error { return b.conn.Close() }

// send writes a message and returns its serial.
func (b *bus) send(m *message) (uint32, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.serial++
	m.Serial = b.serial
	_, err := b.conn.Write(m.encode())
	return m.Serial, err
}

// call sends a method call and reads messages until its reply: only for
// calls made before anything else arrives (Hello, BecomeMonitor,
// RequestName, GetNameOwner at setup).
func (b *bus) call(dest, path, iface, member, sig string, args ...any) (*message, error) {
	serial, err := b.send(&message{Type: msgCall, Destination: dest, Path: path, Interface: iface, Member: member,
		Signature: sig, Body: args})
	if err != nil {
		return nil, err
	}
	for {
		m, err := b.read()
		if err != nil {
			return nil, err
		}
		if m.ReplySerial != serial || (m.Type != msgReturn && m.Type != msgError) {
			continue
		}
		if m.Type == msgError {
			text, _ := m.arg(0).(string)
			return m, fmt.Errorf("%s: %s %s", member, m.ErrorName, text)
		}
		return m, nil
	}
}

func (b *bus) reply(to *message, sig string, args ...any) error {
	if to.Flags&flagNoReply != 0 {
		return nil
	}
	_, err := b.send(&message{Type: msgReturn, ReplySerial: to.Serial, Destination: to.Sender, Signature: sig,
		Body: args})
	return err
}

func (b *bus) replyError(to *message, name, text string) error {
	if to.Flags&flagNoReply != 0 {
		return nil
	}
	_, err := b.send(&message{Type: msgError, ReplySerial: to.Serial, Destination: to.Sender, ErrorName: name,
		Signature: "s", Body: []any{text}})
	return err
}

func (b *bus) signal(path, iface, member, sig string, args ...any) error {
	_, err := b.send(&message{Type: msgSignal, Path: path, Interface: iface, Member: member, Signature: sig, Body: args})
	return err
}

func (m *message) arg(i int) any {
	if i < len(m.Body) {
		return m.Body[i]
	}
	return nil
}

// read returns the next message from the bus.
func (b *bus) read() (*message, error) {
	head := make([]byte, 16)
	if _, err := io.ReadFull(b.r, head); err != nil {
		return nil, err
	}
	var order binary.ByteOrder
	switch head[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("bad message byte order %q", head[0])
	}
	bodyLen, fieldsLen := order.Uint32(head[4:]), order.Uint32(head[12:])
	if bodyLen > 1<<27 || fieldsLen > 1<<26 {
		return nil, errors.New("message too long")
	}
	total := (16+int(fieldsLen)+7)&^7 + int(bodyLen)
	buf := make([]byte, total)
	copy(buf, head)
	if _, err := io.ReadFull(b.r, buf[16:]); err != nil {
		return nil, err
	}
	return parse(buf, order)
}

// ---------------------------------------------------------------- encoding

// encoder writes little-endian messages.
type encoder struct{ b []byte }

func (e *encoder) align(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}

func (e *encoder) u32(v uint32) {
	e.align(4)
	e.b = binary.LittleEndian.AppendUint32(e.b, v)
}

func (e *encoder) str(s string) {
	e.u32(uint32(len(s)))
	e.b = append(append(e.b, s...), 0)
}

func (e *encoder) sig(s string) {
	e.b = append(append(append(e.b, byte(len(s))), s...), 0)
}

// value encodes v as the single complete type at the start of sig and
// returns the rest of sig.
func (e *encoder) value(sig string, v any) (string, error) {
	t, rest, err := firstType(sig)
	if err != nil {
		return "", err
	}
	switch t[0] {
	case 'y':
		e.b = append(e.b, v.(byte))
	case 'b':
		x := uint32(0)
		if v.(bool) {
			x = 1
		}
		e.u32(x)
	case 'n':
		e.align(2)
		e.b = binary.LittleEndian.AppendUint16(e.b, uint16(v.(int16)))
	case 'q':
		e.align(2)
		e.b = binary.LittleEndian.AppendUint16(e.b, v.(uint16))
	case 'i':
		e.u32(uint32(v.(int32)))
	case 'u':
		e.u32(v.(uint32))
	case 'x':
		e.align(8)
		e.b = binary.LittleEndian.AppendUint64(e.b, uint64(v.(int64)))
	case 't':
		e.align(8)
		e.b = binary.LittleEndian.AppendUint64(e.b, v.(uint64))
	case 'd':
		e.align(8)
		e.b = binary.LittleEndian.AppendUint64(e.b, math.Float64bits(v.(float64)))
	case 's':
		e.str(v.(string))
	case 'o':
		e.str(string(v.(objectPath)))
	case 'g':
		e.sig(string(v.(signature)))
	case 'v':
		x := v.(variant)
		e.sig(x.Sig)
		if _, err := e.value(x.Sig, x.Value); err != nil {
			return "", err
		}
	case 'a':
		e.u32(0)
		at := len(e.b) - 4
		elem := t[1:]
		e.align(alignment(elem[0]))
		start := len(e.b)
		for _, x := range v.([]any) {
			if _, err := e.value(elem, x); err != nil {
				return "", err
			}
		}
		binary.LittleEndian.PutUint32(e.b[at:], uint32(len(e.b)-start))
	case '(', '{':
		e.align(8)
		inner := t[1 : len(t)-1]
		for _, x := range v.([]any) {
			if inner, err = e.value(inner, x); err != nil {
				return "", err
			}
		}
	default:
		return "", fmt.Errorf("cannot encode type %q", t)
	}
	return rest, nil
}

func (m *message) encode() []byte {
	e := &encoder{}
	e.b = append(e.b, 'l', m.Type, m.Flags, 1, 0, 0, 0, 0)
	e.u32(m.Serial)
	var fields []any
	add := func(code byte, sig string, v any) { fields = append(fields, []any{code, variant{sig, v}}) }
	if m.Path != "" {
		add(1, "o", objectPath(m.Path))
	}
	if m.Interface != "" {
		add(2, "s", m.Interface)
	}
	if m.Member != "" {
		add(3, "s", m.Member)
	}
	if m.ErrorName != "" {
		add(4, "s", m.ErrorName)
	}
	if m.ReplySerial != 0 {
		add(5, "u", m.ReplySerial)
	}
	if m.Destination != "" {
		add(6, "s", m.Destination)
	}
	if m.Signature != "" {
		add(8, "g", signature(m.Signature))
	}
	if _, err := e.value("a(yv)", fields); err != nil {
		panic(err)
	}
	e.align(8)
	head := len(e.b)
	sig := m.Signature
	for _, v := range m.Body {
		var err error
		if sig, err = e.value(sig, v); err != nil {
			panic(err)
		}
	}
	binary.LittleEndian.PutUint32(e.b[4:], uint32(len(e.b)-head))
	return e.b
}

// ---------------------------------------------------------------- decoding

type decoder struct {
	b     []byte
	at    int
	order binary.ByteOrder
	depth int
}

var errShort = errors.New("message cut off")

func (d *decoder) align(n int) error {
	at := (d.at + n - 1) / n * n
	if at > len(d.b) {
		return errShort
	}
	d.at = at
	return nil
}

func (d *decoder) take(n int) ([]byte, error) {
	if d.at+n > len(d.b) || n < 0 {
		return nil, errShort
	}
	v := d.b[d.at : d.at+n]
	d.at += n
	return v, nil
}

func (d *decoder) u32() (uint32, error) {
	if err := d.align(4); err != nil {
		return 0, err
	}
	v, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return d.order.Uint32(v), nil
}

func (d *decoder) str() (string, error) {
	n, err := d.u32()
	if err != nil {
		return "", err
	}
	v, err := d.take(int(n) + 1)
	if err != nil {
		return "", err
	}
	return string(v[:n]), nil
}

func (d *decoder) sig() (string, error) {
	n, err := d.take(1)
	if err != nil {
		return "", err
	}
	v, err := d.take(int(n[0]) + 1)
	if err != nil {
		return "", err
	}
	return string(v[:n[0]]), nil
}

// value decodes one complete type from the start of sig: bytes, booleans,
// integers (their Go types), float64, strings, objectPath, signature,
// variant, and []any for arrays, structs and dict entries.
func (d *decoder) value(sig string) (any, string, error) {
	t, rest, err := firstType(sig)
	if err != nil {
		return nil, "", err
	}
	if d.depth++; d.depth > 64 {
		return nil, "", errors.New("values nested too deep")
	}
	defer func() { d.depth-- }()
	var v any
	switch t[0] {
	case 'y':
		var b []byte
		if b, err = d.take(1); err == nil {
			v = b[0]
		}
	case 'b':
		var x uint32
		x, err = d.u32()
		v = x != 0
	case 'n', 'q':
		if err = d.align(2); err == nil {
			var b []byte
			if b, err = d.take(2); err == nil {
				x := d.order.Uint16(b)
				if t[0] == 'n' {
					v = int16(x)
				} else {
					v = x
				}
			}
		}
	case 'i', 'u', 'h':
		var x uint32
		x, err = d.u32()
		if t[0] == 'i' {
			v = int32(x)
		} else {
			v = x
		}
	case 'x', 't', 'd':
		if err = d.align(8); err == nil {
			var b []byte
			if b, err = d.take(8); err == nil {
				x := d.order.Uint64(b)
				switch t[0] {
				case 'x':
					v = int64(x)
				case 't':
					v = x
				default:
					v = math.Float64frombits(x)
				}
			}
		}
	case 's':
		v, err = d.str()
	case 'o':
		var s string
		s, err = d.str()
		v = objectPath(s)
	case 'g':
		var s string
		s, err = d.sig()
		v = signature(s)
	case 'v':
		var s string
		if s, err = d.sig(); err == nil {
			var x any
			var left string
			if x, left, err = d.value(s); err == nil {
				if left != "" {
					err = errors.New("variant holds more than one type")
				}
				v = variant{s, x}
			}
		}
	case 'a':
		var n uint32
		if n, err = d.u32(); err != nil {
			break
		}
		if err = d.align(alignment(t[1])); err != nil {
			break
		}
		end := d.at + int(n)
		if end > len(d.b) {
			err = errShort
			break
		}
		list := []any{}
		for d.at < end {
			var x any
			if x, _, err = d.value(t[1:]); err != nil {
				break
			}
			list = append(list, x)
		}
		v = list
	case '(', '{':
		if err = d.align(8); err != nil {
			break
		}
		inner := t[1 : len(t)-1]
		list := []any{}
		for inner != "" && err == nil {
			var x any
			if x, inner, err = d.value(inner); err == nil {
				list = append(list, x)
			}
		}
		v = list
	default:
		err = fmt.Errorf("cannot decode type %q", t)
	}
	return v, rest, err
}

func parse(buf []byte, order binary.ByteOrder) (*message, error) {
	m := &message{Type: buf[1], Flags: buf[2], Serial: order.Uint32(buf[8:]), order: order}
	d := &decoder{b: buf, at: 12, order: order}
	fields, _, err := d.value("a(yv)")
	if err != nil {
		return nil, err
	}
	for _, f := range fields.([]any) {
		kv := f.([]any)
		code, val := kv[0].(byte), kv[1].(variant).Value
		switch code {
		case 1:
			p, _ := val.(objectPath)
			m.Path = string(p)
		case 2:
			m.Interface, _ = val.(string)
		case 3:
			m.Member, _ = val.(string)
		case 4:
			m.ErrorName, _ = val.(string)
		case 5:
			m.ReplySerial, _ = val.(uint32)
		case 6:
			m.Destination, _ = val.(string)
		case 7:
			m.Sender, _ = val.(string)
		case 8:
			s, _ := val.(signature)
			m.Signature = string(s)
		}
	}
	if err := d.align(8); err != nil {
		return nil, err
	}
	sig := m.Signature
	for sig != "" {
		var v any
		if v, sig, err = d.value(sig); err != nil {
			return nil, fmt.Errorf("%s.%s body: %w", m.Interface, m.Member, err)
		}
		m.Body = append(m.Body, v)
	}
	return m, nil
}

func alignment(t byte) int {
	switch t {
	case 'n', 'q':
		return 2
	case 'b', 'i', 'u', 'h', 's', 'o', 'a':
		return 4
	case 'x', 't', 'd', '(', '{':
		return 8
	}
	return 1
}

// firstType splits the first complete type off a signature.
func firstType(sig string) (string, string, error) {
	if sig == "" {
		return "", "", errors.New("signature ends early")
	}
	switch sig[0] {
	case 'a':
		t, rest, err := firstType(sig[1:])
		return "a" + t, rest, err
	case '(', '{':
		closer := map[byte]byte{'(': ')', '{': '}'}[sig[0]]
		depth := 0
		for i := 0; i < len(sig); i++ {
			switch sig[i] {
			case '(', '{':
				depth++
			case ')', '}':
				depth--
				if depth == 0 {
					if sig[i] != closer {
						return "", "", fmt.Errorf("bad signature %q", sig)
					}
					return sig[:i+1], sig[i+1:], nil
				}
			}
		}
		return "", "", fmt.Errorf("bad signature %q", sig)
	}
	return sig[:1], sig[1:], nil
}
