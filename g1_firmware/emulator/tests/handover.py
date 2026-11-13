"""Firmware takeover checks; the virtual central has no SMP pairing or link encryption."""

import struct
import zlib

import common

g = common.g


def handover_body(e, res, alternate=False):
    """Retained device state, a new synthetic initiator and a fresh reference-client session."""
    old = e.arm
    old.reset_session(clear_arena=True)
    marker = old.alloc(32)
    bitmap = old.alloc(16 * 8)
    old.write_arena(marker, b"\x5a" * 32)
    old.fill(bitmap, 16, 0, 0, 32, 8, 9)
    old.wait_draw(old.draw(bitmap, 16, 40, 80, 32, 8))
    old.wait_idle(10)
    panel_before = old.panel_read(80, 8)
    assert panel_before["status"] == 0 and panel_before["match"], panel_before

    # The old program has a handler and a counter. Disconnect must not be mistaken for
    # a new VM session; explicit RESET_SESSION is what discards the program and its state.
    code = g.Code().ins("ADD", 64, 64, old.k(1)).ins("END")
    table = [0] * 16
    table[12] = 0xC000
    old.write(0xC000, code.b)
    old.write(0x40, struct.pack("<16I", *table))
    old.wait_idle(10)
    assert old.vm_start(0x40) == 0
    mask = old.events_mask(1 << 5)  # BATTERY: available on every protocol-5 arm
    assert mask == 1 << 5, mask
    epoch = old.epoch
    before = old.losses()

    if alternate:
        old.tr.set_model("sysbus.vphone", "UseAlternateIdentity", "true")
    old.tr.go_silent(3000)
    old.run_until(lambda: old.losses() > before, 30, "old link dropped and advertising restarted")
    old.setup()
    replacement = g.Arm(old.tr, name="replacement", log=e.note)
    e.arm = replacement  # finish/stop now observes only the new reference-client instance
    hello = replacement.hello()
    assert hello["epoch"] == epoch and hello["in_case"] and not hello["encrypted"], hello
    assert hello["boot_count"] == res["hello"]["boot_count"], "link change rebooted the arm"
    replacement.tr.call_model("sysbus.vphone", "ConnectionIdentity")
    identity = "alternate" if alternate else "primary"
    replacement.run_until(lambda: any(f"result ConnectionIdentity {identity}" in line for line in e.lines),
                          5, "the initiator identity used by CONNECT_IND")
    assert replacement.vm_status()["state"] == 1, "disconnect unexpectedly erased the old program"
    assert replacement.events_mask() == 0, "old host's input subscription survived disconnection"
    retained = replacement.arena_crc(4 * marker, 32)
    assert retained["crc"] == f"{zlib.crc32(bytes([0x5a]) * 32):08x}", retained

    replacement.reset_session(clear_arena=True)
    assert replacement.epoch != epoch, "takeover did not change epoch"
    status = replacement.vm_status()
    assert status["state"] == 0 and status["tasks"] == 0, status
    cleared = replacement.arena_crc()
    assert cleared["crc"] == f"{zlib.crc32(bytes(g.ARENA_SIZE)):08x}", cleared
    assert replacement.events_mask() == 0
    assert replacement.hello()["rx_next"] == 0 and replacement.exec_next == 0

    # RESET_SESSION clears the arena, not the panel's RAM or power. Verify the distinction so hosts do not
    # treat a changed epoch as a privacy blank. The coordinated release path must blank before disconnect.
    retained_panel = replacement.panel_read(80, 8)
    assert retained_panel["status"] == 0 and retained_panel["crc"] == panel_before["crc"], retained_panel
    replacement.set_panel(0)
    replacement.wait_idle(10)
    assert not replacement.hello()["panel_on"]
    replacement.ins("RAISE", 12)
    replacement.wait_idle(10)
    assert replacement.vm_status()["tasks"] == 0, "old trigger table still ran"

    # A fresh full frame overwrites the retained panel instead of reviving old content outside a small draw.
    frame = replacement.alloc(g.FRAME_H * g.FRAME_STRIDE)
    replacement.fill(frame, g.FRAME_STRIDE, 0, 0, g.FRAME_W, g.FRAME_H, 0)
    replacement.fill(frame, g.FRAME_STRIDE, 4, 4, 16, 8, 3)
    replacement.wait_draw(replacement.draw(frame, g.FRAME_STRIDE, 0, g.PANEL_Y, g.FRAME_W, g.FRAME_H))
    replacement.wait_idle(10)
    fresh = replacement.panel_read(80, 8)
    assert fresh["status"] == 0 and fresh["match"] and fresh["crc"] != panel_before["crc"], fresh
    assert not replacement.errors, replacement.errors
    res["handover"] = {
        "same_epoch_after_disconnect": hello["epoch"] == epoch,
        "new_epoch_after_reset": replacement.epoch != epoch,
        "old_input_mask_cleared": True,
        "old_arena_cleared": True,
        "old_program_stopped": True,
        "reset_retains_panel_image": True,
        "explicit_panel_off": True,
        "new_frame_matches": fresh["match"],
        "alternate_initiator_in_case": alternate,
        "real_bond_switching_covered": False,
        "encrypted_takeover_covered": False,
    }
