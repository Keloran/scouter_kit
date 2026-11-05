import numpy as np
import pytest
from PIL import Image

import scouterkit as sk


def pixels(image):
    return list(image.tobytes())


def test_luma_of_colours():
    img = Image.new("RGB", (4, 1))
    img.putdata([(255, 0, 0), (0, 255, 0), (0, 0, 255), (255, 255, 255)])
    assert pixels(sk.to_gray(img)) == [76, 150, 29, 255]


def test_transparent_is_black():
    rgba = Image.new("RGBA", (2, 1))
    rgba.putdata([(255, 255, 255, 128), (255, 255, 255, 0)])
    assert pixels(sk.to_gray(rgba)) == [128, 0]
    la = Image.new("LA", (1, 1), (200, 0))
    assert pixels(sk.to_gray(la)) == [0]
    pal = Image.new("P", (2, 1))
    pal.putpalette([255, 255, 255, 255, 255, 255])
    pal.putdata([0, 1])
    pal.info["transparency"] = 0
    assert pixels(sk.to_gray(pal)) == [0, 255]


def test_gray_is_kept_and_16_bit_scaled():
    gray = Image.new("L", (3, 3), 77)
    assert sk.to_gray(gray) is gray
    deep = Image.new("I;16", (3, 1))
    deep.putdata([0, 32896, 65535])
    assert pixels(sk.to_gray(deep)) == [0, 128, 255]


def test_arrays():
    assert pixels(sk.to_gray(np.array([[0, 128, 255]], np.uint8))) == [0, 128, 255]
    assert pixels(sk.to_gray(np.array([[False, True]]))) == [0, 255]
    assert pixels(sk.to_gray(np.array([[0.0, 0.5, 1.0, 2.0, -1.0]]))) == [0, 128, 255, 255, 0]
    assert pixels(sk.to_gray(np.array([[300, -5, 17]]))) == [255, 0, 17]
    rgb = np.zeros((1, 2, 3), np.uint8)
    rgb[0, 0] = (255, 0, 0)
    assert pixels(sk.to_gray(rgb)) == [76, 0]
    rgba = np.full((1, 2, 4), 255, np.uint8)
    rgba[0, 1, 3] = 0
    assert pixels(sk.to_gray(rgba)) == [255, 0]
    assert sk.to_gray(np.zeros((2, 3, 1), np.uint8)).size == (3, 2)
    with pytest.raises(ValueError):
        sk.to_gray(np.zeros((2, 2, 2), np.uint8))


def test_gamma_response():
    assert sk.gamma_response(1) == bytes(range(256))
    curve = sk.gamma_response(2.2)
    assert (curve[0], curve[128], curve[255]) == (0, 56, 255)


def test_levels_are_kept_exactly(glasses):
    levels = np.tile(np.repeat(np.arange(16, dtype=np.uint8) * 17, 3), (4, 1))
    bitmap = sk.load_bitmap(levels)
    assert bitmap.size == (48, 4)
    assert np.array_equal(np.asarray(bitmap.image()), levels)


def test_floyd_steinberg_keeps_the_mean(glasses):
    dithered = np.asarray(sk.load_bitmap(np.full((32, 32), 128, np.uint8)).image())
    assert set(np.unique(dithered)) == {119, 136}
    assert abs(dithered.mean() - 128) < 0.5
    rounded = np.asarray(sk.load_bitmap(np.full((4, 4), 128, np.uint8), dither=False).image())
    assert set(np.unique(rounded)) == {136}


def test_ramp_gets_16_levels(glasses):
    ramp = np.tile(np.arange(256, dtype=np.uint8), (16, 1))
    out = np.asarray(sk.load_bitmap(Image.fromarray(ramp)).image()).astype(float)
    assert np.all(out % 17 == 0) and len(np.unique(out)) == 16
    blocks = out.reshape(16, 16, 16).mean(axis=(0, 2)) - ramp.reshape(16, 16, 16).mean(axis=(0, 2))
    assert np.abs(blocks).max() < 3


def test_response_curve(glasses):
    try:
        sk.set_response(bytes(255 - v for v in range(256)))
        assert sk.response()[:2] == b"\xff\xfe"
        assert np.asarray(sk.load_bitmap(np.array([[0, 255]], np.uint8)).image()).tolist() == [[255, 0]]
    finally:
        sk.set_response(range(256))
    assert sk.response() == bytes(range(256))
    with pytest.raises(ValueError):
        sk.set_response(b"short")


def test_write_and_fill(glasses):
    bitmap = sk.create_bitmap(10, 4)
    bitmap.write(Image.new("L", (2, 2), 255), 3, 1)
    bitmap.fill((9, 4, 7, 2), 85)
    want = np.zeros((4, 10), np.uint8)
    want[1:3, 3:5] = 255
    want[2:4, 7:9] = 85
    assert np.array_equal(np.asarray(bitmap.image()), want)
