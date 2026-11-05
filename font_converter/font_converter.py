#!/usr/bin/env python3
"""TrueType/OpenType font -> ScouterKit font blob (1 bpp glyphs, format of
g1_firmware/docs/PROTOCOL.md §5). Needs Pillow.

  font_converter.py FONT --size 21 -o font.skf
  font_converter.py DepartureMono-Regular.otf --size 11 --supersample 8 \
      --c-array departure_mono.inc --name kDepartureMonoBlob

--supersample K is for pixel fonts: each glyph is rendered K times larger and
every K x K cell is sampled at its centre, which gives the exact pixels.

--cell is for terminal fonts (one advance for every glyph): each glyph is cut
to its cell (the advance of M by ascent + descent), and the braille patterns
(U+2800-28FF) and block elements (U+2580-259F) the font lacks are drawn to the
cell.
"""
import argparse
import struct
import sys

from PIL import ImageFont


SLOTS = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"


def glyph_order(cps):
    """Codepoints in blob order (VM.md §4.1): glyph i < 62 is SLOTS[i] (0 = an empty
    glyph for one that is missing), the others from index 62 in ascending order; a
    font of SLOTS characters only ends after the last one. So a digit's value is its
    glyph index."""
    slots = [ord(c) if ord(c) in cps else 0 for c in SLOTS]
    others = sorted(cp for cp in cps if chr(cp) not in SLOTS)
    while slots and not slots[-1] and not others:
        slots.pop()
    return slots + others


def parse_ranges(text):
    out = set()
    for part in text.split(","):
        part = part.strip()
        if not part:
            continue
        lo, _, hi = part.partition("-")
        lo = int(lo, 16)
        hi = int(hi, 16) if hi else lo
        out.update(range(lo, hi + 1))
    return out


def cmap_codepoints(path):
    data = open(path, "rb").read()
    if data[:4] == b"ttcf":
        raise SystemExit("font collections (.ttc) are not supported")
    count = struct.unpack_from(">H", data, 4)[0]
    tables = {}
    for i in range(count):
        tag, _sum, off, length = struct.unpack_from(">4sIII", data, 12 + 16 * i)
        tables[tag] = off
    base = tables[b"cmap"]
    n = struct.unpack_from(">H", data, base + 2)[0]
    subtables = []
    for i in range(n):
        pid, eid, off = struct.unpack_from(">HHI", data, base + 4 + 8 * i)
        fmt = struct.unpack_from(">H", data, base + off)[0]
        subtables.append((pid, eid, fmt, base + off))
    ranked = sorted(subtables, key=lambda s: (s[2] != 12, s[2] != 4, s[0] != 3))
    for pid, eid, fmt, off in ranked:
        if fmt == 12:
            groups = struct.unpack_from(">I", data, off + 12)[0]
            cps = set()
            for g in range(groups):
                start, end, gid = struct.unpack_from(">III", data, off + 16 + 12 * g)
                for k, cp in enumerate(range(start, end + 1)):
                    if gid + k:
                        cps.add(cp)
            return cps
        if fmt == 4:
            segs = struct.unpack_from(">H", data, off + 6)[0] // 2
            ends = struct.unpack_from(f">{segs}H", data, off + 14)
            starts = struct.unpack_from(f">{segs}H", data, off + 16 + 2 * segs)
            deltas = struct.unpack_from(f">{segs}h", data, off + 16 + 4 * segs)
            ro_pos = off + 16 + 6 * segs
            ranges = struct.unpack_from(f">{segs}H", data, ro_pos)
            cps = set()
            for s in range(segs):
                for cp in range(starts[s], ends[s] + 1):
                    if cp == 0xFFFF:
                        continue
                    if ranges[s] == 0:
                        gid = (cp + deltas[s]) & 0xFFFF
                    else:
                        p = ro_pos + 2 * s + ranges[s] + 2 * (cp - starts[s])
                        gid = struct.unpack_from(">H", data, p)[0]
                        if gid:
                            gid = (gid + deltas[s]) & 0xFFFF
                    if gid:
                        cps.add(cp)
            return cps
    raise SystemExit("no usable cmap subtable (format 4 or 12)")


def render(font, ch, k, threshold):
    """Ink pixels of one character as (w, h, x_off, y_off, rows of 0/1)."""
    mask, (ox, oy) = font.getmask2(ch, mode="L", anchor="ls")
    mw, mh = mask.size
    px = list(mask)
    ink = {}
    if mw and mh:
        if k == 1:
            for r in range(mh):
                for c in range(mw):
                    if px[r * mw + c] >= threshold:
                        ink[(ox + c, oy + r)] = 1
        else:
            for j in range(oy // k, -(-(oy + mh) // k)):
                for i in range(ox // k, -(-(ox + mw) // k)):
                    cx, cy = k * i + k // 2 - ox, k * j + k // 2 - oy
                    if 0 <= cx < mw and 0 <= cy < mh and px[cy * mw + cx] >= threshold:
                        ink[(i, j)] = 1
    if not ink:
        return 0, 0, 0, 0, []
    x0 = min(x for x, _ in ink)
    x1 = max(x for x, _ in ink) + 1
    y0 = min(y for _, y in ink)
    y1 = max(y for _, y in ink) + 1
    rows = [[1 if (x, y) in ink else 0 for x in range(x0, x1)] for y in range(y0, y1)]
    return x1 - x0, y1 - y0, x0, y0, rows


def crop(g, adv, ascent, descent):
    """The glyph's ink inside its cell: 0 <= x < adv, -ascent <= y < descent."""
    ink = {(g["x_off"] + c, g["y_off"] + r) for r, row in enumerate(g["rows"]) for c, v in enumerate(row) if v}
    return from_ink(g["cp"], g["adv"], {(x, y) for x, y in ink if 0 <= x < adv and -ascent <= y < descent})


def from_ink(cp, adv, ink):
    if not ink:
        return {"cp": cp, "w": 0, "h": 0, "x_off": 0, "y_off": 0, "adv": adv, "rows": []}
    x0, x1 = min(x for x, _ in ink), max(x for x, _ in ink) + 1
    y0, y1 = min(y for _, y in ink), max(y for _, y in ink) + 1
    rows = [[1 if (x, y) in ink else 0 for x in range(x0, x1)] for y in range(y0, y1)]
    return {"cp": cp, "w": x1 - x0, "h": y1 - y0, "x_off": x0, "y_off": y0, "adv": adv, "rows": rows}


def cell_glyph(cp, w, h, ascent):
    """A braille pattern (its 2 x 4 dots) or a block element, drawn to a w x h cell."""
    on = set()
    if 0x2800 <= cp <= 0x28FF:
        dw, dh = max(1, (w + 1) // 4), max(1, (h + 2) // 7)
        gx, gy = max(1, dw // 2), max(1, dh // 2)
        x0, y0 = (w - 2 * dw - gx) // 2, (h - 4 * dh - 3 * gy) // 2
        dots = [(0, 0), (0, 1), (0, 2), (1, 0), (1, 1), (1, 2), (0, 3), (1, 3)]
        for bit, (col, row) in enumerate(dots):
            if (cp - 0x2800) >> bit & 1:
                x, y = x0 + col * (dw + gx), y0 + row * (dh + gy)
                on |= {(x + i, y + j) for i in range(dw) for j in range(dh)}
    else:
        e = lambda n, of: round(of * n / 8)
        hw, hh = (w + 1) // 2, h // 2
        quad = {"ul": (0, 0, hw, hh), "ur": (hw, 0, w, hh), "ll": (0, hh, hw, h), "lr": (hw, hh, w, h)}
        rects = {0x2580: [(0, 0, w, hh)], 0x2588: [(0, 0, w, h)], 0x258C: [(0, 0, hw, h)], 0x2590: [(hw, 0, w, h)],
                 0x2594: [(0, 0, w, e(1, h))], 0x2595: [(w - e(1, w), 0, w, h)]}
        for n in range(1, 8):
            rects[0x2580 + n] = [(0, h - e(n, h), w, h)]
            rects[0x2590 - n] = [(0, 0, e(n, w), h)]
        for c, names in {0x2596: "ll", 0x2597: "lr", 0x2598: "ul", 0x2599: "ul ll lr", 0x259A: "ul lr",
                         0x259B: "ul ur ll", 0x259C: "ul ur lr", 0x259D: "ur", 0x259E: "ur ll",
                         0x259F: "ur ll lr"}.items():
            rects[c] = [quad[q] for q in names.split()]
        shade = {0x2591: lambda x, y: x % 2 == 0 and y % 2 == 0, 0x2592: lambda x, y: (x + y) % 2 == 0,
                 0x2593: lambda x, y: not (x % 2 and y % 2)}
        if cp in shade:
            on = {(x, y) for x in range(w) for y in range(h) if shade[cp](x, y)}
        for x0, y0, x1, y1 in rects.get(cp, []):
            on |= {(x, y) for x in range(x0, x1) for y in range(y0, y1)}
    return from_ink(cp, w, {(x, y - ascent) for x, y in on})


def kerning(font, glyphs, k):
    """Pair adjustments for printable ASCII, measured with Pillow's layout."""
    index = {g["cp"]: i for i, g in enumerate(glyphs)}
    ascii_cps = [cp for cp in range(0x21, 0x7F) if cp in index]
    width = {cp: font.getlength(chr(cp)) for cp in ascii_cps}
    out = []
    for a in ascii_cps:
        for b in ascii_cps:
            adj = round((font.getlength(chr(a) + chr(b)) - width[a] - width[b]) / k)
            if adj:
                out.append((index[a] * 65536 + index[b], max(-128, min(127, adj))))
    return sorted(out)


def build_blob(glyphs, ascent, descent, line_spacing, kerns):
    n, k = len(glyphs), len(kerns)
    bitmap_start = 16 + 16 * n + 5 * k
    bits = []
    shared = {}
    table = bytearray()
    for g in glyphs:
        offset = 0
        if g["w"] and g["h"]:
            key = (g["w"], g["h"], tuple(tuple(r) for r in g["rows"]))
            if key not in shared:
                shared[key] = 8 * bitmap_start + len(bits)
                for r in g["rows"]:
                    bits.extend(r)
            offset = shared[key]
        table += struct.pack("<IBBBBhh", offset, g["w"], g["h"], g["adv"], 0, g["x_off"], g["y_off"])
    stream = bytearray((len(bits) + 7) // 8)
    for i, b in enumerate(bits):
        if b:
            stream[i >> 3] |= 0x80 >> (i & 7)
    size = bitmap_start + len(stream)
    out = bytearray(b"SKF\x01")
    out += struct.pack("<IHHBBBB", size, n, k, ascent, descent, line_spacing, 0)
    out += table
    out += b"".join(struct.pack("<I", g["cp"]) for g in glyphs)
    out += b"".join(struct.pack("<I", key) for key, _adj in kerns)
    out += b"".join(struct.pack("<b", adj) for _key, adj in kerns)
    out += stream
    assert len(out) == size
    return bytes(out)


def c_array(blob, name, source, comment=None):
    lines = [f"// Generated by font_converter.py from {source}. Do not edit."]
    if comment:
        lines.append(f"// {comment}")
    lines.append(f"const unsigned char {name}[{len(blob)}] = {{")
    for i in range(0, len(blob), 16):
        lines.append("  " + ", ".join(f"0x{b:02x}" for b in blob[i:i + 16]) + ",")
    lines.append("};")
    return "\n".join(lines) + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("font")
    ap.add_argument("--size", type=int, required=True, help="pixel size (em height in panel pixels)")
    ap.add_argument("--supersample", type=int, default=1, help="pixel fonts: render K times larger, sample cell centres")
    ap.add_argument("--threshold", type=int, default=128, help="ink threshold 1..255 (default 128)")
    ap.add_argument("--chars", help="only these characters (UTF-8 text)")
    ap.add_argument("--ranges", help="only these codepoints, e.g. 20-7E,A0-FF (hex)")
    ap.add_argument("--kern", action="store_true", help="add kerning pairs for printable ASCII")
    ap.add_argument("--cell", action="store_true",
                    help="terminal font: cut glyphs to the cell, draw missing braille and block elements")
    ap.add_argument("--line-spacing", type=int, help="baseline-to-baseline distance (default ascent + descent + gap)")
    ap.add_argument("-o", "--output", help="write the blob here (.skf)")
    ap.add_argument("--c-array", help="write the blob as a C array include file")
    ap.add_argument("--name", default="kFontBlob", help="array name for --c-array")
    ap.add_argument("--comment", help="one more comment line for --c-array (e.g. the font's licence)")
    args = ap.parse_args()
    if not args.output and not args.c_array:
        ap.error("give -o and/or --c-array")

    k = max(1, args.supersample)
    font = ImageFont.truetype(args.font, args.size * k)
    have = cmap_codepoints(args.font)
    asc, desc = font.getmetrics()
    ascent, descent = round(asc / k), round(desc / k)
    cell = round(font.getlength("M") / k)
    drawn = set(range(0x2580, 0x25A0)) | set(range(0x2800, 0x2900)) if args.cell else set()
    cps = have | drawn
    if args.chars is not None:
        cps &= {ord(c) for c in args.chars}
    if args.ranges:
        cps &= parse_ranges(args.ranges)
    cps = sorted(cp for cp in cps if cp >= 0x20 and not 0x7F <= cp < 0xA0 and not 0xD800 <= cp < 0xE000)
    if not cps:
        raise SystemExit("no characters selected")

    glyphs = []
    for cp in glyph_order(set(cps)):
        if not cp:
            glyphs.append({"cp": 0, "w": 0, "h": 0, "x_off": 0, "y_off": 0, "adv": 0, "rows": []})
            continue
        if cp not in have:
            glyphs.append(cell_glyph(cp, cell, ascent + descent, ascent))
            continue
        w, h, xo, yo, rows = render(font, chr(cp), k, args.threshold)
        adv = round(font.getlength(chr(cp)) / k)
        if w > 255 or h > 255 or not 0 <= adv <= 255:
            raise SystemExit(f"U+{cp:04X}: glyph too large for the format ({w}x{h}, advance {adv})")
        g = {"cp": cp, "w": w, "h": h, "x_off": xo, "y_off": yo, "adv": adv, "rows": rows}
        glyphs.append(crop(g, cell, ascent, descent) if args.cell else g)

    line_spacing = args.line_spacing or ascent + descent
    if max(ascent, descent, line_spacing) > 255:
        raise SystemExit("metrics too large for the format")
    kerns = kerning(font, glyphs, k) if args.kern else []
    blob = build_blob(glyphs, ascent, descent, line_spacing, kerns)
    if args.output:
        open(args.output, "wb").write(blob)
    if args.c_array:
        source = args.font.replace("\\", "/").split("/")[-1]
        open(args.c_array, "w").write(c_array(blob, args.name, f"{source}, --size {args.size} --supersample {k}",
                                              args.comment))
    print(f"{len(glyphs)} glyphs, {len(kerns)} kerning pairs, {len(blob)} bytes, "
          f"ascent {ascent}, descent {descent}, line spacing {line_spacing}", file=sys.stderr)


if __name__ == "__main__":
    main()
