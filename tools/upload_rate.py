#!/usr/bin/env python3
"""Per-arm upload rate of a ScouterKit session, from a btmon capture (btsnoop).

Usage: [G1_ARMS=L=ADDR,R=ADDR] upload_rate.py FILE.btsnoop [FILE ...] [--gap 1.0]
Per LE connection: the largest burst of ATT writes (gaps below --gap s) to the handle with the most
writes (the CMD characteristic): packets, resends (a u16 seq sent again), bytes, time from the first
write to the controller's completion of the last one (Number of Completed Packets), KB/s
(1 KB = 1000 B), and the link at the end of the burst (interval, PHY, data length, LE buffers).
"""
import argparse
import os
import struct
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from btsum import ARMS, PHYS, addr, records  # noqa: E402



class Conn:
    def __init__(self, address, t, interval):
        self.address, self.start = address, t
        self.link = [(t, {"interval_ms": interval, "phy": "1M", "tx_octets": 27})]
        self.acl, self.done, self.writes, self.buf = [], [], [], None

    def set(self, t, **kw):
        self.link.append((t, {**self.link[-1][1], **kw}))

    def link_at(self, t):
        return [v for s, v in self.link if s <= t][-1]


def parse(path):
    conns, closed, le_buffers = {}, [], None
    for t, op, data in records(path):
        if op == 3 and len(data) >= 2:
            code, p = data[0], data[2:]
            if code == 0x0E and len(p) >= 7 and struct.unpack_from("<H", p, 1)[0] == 0x2002 and p[3] == 0:
                le_buffers = struct.unpack_from("<HB", p, 4)
            elif code == 0x3E:
                sub = p[0]
                if sub in (0x01, 0x0A, 0x29) and p[1] == 0:
                    handle = struct.unpack_from("<H", p, 2)[0]
                    interval = struct.unpack_from("<H", p, 12 if sub == 0x01 else 24)[0] * 1.25
                    conns[handle] = Conn(addr(p[6:12]), t, interval)
                elif sub == 0x03 and p[1] == 0 and struct.unpack_from("<H", p, 2)[0] in conns:
                    conns[struct.unpack_from("<H", p, 2)[0]].set(t, interval_ms=struct.unpack_from("<H", p, 4)[0] * 1.25)
                elif sub == 0x07 and struct.unpack_from("<H", p, 1)[0] in conns:
                    conns[struct.unpack_from("<H", p, 1)[0]].set(t, tx_octets=struct.unpack_from("<H", p, 3)[0])
                elif sub == 0x0C and p[1] == 0 and struct.unpack_from("<H", p, 2)[0] in conns:
                    conns[struct.unpack_from("<H", p, 2)[0]].set(t, phy=PHYS.get(p[4], "?"))
            elif code == 0x05 and len(p) >= 4:
                c = conns.pop(struct.unpack_from("<H", p, 1)[0], None)
                if c:
                    closed.append(c)
            elif code == 0x13:
                for i in range(p[0]):
                    handle, count = struct.unpack_from("<HH", p, 1 + 4 * i)
                    if handle in conns:
                        conns[handle].done += [t] * count
        elif op == 4 and len(data) >= 4:
            hf, dlen = struct.unpack_from("<HH", data)
            c = conns.get(hf & 0x0FFF)
            if not c:
                continue
            c.acl.append(t)
            payload = data[4:4 + dlen]
            if (hf >> 12) & 3 == 1:
                if c.buf is None:
                    continue
                c.buf += payload
            else:
                c.buf = bytearray(payload)
            if len(c.buf) < 4 or len(c.buf) < 4 + struct.unpack_from("<H", c.buf)[0]:
                continue
            length, cid = struct.unpack_from("<HH", c.buf)
            l2, c.buf = bytes(c.buf[4:4 + length]), None
            if cid == 4 and len(l2) >= 5 and l2[0] in (0x52, 0x12):
                c.writes.append((t, struct.unpack_from("<H", l2, 1)[0], len(l2) - 3,
                                 struct.unpack_from("<H", l2, 3)[0], len(c.acl) - 1))
    return closed + list(conns.values()), le_buffers


def bursts(writes, gap):
    out, cur = [], []
    for w in writes:
        if cur and w[0] - cur[-1][0] > gap:
            out.append(cur)
            cur = []
        cur.append(w)
    return out + ([cur] if cur else [])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("files", nargs="+")
    ap.add_argument("--gap", type=float, default=1.0)
    a = ap.parse_args()
    for path in a.files:
        conns, le_buffers = parse(path)
        print(f"{os.path.basename(path)}: LE buffers {le_buffers[0]} B x {le_buffers[1]}" if le_buffers
              else f"{os.path.basename(path)}:")
        for c in conns:
            if not c.writes:
                continue
            handles = {}
            for w in c.writes:
                handles[w[1]] = handles.get(w[1], 0) + 1
            cmd = max(handles, key=handles.get)
            b = max(bursts([w for w in c.writes if w[1] == cmd], a.gap), key=lambda ws: sum(w[2] for w in ws))
            size = sum(w[2] for w in b)
            last = b[-1][4]
            end = c.done[last] if last < len(c.done) else b[-1][0]
            dur = end - b[0][0]
            link = c.link_at(end)
            resends = len(b) - len({w[3] for w in b})
            print(f"  {ARMS.get(c.address, '?')} {c.address}: {len(b)} packets ({resends} resent), {size / 1000:.1f} KB in "
                  f"{dur:.2f} s = {size / 1000 / dur if dur > 0 else 0:.1f} KB/s; {link['interval_ms']:g} ms, "
                  f"{link['phy']}, data length {link['tx_octets']}")


if __name__ == "__main__":
    main()
