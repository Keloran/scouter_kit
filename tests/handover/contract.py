"""Test-only v1 contract checks; no networking, BLE or production reducer."""

import json
import math
from pathlib import Path
import re
import struct


ROOT = Path(__file__).resolve().parent
POLICY = json.loads((ROOT / "policy.json").read_text())


class Invalid(ValueError):
    pass


def require(condition, reason):
    if not condition:
        raise Invalid(reason)


def resolve(root, reference):
    require(reference.startswith("#/"), "only local schema references")
    node = root
    for part in reference[2:].split("/"):
        node = node[part.replace("~1", "/").replace("~0", "~")]
    return node


def check_schema(schema, root=None):
    """Fail closed on keywords outside the JSON Schema subset used here."""
    root = root or schema
    supported = {"$schema", "$defs", "$ref", "title", "description", "type", "enum", "const",
                 "oneOf", "properties", "required", "additionalProperties", "items", "maxItems",
                 "minimum", "maximum", "minLength", "maxLength", "pattern"}
    require(isinstance(schema, dict) and not set(schema) - supported, "unsupported schema keyword")
    if "$ref" in schema:
        resolve(root, schema["$ref"])
    if "pattern" in schema:
        re.compile(schema["pattern"])
    for keyword in ("$defs", "properties"):
        for value in schema.get(keyword, {}).values():
            check_schema(value, root)
    for value in schema.get("oneOf", []):
        check_schema(value, root)
    if "items" in schema:
        check_schema(schema["items"], root)


def validate(value, schema, root=None):
    """Dependency-free evaluator for the explicitly checked schema subset."""
    root = root or schema
    if "$ref" in schema:
        validate(value, resolve(root, schema["$ref"]), root)
    if "oneOf" in schema:
        matches = 0
        for choice in schema["oneOf"]:
            try:
                validate(value, choice, root)
                matches += 1
            except Invalid:
                pass
        require(matches == 1, "oneOf must match exactly once")
    # JSON Schema equality distinguishes booleans from numbers, not 1 from 1.0.
    def equal(a, b):
        numeric = type(a) in (int, float) and type(b) in (int, float)
        return (numeric or type(a) is type(b)) and a == b
    if "const" in schema:
        require(equal(value, schema["const"]), "constant mismatch")
    if "enum" in schema:
        require(any(equal(value, item) for item in schema["enum"]), "unknown enum")
    kinds = {"object": lambda v: type(v) is dict, "array": lambda v: type(v) is list,
             "string": lambda v: type(v) is str, "boolean": lambda v: type(v) is bool,
             "integer": lambda v: type(v) is int or
             (type(v) is float and math.isfinite(v) and v.is_integer()),
             "number": lambda v: type(v) is int or (type(v) is float and math.isfinite(v)),
             "null": lambda v: v is None}
    if "type" in schema:
        require(kinds[schema["type"]](value), "wrong JSON type")
    if type(value) is dict:
        props = schema.get("properties", {})
        require(set(schema.get("required", [])) <= value.keys(), "missing field")
        if schema.get("additionalProperties") is False:
            require(value.keys() <= props.keys(), "unknown field")
        for name, item in value.items():
            if name in props:
                validate(item, props[name], root)
    if type(value) is list:
        require(len(value) <= schema.get("maxItems", len(value)), "too many items")
        if "items" in schema:
            for item in value:
                validate(item, schema["items"], root)
    if type(value) is str:
        require(len(value) >= schema.get("minLength", 0), "short string")
        require(len(value) <= schema.get("maxLength", len(value)), "long string")
        if "pattern" in schema:
            require(re.search(schema["pattern"], value) is not None, "pattern mismatch")
    if type(value) in (int, float):
        require(type(value) is int or math.isfinite(value), "nonfinite number")
        require(value >= schema.get("minimum", value), "number too small")
        require(value <= schema.get("maximum", value), "number too large")


def load_schema(name):
    schema = json.loads((ROOT / name).read_text())
    check_schema(schema)
    return schema


PROTOCOL = load_schema("protocol.schema.json")
CONTROLLER = load_schema("controller.schema.json")


def strict_json(payload):
    require(len(payload) <= POLICY["wire"]["max_frame_bytes"], "frame too large")
    # Bound nesting before json.loads can recurse. Ignore delimiters inside strings.
    depth, quoted, escaped = 0, False, False
    for ch in payload.decode("utf-8", errors="strict"):
        if quoted:
            if escaped:
                escaped = False
            elif ch == "\\":
                escaped = True
            elif ch == '"':
                quoted = False
        elif ch == '"':
            quoted = True
        elif ch in "[{":
            depth += 1
            require(depth <= POLICY["wire"]["max_depth"], "nesting too deep")
        elif ch in "]}":
            depth -= 1
    def pairs(entries):
        result = {}
        for key, value in entries:
            require(key not in result, "duplicate key")
            result[key] = value
        return result
    def constant(_):
        raise Invalid("nonfinite number")
    def floating(token):
        value = float(token)
        require(math.isfinite(value), "nonfinite number")
        return value
    try:
        value = json.loads(payload, object_pairs_hook=pairs, parse_constant=constant, parse_float=floating)
    except (ValueError, RecursionError) as error:
        raise Invalid("invalid JSON") from error
    def scalar_strings(node):
        if type(node) is str:
            require(not any(0xD800 <= ord(ch) <= 0xDFFF for ch in node), "unpaired surrogate")
            require(not any(ord(ch) < 0x20 or ord(ch) == 0x7F for ch in node), "control character")
        elif type(node) is dict:
            for key, item in node.items():
                scalar_strings(key)
                scalar_strings(item)
        elif type(node) is list:
            for item in node:
                scalar_strings(item)
    scalar_strings(value)
    return value


def frame(message):
    payload = json.dumps(message, allow_nan=False, separators=(",", ":")).encode("utf-8")
    return struct.pack(">I", len(payload)) + payload


def decode_frame(data):
    require(len(data) >= 4, "truncated header")
    size = struct.unpack(">I", data[:4])[0]
    require(0 < size <= POLICY["wire"]["max_frame_bytes"], "invalid frame length")
    require(len(data) == size + 4, "truncated or trailing frame")
    try:
        message = strict_json(data[4:])
    except UnicodeError as error:
        raise Invalid("invalid UTF-8") from error
    validate(message, PROTOCOL)
    return message


def payload_checks(message):
    kind, body = message["kind"], message["body"]
    if kind == "hello":
        required = body["required_capabilities"]
        baseline = set(POLICY["wire"]["required_capabilities"])
        known = baseline | set(POLICY["wire"]["optional_capabilities"])
        require(baseline <= set(required) <= known, "unsupported required capabilities")
        require(len(required) == len(set(required)), "duplicate required capability")
        optional = body["optional_capabilities"]
        require(len(optional) == len(set(optional)), "duplicate optional capability")
        require(not set(required) & set(optional), "ambiguous capabilities")
    if kind in ("ready", "released"):
        wanted = kind == "ready"
        require(all(value == wanted for value in body["arms"].values()), "wrong arm readiness")
    if kind == "status":
        require(body["ready_epoch"] <= message["epoch"], "future ready epoch")
        if body["phase"] == "active":
            require(all(body["arms"].values()) and body["renderer_ready"], "active before ready")
            require(body["ready_epoch"] == message["epoch"] and body["transfer"] is not None,
                    "active with stale attempt")
        if body["phase"] in ("idle", "requesting", "recovering"):
            require(not any(body["arms"].values()) and not body["renderer_ready"], "inactive retains arms")
        if body["role"] == "laptop":
            require(not body["pinned"], "laptop pin unsupported")
    if kind == "snapshot":
        data = body["snapshot"]
        require(len(json.dumps(data).encode()) <= POLICY["wire"]["max_snapshot_bytes"], "snapshot too large")
        require(data["rotation_ms"] == 0 or data["rotation_ms"] >= 1000, "rotation too fast")
        require(re.fullmatch(r"[A-Za-z0-9_:/-]+", data["page_id"]) is not None, "invalid page ID")
        if data["view"] == "notifications":
            require(data["page_id"] == "notifications/empty", "notification history is local")
    if kind == "telemetry":
        data = body["telemetry"]
        require(len(json.dumps(data).encode()) <= POLICY["wire"]["max_telemetry_bytes"], "telemetry too large")
        if data["status"] == "unavailable":
            require(data["values"] is None, "unavailable must not invent values")
        else:
            require(data["values"] is not None and data["sample_unix_ms"] is not None, "sample missing")
        if data["values"] is not None:
            require((type(data["values"]) is list) == (data["source"] == "ai"), "source/value mismatch")
            if data["source"] == "system":
                require(data["values"]["memory_used_bytes"] <= data["values"]["memory_total_bytes"], "memory exceeds total")


class PeerStream:
    """Conformance oracle after transport authentication, never authentication itself."""

    def __init__(self, sender, recipient, local_session, retired=(), pending=None, local_capabilities=None):
        self.sender, self.recipient, self.local_session = sender, recipient, local_session
        self.retired, self.pending = set(retired), pending
        self.session, self.role, self.last_seq, self.last_epoch = None, None, 0, 0
        self.release_epoch, self.active, self.capabilities = None, False, set()
        self.local_capabilities = set(local_capabilities if local_capabilities is not None else
                                      POLICY["wire"]["required_capabilities"] +
                                      POLICY["wire"]["optional_capabilities"])

    def receive(self, message):
        validate(message, PROTOCOL)
        payload_checks(message)
        require(message["sender"] == self.sender and message["recipient"] == self.recipient,
                "wrong enrolled peer")
        require(message["sender"] != message["recipient"], "self message")
        kind, body = message["kind"], message["body"]
        require(message["session"] not in self.retired, "retired session")
        require(message["seq"] > self.last_seq and message["epoch"] >= self.last_epoch, "replay or stale epoch")
        if self.session is None:
            require(kind == "hello", "hello required first")
            require(message["peer_session"] in (None, self.local_session), "wrong recipient session")
            require(set(body["required_capabilities"]) <= self.local_capabilities,
                    "locally unsupported required capabilities")
        else:
            require(kind != "hello" and message["session"] == self.session, "session changed without handshake")
            require(message["peer_session"] == self.local_session, "wrong recipient session")
        if kind == "status":
            require(body["role"] == self.role, "role changed")
        if kind in ("request", "acquiring", "ready"):
            require(body["target"] == self.role, "wrong incoming target")
        if kind == "request" and body["pinned"]:
            require(self.role == "phone" and body["reason"] == "manual", "invalid pinned request")
        if kind in ("release", "released"):
            require(body["target"] != self.role, "wrong release target")
            require(self.pending in (None, message["transfer"]), "wrong transfer")
            if kind == "release":
                require(self.pending is not None or self.active, "unsolicited release without known owner")
            else:
                require(self.pending == message["transfer"] and self.release_epoch == message["epoch"],
                        "released without matching owner release")
        if kind == "snapshot":
            require("snapshot_v1" in self.capabilities and self.pending == message["transfer"], "unnegotiated/stale snapshot")
        if kind == "telemetry":
            require("telemetry_v1" in self.capabilities, "unnegotiated telemetry")
        # Commit receive watermarks only after every guard passed.
        self.session, self.last_seq, self.last_epoch = message["session"], message["seq"], message["epoch"]
        if kind == "hello":
            self.role = body["role"]
            self.capabilities = set(body["required_capabilities"] + body["optional_capabilities"]) & self.local_capabilities
        if kind == "status":
            self.active = body["phase"] == "active"
        if kind == "ready":
            self.active = True
        if kind == "release":
            self.pending, self.release_epoch = message["transfer"], message["epoch"]
        if kind == "released":
            self.pending, self.release_epoch, self.active = None, None, False


def validate_state(state):
    validate(state, CONTROLLER["$defs"]["state"], CONTROLLER)
    require(not state["pinned"] or state["role"] == "phone", "laptop pin unsupported")
    require(not state["peer_pinned"] or state["role"] == "laptop", "peer laptop cannot pin")
    if state["phase"] == "active":
        require(state["eligible"] and all(state["arms"].values()) and state["renderer_ready"], "active before ready")
        require(state["permission"] != "none", "active without permission")
        require(not (state["peer_active"] and state["peer_reachable"]), "two active owners")
    if state["renderer_ready"]:
        require(state["phase"] == "active", "renderer outside active session")
    if state["phase"] == "acquiring":
        require(state["eligible"] and state["permission"] != "none", "acquiring without permission")
        require(not (state["peer_active"] and state["peer_reachable"]), "known owner not released")
    if state["permission"] == "standalone":
        require(not state["enrolled"], "standalone permission on enrolled host")
    if state["phase"] in ("idle", "requesting", "recovering"):
        require(not any(state["arms"].values()) and not state["renderer_ready"], "inactive state retains resources")


def validate_step(before, event, after, effects):
    """Check temporal safety; expected behaviour is in portable transition fixtures."""
    validate_state(before)
    validate_state(after)
    validate(event, CONTROLLER["$defs"]["event"], CONTROLLER)
    for effect in effects:
        validate(effect, CONTROLLER["$defs"]["effect"], CONTROLLER)
        require(effect["generation"] == after["generation"], "effect generation mismatch")
        require(effect["transfer"] == after["transfer"], "effect transfer mismatch")
    names = [effect["kind"] for effect in effects]
    if event["generation"] != before["generation"]:
        require(before == after and names == ["ignore"], "stale callback changed state")
    require(after["generation"] >= before["generation"], "generation regressed")
    if "send_request" in names:
        require(after["eligible"] and after["peer_reachable"], "request without eligible local/control path")
        if after["role"] == "phone":
            require(after["pinned"] or not after["peer_eligible"], "phone violates laptop preference")
        else:
            require(not (after["peer_pinned"] and after["peer_eligible"]) and event["kind"] == "prefer_local" and
                    event["data"]["stable_ms"] >= POLICY["timers_ms"]["laptop_stability"],
                    "laptop preference unstable or phone pinned")
    if after["transfer"] != before["transfer"]:
        if event["kind"] in ("release_request", "release_notice"):
            require(after["transfer"] == event["data"]["transfer"], "wrong incoming transfer")
        else:
            require(after["transfer"] is not None and after["transfer"] == event["attempt_nonce"],
                    "new transfer lacks injected nonce")
    if "acquire_both" in names:
        require(after["phase"] == "acquiring" and after["generation"] > before["generation"], "unfenced acquisition")
        require(after["transfer"] is not None, "acquire missing attempt ID")
        if before["peer_reachable"] and before["peer_active"]:
            require(event["kind"] == "released" and event["data"]["transfer"] == before["transfer"],
                    "acquisition before owner release")
        if after["permission"] == "released":
            require(event["kind"] == "released" and event["data"]["transfer"] == before["transfer"], "no release proof")
        if after["permission"] == "probe":
            require(not before["peer_reachable"] and event["kind"] == "timeout", "probe with control path")
            timer = event["data"]["timer"]
            require(timer in ("peer_suspect", "retry", "cold_start"), "probe before suspicion")
            extra = POLICY["timers_ms"]["phone_cold_start_delay"] if after["role"] == "phone" else 0
            if timer == "cold_start":
                require(not before["peer_active"], "cold probe of previous owner")
                minimum = extra
            else:
                key = "peer_suspect" if timer == "peer_suspect" else "retry_initial"
                minimum = POLICY["timers_ms"][key] + extra
            require(event["data"]["elapsed_ms"] >= minimum, "early probe")
    if "send_released" in names:
        require(event["kind"] == "release_complete" and all(event["data"].values()), "release not confirmed")
        require(not any(after["arms"].values()) and not after["renderer_ready"], "released retains links")
    if "send_ready" in names or "redraw" in names:
        require(after["phase"] == "active" and event["kind"] == "initialized", "premature ready")
        require(event["data"]["renderer_ready"], "renderer not ready")
        if "send_ready" in names:
            require("redraw" in names and names.index("redraw") < names.index("send_ready"), "ready before redraw")
    if "release_both" in names:
        require(after["generation"] > before["generation"] and not after["renderer_ready"], "release not fenced")
        if event["kind"] == "release_request":
            data = event["data"]
            require(data["target"] != before["role"], "release to self")
            require(not (before["role"] == "phone" and before["pinned"] and before["eligible"] and data["reason"] != "manual"),
                    "pinned phone yielded")
            if before["role"] == "laptop" and before["eligible"]:
                require(data["reason"] == "manual" and data["peer_pinned"], "laptop preference violated")
    if event["kind"] == "clock_adjusted":
        require(before == after and names == ["ignore"], "wall clock changed ownership")
    if event["kind"] == "route_lost" and before["phase"] == "active":
        require(after["phase"] == "active" and "release_both" not in names, "partition evicted healthy owner")
