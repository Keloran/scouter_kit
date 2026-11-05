# ScouterKit G1 firmware protocol, version 4

The wire protocol between a host and one G1 arm running the scouter firmware (`g1v0` 0.4.x). It is
defined in bytes: the layouts of §4 (commands), §5 (font blob), §6 (events) and §8 (control requests)
are the source of truth. Host libraries, such as the C++ `scouter_kit_g1` helpers or `g1host.py`, only
build and parse these bytes (Appendix A). Version 4 keeps the reliability design of version 3:
sequence numbers, cumulative and selective acks, credits, an epoch, and control requests outside the
drawing queue.

Version 4 is not compatible with version 3. A host reads `HELLO` (§8.1) first and stops if `proto` is
not 4. Firmware updates (MCUmgr SMP) work the same in every version.

## 1. Conventions

- Little-endian. `u8/u16/u24/u32` unsigned, `i8/i16` two's complement. A `u24` is 3 bytes, low byte first.
- Device time `t_us`: `u32` microseconds since boot. It wraps after 71.6 min; compare two times with
  the signed 32-bit difference.
- "Arena" = the arm's drawing memory. `aw` = arena word address (`u16`, byte address = 4·aw).
  `bit` = arena bit address (`u24`, byte = bit/8; bit 0 of an address is the MSB of its byte).
- Integer division truncates. `ceil(a/b)` = (a + b − 1)/b.

## 2. Transport

One GATT service. MCUmgr SMP (firmware update) sits beside it, unchanged.

| Item | UUID | Properties | Content |
|---|---|---|---|
| service | `f3da4d59-277a-4fc9-a1ed-41c9c22d4400` | | |
| `CMD` | `f3da4d59-277a-4fc9-a1ed-41c9c22d4401` | write, write without response | sequenced command packets (§3, §4) |
| `CTRL` | `f3da4d59-277a-4fc9-a1ed-41c9c22d4402` | write, write without response | control requests (§8) |
| `EVT` | `f3da4d59-277a-4fc9-a1ed-41c9c22d4403` | notify | acks, events, control replies (§6, §7) |

- The host subscribes to `EVT`, then exchanges the MTU (ATT MTU ≥ 65 is required; the longest event
  is 59 bytes), then sends `HELLO`. An event larger than MTU − 3 is not sent.
- Packet size: a `CMD` packet is at most `max_packet` bytes (`HELLO`, = min(ATT MTU − 3, 244)). A
  `CTRL` request is at most 24 bytes.
- Write without response is the normal case; the protocol's own acks and retries make it reliable.
- Security (§10): out of the charging case only a bonded, encrypted link is obeyed.

### 2.1 Advertising and the pair record (0.4.2+)
While no host is connected an arm advertises, connectable: every 100–150 ms for 60 s after boot or a
disconnection and whenever it is in the case (VBUS), otherwise every 1.0–1.1 s (0.4.8+; before 0.4.8
always 100–150 ms). A host that scans for a few seconds finds a slow-advertising arm as well.
- **Advertising data:** flags `06`, the service UUID (complete list of 128-bit UUIDs), the complete name
  `G1v0_L` or `G1v0_R` (`G1v0S_L` / `G1v0S_R` in safe mode). 29 or 30 bytes.
- **Scan response:** the **pair record**, manufacturer-specific data (AD type 0xFF) for company ID
  0xFFFF (no company ID is assigned to this firmware). At most 22 bytes.

| Offset | Type | Field |
|---|---|---|
| 0 | u16 | company ID = 0xFFFF |
| 2 | u8 | `format` = 1 |
| 3 | u8 | `flags`: bit 0 left arm (else right), bit 1 board without a panel (the nRF5340 DK build), bit 2 `pair_id` is the frame serial |
| 4 | 1–16 bytes | `pair_id`, ASCII, to the end of the AD structure |

- `pair_id` is the glasses' frame serial (14 characters such as `S110LXXA123456`), read from the arm's
  factory record (QSPI 0x130000 + 0x15, read only). Stock firmware advertised the same serial on both
  arms, so both arms of one pair carry the same `pair_id`. A board without a readable serial (the DK)
  sends its chip's 64-bit device ID as 16 hex digits instead, with flags bit 2 clear; that ID matches
  no other board.
- The record does not change during a boot. Live state (in the case, bondable, panel) is in `HELLO`.
- Finding a pair: scan actively (the record is in the scan response) for the service UUID, group the
  advertisers by `pair_id`, take one left and one right arm, and skip boards with flags bit 1 unless a
  panel-less board is wanted. With BlueZ the record is the `ManufacturerData` entry for key 0xFFFF of
  the device object (the bytes from offset 2), filled by an active discovery.

## 3. Device model

### 3.1 Arena
A flat byte array of **262144 bytes** (256 KiB, `HELLO.arena_size`), zero at boot. The host owns all
of it: it decides where fonts, bitmaps and frames live, and keeps an exact mirror of it. The arm
never interprets arena bytes except where a command points at them, and checks every access.

### 3.2 Bitmaps
A bitmap is a base (`aw`), a row stride in bytes, and a depth:
- **4 bpp** (the panel's depth, levels 0…15): two pixels per byte, the left pixel in the high nibble.
  Pixel (x, y) is nibble x of row y: byte 4·aw + y·stride + x/2, high nibble if x is even.
- **1 bpp** (the "compressed" depth: fonts, masks, packed transfers): eight pixels per byte, MSB
  first. Pixel (x, y) is bit 7 − x%8 of byte 4·aw + y·stride + x/8. A 1 is "ink".

The 4-bpp pixel (x, y) of a bitmap at `aw` has the bit address 32·aw + 8·stride·y + 4·x (used by `DRAW`).

**Rectangle rule.** A rectangle (x, y, w, h) of a bitmap (base B = 4·aw, stride s, depth b) is *inside*
when w = 0 or h = 0 (empty: nothing is read or written), or when both hold:
ceil((x + w)·b/8) ≤ s and B + (y + h − 1)·s + ceil((x + w)·b/8) ≤ 262144.
`FILL`, `BLIT` and `BLIT_COMPRESSED` require their rectangles to be inside, else error 3 (arena).

### 3.3 Panel
JBD013VGA, **640 × 480, 4 bpp**, (0, 0) top-left as the wearer sees it. The panel's own memory holds
the displayed image; there are no framebuffers in the firmware. `DRAW` (§4.11) writes arena pixels into
the panel's frame cache and then latches the cache (JBD `97`), so a DRAW is never seen half done and
consecutive DRAWs appear one after the other, each complete. After `SET_PANEL(1)` the panel is on and
all pixels are 0. Level 0 is off (see-through), 15 is brightest; the levels are roughly linear in
luminance.

Power (0.4.8+): the panel is off at boot (powered once to check its ID: `HELLO` flags bit 1); the first
`DRAW` or `SET_PANEL(1)` switches it on with all pixels 0. With no host connected, the panel switches
off 30 s after the last `DRAW` or `SET_PANEL(1)` (the unattended-panel rule; queued DRAWs that run after
a disconnect count). Its image is then lost: a host that reconnects and finds `HELLO` flags bit 3 (panel
on) clear draws again. Before 0.4.8 the panel was on at boot and stayed on.

## 4. Sequenced commands (`CMD`)

### 4.1 Packet
```
offset 0: u16 seq | offset 2: command | command | ...      (≤ max_packet bytes)
```
Packets run in `seq` order (§7), the commands of a packet in order. A command never spans packets.

### 4.2 Command encoding (the source of truth)
The byte layouts of §4.4–§4.14 define the protocol; host libraries (such as the C++ helpers of
`scouter_kit_g1`) only build these bytes. For every command:
- Byte 0 is the opcode. Offsets below count from it. Multi-byte fields are little-endian; there is no
  alignment and no padding.
- The opcode alone fixes the size of a **fixed-size** command; it has no length byte.
- A **variable-size** command (`WRITE_ARENA`, `WRITE_ARENA_RLE`, `GLYPH_RUN_COMPRESSED`) has a fixed
  part whose last byte is `u8 n`, followed by exactly n data bytes: size = fixed part + n. **All fixed
  fields come before the variable bytes.**
- A command's meaning depends only on its own bytes, never on where it sits in a packet.

Size table (for parsers):

| op | Name | Size in bytes |
|---|---|---|
| 0x01 | `WRITE_ARENA` | 4 + n (n at offset 3) |
| 0x02 | `WRITE_ARENA_RLE` | 6 + n (n at offset 5) |
| 0x03 | `COPY_ARENA` | 7 |
| 0x10 | `FILL` | 14 |
| 0x11 | `BLIT` | 22 |
| 0x12 | `BLIT_COMPRESSED` | 23 |
| 0x13 | `GLYPH_RUN_COMPRESSED` | 14 + n (n at offset 13) |
| 0x20 | `DRAW` | 14 |
| 0x21 | `WAIT_UNTIL` | 5 |
| 0x22 | `SET_PANEL` | 2 |
| 0x30 | `FENCE` | 5 |

All other opcodes are reserved (§14).

### 4.3 Execution rules
- **Atomic:** a command either runs completely or changes nothing. A command that fails its checks is
  reported with `ERROR` (§6.2: seq, its index in the packet, op, code) and the next command of the
  packet runs.
- An unknown opcode (code 2) or a command cut off by the end of the packet (code 1) ends the packet,
  since the size of what follows is unknown; the commands before it stay done.
- The host mirror applies the same checks, so a correct host never causes an `ERROR`.

### 4.4 WRITE_ARENA (0x01)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x01 |
| 1 | 2 | u16 | `aw`: destination word address |
| 3 | 1 | u8 | `n`: number of data bytes |
| 4 | n | bytes | data |

Copies the n bytes to arena byte 4·aw. Error 3 if 4·aw + n > 262144. A longer write is split into
several commands at multiples of 4 bytes.

### 4.5 WRITE_ARENA_RLE (0x02): PackBits
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x02 |
| 1 | 2 | u16 | `aw`: destination word address |
| 3 | 2 | u16 | `out_length`: decoded size in bytes |
| 5 | 1 | u8 | `n`: number of PackBits bytes |
| 6 | n | bytes | PackBits stream |

Decodes into arena bytes 4·aw … 4·aw + out_length − 1. The stream is a sequence of runs, each starting
with a header byte h read as i8:
- 0 ≤ h ≤ 127: the next h + 1 bytes are copied (literal run);
- −127 ≤ h ≤ −1: the next byte is repeated 1 − h times (2…128);
- h = −128 (0x80): nothing (skipped).

Checks, before anything is written: error 3 if 4·aw + out_length > 262144; error 4 (format) unless the
runs consume exactly the n bytes (no run cut off) and produce exactly out_length bytes. Each command is
self-contained; large data is split into several commands at multiples of 4 output bytes.

### 4.6 COPY_ARENA (0x03)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x03 |
| 1 | 2 | u16 | `src`: source word address |
| 3 | 2 | u16 | `dst`: destination word address |
| 5 | 2 | u16 | `n_words`: number of 4-byte words |

memmove of 4·n_words bytes from 4·src to 4·dst (overlap allowed: as if the source were read first).
Error 3 if either range ends past 262144. Used for scrolling whole rows and moving data.

### 4.7 FILL (0x10)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x10 |
| 1 | 2 | u16 | `dst`: 4-bpp bitmap word address |
| 3 | 2 | u16 | `stride`: bytes per row |
| 5 | 2 | u16 | `x` |
| 7 | 2 | u16 | `y` |
| 9 | 2 | u16 | `w` |
| 11 | 2 | u16 | `h` |
| 13 | 1 | u8 | `level` (0…15) |

Sets every pixel of the rectangle (x, y, w, h) to `level`. Error 5 if level > 15, error 3 if the
rectangle is not inside (§3.2).

### 4.8 BLIT (0x11) and BLIT_COMPRESSED (0x12)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x11 (`BLIT`) or 0x12 (`BLIT_COMPRESSED`) |
| 1 | 2 | u16 | `src`: source bitmap word address |
| 3 | 2 | u16 | `src_stride`: bytes per source row |
| 5 | 2 | u16 | `src_x` |
| 7 | 2 | u16 | `src_y` |
| 9 | 2 | u16 | `dst`: destination 4-bpp bitmap word address |
| 11 | 2 | u16 | `dst_stride` |
| 13 | 2 | u16 | `dst_x` |
| 15 | 2 | u16 | `dst_y` |
| 17 | 2 | u16 | `w` |
| 19 | 2 | u16 | `h` |
| 21 | 1 | u8 | `mode` (§4.9) |
| 22 | 1 | u8 | `level` (0…15), `BLIT_COMPRESSED` only |

Combine the source rectangle (src_x, src_y, w, h) with the destination rectangle (dst_x, dst_y, w, h),
pixel by pixel, with `mode`:
- `BLIT`: the source is 4 bpp; a = the source level (0…15); the level L is 15.
- `BLIT_COMPRESSED`: the source is 1 bpp; a = 15·bit; L = `level`.

Errors: 5 if mode > 3 or (`BLIT_COMPRESSED`) level > 15; 3 if a rectangle is not inside (the source
with its depth, the destination with 4 bpp).

**Order, for overlapping source and destination.** Let S = 8·(4·src + src_y·src_stride) + src_x·b and
D = 8·(4·dst + dst_y·dst_stride) + 4·dst_x (the bit addresses of the first pixels, b = source depth).
If D > S, rows are processed from the last to the first and pixels from right to left, otherwise from
the first row and the left. Each destination pixel is read, combined and written before the next one.
For a source and destination with the same depth and stride this equals reading the whole source first
(memmove), so a region can be scrolled in any direction with one `BLIT`. Disjoint regions do not
depend on the order.

### 4.9 Modes
For a destination pixel d (0…15), a source value a (0…15) and a level L (0…15):

| mode | Name | d' | 1-bpp source (a = 0 or 15) |
|---|---|---|---|
| 0 | `COPY` | (a·L + 7)/15 | bit ? L : 0 |
| 1 | `MAX` | max(d, (a·L + 7)/15) | bit ? max(d, L) : d |
| 2 | `MASK` | (d·(15 − a) + L·a + 7)/15 | bit ? L : d |
| 3 | `ERASE` | (d·(15 − a) + 7)/15 | bit ? 0 : d |

So a is the source's coverage and L its level: COPY replaces the rectangle, MAX lightens, MASK draws
L where the source covers (a 4-bpp source is an anti-aliased mask, with L = 15 for `BLIT`), ERASE
darkens toward 0. With a 4-bpp source and L = 15, COPY gives d' = a and MAX gives max(d, a).

### 4.10 GLYPH_RUN_COMPRESSED (0x13)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x13 |
| 1 | 2 | u16 | `font`: font blob word address (§5) |
| 3 | 2 | u16 | `dst`: destination 4-bpp bitmap word address |
| 5 | 2 | u16 | `dst_stride` |
| 7 | 2 | i16 | `x`: pen start |
| 9 | 2 | i16 | `y`: baseline |
| 11 | 1 | u8 | `mode` (§4.9) |
| 12 | 1 | u8 | `level` (0…15) |
| 13 | 1 | u8 | `n`: number of varint bytes |
| 14 | n | bytes | glyph indices, varints |

The pen starts at (x, y). For each glyph index g, in order:
1. if a glyph p came before it in this command, pen_x += kern(p, g) (§5.4; 0 without a pair);
2. its 1-bpp bitmap (w × h) is combined with `mode` and `level` (§4.9, a = 15·bit) into the
   destination with its top-left pixel at (pen_x + x_off, y + y_off);
3. pen_x += advance.

**Clipping.** The destination bitmap is 2·dst_stride pixels wide and reaches to the end of the arena.
Glyph pixels left of column 0, right of column 2·dst_stride − 1, above row 0 or past the arena's end are
not drawn (no error). The host keeps a run inside its own bitmap's height.

**Varints.** Unsigned LEB128: 7 bits per byte, the least significant group first, bit 7 set on every
byte except the last of each index; at most 3 bytes. Minimal encodings take 1 byte below 128 and
2 bytes below 16384; longer (non-minimal) encodings of the same value are accepted. Examples: 127 = `7f`,
128 = `80 01`, 300 = `ac 02`, 65535 = `ff ff 03`.

Checks, before anything is drawn: error 5 if mode > 3 or level > 15; error 9 (font) if the blob is
invalid (§5.5) or an index ≥ glyph_count; error 1 if a varint is cut off at the end of the n bytes or
longer than 3 bytes. With n = 0 nothing is drawn. `MASK` is the text mode; `COPY` also writes the 0
bits of each glyph rectangle, so overlapping glyph boxes clear each other.

### 4.11 DRAW (0x20)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x20 |
| 1 | 3 | u24 | `arena_bit`: bit address of the first source pixel (a multiple of 4) |
| 4 | 2 | i16 | `dst_x`: panel column |
| 6 | 2 | i16 | `dst_y`: panel row |
| 8 | 2 | u16 | `src_w` |
| 10 | 2 | u16 | `src_h` |
| 12 | 2 | u16 | `src_stride`: bytes per source row |

Writes the src_w × src_h 4-bpp pixels starting at `arena_bit` (rows src_stride bytes apart) to the
panel at (dst_x, dst_y), then latches the panel cache. Any x, width and source pixel are allowed.
- Pixels outside the panel (0 ≤ X < 640, 0 ≤ Y < 480) are skipped (clipping on all sides).
- Errors, checked in this order, each sent without a `DRAW_DONE`: 5 if `arena_bit` is not a multiple
  of 4 or src_w = 0 or src_h = 0; 3 if the source is not inside the arena (with odd = (arena_bit/4) & 1:
  ceil((odd + src_w)/2) ≤ src_stride and arena_bit/8 + (src_h − 1)·src_stride + ceil((odd + src_w)/2)
  ≤ 262144); 7 in safe mode.
- If no panel is available (powered off and failing to power on, faulted, or a board without one) the
  DRAW sends `DRAW_DONE` with 0 rows and then `ERROR` 8, so host timing goes on.
- Otherwise `DRAW_DONE` (§6.1) reports the rows written (after clipping; 0 if entirely off the panel)
  and the times: the image changes at `t_end`. A full 640 × 480 DRAW takes ≈ 53 ms, a 30-row text band
  ≈ 2.4 ms.

`DRAW` and `WAIT_UNTIL` are the only commands that take panel or wait time; the others work in RAM.

### 4.12 WAIT_UNTIL (0x21)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x21 |
| 1 | 4 | u32 | `t_us`: device time |

Holds the queue until device time t_us. With d = (i32)(t_us − now): d ≤ 0 continues at once and counts
as late (`STATUS.late`); otherwise the queue waits min(d, 2 000 000) µs. Used before `DRAW` for eye
sync and animation (§9).

### 4.13 SET_PANEL (0x22)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x22 |
| 1 | 1 | u8 | `on`: 0 off, 1 on |

0: the panel's rails are switched off (its image is lost). 1: power on if off (the image is all 0).
Error 5 for on > 1, error 8 if the panel does not come up. A DRAW while the panel is off powers it on
first.

### 4.14 FENCE (0x30)
| Offset | Size | Type | Field |
|---|---|---|---|
| 0 | 1 | u8 | op = 0x30 |
| 1 | 4 | u32 | `token` |

Sends `FENCE` (§6.1) with the token when the queue reaches it: a sync point and a queue-latency probe.

## 5. Font blob (shared by the firmware, the host library and the font converter)

One format for the converter's output file, the library's input (`LoadFont`), the subset the library
uploads, and what the firmware reads. A blob is placed at a word address in the arena as is.

### 5.1 Layout
| Offset | Size | Field |
|---|---|---|
| 0 | 3 | magic `"SKF"` (`53 4B 46`) |
| 3 | 1 | format version = 1 |
| 4 | 4 | `size`: blob length in bytes, header included |
| 8 | 2 | `glyph_count` n (1…65535) |
| 10 | 2 | `kern_count` k (0…65535) |
| 12 | 1 | `ascent`: pixels above the baseline |
| 13 | 1 | `descent`: pixels below the baseline |
| 14 | 1 | `line_spacing`: baseline-to-baseline distance in pixels |
| 15 | 1 | flags = 0 (reserved) |
| 16 | 12·n | glyph table (§5.2) |
| 16 + 12n | 4·n | codepoints (§5.3) |
| 16 + 16n | 4·k | kerning keys (§5.4) |
| 16 + 16n + 4k | k | kerning adjustments (§5.4) |
| 16 + 16n + 5k | … | glyph bitmaps (§5.2), up to `size` |

### 5.2 Glyph table and bitmaps
Entry i (12 bytes) describes glyph index i:

| Offset | Size | Field |
|---|---|---|
| 0 | 4 | `bits`: bit offset of the glyph bitmap from the blob start |
| 4 | 1 | `w`: bitmap width (0…255) |
| 5 | 1 | `h`: bitmap height (0…255) |
| 6 | 1 | `advance`: pen advance in pixels (0…255) |
| 7 | 1 | reserved = 0 |
| 8 | 2 | `x_off` (i16): bitmap left edge relative to the pen |
| 10 | 2 | `y_off` (i16): bitmap top edge relative to the baseline (negative = above) |

The bitmap is 1 bpp, w·h bits, **tightly packed**: row after row, w bits per row, no padding between
rows or glyphs. Bitmap pixel (c, r) is blob bit `bits` + r·w + c, i.e. bit 7 − j%8 of blob byte j/8
with j = bits + r·w + c (in the arena: bit address 32·font + j). A glyph with w = 0 or h = 0 (a space)
has no bitmap; its `bits` is 0. Bitmaps may start at any bit and may be shared between glyphs.

### 5.3 Codepoints (host only)
n × `u32`: the Unicode codepoint of glyph i. Glyph order (user decision, shared with VM.md §4.1):
glyphs 0–9 are `0`–`9`, 10–35 are `A`–`Z`, 36–61 are `a`–`z` (each present in that slot when the font
has it), and any other glyphs follow from index 62 in ascending codepoint order. So the table is not
sorted as a whole; hosts build their own lookup. The firmware ignores this table. Hosts map a character
without a glyph to U+FFFD if the font has it, else they skip it.

### 5.4 Kerning
k × `u32` keys, strictly ascending, key = left_index·65536 + right_index, then k × `i8` adjustments
in the same order. kern(p, g) is the adjustment of key p·65536 + g, or 0 if the key is absent
(binary search). Monospaced fonts (Departure Mono) have k = 0. A subset blob renumbers the keys.

### 5.5 Validity (what the firmware checks, error 9)
The 16-byte header lies inside the arena; magic and version match; n ≥ 1; 16 + 16n + 5k ≤ size;
4·font + size ≤ 262144; and every glyph of the run has bits + w·h ≤ 8·size.

### 5.6 Metrics for layout (host)
A line's height is ascent + descent; the next line's baseline is line_spacing lower. The advance width
of a run is Σ advance + Σ kern of its consecutive pairs; its ink can extend past the pen by the
glyphs' x_off and w. Glyph sizes, offsets and advances are in panel pixels (no scaling on the arm).

## 6. Messages from the arm (`EVT`)

Every message starts with `u8 type`. Layouts as `offset: type field`, little-endian.

### 6.1 Table
| type | Name | Layout | Size |
|---|---|---|---|
| 0x01 | `ACK` | 0: u8 type, 1: u32 epoch, 5: u16 rx_next, 7: u16 exec_next, 9: u8 credits, 10: u32 sack, 14: u32 t_us (§7) | 18 |
| 0x02 | `DRAW_DONE` | 0: u8 type, 1: u32 epoch, 5: u16 seq, 7: u32 t_start, 11: u32 t_end, 15: u16 rows (the DRAW in packet `seq`) | 17 |
| 0x03 | `ERROR` | 0: u8 type, 1: u32 epoch, 5: u16 seq, 7: u8 index, 8: u8 op, 9: u8 code, 10: u32 t_us | 14 |
| 0x04 | `FENCE` | 0: u8 type, 1: u32 epoch, 5: u16 seq, 7: u32 token, 11: u32 t_us | 15 |
| 0x05 | `NOTICE` | 0: u8 type, 1: u32 epoch, 5: u32 t_us, 9: u8 kind, 10: body (§6.3) | 10 + body |
| 0x10–0x1F | input events | reserved, §6.4 | |
| 0x80 + op | control reply | 0: u8 type, 1: u8 tag, 2: u32 epoch, 6: u32 t_us, 10: u8 status, 11: body (§8) | 11 + body |

Notifications made while no host is subscribed are lost; `HELLO` gives the state after a reconnect.
The host matches a `DRAW_DONE` to its DRAW by `seq`, so it sends at most one DRAW per packet.

### 6.2 Error codes (`ERROR.code`, and `status` in control replies)
| code | Name | Meaning |
|---|---|---|
| 1 | malformed | command cut off by the packet's end, bad varint, packet shorter than 2 or longer than 244 bytes |
| 2 | unknown op | unknown command op (ends the packet) or unknown control op |
| 3 | arena | a rectangle or range outside the arena or its bitmap |
| 4 | format | invalid PackBits stream |
| 5 | argument | mode > 3, level > 15, DRAW bit address or size, SET_PANEL value, control argument |
| 6 | window | packet outside the receive window (§7) |
| 7 | safe mode | drawing disabled after repeated unhealthy boots |
| 8 | panel | no panel available (off, faulted, SPI error, board without a panel) |
| 9 | font | invalid font blob or glyph index (§5.5) |

A packet-level error has index 0 and op 0 (code 1, bad packet length) or the packet's first op byte
(code 6).

### 6.3 Notices
`NOTICE.kind` and body: 1 link parameters (0: u16 interval ×1.25 ms, 2: u16 latency, 4: u16 timeout
×10 ms); 2 PHY (0: u8 tx, 1: u8 rx; 1 = 1M, 2 = 2M); 3 panel fault at boot (0: u8 1); 6 link encrypted
(0: u8 1). Kinds 4 (safe mode) and 5 (PMIC) are reserved.

### 6.4 Reserved input events (not sent by 0.4.x)
Layout: 0: u8 type, 1: u32 epoch, 5: u32 t_us, 9: u16 count, 11: body. `count` increases by 1 per
input event of any type, so a host sees gaps. A firmware that sends a type sets its capability bit
(§8.1).

| type | Name | Body | Capability |
|---|---|---|---|
| 0x10 | `TOUCH` | 0: u8 sensor (0 TouchBar), 1: u8 gesture (1 tap, 2 double tap, 3 triple tap, 4 long press start, 5 long press end, 6 swipe forward, 7 swipe back) | bit 8 |
| 0x11 | `WEAR` | 0: u8 state (0 removed, 1 worn) | bit 9 |
| 0x12 | `HEAD` | 0: u8 gesture (1 head up, 2 head down), 1: i8 pitch (degrees) | bit 10 |
| 0x13 | `CASE` | 0: u8 state (bit 0 in the case) | bit 11 |
| 0x14 | `BATTERY_LOW` | 0: u16 vbat_mv, 2: u8 percent | bit 12 |
| 0x15–0x1F | reserved | | |

## 7. Reliability

- **Window.** Q = 32 packets (`HELLO.queue_slots`). The arm stores packet s if exec_next ≤ s <
  exec_next + Q (16-bit sequence arithmetic), runs packets from exec_next while the next one is there,
  and frees each slot after running it. rx_next = the lowest sequence number not yet received.
  credits = exec_next + Q − rx_next = how many packets the host may have beyond rx_next.
- **ACK** carries rx_next (cumulative), exec_next, credits, `sack` (bit i set = packet rx_next + 1 + i is
  held) and the device time. Sent ≤ 2 ms after a burst of packets, at once on a hole or a duplicate,
  and after each executed packet (coalesced). The ACK that reports a drained queue (exec_next =
  rx_next) is sent once more 100 ms later unless another ACK went out meanwhile (0.4.2+), so a lost
  last ACK does not leave the host waiting. A repeated ACK is no news to the host, not a loss signal.
- **Duplicates and out-of-window packets** are not stored; they are acked, and an out-of-window packet
  is also reported with `ERROR` 6. Nothing is dropped silently.
- **Host.** Sends only within the credits (and its own in-flight limit). A host may stop sending at any
  point and continue later. A packet is unacked until an ACK's rx_next passes it or its sack bit is set.
- **Retransmission.** The host resends a hole that the sack shows (a missing packet below a held one) at
  once, at most once per round trip, and the oldest unacked packet when no ACK has moved rx_next for
  RTO. A fixed short timeout is not enough: packets the host has written can wait in its own Bluetooth
  stack (BlueZ, the kernel, the controller) for many connection intervals, so they look lost, and
  resending them queues more (congestion collapse, seen with a fixed 100 ms). Use an adaptive RTO as
  TCP does (RFC 6298):
  - Sample R = time from a packet's first write to the first ACK that covers it, only for packets never
    resent (Karn's rule).
  - First sample: SRTT = R, RTTVAR = R/2. Then RTTVAR = 3/4·RTTVAR + 1/4·|SRTT − R|,
    SRTT = 7/8·SRTT + 1/8·R.
  - RTO = max(SRTT + 4·RTTVAR, floor) with floor = max(4 × connection interval, 100 ms); 1 s before the
    first sample. The floor only bounds the RTO from below; it is not a fixed timeout. An upper bound of
    a few seconds keeps recovery fast (the library: 4 s).
  - Each resend doubles the RTO (exponential backoff; the library caps it at ×8); an ACK that moves
    rx_next resets it.
- **Window query.** `HELLO` (§8.1) returns rx_next, exec_next and credits as an ACK does (without the
  sack). A host that still waits for the arm (packets not run, credits, a `DRAW_DONE`) with nothing
  unacked, so that no resend would prompt an ACK, and has heard nothing from the arm for a while
  (the library: 300 ms) sends `HELLO` and applies these fields. Its epoch also shows a reboot. A DRAW
  whose packet exec_next has passed without a `DRAW_DONE` has run; its `DRAW_DONE` was lost.
- **Epoch** (`u32`, non-zero): random at boot and at `RESET_SESSION`, in every ack, event and reply. The
  window restarts at sequence 0 with a new epoch. After a reconnect the host sends `HELLO`: same epoch →
  the arena and window are as the arm left them; the host resends from rx_next. New epoch → the arm's
  memory is not what the host thinks (a reboot clears the arena): the host resets its mirror, uploads
  what it needs again and redraws. The library re-uploads its fonts and bitmaps on a new epoch.
- **Control requests** (§8) do not use the window; they are answered by another thread on arrival, also
  while the queue waits (`WAIT_UNTIL`) or draws. A lost request or reply is retried by the host after a
  timeout, with a new tag (`RESET_SESSION` and `REBOOT` may then run twice).

## 8. Control requests (`CTRL`)

Request: 0: u8 op, 1: u8 tag, 2: body. Reply on `EVT`: 0: u8 0x80 + op, 1: u8 tag, 2: u32 epoch,
6: u32 t_us, 10: u8 status, 11: body. Status 0 = ok, else an error code of §6.2 (the body may then be
empty). Body offsets below count from the body's first byte.

| op | Name | Request body | Reply body |
|---|---|---|---|
| 0x01 | `HELLO` | — | §8.1 (43 bytes) |
| 0x02 | `PING` | 0: u32 host_time | 0: u32 host_time (echo), 4: u32 t_rx, 8: u32 t_tx (§9) |
| 0x03 | `STATUS` | — | §8.3 (48 bytes) |
| 0x04 | `RESET_SESSION` | 0: u8 flags (bit 0: clear the arena to 0) | 0: u16 flushed (packets dropped from the queue), 2: the `HELLO` body |
| 0x05 | `LINK` | 0: u16 interval_min, 2: u16 interval_max (×1.25 ms), 4: u16 latency, 6: u16 timeout (×10 ms), 8: u8 phy (0 keep, 1 1M, 2 2M) | 0: i8 param_result, 1: i8 phy_result (0 = asked; the central decides, `NOTICE` 1/2 report changes) |
| 0x06 | `LOG_READ` | 0: u16 offset | 0: u16 total, 2: u16 offset, 4: bytes of the arm's text log |
| 0x07 | `HEALTH_OK` | — | 0: u8 unhealthy (the host confirms the arm works; resets the safe-mode counter) |
| 0x08 | `REBOOT` | 0: u8 mode (0 normal, 1 clear the boot counter, 2 into safe mode) | empty, then the arm reboots |
| 0x09 | `PANEL_PROBE` | diagnostic | diagnostic |
| 0x0A | `BRIGHTNESS` | 0: u8 level (0…42; 0xFF or no body = query) | §8.4 (9 bytes) |
| 0x0B | `PANEL_READ` | 0: u16 y0, 2: u16 rows | §8.5 (22 bytes) |
| 0x0C | `BATTERY` | — | §8.2 (6 bytes) |
| 0x0D | `ARENA_CRC` | 0: u16 aw, 2: u16 n_words (0 = to the arena's end) | 0: u32 crc32 (zlib's) of arena bytes 4·aw … 4·(aw + n_words) − 1, 4: their first 16 bytes (0-padded); status 3 if the range ends past the arena. Computed on arrival: send it once every packet has run. |
| 0x0E | `POWER` | 0: u8 page (0 or no body: live values; 1…8: the accumulators of state page − 1) | §8.6 (48 or 40 bytes); status 5 for a page above 8. 0.4.4+, read only |
| 0x3F | `FAULT` | test builds only | |

### 8.1 HELLO
The reply header carries the **epoch**. Body (43 bytes; 42 before 0.4.2; a host accepts a longer body,
later fields are appended):

| Offset | Type | Field |
|---|---|---|
| 0 | u8 | `proto` = 4 |
| 1 | u8 ×3 | `fw_major, fw_minor, fw_patch` (0.4.x) |
| 4 | u8 | `role`: 0 right arm, 1 left arm |
| 5 | u8 | `flags`: bit 0 safe mode, 1 panel ok, 2 PMIC ok, 3 panel on, 4 charging enabled, 5 fault-injection build, 6 link encrypted, 7 in the case (VBUS) |
| 6 | u32 | `caps` (below) |
| 10 | u16 | `panel_w` = 640 |
| 12 | u16 | `panel_h` = 480 |
| 14 | u8 | `panel_bpp` = 4 |
| 15 | u32 | `arena_size` = 262144 |
| 19 | u8 | `queue_slots` = 32 |
| 20 | u16 | `max_packet` |
| 22 | u16 | `rx_next` |
| 24 | u16 | `exec_next` |
| 26 | u8 | `credits` |
| 27 | u8 | `brightness`: current level 0…42 |
| 28 | u32 | `boot_count` |
| 32 | u32 | `reset_reason` |
| 36 | u32 | `last_draw_us` |
| 40 | u8 | `safe_level` |
| 41 | u8 | `unhealthy` |
| 42 | u8 | `board` (0.4.2+): bit 0 board without a panel (the nRF5340 DK build), bit 1 bondable now (in the case, or in the DK's pairing window after boot) |

`caps` (features of this firmware; hardware state is in `flags`): bit 0 kerning in glyph runs, bit 1
`BRIGHTNESS`, bit 2 `BATTERY`, bit 3 `PANEL_READ`, bit 4 `ARENA_CRC`, bit 5 `POWER` (0.4.4+), bits 8–12
the input events of §6.4; others 0. 0.4.0–0.4.2 send caps = 0x0000001F, 0.4.4+ 0x0000003F.

### 8.2 BATTERY
Body: 0: u16 vbat_mv, 2: u8 percent, 3: u8 flags, 4: u16 vterm_mv (values at most 5 s old).
- `flags`: bit 0 PMIC answering, 1 battery detected, 2 charging (trickle, constant current or constant
  voltage), 3 charge complete, 4 in the case (VBUS), 5 charging enabled.
- `vterm_mv`: the end-of-charge voltage set by the firmware (4400 or 4200; 0 unknown).
- `percent`: a rough estimate: 100 if the charge is complete, else (vbat_mv − 3500)·100/(vterm_mv −
  3600) clamped to 0…100; 255 if unknown (no PMIC reading). It reads high while charging.

### 8.3 STATUS (diagnostics)
Body (48 bytes): 0: u16 vbat_mv, 2: u16 ntc_code, 4: u8 charge_status, 5: u8 vbus_status, 6: u8
vterm_code, 7: u8 pmic_flags, 8: u8 charge_refused, 9: u32 packets, 13: u32 commands, 17: u16 errors,
19: u32 draws, 23: u16 late, 25: u16 duplicates, 27: u16 window_errors, 29: u16 event_drops, 31: u16
interval, 33: u16 latency, 35: u16 timeout, 37: u8 phy, 38: u16 mtu, 40: u32 uptime_ms, 44: u32
i2c_errors.

### 8.4 BRIGHTNESS
Levels 0…42 are the stock firmware's user brightness levels (default 21 at boot; kept in RAM only).
Body (9 bytes): 0: u8 level, 1: u16 luminance, 3: u8 current, 4: u16 luminance_read_back, 6: u8
current_read_back, 7: u8 base, 8: u8 applied (the panel's luminance and current registers as written
and as read back; `applied` = 0 if the panel is off: the level is used at the next power-on). Status 5
for a level above 42.

### 8.5 PANEL_READ
Body (22 bytes): 0: u32 crc32, 4: u16 rows_read, 6: 16 bytes. Reads rows y0 … y0 + rows − 1 back from
the panel cache and returns their CRC-32 (zlib's, over 320 bytes per row, two pixels per byte, left
pixel in the high nibble) and the first 16 bytes. Equal to the host mirror's CRC after the host drew
those rows: proves the pixels reached the panel. Status 8 if the panel is off.

### 8.6 POWER (0.4.4+)
Read-only power telemetry. The arm takes one nPM1300 sample a second (VBAT and IBAT, the pack's NTC, the
PMIC's die temperature, VSYS, VBUS) and converts IBAT as the stock firmware's driver does (NCS v2.5.1
`npm1300_charger`: full scale 1000 mA while discharging, 38 mA while charging, 1024 steps; positive =
discharging). The names `BATT_MV`, `BATT_MA` and `BATT_TEMP` are those of the protocol-5 properties
(VM.md §2.5). A sample whose `ADCIBATMEASSTATUS` has bit 4 set (seen while VBUS comes and goes) is left out
of the current averages.

Page 0 (48 bytes): 0: u16 `BATT_MV`, 2: i16 `BATT_MA` (64-sample average, mA), 4: i16 `BATT_TEMP` (0.1 °C,
β 4250 as stock; −32768 unknown), 6: i16 die temperature (0.1 °C, 394.67 − 0.7926 × code), 8: i32 batt_ua
(64-sample average, µA), 12: i32 ibat_ua (last sample), 16: u16 vbus_mv, 18: u16 vsys_mv, 20: u16 vrect_mv
(the case link's last VRECT, §8.6.1), 22: u8 charge_status (0x03 0x34), 23: u8 charge_error (0x03 0x36),
24: u8 vbus_status (0x02 0x07), 25: u8 ldsw_status (0x08 0x04), 26: u8 ibat_stat (0x05 0x10), 27: u8
case_status (last status sent to the case: 0 charging, 1 full, 2 too warm, 4 not charging; 0xFF none
yet), 28: u8 case_level (0xFF none), 29: u8 case_lid (last lid byte from the case: 7 open, 8 or 6
closed), 30: u16 case_requests, 32: u16 case_age_s (seconds since the last case request, 0xFFFF none),
34: u16 cpu_permille (app core not idle during the last second), 36: u32 samples, 40: u8 flags (bit 0 ESB
quiet, 1 panel on, 2 host connected, 3 advertising at 1 s, 4 console UART built in, 5 battery full
latched, 6 IMU set to its boot state, 7 case link built in), 41: u8 state (bit 0 panel on, bit 1 host
connected, bit 2 VBUS), 42: u8 LSM6DSO WHO_AM_I, 43: u8 its CTRL1_XL, 44: u8 its CTRL2_G (0xFF unknown),
45: u16 IBAT samples left out, 47: u8 GPO events (low byte, wraps).

Pages 1…8 (40 bytes; 36 in 0.4.4, without `ua_samples`): the accumulators since boot of state page − 1:
0: u32 samples, 4: u32 ua_samples (current samples used), 8: i64 sum of IBAT (µA), 16: i32 sum of
`BATT_TEMP` (0.1 °C), 20: i32 sum of the die temperature (0.1 °C), 24: u64 busy cycles, 32: u64 all cycles
(app core, 32768 Hz). The difference of two reads gives the averages over the samples in between, also
for states with no host connected (`tools/power_log.py away`).

#### 8.6.1 The case link (0.4.6+, not part of this protocol)
The arm answers the charging case's requests over its ST25DV NFC tag as the stock firmware does
(development tree `docs/power.md` §4): the battery reply tells the case to keep powering the arm
(charging) or to stop (full); without it the case never stops its NFC power transfer. Nothing in this
protocol changes it; `POWER` shows its state.

## 9. Time and eye sync

- **Clock model (PING).** The host sends its time s; the arm replies t_rx (receive) and t_tx (send);
  the host notes the reply's arrival r. Offset = t_rx − s + ((t_tx − r) − (t_rx − s))/2 (mod 2^32),
  delay = (r − s) − (t_tx − t_rx). Keep the sample with the smallest delay of the last ~16; its error
  bound is delay/2. Repeat every few minutes (drift ≤ ~130 ppm).
- **Both eyes at once.** Compose on both arms; when both have acked every packet (rx_next), choose
  T = now + lead (lead ≈ 2 × connection interval + clock error + 15 ms) and send `WAIT_UNTIL(T in each
  arm's clock)` followed by the DRAWs to each arm. Both images change at their DRAW's `t_end` ≈ T + SPI
  time of the DRAW; `DRAW_DONE` lets the host measure the skew (≈ 1 ms between the arms).
- **Animation.** A sequence of `WAIT_UNTIL(T_k)` + `DRAW` from frames already in the arena plays at
  exact times, independent of the link (at most 2 s per wait).

## 10. Security and link policy

- In the charging case (the arm sees VBUS from the case's charger): bondable, and every write (CMD,
  CTRL, SMP) is obeyed, bonded or not.
- Out of the case: not bondable; only a bonded, encrypted link is obeyed. The arm asks for encryption at
  connect; a peer that cannot encrypt is disconnected (after 10 s at most). Writes on an unencrypted
  link are ignored without a reply.
- Pairing: LE Secure Connections, Just Works, bonding required, up to 3 bonds (the oldest is replaced).
- Pairing again: a host that lost its bond (for example removed the device) pairs again in the case.
  Since 0.4.2 the new Just Works bond replaces the arm's old one for that host's address (Zephyr's
  `BT_SMP_ALLOW_UNAUTH_OVERWRITE`); 0.4.0 refused it. Out of the case every pairing request is refused.
- Accepted limit: with all 3 bonds in use, any new pairing attempt drops the oldest bond to make room (Zephyr), even an unbonded peer out of the case whose pairing is then refused; the user accepts this (only the oldest bond is ever lost).
- One connection per arm. The arm accepts any link parameters; the host chooses them (`LINK`): e.g.
  7.5–15 ms on the 2M PHY for uploads and animation, 50–100 ms when idle.

## 11. Constants and limits
| Item | Value |
|---|---|
| protocol version | 4 |
| arena | 262144 bytes; `aw` 0…65535; `bit` 0…2097151 |
| panel | 640 × 480, 4 bpp |
| CMD packet | ≤ max_packet ≤ 244 bytes, `u16 seq` included |
| window | 32 packets |
| CTRL request | ≤ 24 bytes |
| font | n ≤ 65535 glyphs, glyphs ≤ 255 × 255, offsets i16, advance ≤ 255 |
| WAIT_UNTIL | ≤ 2 s per wait |

## 12. Example: a one-packet text update
Clear a 400 × 30 band of a 640 × 480 frame at arena word 0 (stride 320), write "Hi 12:30" with a font
blob at word 0x9600 whose glyph i is codepoint 32 + i, and show the band at panel (20, 200):
```
seq                  07 00
FILL                 10  00 00  40 01  14 00  28 00  90 01  1e 00  00
GLYPH_RUN_COMPRESSED 13  00 96  00 00  40 01  14 00  41 00  02  0f  08  28 49 00 11 12 1a 13 10
DRAW                 20  50 90 01  14 00  c8 00  90 01  1e 00  40 01
```
FILL (dst 0, stride 320, x 20, y 40, w 400, h 30, level 0); GLYPH_RUN_COMPRESSED (font 0x9600, dst 0,
stride 320, pen (20, 65), mode MASK, level 15, n = 8 varint bytes); DRAW (bit 0x019050 = pixel (20, 40) of the frame,
to (20, 200), 400 × 30, stride 320). 52 bytes in all.

## 13. Changes from version 3
- `BLIT` takes a 4-bpp source only and no level; the 1-bpp source is the new `BLIT_COMPRESSED` (0x12).
- `GLYPH_RUN` (0x12, u8/u16 indices, absolute glyph addresses, per-glyph adjust bytes) is replaced by
  `GLYPH_RUN_COMPRESSED` (0x13): varint indices, the position-independent font blob of §5 with kerning,
  clipping at the bitmap's edges instead of an error.
- `SET_PANEL` has one byte; `HELLO` has a new layout with `caps` and the brightness level; `BATTERY`
  and `ARENA_CRC` are new; failing commands change nothing (atomic); a `BLIT` within one bitmap works
  in any direction; `WAIT_UNTIL` really stops waiting after 2 s.

### 13.1 Changes in 0.4.2 (still protocol 4, compatible)
- Pair record in the scan response, service UUID moved into the advertising data (§2.1).
- `HELLO` body 43 bytes: `board` byte (§8.1).
- The ACK of a drained queue is repeated once after 100 ms (§7).
- Pairing again over an old bond in the case (§10).

### 13.2 Changes in 0.4.4–0.4.10 (still protocol 4, compatible; development tree `docs/power.md`)
- 0.4.4: `POWER` (CTRL 0x0E, §8.6), caps bit 5.
- 0.4.6: the case link (§8.6.1).
- 0.4.8: power rules: the panel is off at boot and switches off 30 s after the last `DRAW` with no host
  connected (§3.3); advertising at 1 s after 60 s (§2.1); the console UART transmits only (no receiver);
  the LSM6DSO at 12.5 Hz low power with the gyroscope off; ESB quiet (work mode 1 in the net core's role
  reply). A `GLYPH_RUN` uses its glyph entries as validated before drawing (a destination that overlaps
  the font no longer changes them mid-run, matching the host mirror); a `FILL` of width or height 0 does
  nothing.
- 0.4.10: the case link keeps "full" through the case's periodic re-check (§8.6.1).

## 14. Future: command programs (not in v4)
The protocol is meant to become VM-like: the host stores command sequences in the arena and the arm
runs them in the background (animations, scheduled or input-driven screens). v4 already guarantees:
- **Self-delimiting commands.** The opcode fixes the size, or the fixed part ends with `u8 n` and
  exactly n data bytes follow (§4.2); all fixed fields come before the variable bytes. A sequence can be
  parsed from any byte buffer.
- **No dependence on BLE packets.** The packet header (`u16 seq`) is outside the commands, a command
  never spans packets, and its meaning depends only on its own bytes, not on its position.
- **Absolute operands.** Every operand is an arena word or bit address, a panel position, a level or a
  device time; font blobs are position-independent (§5).
- **Atomic commands.** A failing command changes nothing, so a program can report it and continue or
  stop deterministically.
- **Room for control opcodes.** 0x40–0x7F are reserved for program control (for example: run the
  sequence at an arena address, loop, relative wait, stop).

Still open for programs: events name a command by (seq, index), a stored command would need its arena
address instead; `WAIT_UNTIL` takes an absolute time, programs will want a relative wait.

Draft protocol 5 defines these programs as an on-arm VM: [VM.md](VM.md), reviewed on 2026-10-26
(development tree `docs/review-v5.md`). It answers the two open items above: `ERROR` carries the packet
offset and the arena address of a failing stored instruction, and programs wait with `SLEEP_UNTIL` on a
computed time or with the `TIMER` trigger.

## Appendix A. Host helpers (informative)
The C++ helpers in `scouter_kit/src/g1/scouter_kit_g1.hpp` each build one command of §4:

| Helper | Command |
|---|---|
| `WriteArena` | `WRITE_ARENA`, split into several commands when longer than one packet |
| `WriteArenaRLE` | `WRITE_ARENA_RLE` |
| `CopyArena` | `COPY_ARENA` |
| `Fill` | `FILL` |
| `Blit` | `BLIT` |
| `BlitCompressed` | `BLIT_COMPRESSED` |
| `GlyphRunCompressed` | `GLYPH_RUN_COMPRESSED`; its `U16 x, y` are sent as the i16 fields (the same 16 bits) |
| `Draw` | `DRAW`; its `U16 dst_x, dst_y` are sent as the i16 fields (the same 16 bits) |
