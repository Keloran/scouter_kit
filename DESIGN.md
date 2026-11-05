ScouterKit objective: intermediate layer - so that it's possible to write an application for: PC/Smartphone/whatever device with IoT AND allow that app to talk with the glasses (glasses part should be modular - so it works with G1/G2/maybe others)

Exposed capabilities:
- ground truth that the device provides - all functions exposed by the glasses should be exposed
- some simple abstraction (single greyscale bitmap) that can be mapped to arbitrary smart glasses

Application side:
- C/C++ - core library
- golang (wrapper) - preferred for desktop apps
- python (wrapper)

Device side (provides communication with glasses):
- BlueZ over D-Bus
- TODO: Android

Plan:
- small python script converts python fonts to scouter-compatible font blobs
- we can build scouter_kit_g1 AND it produces a library that provides scouter_kit + scouter_kit_g1
- for now scouter_kit_g1 will support linux and talk over bluez but will allow some #ifdef settings to make it work also on windows, ESP32 maybe others
- golang wrapper wraps the C/C++ library
- a set of golang demos show different capabilities of the glasses (images, video, scrolling, text, etc)

List of things for V1:
- firmware that runs streamlined protocol (based on scouter_kit_g1) - varint glyph encoding, new opcodes
- finish scouter_kit.hpp & scouter_kit_g1.hpp
- implement the scouter_kit_g1 library (no build system - just .hpp + .cpp - can be dropped into any project, preprocessor-based configuration, mostly automatic - only linux + bluez for now)
- golang wrapper
- golang demo that draws an image

# VM

NOOP
WRITE (addr, <bytes>)
WRITE_RLE (addr, <len>, <bytes>)
COPY (dst, src, n)
MOV (dst, src)
LD (dst, src ptr)
ST (dst ptr, src)
JMP (dst)

BEQ (x, y)
BNE (x, y)
BLTU (x, y)
BGEU (x, y)
BLTS (x, y)
BGES (x, y)

CALL (addr)
RET

SLEEP_UNTIL (t)
RAISE (t)
NOTIFY (a, b)

SYS_READ
SYS_WRITE

FILL
BLIT
GLYPHS
DRAW

ADD
SUB
MUL
DIVU
MODU
DIVS
MODS
AND
OR
XOR
SHL
SHR
SAR
