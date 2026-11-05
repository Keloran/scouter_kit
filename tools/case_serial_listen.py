#!/usr/bin/env python3
"""Passive listener for the G1 charging case serial port. Never writes to the port."""
import argparse
import json
import time

import serial

DEFAULT_BAUDS = [115200, 115200, 9600, 19200, 38400, 57600, 230400, 460800,
                 921600, 1000000, 1500000, 2000000, 115200]


def hexdump(data, width=16):
    lines = []
    for off in range(0, len(data), width):
        chunk = data[off:off + width]
        hexpart = " ".join("%02x" % b for b in chunk)
        text = "".join(chr(b) if 32 <= b < 127 else "." for b in chunk)
        lines.append("  %06x  %-*s  %s" % (off, width * 3 - 1, hexpart, text))
    return "\n".join(lines)


def modem_lines(port):
    return {"cts": port.cts, "dsr": port.dsr, "ri": port.ri, "cd": port.cd}


def listen(port, baud, seconds, log):
    port.baudrate = baud
    start = time.time()
    lines = modem_lines(port)
    log.write(json.dumps({"event": "baud", "baud": baud, "t": start, "lines": lines}) + "\n")
    print("=== %d baud for %.0f s, modem lines %s" % (baud, seconds, lines), flush=True)
    received = bytearray()
    while time.time() - start < seconds:
        chunk = port.read(port.in_waiting or 1)
        now_lines = modem_lines(port)
        if now_lines != lines:
            lines = now_lines
            log.write(json.dumps({"event": "lines", "baud": baud, "t": time.time(), "lines": lines}) + "\n")
            print("  modem lines changed: %s" % lines, flush=True)
        if chunk:
            received += chunk
            log.write(json.dumps({"event": "rx", "baud": baud, "t": time.time(), "hex": chunk.hex()}) + "\n")
            log.flush()
    print("  %d bytes received" % len(received))
    if received:
        print(hexdump(bytes(received[:1024])))
        if len(received) > 1024:
            print("  ... (%d more bytes in log)" % (len(received) - 1024))
        print("  zero bytes: %d, printable: %d, contains 5a a5: %s"
              % (received.count(0), sum(32 <= b < 127 or b in (9, 10, 13) for b in received),
                 b"\x5a\xa5" in received))
    return bytes(received)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", default="/dev/ttyUSB0")
    parser.add_argument("--seconds", type=float, default=10.0)
    parser.add_argument("--log", required=True)
    parser.add_argument("--bauds", default=",".join(str(b) for b in DEFAULT_BAUDS))
    args = parser.parse_args()
    bauds = [int(b) for b in args.bauds.split(",")]

    port = serial.Serial()
    port.port = args.port
    port.baudrate = bauds[0]
    port.bytesize = serial.EIGHTBITS
    port.parity = serial.PARITY_NONE
    port.stopbits = serial.STOPBITS_ONE
    port.xonxoff = False
    port.rtscts = False
    port.dsrdtr = False
    port.timeout = 0.05
    port.exclusive = True
    port.dtr = False
    port.rts = False

    totals = {}
    with open(args.log, "w") as log:
        port.open()
        opened = time.time()
        log.write(json.dumps({"event": "open", "port": args.port, "t": opened,
                              "dtr": port.dtr, "rts": port.rts}) + "\n")
        print("opened %s, dtr=%s rts=%s" % (args.port, port.dtr, port.rts), flush=True)
        try:
            for i, baud in enumerate(bauds):
                data = listen(port, baud, args.seconds, log)
                totals["%02d_%d" % (i, baud)] = len(data)
        finally:
            port.close()
            log.write(json.dumps({"event": "close", "t": time.time()}) + "\n")
    print("summary (slot_baud: bytes): %s" % totals)


if __name__ == "__main__":
    main()
