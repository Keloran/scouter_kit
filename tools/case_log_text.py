#!/usr/bin/env python3
"""Turn a case_serial_listen.py JSONL capture into the case's text log, one line per log line,
each prefixed with the PC time in seconds since the capture opened. Reads the capture only."""
import json
import sys


def lines(path, baud=115200):
    t0 = None
    buf = bytearray()
    t_line = None
    with open(path) as f:
        for raw in f:
            rec = json.loads(raw)
            if rec["event"] == "open":
                t0 = rec["t"]
            if rec["event"] != "rx" or rec["baud"] != baud:
                continue
            for b in bytes.fromhex(rec["hex"]):
                if t_line is None:
                    t_line = rec["t"] - (t0 or rec["t"])
                if b == 10:
                    yield t_line, buf.decode("ascii", "replace").rstrip("\r")
                    buf.clear()
                    t_line = None
                else:
                    buf.append(b)
    if buf:
        yield t_line or 0.0, buf.decode("ascii", "replace")


def main():
    for t, text in lines(sys.argv[1]):
        print("%8.3f %s" % (t, text))


if __name__ == "__main__":
    main()
