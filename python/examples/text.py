"""Shows a few lines in the built-in Departure Mono font, in the columns both eyes see, and reads both panels back.

    python examples/text.py [--save display.png] [--emulator]
"""

from __future__ import annotations

import sys
import time

import common
import scouterkit as sk

LINES = [
    ("Hello from Python, on both eyes.", 255),
    ("The arms draw the glyphs:", 255),
    ("one small command per line.", 255),
    ("Dimmer text, gray 119.", 119),
]


def main(argv: list[str] | None = None) -> int:
    p = common.parser(__doc__.splitlines()[0])
    p.add_argument("--save", help="write the display read-back to this PNG file")
    args = p.parse_args(argv)

    with common.connect(args) as session:
        title = sk.load_font(sk.departure_mono(), "ScouterKit", 3)
        body = sk.load_font(sk.departure_mono(), "", 2)
        width = sk.PANEL_WIDTH - session.dx
        height = title.line_spacing + (len(LINES) + 1) * body.line_spacing
        page = sk.create_bitmap(width, height)
        y = title.ascent
        w = title.draw_line(page, "ScouterKit", width // 2, y, sk.Align.CENTER)
        page.fill((width // 2 - w // 2, y + 4, width // 2 + w // 2, y + 6), 255)
        y = title.line_spacing + body.ascent
        for text, gray in LINES:
            body.draw_line(page, text, 0, y, sk.Align.LEFT, gray)
            y += body.line_spacing
        body.draw_line(page, time.strftime("%H:%M"), width, y, sk.Align.RIGHT)
        sk.draw(page, session.dx, (session.size[1] - height) // 2)
        sk.flush()
        failed = False
        for arm in common.present():
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
        print("text: the panels do not show the text", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, sk.ScouterKitError) as e:
        sys.exit(f"text: {e}")
