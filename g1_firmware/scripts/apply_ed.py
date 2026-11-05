"""Applies an ed script made by `diff -e ORIGINAL MODIFIED` (commands a, c, d) to ORIGINAL.
Usage: python3 -I apply_ed.py ORIGINAL SCRIPT OUTPUT"""
import re
import sys

src, script, out = sys.argv[1:]
lines = open(src, newline="").read().splitlines(keepends=True)
cmds = open(script, newline="").read().splitlines(keepends=True)
i = 0
while i < len(cmds):
    m = re.fullmatch(r"(\d+)(?:,(\d+))?([acd])\n", cmds[i])
    if not m:
        sys.exit(f"{script}:{i + 1}: unsupported command {cmds[i]!r}")
    a = int(m.group(1))
    b = int(m.group(2) or a)
    i += 1
    text = []
    if m.group(3) != "d":
        while cmds[i] != ".\n":
            text.append(cmds[i])
            i += 1
        i += 1
    if m.group(3) == "a":
        lines[a:a] = text
    else:
        lines[a - 1:b] = text
open(out, "w", newline="").write("".join(lines))
