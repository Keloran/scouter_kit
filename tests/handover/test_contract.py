"""Run: python3 -I -m unittest discover -s tests/handover -v"""

import base64
from copy import deepcopy
import hashlib
import hmac
import json
import struct
import unittest

from contract import (CONTROLLER, Invalid, POLICY, PROTOCOL, ROOT, PeerStream, check_schema,
                      decode_frame, frame, payload_checks, strict_json, validate, validate_state,
                      validate_step)


def load(name):
    return json.loads((ROOT / name).read_text())


MESSAGES = load("messages.json")
SEQUENCES = load("sequences.json")
SCENARIOS = load("transitions.json")["scenarios"]


def message(kind):
    return deepcopy(next(item["message"] for item in MESSAGES["valid"] if item["name"] == kind))


def scenario(name):
    return deepcopy(next(item for item in SCENARIOS if item["name"] == name))


class WireContractTests(unittest.TestCase):
    def test_supported_schemas(self):
        check_schema(PROTOCOL)
        check_schema(CONTROLLER)
        with self.assertRaises(Invalid):
            check_schema({"additionalPropertes": False})

    def test_valid_messages_round_trip(self):
        for case in MESSAGES["valid"]:
            with self.subTest(case=case["name"]):
                encoded = frame(case["message"])
                decoded = decode_frame(encoded)
                payload_checks(decoded)
                self.assertEqual(decoded, case["message"])

    def test_invalid_messages(self):
        for case in MESSAGES["invalid"]:
            with self.subTest(case=case["name"]), self.assertRaises(Invalid):
                payload_checks(decode_frame(frame(case["message"])))

    def test_replay_and_session_sequences(self):
        ids = SEQUENCES["identities"]
        for case in SEQUENCES["streams"]:
            stream = PeerStream(ids["sender"], ids["recipient"], ids["local_session"], **case["options"])
            for index, step in enumerate(case["steps"]):
                with self.subTest(case=case["name"], step=index):
                    if step["accept"]:
                        stream.receive(decode_frame(frame(step["message"])))
                    else:
                        with self.assertRaises(Invalid):
                            stream.receive(decode_frame(frame(step["message"])))
                    self.assertEqual({"seq": stream.last_seq, "epoch": stream.last_epoch},
                                     step["expected_watermarks"])

    def test_required_capabilities_supported_on_both_hosts(self):
        ids = SEQUENCES["identities"]
        stream = PeerStream(ids["sender"], ids["recipient"], ids["local_session"],
                            local_capabilities=POLICY["wire"]["required_capabilities"])
        packet = message("hello")
        packet["body"]["required_capabilities"].append("telemetry_v1")
        packet["body"]["optional_capabilities"].remove("telemetry_v1")
        with self.assertRaises(Invalid):
            stream.receive(packet)
        self.assertEqual(stream.last_seq, 0)
        stream.receive(message("hello"))

    def test_pinned_request_requires_phone_manual_action(self):
        ids = SEQUENCES["identities"]
        stream = PeerStream(ids["sender"], ids["recipient"], ids["local_session"])
        stream.receive(message("hello"))
        packet = message("request")
        packet.update(seq=2, epoch=1)
        packet["body"]["pinned"] = True
        with self.assertRaises(Invalid):
            stream.receive(packet)
        packet["body"]["reason"] = "manual"
        stream.receive(packet)

    def test_length_header_and_utf8(self):
        for raw in (b"", b"\x00\x00\x00", struct.pack(">I", 0), struct.pack(">I", 65537),
                    struct.pack(">I", 2) + b"{", struct.pack(">I", 1) + b"{}",
                    struct.pack(">I", 1) + b"\xff"):
            with self.subTest(raw=raw), self.assertRaises(Invalid):
                decode_frame(raw)
        value = message("hello")
        data = frame(value)
        self.assertEqual(struct.unpack(">I", data[:4])[0], len(data) - 4)
        # UTF-8 byte lengths, not code-point counts.
        raw = '"\u03bb"'.encode()
        self.assertEqual(strict_json(raw), "\u03bb")

    def test_duplicate_keys_and_nonfinite_numbers(self):
        for raw in (b'{"seq":1,"seq":2}', b'{"body":{"x":1,"x":2}}', b"NaN", b"Infinity",
                    b"-Infinity", b"1e999", b"{} {}", b'{"unterminated":'):
            with self.subTest(raw=raw), self.assertRaises(Invalid):
                strict_json(raw)

    def test_unicode_scalar_strings(self):
        for raw in (b'"\\ud800"', b'{"\\udfff":0}', b'{"body":["\\ud800"]}', b'"\\u0000"', b'"\\n"'):
            with self.subTest(raw=raw), self.assertRaises(Invalid):
                strict_json(raw)
        self.assertEqual(strict_json(b'"\\ud83d\\ude00"'), "\U0001f600")

    def test_size_and_depth_bounds(self):
        with self.assertRaises(Invalid):
            strict_json(b'"' + b"a" * 65535 + b'"')
        self.assertEqual(strict_json(b'"' + b"a" * 65534 + b'"'), "a" * 65534)
        strict_json(b"[" * 16 + b"0" + b"]" * 16)
        with self.assertRaises(Invalid):
            strict_json(b"[" * 17 + b"0" + b"]" * 17)
        self.assertEqual(strict_json(b'"[[[\\\"\\\\]]]"'), '[[["\\]]]')

    def test_numeric_json_schema_semantics(self):
        validate(1.0, {"type": "integer", "const": 1})
        for value in (True, 1.5, float("inf")):
            with self.subTest(value=value), self.assertRaises(Invalid):
                validate(value, {"type": "integer"})
        with self.assertRaises(Invalid):
            validate(True, {"const": 1})
        with self.assertRaises(Invalid):
            validate(10 ** 400, {"type": "number", "maximum": 100})


class OwnershipContractTests(unittest.TestCase):
    def test_portable_transition_fixtures(self):
        for case in SCENARIOS:
            before = case["initial"]
            validate_state(before)
            for index, step in enumerate(case["steps"]):
                with self.subTest(case=case["name"], step=index):
                    validate_step(before, step["event"], step["expected"], step["effects"])
                before = step["expected"]

    def test_no_ready_from_single_arm_or_missing_renderer(self):
        state = deepcopy(scenario("unenrolled_desktop_start_and_render")["steps"][-1]["expected"])
        for field in ("left", "right", "renderer"):
            value = deepcopy(state)
            if field == "renderer":
                value["renderer_ready"] = False
            else:
                value["arms"][field] = False
            with self.subTest(field=field), self.assertRaises(Invalid):
                validate_state(value)

    def test_acquisition_before_release_is_rejected(self):
        case = scenario("laptop_reclaims_only_after_matching_release")
        before = case["steps"][1]["expected"]
        step = case["steps"][3]
        step["event"]["data"]["transfer"] = "2" * 32
        with self.assertRaises(Invalid):
            validate_step(before, step["event"], step["expected"], step["effects"])
        step["event"] = {"kind": "timeout", "generation": before["generation"],
                         "attempt_nonce": None, "data": {"timer": "peer_suspect", "elapsed_ms": 15000}}
        step["expected"]["permission"] = "probe"
        with self.assertRaises(Invalid):
            validate_step(before, step["event"], step["expected"], step["effects"])

    def test_release_ack_requires_all_teardown_conditions(self):
        case = scenario("phone_yields_and_acknowledges_only_after_teardown")
        before, step = case["steps"][0]["expected"], case["steps"][-1]
        for field in ("both_down", "workers_stopped", "reconnect_disabled"):
            event = deepcopy(step["event"])
            event["data"][field] = False
            with self.subTest(field=field), self.assertRaises(Invalid):
                validate_step(before, event, step["expected"], step["effects"])

    def test_old_generation_and_wall_clock_cannot_change_owner(self):
        case = scenario("wall_clock_jump_does_not_change_deadlines")
        changed = deepcopy(case["initial"])
        changed["peer_reachable"] = True
        with self.assertRaises(Invalid):
            validate_step(case["initial"], case["steps"][0]["event"], changed, [])
        case = scenario("stale_callback_cannot_restore_old_owner")
        before, step = case["steps"][0]["expected"], case["steps"][1]
        changed = deepcopy(step["expected"])
        changed["peer_reachable"] = True
        with self.assertRaises(Invalid):
            validate_step(before, step["event"], changed, step["effects"])

    def test_network_loss_keeps_active_owner(self):
        case = scenario("network_partition_preserves_healthy_owner")
        step = case["steps"][0]
        changed = deepcopy(step["expected"])
        changed.update(phase="degraded", renderer_ready=False)
        with self.assertRaises(Invalid):
            validate_step(case["initial"], step["event"], changed, [])

    def test_probe_deadline_and_generation_are_enforced(self):
        case = scenario("unreachable_owner_only_allows_late_nonpreemptive_probe")
        step = case["steps"][1]
        step["event"]["data"]["elapsed_ms"] = 16999
        with self.assertRaises(Invalid):
            validate_step(case["initial"], step["event"], step["expected"], step["effects"])
        case = scenario("cold_phone_probe_after_asymmetric_start_delay")
        step = case["steps"][1]
        step["expected"]["generation"] = 0
        for effect in step["effects"]:
            effect["generation"] = 0
        with self.assertRaises(Invalid):
            validate_step(case["initial"], step["event"], step["expected"], step["effects"])

    def test_unknown_event_and_ineligible_acquisition_are_rejected(self):
        case = scenario("unenrolled_desktop_start_and_render")
        step = case["steps"][0]
        step["event"]["kind"] = "steal_owner"
        with self.assertRaises(Invalid):
            validate_step(case["initial"], step["event"], step["expected"], step["effects"])
        step["expected"]["eligible"] = False
        with self.assertRaises(Invalid):
            validate_state(step["expected"])

    def test_new_transfer_requires_injected_nonce(self):
        case = scenario("unenrolled_desktop_start_and_render")
        step = case["steps"][0]
        step["event"]["attempt_nonce"] = None
        with self.assertRaises(Invalid):
            validate_step(case["initial"], step["event"], step["expected"], step["effects"])

    def test_phone_pin_and_laptop_preference_guards(self):
        case = scenario("phone_yields_and_acknowledges_only_after_teardown")
        before = case["initial"]
        before["pinned"] = True
        step = case["steps"][0]
        step["expected"]["pinned"] = True
        with self.assertRaises(Invalid):
            validate_step(before, step["event"], step["expected"], step["effects"])
        case = scenario("stale_callback_cannot_restore_old_owner")
        step = case["steps"][0]
        step["event"]["data"].update(reason="preference", peer_pinned=False)
        with self.assertRaises(Invalid):
            validate_step(case["initial"], step["event"], step["expected"], step["effects"])

    def test_fixture_coverage_and_timing_policy(self):
        kinds = {step["event"]["kind"] for case in SCENARIOS for step in case["steps"]}
        expected = {choice["properties"]["kind"]["const"]
                    for choice in CONTROLLER["$defs"]["event"]["oneOf"]}
        self.assertEqual(kinds, expected)
        self.assertEqual(len(SCENARIOS), len({case["name"] for case in SCENARIOS}))
        timers = POLICY["timers_ms"]
        self.assertGreaterEqual(timers["acquire_deadline"], 30000)
        self.assertLess(timers["release_target"], timers["release_deadline"])
        self.assertLessEqual(timers["suspend_cleanup_budget"], 1000)
        self.assertGreater(timers["peer_suspect"], 2 * timers["heartbeat"])
        self.assertEqual(set(POLICY["snapshot"]["allow"]),
                         set(PROTOCOL["$defs"]["snapshot"]["properties"]))

    def test_enrollment_vectors(self):
        vectors = json.loads((ROOT / "enrollment.json").read_text())
        secret = bytes.fromhex(vectors["secret_hex"])
        host, guest, role = vectors["host_id"], vectors["guest_id"], vectors["guest_role"]

        def mac(label, *parts):
            text = "chewedfeed-enroll-v1/" + label + "".join("\n" + part for part in parts)
            return hmac.new(secret, text.encode(), hashlib.sha256).digest()
        self.assertEqual(mac("request", host, guest, role).hex(), vectors["proof_hex"])
        self.assertEqual(mac("confirm", host, guest, role).hex(), vectors["confirm_hex"])
        code = int.from_bytes(mac("code", host, guest)[:4], "big") % 1000000
        self.assertEqual("%06d" % code, vectors["code"])
        encoded = base64.urlsafe_b64encode(json.dumps(vectors["invitation"], separators=(",", ":")).encode())
        self.assertEqual("CFE1." + encoded.rstrip(b"=").decode(), vectors["invitation_text"])
        self.assertEqual(len(secret), 32)


if __name__ == "__main__":
    unittest.main()
