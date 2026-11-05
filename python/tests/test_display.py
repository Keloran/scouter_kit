"""The gray display end to end on the emulated arms, read back from both panels."""

import re
import threading
import time

import numpy as np
import pytest
from PIL import Image

import scouterkit as sk


def panels():
    return np.asarray(sk.left.emulated_panel()), np.asarray(sk.right.emulated_panel())


def test_draw_flush_and_read_back(glasses):
    assert sk.size() == glasses.size == (740, 471)
    ramp = Image.fromarray(np.tile(np.linspace(0, 255, 300).astype(np.uint8), (200, 1)))
    picture = sk.load_bitmap(ramp)
    font = sk.load_font(sk.departure_mono(), scale=2)
    line = sk.create_bitmap(600, 40)
    width = font.draw_line(line, "Read back on both eyes", 300, 30, sk.Align.CENTER)
    assert width == font.measure("Read back on both eyes").width > 0
    sk.draw(picture, 0, 0)
    sk.draw(line, 70, 420)
    sk.flush()
    for arm in (sk.left, sk.right):
        got, want = arm.panel_crc()
        assert got == want
    shown = np.asarray(sk.display())
    assert np.array_equal(shown[0:200, 0:300], np.asarray(picture.image()))
    assert np.array_equal(shown[420:460, 70:670], np.asarray(line.image()))
    assert shown[200:420].max() == 0
    left, right = panels()
    assert np.array_equal(left[0:471], shown[:, 0:640]) and left[471:].max() == 0
    assert np.array_equal(right[9:480], shown[:, 100:740]) and right[:9].max() == 0
    assert sk.left.state().shown_update == sk.right.state().shown_update == 1

    sk.clear()
    sk.draw(line, 70, 100)
    sk.flush()
    shown = np.asarray(sk.display())
    assert shown[0:100].max() == 0 and np.array_equal(shown[100:140, 70:670], np.asarray(line.image()))
    left, right = panels()
    assert np.array_equal(left[0:471], shown[:, 0:640])
    assert np.array_equal(right[9:480], shown[:, 100:740])


def test_handles(glasses):
    total, free = sk.total_memory(), sk.free_memory()
    assert total == free > 200_000
    bitmap = sk.create_bitmap(100, 100)
    assert sk.free_memory() < free and bitmap.address and bitmap.stride == 50
    bitmap.unload()
    bitmap.unload()
    assert sk.free_memory() == free and not bitmap.loaded
    with pytest.raises(sk.ScouterKitError):
        sk.draw(bitmap, 0, 0)
    with sk.create_bitmap(8, 8) as scoped:
        assert scoped.loaded
    assert not scoped.loaded
    font = sk.load_font(sk.departure_mono(), "0123456789:", 2)
    assert font.glyph_indices("09:") == [0, 9, font.glyph_indices(":")[0]] and font.address
    sk.unload_all()
    assert not font.loaded and sk.free_memory() == free
    with pytest.raises(sk.ScouterKitError):
        font.measure("1")
    with pytest.raises(sk.DeviceMemoryError):
        sk.create_bitmap(2000, 2000)
    with pytest.raises(sk.ScouterKitError):
        sk.load_font(b"not a font")


def test_not_initialized():
    assert sk.size() == (0, 0)
    with pytest.raises(sk.NotInitializedError):
        sk.create_bitmap(8, 8)
    with pytest.raises(sk.NotInitializedError):
        sk.load_font(sk.departure_mono())
    with pytest.raises(sk.NotInitializedError):
        sk.alloc(64)


def test_init_errors(glasses):
    with pytest.raises(sk.ScouterKitError, match="already"):
        sk.init()
    sk.deinit()
    with pytest.raises(sk.BluetoothError):
        sk.init(adapter="hci7", timeout=10)


def test_kept_sessions():
    with sk.init(left="none", keep_sessions=True) as session:
        assert session.keep_sessions and sk.total_memory() == 0
        assert not sk.left.state().present and sk.right.state().connected
        with pytest.raises(sk.DeviceMemoryError):
            sk.alloc(64)
        with pytest.raises(sk.ScouterKitError, match="keep_sessions"):
            sk.create_bitmap(8, 8)


def test_in_case_and_log(capfd):   # no case hold (VM.md §6.6, 0.5.60): the panel keeps its image in the case
    with sk.init(left="none", log=True):
        bitmap = sk.create_bitmap(64, 64)
        bitmap.fill((0, 0, 64, 64), 255)
        sk.draw(bitmap, 300, 100)
        sk.flush()
        before = np.asarray(sk.right.emulated_panel())
        assert before.max() == 255
        sk.right.set_emulated_property(sk.Prop.IN_CASE, 1)
        time.sleep(0.3)
        assert np.array_equal(np.asarray(sk.right.emulated_panel()), before)
    assert re.search(r"^\[scouter_kit_g1 \d+\.\d{3}\] right arm: ", capfd.readouterr().err, re.M)


def test_threads(glasses):
    bitmap = sk.create_bitmap(64, 64)
    errors = []

    def draw():
        try:
            for i in range(20):
                bitmap.fill((0, 0, 64, 64), 17 * (i % 16))
                sk.draw(bitmap, 120 + i, i)
                sk.flush()
        except Exception as e:
            errors.append(e)

    threads = [threading.Thread(target=draw) for _ in range(3)]
    for t in threads:
        t.start()
    while any(t.is_alive() for t in threads):
        sk.display()
        sk.left.next_event(0.01)
    assert not errors
    for arm in (sk.left, sk.right):
        got, want = arm.panel_crc()
        assert got == want
