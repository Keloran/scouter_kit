#!/usr/bin/env python3
"""Summarize a btmon capture or an Android HCI snoop log (btsnoop) per connection.

Usage: [G1_ARMS=L=ADDR,R=ADDR] btsum.py <file.btsnoop> [--json out.json]
Reports connection parameters and updates, PHY, data length, L2CAP parameter
requests, SMP/encryption events, ATT MTU/errors, disconnect reasons and
Number-of-Completed-Packets pacing for BMP (0x15) write bursts.
"""
import argparse
import datetime
import json
import os
import statistics
import struct
import sys

# the arms' addresses, for the labels: G1_ARMS="L=AA:BB:CC:DD:EE:FF,R=..."
ARMS = {a.strip().upper(): s.strip() for s, a in (p.split("=", 1) for p in os.environ.get("G1_ARMS", "").split(",") if "=" in p)}
EPOCH_DELTA = 0x00E03AB44A676000 - 946684800 * 1000000
SMP_CODES = {1: "Pairing Request", 2: "Pairing Response", 3: "Pairing Confirm", 4: "Pairing Random",
             5: "Pairing Failed", 6: "Encryption Information", 7: "Central Identification",
             8: "Identity Information", 9: "Identity Address Information", 10: "Signing Information",
             11: "Security Request", 12: "Pairing Public Key", 13: "Pairing DHKey Check"}
DISCONNECT_REASONS = {0x08: "Connection Timeout", 0x13: "Remote User Terminated", 0x16: "Terminated By Local Host",
                      0x22: "LL Response Timeout", 0x3B: "Unacceptable Connection Parameters",
                      0x3D: "MIC Failure", 0x3E: "Failed To Be Established"}
PHYS = {1: "1M", 2: "2M", 3: "Coded"}


def records(path):
    with open(path, "rb") as f:
        head = f.read(16)
        datalink = struct.unpack(">I", head[12:16])[0] if len(head) == 16 else None
        if head[:8] != b"btsnoop\0" or datalink not in (1002, 2001):
            sys.exit("need a btsnoop file from btmon (datalink 2001) or an Android HCI snoop log (1002)")
        while True:
            rec = f.read(24)
            if len(rec) < 24:
                return
            _, incl, flags, _, ts = struct.unpack(">IIIIq", rec)
            data = f.read(incl)
            t = (ts - EPOCH_DELTA) / 1e6
            if datalink == 2001:
                yield t, flags & 0xFFFF, data
            elif data and data[0] in (1, 2, 4):
                kind = {1: 2, 4: 3}.get(data[0], 5 if flags & 1 else 4)
                yield t, kind, data[1:]


def addr(raw):
    return ":".join(f"{b:02X}" for b in raw[::-1])


class Conn:
    def __init__(self, handle, address, t, params):
        self.handle = handle
        self.address = address
        self.arm = ARMS.get(address, "?")
        self.t_start = t
        self.events = [(t, "connected", params)]
        self.tx_pending = []
        self.tx_done = []
        self.att = {"tx": {}, "rx": {}}
        self.att_errors = []
        self.bursts = []
        self.reasm = {}
        self.writes = []
        self.notifies = []
        self.req_rsp = []


def ms(x):
    return round(x * 1000, 1)


def conn_params(interval, latency, timeout):
    return {"interval_ms": interval * 1.25, "latency": latency, "timeout_ms": timeout * 10}


def parse(path):
    conns, closed, t0 = {}, [], None
    for t, op, data in records(path):
        t0 = t0 if t0 is not None else t
        if op == 3 and len(data) >= 2:
            code, params = data[0], data[2:]
            if code == 0x3E:
                sub = params[0]
                if sub in (0x01, 0x0A, 0x29) and params[1] == 0:
                    handle = struct.unpack_from("<H", params, 2)[0]
                    off = 18 if sub == 0x01 else 30
                    interval, latency, timeout = struct.unpack_from("<HHH", params, off - 7 + 1)
                    c = Conn(handle, addr(params[6:12]), t, conn_params(interval, latency, timeout))
                    conns[handle] = c
                elif sub == 0x03 and params[1] == 0:
                    handle, interval, latency, timeout = struct.unpack_from("<HHHH", params, 2)
                    if handle in conns:
                        conns[handle].events.append((t, "conn update", conn_params(interval, latency, timeout)))
                elif sub == 0x07:
                    handle, txo, txt, rxo, rxt = struct.unpack_from("<HHHHH", params, 1)
                    if handle in conns:
                        conns[handle].events.append((t, "data length", {"tx_octets": txo, "tx_us": txt,
                                                                        "rx_octets": rxo, "rx_us": rxt}))
                elif sub == 0x0C and params[1] == 0:
                    handle = struct.unpack_from("<H", params, 2)[0]
                    if handle in conns:
                        conns[handle].events.append((t, "phy update", {"tx": PHYS.get(params[4]), "rx": PHYS.get(params[5])}))
                elif sub == 0x06:
                    handle, mn, mx, lat, to = struct.unpack_from("<HHHHH", params, 1)
                    if handle in conns:
                        conns[handle].events.append((t, "LL param request", {"min_ms": mn * 1.25, "max_ms": mx * 1.25,
                                                                             "latency": lat, "timeout_ms": to * 10}))
            elif code == 0x05:
                status, handle, reason = struct.unpack_from("<BHB", params)
                if handle in conns:
                    c = conns.pop(handle)
                    c.events.append((t, "disconnected", {"reason": f"0x{reason:02x} {DISCONNECT_REASONS.get(reason, '')}"}))
                    c.t_end = t
                    closed.append(c)
            elif code == 0x08:
                status, handle, enabled = struct.unpack_from("<BHB", params)
                if handle in conns:
                    conns[handle].events.append((t, "encryption change", {"status": status, "enabled": enabled}))
            elif code == 0x13:
                for i in range(params[0]):
                    handle, count = struct.unpack_from("<HH", params, 1 + 4 * i)
                    c = conns.get(handle)
                    for _ in range(count):
                        if c and c.tx_pending:
                            sent, info = c.tx_pending.pop(0)
                            c.tx_done.append((sent, t, info))
        elif op in (4, 5) and len(data) >= 4:
            hf, dlen = struct.unpack_from("<HH", data)
            handle, pb = hf & 0x0FFF, (hf >> 12) & 0x3
            c = conns.get(handle)
            if not c:
                continue
            direction = "tx" if op == 4 else "rx"
            payload = data[4:4 + dlen]
            if pb == 1:
                buf = c.reasm.get(direction)
                if buf is None:
                    continue
                buf += payload
            else:
                buf = bytearray(payload)
            c.reasm[direction] = buf
            if direction == "tx":
                c.tx_pending.append((t, None))
            if len(buf) < 4 or len(buf) < 4 + struct.unpack_from("<H", buf)[0]:
                continue
            c.reasm[direction] = None
            length, cid = struct.unpack_from("<HH", buf)
            l2 = bytes(buf[4:4 + length])
            if direction == "tx":
                c.tx_pending[-1] = (c.tx_pending[-1][0], l2[:3] if cid == 4 else None)
            if cid == 4 and l2:
                c.att[direction][l2[0]] = c.att[direction].get(l2[0], 0) + 1
                if l2[0] == 0x01:
                    req, hnd, err = struct.unpack_from("<BHB", l2, 1)
                    c.att_errors.append((t, direction, f"req 0x{req:02x} handle 0x{hnd:04x} error 0x{err:02x}"))
                elif l2[0] in (0x02, 0x03):
                    c.events.append((t, f"ATT MTU {'request' if l2[0] == 2 else 'response'} ({direction})",
                                     {"mtu": struct.unpack_from("<H", l2, 1)[0]}))
                if l2[0] in (0x12, 0x13):
                    c.req_rsp.append((t, l2[0]))
                if l2[0] in (0x52, 0x12) and direction == "tx" and len(l2) > 3:
                    c.writes.append((t, l2[3], len(l2) - 3))
                elif l2[0] == 0x1B and len(l2) > 3:
                    c.notifies.append((t, l2[3:5].hex(" ")))
            elif cid == 5 and l2:
                if l2[0] == 0x12:
                    mn, mx, lat, to = struct.unpack_from("<HHHH", l2, 4)
                    c.events.append((t, f"L2CAP param update request ({direction})",
                                     {"min_ms": mn * 1.25, "max_ms": mx * 1.25, "latency": lat, "timeout_ms": to * 10}))
                elif l2[0] == 0x13:
                    c.events.append((t, f"L2CAP param update response ({direction})",
                                     {"result": struct.unpack_from("<H", l2, 4)[0]}))
            elif cid == 6 and l2:
                name = SMP_CODES.get(l2[0], f"0x{l2[0]:02x}")
                extra = {"auth_req": f"0x{l2[1]:02x}"} if l2[0] == 11 else {"reason": f"0x{l2[1]:02x}"} if l2[0] == 5 else {}
                c.events.append((t, f"SMP {name} ({direction})", extra))
    for c in conns.values():
        c.t_end = None
        closed.append(c)
    return sorted(closed, key=lambda c: c.t_start), t0


def wall(t):
    return datetime.datetime.fromtimestamp(t).strftime("%H:%M:%S.%f")[:-3]


def params_at(c, t):
    current = None
    for te, name, info in c.events:
        if te <= t and name in ("connected", "conn update"):
            current = info
    return current


def phy_at(c, t):
    phy = "1M"
    for te, name, info in c.events:
        if te <= t and name == "phy update":
            phy = f"{info['tx']}/{info['rx']}"
    return phy


def write_rtts(c, start, end):
    out, pending = [], None
    for t, op in c.req_rsp:
        if op == 0x12:
            pending = t
        elif pending is not None:
            if start <= pending <= end:
                out.append(t - pending)
            pending = None
    return out


def bmp_bursts(c):
    bursts, cur = [], []
    for t, first, size in c.writes:
        if first == 0x15 and size >= 150:
            if cur and t - cur[-1][0] > 0.5:
                bursts.append(cur)
                cur = []
            cur.append((t, size))
    if cur:
        bursts.append(cur)
    out = []
    for b in bursts:
        start, end = b[0][0], b[-1][0]
        done = [d for s, d, _ in c.tx_done if start <= s <= end + 1e-6]
        nocp = sorted(set(done))
        per_event = [done.count(x) for x in nocp]
        gaps = [b2 - a for a, b2 in zip(nocp, nocp[1:])]
        replies = [(t, v) for t, v in c.notifies if t > end and t < end + 10]
        rtt = write_rtts(c, start, end)
        size = sum(n for _, n in b)
        params = params_at(c, start) or {}
        out.append({"start": wall(start), "air_done": wall(max(done)) if done else None,
                    "interval_ms": params.get("interval_ms"), "timeout_ms": params.get("timeout_ms"), "phy": phy_at(c, start),
                    "packets": len(b), "bytes": size, "host_submit_ms": ms(end - start),
                    "kB_per_s": round(size / (max(done) - start) / 1000, 1) if done and max(done) > start else None,
                    "write_rtt_median_ms": ms(statistics.median(rtt)) if rtt else None,
                    "write_rtt_max_ms": ms(max(rtt)) if rtt else None,
                    "air_complete_ms": ms(max(done) - start) if done else None,
                    "nocp_events": len(nocp), "packets_per_nocp_max": max(per_event) if per_event else None,
                    "packets_per_nocp_median": statistics.median(per_event) if per_event else None,
                    "nocp_gap_median_ms": ms(statistics.median(gaps)) if gaps else None,
                    "max_in_flight": max_in_flight(c, start, end),
                    "first_replies_after_last_packet_ms": [(ms(t - end), v) for t, v in replies[:3]]})
    return out


def max_in_flight(c, start, end):
    marks = []
    for s, d, _ in c.tx_done:
        if start <= s <= end + 1e-6:
            marks += [(s, 1), (d, -1)]
    level = best = 0
    for _, step in sorted(marks):
        level += step
        best = max(best, level)
    return best


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("file")
    p.add_argument("--json")
    args = p.parse_args()
    conns, t0 = parse(args.file)
    report = []
    for c in conns:
        lat = [d - s for s, d, _ in c.tx_done]
        r = {"arm": c.arm, "address": c.address, "handle": c.handle,
             "start_s": round(c.t_start - t0, 3), "duration_s": round((c.t_end or c.t_start) - c.t_start, 1),
             "events": [(round(t - t0, 3), name, info) for t, name, info in c.events],
             "att_counts": {d: {f"0x{k:02x}": v for k, v in sorted(m.items())} for d, m in c.att.items()},
             "att_errors": [(round(t - t0, 3), d, e) for t, d, e in c.att_errors],
             "acl_tx_completed": len(lat),
             "acl_tx_completion_ms": {"median": ms(statistics.median(lat)), "max": ms(max(lat))} if lat else None,
             "bmp_bursts": bmp_bursts(c)}
        report.append(r)
        print(f"== {c.arm} {c.address} handle {c.handle} at {r['start_s']}s for {r['duration_s']}s")
        for t, name, info in r["events"]:
            print(f"   {t:9.3f}s {name} {info if info else ''}")
        print(f"   ATT opcodes {r['att_counts']}")
        if r["att_errors"]:
            print(f"   ATT errors {r['att_errors']}")
        print(f"   ACL TX completed {r['acl_tx_completed']} completion {r['acl_tx_completion_ms']}")
        for b in r["bmp_bursts"]:
            print(f"   BMP {b['start']} int {b['interval_ms']}ms {b['phy']}: {b['packets']} pkt, submit {b['host_submit_ms']} ms, "
                  f"on-air done {b['air_complete_ms']} ms ({b['kB_per_s']} kB/s), NOCP {b['nocp_events']} ev "
                  f"(max {b['packets_per_nocp_max']}/ev, gap med {b['nocp_gap_median_ms']} ms), in flight max "
                  f"{b['max_in_flight']}, write RTT med/max {b['write_rtt_median_ms']}/{b['write_rtt_max_ms']} ms, "
                  f"replies {b['first_replies_after_last_packet_ms']}")
    if args.json:
        with open(args.json, "w") as f:
            json.dump(report, f, indent=1)


if __name__ == "__main__":
    main()
