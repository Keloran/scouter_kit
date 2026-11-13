package handover

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Wire format (policy.json "wire"): a u32 big-endian payload length, then that
// many bytes of UTF-8 JSON, one protocol message per frame.
const (
	MaxFrameBytes     = 65536
	maxDepth          = 16
	maxSnapshotBytes  = 8192
	maxTelemetryBytes = 32768
	ProtocolVersion   = 1
)

// Capabilities (policy.json): both required ones in every hello; optional ones
// are used only when both hellos list them.
const (
	CapTwoArm        = "two_arm_v1"
	CapReleaseFirst  = "release_before_acquire_v1"
	CapSnapshot      = "snapshot_v1"
	CapTelemetry     = "telemetry_v1"
	errInvalidPrefix = "invalid message: "
)

var (
	RequiredCapabilities = []string{CapTwoArm, CapReleaseFirst}
	OptionalCapabilities = []string{CapSnapshot, CapTelemetry}
)

// ErrInvalid wraps every rejection of a frame or message.
var ErrInvalid = errors.New("invalid message")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// A copy of tests/handover/protocol.schema.json (a test keeps them equal).
//
//go:embed protocol.schema.json
var protocolSchemaJSON []byte

var protocolSchema = func() map[string]any {
	var s map[string]any
	if err := json.Unmarshal(protocolSchemaJSON, &s); err != nil {
		panic(err)
	}
	if err := checkSchema(s, s); err != nil {
		panic(err)
	}
	return s
}()

// Message is a protocol envelope. PeerSession and Transfer are nil for JSON null.
type Message struct {
	Version     int             `json:"version"`
	Sender      string          `json:"sender"`
	Recipient   string          `json:"recipient"`
	Session     string          `json:"session"`
	PeerSession *string         `json:"peer_session"`
	Seq         uint64          `json:"seq"`
	Epoch       uint64          `json:"epoch"`
	Transfer    *string         `json:"transfer"`
	Kind        string          `json:"kind"`
	Body        json.RawMessage `json:"body"`
}

type HelloBody struct {
	Role                 Role     `json:"role"`
	RequiredCapabilities []string `json:"required_capabilities"`
	OptionalCapabilities []string `json:"optional_capabilities"`
}

type StatusBody struct {
	Role          Role    `json:"role"`
	Phase         Phase   `json:"phase"`
	Eligible      bool    `json:"eligible"`
	Pinned        bool    `json:"pinned"`
	Arms          Arms    `json:"arms"`
	RendererReady bool    `json:"renderer_ready"`
	Transfer      *string `json:"transfer"`
	ReadyEpoch    uint64  `json:"ready_epoch"`
}

type AvailabilityBody struct {
	Eligible bool   `json:"eligible"`
	Lid      string `json:"lid"` // open, closed, absent, unknown
	Awake    bool   `json:"awake"`
	Reason   string `json:"reason"`
}

type RequestBody struct {
	Target Role   `json:"target"`
	Reason string `json:"reason"`
	Pinned bool   `json:"pinned"`
}

type ReleaseBody struct {
	Target Role   `json:"target"`
	Reason string `json:"reason"`
}

type ReleasedBody struct {
	Target            Role `json:"target"`
	Arms              Arms `json:"arms"`
	ReconnectDisabled bool `json:"reconnect_disabled"`
	WorkersStopped    bool `json:"workers_stopped"`
}

type AcquiringBody struct {
	Target Role   `json:"target"`
	Mode   string `json:"mode"` // released, probe, standalone
}

type ReadyBody struct {
	Target        Role `json:"target"`
	Arms          Arms `json:"arms"`
	RendererReady bool `json:"renderer_ready"`
}

type AbortBody struct {
	Reason       string `json:"reason"`
	ReleasedBoth bool   `json:"released_both"`
}

type HeartbeatBody struct {
	Phase      Phase  `json:"phase"`
	ReadyEpoch uint64 `json:"ready_epoch"`
}

type Snapshot struct {
	SchemaVersion  int     `json:"schema_version"`
	Revision       uint64  `json:"revision"`
	View           string  `json:"view"` // sites, ai, notifications, stats
	PageID         string  `json:"page_id"`
	Project        *string `json:"project"`
	RotationPaused bool    `json:"rotation_paused"`
	RotationMs     int64   `json:"rotation_ms"`
}

type SnapshotBody struct {
	Snapshot Snapshot `json:"snapshot"`
}

// Telemetry.Values is []AIWindow for source "ai", *SystemValues for "system", or nil.
type Telemetry struct {
	SchemaVersion int             `json:"schema_version"`
	Revision      uint64          `json:"revision"`
	Source        string          `json:"source"` // ai, system
	SampleUnixMs  *uint64         `json:"sample_unix_ms"`
	AgeMs         uint64          `json:"age_ms"`
	Status        string          `json:"status"` // fresh, stale, unavailable
	Values        json.RawMessage `json:"values"`
}

type AIWindow struct {
	Provider    string   `json:"provider"`
	Window      string   `json:"window"`
	UsedPercent *float64 `json:"used_percent"`
	ResetUnixMs *uint64  `json:"reset_unix_ms"`
}

type SystemValues struct {
	CPUPercent       *float64 `json:"cpu_percent"`
	MemoryUsedBytes  uint64   `json:"memory_used_bytes"`
	MemoryTotalBytes uint64   `json:"memory_total_bytes"`
	BatteryPercent   *int     `json:"battery_percent"`
}

type TelemetryBody struct {
	Telemetry Telemetry `json:"telemetry"`
}

// WriteFrame writes one message after checking it as a receiver would: a
// local bug fails here instead of on the peer.
func WriteFrame(w io.Writer, m Message) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := DecodeMessage(payload); err != nil {
		return fmt.Errorf("refusing to send: %w", err)
	}
	frame := make([]byte, 4, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	_, err = w.Write(append(frame, payload...))
	return err
}

// ReadFrame reads one frame and decodes it. The length is checked before the
// payload is read, so an oversized frame costs four bytes.
func ReadFrame(r io.Reader) (Message, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Message{}, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > MaxFrameBytes {
		return Message{}, invalid("frame length %d", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Message{}, err
	}
	return DecodeMessage(payload)
}

// DecodeMessage applies the strict JSON rules, the protocol schema and the
// per-kind payload checks (contract.py strict_json, validate, payload_checks).
func DecodeMessage(payload []byte) (Message, error) {
	v, err := strictJSON(payload)
	if err != nil {
		return Message{}, err
	}
	if err := validate(v, protocolSchema, protocolSchema); err != nil {
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(payload, &m); err != nil {
		return Message{}, invalid("%v", err)
	}
	if err := payloadChecks(m); err != nil {
		return Message{}, err
	}
	return m, nil
}

// strictJSON: size, UTF-8, depth, duplicate keys, lone surrogates, control
// characters, trailing data; numbers stay json.Number.
func strictJSON(payload []byte) (any, error) {
	if len(payload) > MaxFrameBytes {
		return nil, invalid("frame too large")
	}
	if !utf8.Valid(payload) {
		return nil, invalid("invalid UTF-8")
	}
	if err := scanRaw(payload); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	v, err := readValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, invalid("trailing data")
	}
	return v, nil
}

// scanRaw bounds nesting before decoding and rejects \u escapes of lone
// surrogates (Go's decoder would silently turn them into U+FFFD).
func scanRaw(p []byte) error {
	depth, quoted := 0, false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if quoted {
			switch c {
			case '"':
				quoted = false
			case '\\':
				if i+1 < len(p) && p[i+1] == 'u' {
					u, ok := hex4(p, i+2)
					if !ok {
						return invalid("invalid JSON")
					}
					switch {
					case u >= 0xDC00 && u <= 0xDFFF:
						return invalid("unpaired surrogate")
					case u >= 0xD800 && u <= 0xDBFF:
						lo, ok := hex4(p, i+8)
						if !ok || p[i+6] != '\\' || p[i+7] != 'u' || lo < 0xDC00 || lo > 0xDFFF {
							return invalid("unpaired surrogate")
						}
						i += 6
					}
					i += 5
				} else {
					i++
				}
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '[', '{':
			if depth++; depth > maxDepth {
				return invalid("nesting too deep")
			}
		case ']', '}':
			depth--
		}
	}
	return nil
}

func hex4(p []byte, i int) (uint16, bool) {
	if i+4 > len(p) {
		return 0, false
	}
	u, err := strconv.ParseUint(string(p[i:i+4]), 16, 16)
	return uint16(u), err == nil
}

func readValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, invalid("invalid JSON")
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, invalid("invalid JSON")
				}
				key := kt.(string)
				if _, dup := obj[key]; dup {
					return nil, invalid("duplicate key")
				}
				if err := plainText(key); err != nil {
					return nil, err
				}
				if obj[key], err = readValue(dec); err != nil {
					return nil, err
				}
			}
			_, err = dec.Token()
			return obj, err
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := readValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err = dec.Token()
			return arr, err
		}
		return nil, invalid("invalid JSON")
	case string:
		return t, plainText(t)
	case json.Number:
		if f, err := t.Float64(); err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, invalid("nonfinite number")
		}
		return t, nil
	}
	return t, nil // bool, nil
}

func plainText(s string) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			return invalid("control character")
		}
	}
	return nil
}

func resolveRef(root map[string]any, ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, invalid("only local schema references")
	}
	var node any = root
	for _, part := range strings.Split(ref[2:], "/") {
		m, ok := node.(map[string]any)
		if !ok {
			return nil, invalid("bad reference %s", ref)
		}
		node = m[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
	}
	m, ok := node.(map[string]any)
	if !ok {
		return nil, invalid("bad reference %s", ref)
	}
	return m, nil
}

var schemaKeywords = map[string]bool{"$schema": true, "$defs": true, "$ref": true, "title": true,
	"description": true, "type": true, "enum": true, "const": true, "oneOf": true, "properties": true,
	"required": true, "additionalProperties": true, "items": true, "maxItems": true, "minimum": true,
	"maximum": true, "minLength": true, "maxLength": true, "pattern": true}

var patterns = map[string]*regexp.Regexp{}

// checkSchema fails on keywords outside the supported subset (contract.py check_schema).
func checkSchema(s, root map[string]any) error {
	for k := range s {
		if !schemaKeywords[k] {
			return fmt.Errorf("unsupported schema keyword %q", k)
		}
	}
	if ref, ok := s["$ref"].(string); ok {
		if _, err := resolveRef(root, ref); err != nil {
			return err
		}
	}
	if p, ok := s["pattern"].(string); ok {
		re, err := regexp.Compile(p)
		if err != nil {
			return err
		}
		patterns[p] = re
	}
	for _, kw := range []string{"$defs", "properties"} {
		if m, ok := s[kw].(map[string]any); ok {
			for _, v := range m {
				if err := checkSchema(v.(map[string]any), root); err != nil {
					return err
				}
			}
		}
	}
	if list, ok := s["oneOf"].([]any); ok {
		for _, v := range list {
			if err := checkSchema(v.(map[string]any), root); err != nil {
				return err
			}
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		return checkSchema(items, root)
	}
	return nil
}

// jsonEqual compares a decoded value with a schema constant: numbers by value,
// everything else by type and value.
func jsonEqual(a, b any) bool {
	fa, aNum := number(a)
	fb, bNum := number(b)
	if aNum || bNum {
		return aNum && bNum && fa == fb
	}
	switch b := b.(type) {
	case string, bool, nil:
		return a == b
	}
	return false
}

func number(v any) (float64, bool) {
	switch v := v.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	}
	return 0, false
}

// validate evaluates the schema subset (contract.py validate). Integers must
// be written without a fraction or exponent.
func validate(v any, s, root map[string]any) error {
	if ref, ok := s["$ref"].(string); ok {
		r, err := resolveRef(root, ref)
		if err != nil {
			return err
		}
		if err := validate(v, r, root); err != nil {
			return err
		}
	}
	if list, ok := s["oneOf"].([]any); ok {
		matches := 0
		var last error
		for _, c := range list {
			if err := validate(v, c.(map[string]any), root); err == nil {
				matches++
			} else {
				last = err
			}
		}
		if matches != 1 {
			if matches == 0 && len(list) > 0 && last != nil {
				return invalid("no schema matches (%v)", strings.TrimPrefix(last.Error(), errInvalidPrefix))
			}
			return invalid("oneOf must match exactly once")
		}
	}
	if c, ok := s["const"]; ok && !jsonEqual(v, c) {
		return invalid("constant mismatch")
	}
	if e, ok := s["enum"].([]any); ok && !slices.ContainsFunc(e, func(x any) bool { return jsonEqual(v, x) }) {
		return invalid("unknown enum value")
	}
	if t, ok := s["type"].(string); ok && !hasType(v, t) {
		return invalid("wrong JSON type")
	}
	switch v := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, name := range req {
				if _, ok := v[name.(string)]; !ok {
					return invalid("missing field %s", name)
				}
			}
		}
		for name, item := range v {
			p, known := props[name]
			if !known {
				if ap, ok := s["additionalProperties"].(bool); ok && !ap {
					return invalid("unknown field %s", name)
				}
				continue
			}
			if err := validate(item, p.(map[string]any), root); err != nil {
				return err
			}
		}
	case []any:
		if m, ok := number(s["maxItems"]); ok && float64(len(v)) > m {
			return invalid("too many items")
		}
		if items, ok := s["items"].(map[string]any); ok {
			for _, item := range v {
				if err := validate(item, items, root); err != nil {
					return err
				}
			}
		}
	case string:
		n := float64(utf8.RuneCountInString(v))
		if m, ok := number(s["minLength"]); ok && n < m {
			return invalid("short string")
		}
		if m, ok := number(s["maxLength"]); ok && n > m {
			return invalid("long string")
		}
		if p, ok := s["pattern"].(string); ok && !patterns[p].MatchString(v) {
			return invalid("pattern mismatch")
		}
	case json.Number:
		f, _ := v.Float64()
		if m, ok := number(s["minimum"]); ok && f < m {
			return invalid("number too small")
		}
		if m, ok := number(s["maximum"]); ok && f > m {
			return invalid("number too large")
		}
	}
	return nil
}

func hasType(v any, t string) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := strconv.ParseInt(string(n), 10, 64)
		return err == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	}
	return false
}

var pageID = regexp.MustCompile(`^[A-Za-z0-9_:/-]+$`)

// payloadChecks: rules between fields (contract.py payload_checks).
func payloadChecks(m Message) error {
	switch m.Kind {
	case "hello":
		var b HelloBody
		json.Unmarshal(m.Body, &b)
		known := append(slices.Clone(RequiredCapabilities), OptionalCapabilities...)
		for _, c := range RequiredCapabilities {
			if !slices.Contains(b.RequiredCapabilities, c) {
				return invalid("missing required capability %s", c)
			}
		}
		seen := map[string]bool{}
		for _, c := range b.RequiredCapabilities {
			if !slices.Contains(known, c) {
				return invalid("unsupported required capability %s", c)
			}
			if seen[c] {
				return invalid("duplicate required capability")
			}
			seen[c] = true
		}
		opt := map[string]bool{}
		for _, c := range b.OptionalCapabilities {
			if opt[c] {
				return invalid("duplicate optional capability")
			}
			if seen[c] {
				return invalid("ambiguous capabilities")
			}
			opt[c] = true
		}
	case "ready", "released":
		var b struct{ Arms Arms }
		json.Unmarshal(m.Body, &b)
		want := m.Kind == "ready"
		if b.Arms.Left != want || b.Arms.Right != want {
			return invalid("wrong arm readiness")
		}
	case "status":
		var b StatusBody
		json.Unmarshal(m.Body, &b)
		if b.ReadyEpoch > m.Epoch {
			return invalid("future ready epoch")
		}
		if b.Phase == Active && (!b.Arms.both() || !b.RendererReady || b.ReadyEpoch != m.Epoch || b.Transfer == nil) {
			return invalid("active before ready")
		}
		if (b.Phase == Idle || b.Phase == Requesting || b.Phase == Recovering) && (b.Arms.any() || b.RendererReady) {
			return invalid("inactive retains arms")
		}
		if b.Role == Laptop && b.Pinned {
			return invalid("laptop pin unsupported")
		}
	case "snapshot":
		var raw struct{ Snapshot json.RawMessage }
		json.Unmarshal(m.Body, &raw)
		var b SnapshotBody
		json.Unmarshal(m.Body, &b)
		s := b.Snapshot
		switch {
		case compactLen(raw.Snapshot) > maxSnapshotBytes:
			return invalid("snapshot too large")
		case s.RotationMs != 0 && s.RotationMs < 1000:
			return invalid("rotation too fast")
		case !pageID.MatchString(s.PageID):
			return invalid("invalid page ID")
		case s.View == "notifications" && s.PageID != "notifications/empty":
			return invalid("notification history is local")
		}
	case "telemetry":
		var raw struct{ Telemetry json.RawMessage }
		json.Unmarshal(m.Body, &raw)
		var b TelemetryBody
		json.Unmarshal(m.Body, &b)
		t := b.Telemetry
		null := len(t.Values) == 0 || string(t.Values) == "null"
		switch {
		case compactLen(raw.Telemetry) > maxTelemetryBytes:
			return invalid("telemetry too large")
		case t.Status == "unavailable" && !null:
			return invalid("unavailable must not invent values")
		case t.Status != "unavailable" && (null || t.SampleUnixMs == nil):
			return invalid("sample missing")
		}
		if !null {
			isList := bytes.HasPrefix(bytes.TrimSpace(t.Values), []byte("["))
			if isList != (t.Source == "ai") {
				return invalid("source/value mismatch")
			}
			if t.Source == "system" {
				var sv SystemValues
				json.Unmarshal(t.Values, &sv)
				if sv.MemoryUsedBytes > sv.MemoryTotalBytes {
					return invalid("memory exceeds total")
				}
			}
		}
	}
	return nil
}

func compactLen(raw []byte) int {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return len(raw)
	}
	return b.Len()
}
