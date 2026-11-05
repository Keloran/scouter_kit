"""Shows a picture (PNG, JPEG, the first frame of a GIF, ...) on both eyes, then reads both
panels back to check them.

    python examples/image.py [--gamma 2.2] [--save display.png] [--emulator] FILE
"""

from __future__ import annotations

import sys
import time

from PIL import Image

import common
import scouterkit as sk


def trim(gray: Image.Image, threshold: int = 8) -> Image.Image:
    """Cuts off the dark margin: black is see-through on the glasses."""
    box = gray.point(lambda v: 255 if v > threshold else 0).getbbox()
    return gray.crop(box) if box else gray


def fit(gray: Image.Image, width: int, height: int) -> Image.Image:
    """Shrinks the picture to fit, averaging the pixels under each new one; one that fits
    stays 1:1."""
    w, h = gray.size
    if w <= width and h <= height:
        return gray
    dw, dh = width, max(1, h * width // w)
    if dh > height:
        dw, dh = max(1, w * height // h), height
    return gray.resize((dw, dh), Image.Resampling.BOX)


def main(argv: list[str] | None = None) -> int:
    p = common.parser(__doc__.splitlines()[0])
    p.add_argument("--gamma", type=float, default=1.0,
                   help="tone curve exponent before dithering (1: the library's curve)")
    p.add_argument("--save", help="write the display read-back to this PNG file")
    p.add_argument("file")
    args = p.parse_args(argv)

    with Image.open(args.file) as img:
        img.load()
        picture = sk.to_gray(img)
    with common.connect(args):
        w, h = sk.size()
        picture = fit(trim(picture), w, h)
        if args.gamma != 1:
            sk.set_response(sk.gamma_response(args.gamma))
        bitmap = sk.load_bitmap(picture)
        x, y = (w - picture.width) // 2, (h - picture.height) // 2
        arms = common.present()
        before = {arm: arm.state().shown_update for arm in arms}
        start = time.monotonic()
        sk.draw(bitmap, x, y)
        sk.flush()
        for arm in arms:
            while arm.state().shown_update == before[arm] and time.monotonic() < start + 120:
                time.sleep(0.05)
        print(f"{picture.width}x{picture.height} picture at ({x}, {y}) shown after "
              f"{time.monotonic() - start:.3f} s")
        failed = False
        for arm in arms:
            try:
                got, want = arm.panel_crc()
            except sk.NoReplyError as e:
                print(f"{arm.name} panel: {e}")
                failed = True
                continue
            if got == want:
                print(f"{arm.name} panel: read back {got:08x}, as expected")
            else:
                print(f"{arm.name} panel: read back {got:08x}, expected {want:08x}")
                failed = True
        if args.save:
            sk.display().save(args.save)
    if failed:
        print("image: the panels do not show the picture", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, sk.ScouterKitError) as e:
        sys.exit(f"image: {e}")
