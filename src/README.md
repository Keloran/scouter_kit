# ScouterKit C++ library

Drop-in C++20 library for drawing on Even Realities G1 glasses that run the scouter firmware
(g1v0 0.5.x, protocol 5 with the on-arm VM, `../g1_firmware/docs/VM.md`). No build system: add the
`.cpp` files and the VM core's two `.c` files to your project. Linux + BlueZ for now.

| Path | What |
|---|---|
| `base/scouter_kit.hpp`, `.cpp` | device-neutral part: the gray virtual display, fonts, bitmaps, memory |
| `g1/scouter_kit_g1.hpp`, `.cpp` | G1 backend: BlueZ transport, Bluetooth thread, protocol 5 client, arena mirrors, program builder, emulated arms |
| `vm/vm.h`, `vm.c`, `vm_gfx.h`, `vm_gfx.c` | the arms' interpreter (`vm/README.md`): the library's mirrors and emulated arms run it |
| `c/scouter_kit_c.h`, `.cpp` | C interface to all of it, for the Go, Python and JavaScript bindings |
| `base/departure_mono.inc` | optional: Departure Mono 11 px as a font blob (`kDepartureMonoBlob`, SIL OFL 1.1) |
| `../third_party/` | moodycamel ConcurrentQueue 1.0.5 (BSD), Departure Mono 1.500 (OFL), liblc3 1.1.3 (Apache-2.0, the microphone), TinyCrypt 0.2.8's SHA-256 (BSD-3-Clause, the asset IDs; included by `scouter_kit_g1.cpp`, its includes made relative), with their licences |
| `../font_converter/font_converter.py` | TrueType/OpenType font → font blob |
| `../tests/` | emulator tests, C test, DK smoke test, glasses check |

## Compile

```
# app + library, talking to the glasses through BlueZ (needs libsystemd for sd-bus)
g++ -std=c++20 -O2 app.cpp src/base/scouter_kit.cpp src/g1/scouter_kit_g1.cpp src/vm/vm.c src/vm/vm_gfx.c \
    $(pkg-config --cflags --libs libsystemd) -lpthread -o app

# the same against two emulated arms (no Bluetooth, no libsystemd); define it for every file
g++ -std=c++20 -O2 -DSCOUTER_G1_EMULATOR app.cpp src/base/scouter_kit.cpp src/g1/scouter_kit_g1.cpp \
    src/vm/vm.c src/vm/vm_gfx.c -lpthread -o app

# C layer: add src/c/scouter_kit_c.cpp, include src/c/scouter_kit_c.h from C

# the microphone (Arm::MicStart): -DSCOUTER_G1_MIC for every file, and liblc3 compiled as C
for f in third_party/liblc3/src/*.c; do gcc -std=c11 -O2 -DLC3_PLUS=0 -DLC3_PLUS_HR=0 \
    -Ithird_party/liblc3/include -c $f -o lc3_$(basename $f .c).o; done
g++ -std=c++20 -O2 -DSCOUTER_G1_MIC app.cpp src/base/scouter_kit.cpp src/g1/scouter_kit_g1.cpp \
    src/vm/vm.c src/vm/vm_gfx.c lc3_*.o $(pkg-config --cflags --libs libsystemd) -lpthread -lm -o app
```
`g++` compiles the two `.c` files as C++ (they are valid C99 and C++20); `gcc` works for them too.
Tested with GCC 14.2 and libsystemd 257 (Debian 13), BlueZ 5.82.

## Example

```cpp
#include "scouter_kit/src/base/departure_mono.inc" // optional built-in font
#include "scouter_kit/src/g1/scouter_kit_g1.hpp"

namespace sk = scouter_kit;

int main() {
  std::string error;
  sk::g1::GrayConfig config;                  // dx 100, dy 9: a 740 x 471 display
  if (sk::g1::Init(&config, &error) != sk::g1::ERROR_NONE) {
    std::fprintf(stderr, "%s\n", error.c_str());
    return 1;
  }
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "", &error, 2); // all glyphs, 22 px
  sk::gray::Bitmap &line = sk::gray::CreateBitmap(700, 30);
  font.DrawLine(line, "Hello, both eyes", 350, 24, sk::ALIGN_CENTER);
  sk::gray::Draw(line, 20, 200);
  sk::Flush();                                // both eyes change together
  sk::g1::Deinit();                           // waits until it is shown, then disconnects
}
```

## The gray display (`scouter_kit.hpp`)

- **Pixels** are 0 (off, see-through) … 255 (brightest). The G1 has 16 levels; `contents` and
  `display` always hold the rounded values (level × 17).
- **Bitmaps** live in the glasses' memory. `CreateBitmap(w, h)` (all 0) or `LoadBitmap(pixels)`;
  `Write(bitmap, pixels, x, y)` puts an image into one. Both apply `gray::response[256]` (a level
  response curve, identity by default, for calibrating the panel's uneven levels) and then
  Floyd–Steinberg dithering (or `DITHER_NONE`). `Fill(bitmap, …)` fills a rectangle.
  `bitmap.contents` may also be written directly: `Flush()` uploads what changed (rounded, no curve).
- **Text:** `Font::DrawLine(bitmap, text, x, baseline_y, align, &width, gray)` draws UTF-8 text into
  a bitmap with glyph runs on the arms (one small command per line). `Measure` gives the advance
  width and the ink's height, ascent and descent; `ascent`, `descent`, `line_spacing` are the font's.
- **Display:** `gray::Draw(bitmap, x, y)` places a bitmap on the virtual display (later calls on top);
  `gray::Clear()` places black everywhere. `Flush()` uploads changed bitmap pixels, works out which
  display pixels change, and shows exactly those on both eyes at the same moment. Bitmaps are shown
  with their contents at the time of the `Flush`. `Flush` returns once queued; if the previous update
  is not on the display yet, it waits for it first. `gray::display` is the read-back of what the eyes
  show (read-only).
- **Fonts:** `LoadFont(blob, glyphs_to_load, &error, scale)`: the blob of `font_converter.py`;
  `glyphs_to_load` is the text of the characters to load (empty: all), plus the space and U+FFFD.
  `scale` enlarges pixel fonts. Only the selected glyphs go to the glasses.
- **Kept fonts and bitmaps:** a last `Keep::kStored` argument (`LoadFont(blob, glyphs, &error, scale,
  Keep::kStored)`, `LoadBitmap(pixels, dither, Keep::kStored)`) also keeps the font, or the bitmap's
  pixels as at its first `Flush`, in the device's own storage (the G1: each arm's flash). An arm that holds
  it gets it from there (`ASSET_LOAD`, a flash read) instead of over Bluetooth, also after a reboot; an
  arm that lacks it gets it as before and stores it in the background (about 1 s per 64 KB); an arm
  without the store (a firmware before 0.5.56) only gets it as before.
- **Glyph order** (every blob and every subset sent to the glasses, `../g1_firmware/docs/VM.md`
  §4.1): glyphs 0…61 are `0`–`9`, `A`–`Z`, `a`–`z`, so a digit's value is its glyph index (a missing
  one is an empty glyph with codepoint 0), and the other glyphs follow from index 62 by codepoint;
  a font of those 62 characters only may end before 62. Blobs from the older converter (plain
  codepoint order) still load; their subsets get this order.
- **Memory:** `total_memory` / `free_memory` (bytes per arm, 249 692 of the arena's 262 144: the
  registers and the library's own constants, text and staging rows are left out);
  `Unload(bitmap)`, `Unload(font)`, `UnloadAll()`. Failed loads return an object with
  `pimpl == nullptr` (`LoadFont` also sets the error text).
- Every call is thread-safe (one lock).

### Geometry
With `dx` and `dy` (`GrayConfig`, defaults 100 and 9 from the user's calibration) the display is
(640 + |dx|) × (480 − |dy|). The left eye shows columns 0…639 on its panel rows 0…470, the right eye
columns dx…dx + 639 on its rows dy…479 (`docs/virtual-display.md`).

## The G1 backend (`scouter_kit_g1.hpp`)

- **`Init(&config, &error, &connection)`** finds the arms, connects and returns once both are ready.
  `Connection{left, right}`: addresses, or empty for the bonded glasses (or, with the case open,
  glasses in range, which it bonds with through its own BlueZ agent: LE Secure Connections, Just
  Works). Candidates are matched by the pair record in their scan response (VM.md §6.1): boards
  without a panel are skipped without connecting, the right arm must carry the left one's pair ID
  (and the other way round), then connected, bonded and the strongest signal win; with several
  eligible candidates an active discovery runs for 4 s first. Addresses in `Connection` override all
  of this. `"none"` leaves an arm out (a single-arm board such as the nRF5340 DK).
  `Connection.adapter` picks the BlueZ adapter by name (`"hci1"`) or address (C: `sk_g1_init`'s
  `adapter`, Go: `Config.Adapter`); empty: the first powered adapter. Bonds belong to an adapter, so
  the arms bond anew with another one (`docs/dk-adapter.md`: the nRF5340 DK as a second adapter).
  Errors: `ERROR_BLUETOOTH_ERROR` (also for an adapter that is missing or off),
  `ERROR_NO_G1_FOUND`, `ERROR_G1_BONDING_ERROR`, `ERROR_G1_FIRMWARE` (not protocol 5; a protocol 4
  arm is recognised by its HELLO reply). A new session switches the panel off (VM.md §6.6: the
  display starts black, and black needs no panel power).
- **`Arm`** (`left`, `right`): one method per instruction of host code (VM.md §2.2) with plain
  values: `WriteArena`, `WriteArenaRLE`, `CopyArena`, `Unpack`, `Fill`, `Blit`, `BlitCompressed`
  (1-bpp source), `GlyphRunCompressed`, `Draw`, `Line`, `WaitUntil` (`SLEEP_UNTIL`), `SetPanel`,
  `SysWrite`, `Notify`/`Fence`, `Raise`, `Call` (stored code inside a packet), and `Run` for host
  code built with `Program`. Addresses are arena bytes. The library keeps the values in a pool of
  arena words at the top of the arena (VM.md has no immediates), writes a value once and reuses its
  word, and checks the word against the mirror before reuse, so host code that overwrote it heals.
  Each call is queued at once and run on the mirror; code the arm would refuse is not sent and
  counts in `GetState().errors` with `last_error`. `DeviceTime(in_us)` converts the host clock
  (`NowUs()`). `GetState()` gives the link (interval, idle profile), firmware, HELLO's flags
  (`safe_mode`, `encrypted`, `in_case`, `microphone`: `MIC` captures and streams), `triggers`, the
  expected `panel_on`, counters and when the eye showed its last update.
  Control requests: `Battery` and `Prop`/`Props` (`PROPS`, up to 12; `UNKNOWN` = 0x80000000 for a
  missing value), `PanelCrc` and `ArenaCrc` (compare the panel and the arena with what the library
  expects), `ReadArena` (arena bytes, 16 per `ARENA_CRC` reply). They first wait until the arm ran
  everything queued before, however long an upload takes while the arm makes progress (through
  reconnects too); they return false after 15 s without progress or 10 minutes in all, never a
  read-back of a panel that is still being updated. `PanelCrc` is false while the panel is off
  (the arm answers error 11). `SetBrightness` queues `SYS_WRITE BRIGHTNESS`.
- **Memory:** `g1::Alloc(bytes)` gives an address that is the same on both arms (one allocator, so a
  program image runs on both), shared with the fonts and bitmaps; `g1::Free`. `AddressOf(bitmap,
  &stride)` and `AddressOf(font)` say where the library put them, and `GlyphIndices(font, text)`
  gives the glyph indices a program draws (digits are their own values, VM.md §4.1).
- **Programs** (`Program`, VM.md §5): a builder for the byte code. Its layout is fixed when it is
  made: `Program(table, pool, pool_words, code)`, or `Program(address, pool_words)` for the table,
  the pool and the code one after the other. Operands are arena words (`ArenaWord`; `R(0)` …
  `R(14)` are the registers, any other word is an operand escape); `Const(v)` is the pool word that
  holds v. Labels (`NewLabel`, `Bind`, `At` for a fixed address) for `Jmp`, `Call` and the six
  branches; `Load(bits, …)`/`Store(bits, …)` for 32, 16, 8 and 4 bits; `SysRead`/`SysWrite`;
  `Fill`, `Blit`, `Glyphs`, `Draw`, `Line` in operand form; `SleepUntil`, `WaitEvent`, `Raise`,
  `Notify`, `End`, `Halt`, `Ret`; `Write`/`WriteRLE`; `Op` for any of them by opcode. `On(trigger,
  label)` fills the trigger table, `Changes(address, length)` declares what the program writes.
  `Valid(&error)` reports unbound labels, a full pool or bad operand counts. `Arm::Load(program)`
  queues the WRITEs of its table, pool and code and marks the declared regions (and the registers)
  as changed by a program: the mirror keeps what the host last wrote there and `ArenaCrc` refuses
  them until they are freed. `Start(table)` waits until the arm ran everything queued, then sends
  `VM_START`; `Stop()`, `Status(&VmStatus)` (`VM_STATUS`). Programs never survive a reboot: after
  one the library restores the arm's memory but does not start the program again.
- **Events:** `NextEvent(&event, timeout_ms)` returns the arm's `EVENT_NOTIFY` (from programs and
  host code; the library's own present NOTIFYs are filtered), `EVENT_HALT`, `EVENT_ERROR`,
  `EVENT_INPUT` and the library's `EVENT_RESET` (the arm lost its session: program stopped, memory
  and panel restored), oldest first; the last 256 are kept. `ReportInputs(mask, &in_force)` asks
  for `INPUT` events (`EVENTS`; in force = the request ∧ `HELLO.triggers`) and repeats the request
  after every reconnect, since the arm forgets it at a disconnection.
- **`Disconnect(ms)`** drops the link and connects again after ms; the session, the arm's memory and
  its program stay (a program runs on with the host away, VM.md §6.6).
- **Microphone** (`SCOUTER_G1_MIC`; VM.md §6.5, on the G1 the right arm, `State::microphone` =
  `HELLO.board` bit 2): `MicStart(MIC_32KBPS | MIC_16KBPS)` sends `MIC` 1 once the arm ran what was
  queued before (a queued `PANEL` = 0 would stop it); the arm shows its indicator (a 16 × 16 block in
  the panel's outer top corner, powering the panel on) and streams `MIC_DATA`. The Bluetooth thread
  decodes each frame with liblc3 into a `MicFrame` (10 ms, 160 samples of 16 kHz mono, its counter,
  the arm's time, the arrival), conceals a gap in the counter with the decoder's PLC (up to 100 frames;
  `concealed`), and keeps the last 200 frames for `MicRead(&frame, timeout_ms)`. The arm stops by
  itself at a disconnection, at `PANEL` = 0 and at a capture fault (`NOTICE` 5); the library never
  starts it again, and `GetMicState()` says why it stopped (`MIC_STOP_HOST`, `_PANEL_OFF`, `_FAULT`,
  `_LINK`) and counts events, frames, gaps, missing, undecodable and dropped frames. `MicStop()` sends
  `MIC` 0 (true without a session: the arm is off then) and switches the panel off again if it was off
  before `MicStart` (read with `PROPS`) and the library has not drawn since. While the microphone is on,
  the link keeps the active profile (VM.md: at most 15 ms). The library's mirror does not draw the
  indicator, so `PanelCrc` over rows 4–19 differs while it is on. A program that writes `PANEL` = 0
  while the microphone is on stops it (`NOTICE` 5, `MIC_STOP_PANEL_OFF`): the firmware never captures
  without the indicator.
- **`Connection.keep_sessions`** adopts each arm's session as `HELLO` reports it (its epoch and
  window) instead of resetting it: a program on the arm runs on and its memory stays, and no restore or
  drawing happens. The library then owns none of the arena (bitmaps, fonts and `Alloc` fail, the
  memory totals are 0) and its packets continue the arm's window: for apps that use control requests
  and events only, such as the microphone. After a reboot (new epoch) it resets as usual. C:
  `sk_g1_init_config` with `keep_sessions`; Go: `Config.KeepSessions`.
- **`Program::PanelPolicy(redraw)`**, the shared display policy (VM.md §6.6): the panel off while the
  arm lies in the case (`IN_CASE` 1) or the glasses are folded (`WORN` 0, which the firmware debounces:
  not worn after 3 s below the link's threshold), and while it holds the panel off the program's `DRAW`
  and `VIDEO` run aimed off the panel (VIDEO still decodes), so nothing shows. It takes the `CASE` and
  `WEAR` triggers and runs before the program's `START` handler (bound before or after it); once both
  have ended the next `DRAW` shows, and it jumps to `redraw` (a label of code that draws and ends) if
  given. Call it right after creating the program, before its first `DRAW` or `VIDEO`; it costs 80 bytes
  of code, 21 bytes per `DRAW`/`VIDEO`, two pool words (`PanelHidden()`: 1 while held off) and up to six
  constants. Host code and other programs' draws are not covered. C: `sk_g1_program_panel_policy(p,
  label or -1)`; Go: `Program.PanelPolicy(label or NoLabel)`. g1v0 0.5.32–0.5.58 kept the panel off in
  the case by themselves (the case hold, `NOTICE` 6, after which the library redraws the panel from its
  mirror) unless a host sent `CASE_PANEL` 1; since 0.5.60 the firmware only darkens the LEDs while the
  case talks to the arm.
  **`Connection.log`** logs to stderr. C: `sk_g1_config`'s `log`; Go: `Config.Log`; Python:
  `init(log=True)`. The library reads no environment variables.
- **Transport:** BlueZ over D-Bus with sd-bus: `Device1.Connect`/`Pair`, and the
  `AcquireWrite` (CMD, CTRL) and `AcquireNotify` (EVT) sockets for the data. Lost links reconnect by
  themselves (backoff 0.5…4 s).
- **Bluetooth thread:** drawing calls encode their host code at once and pass it through a
  moodycamel ConcurrentQueue per arm. The thread packs it into packets, keeps sequence numbers,
  credits, acks, selective acks and retransmits (RTO of VM.md §6.2: RFC 6298, at least max(4 ×
  interval, 100 ms), doubled per resend up to × 8 and at most 3 s; no RTT samples from resent packets
  or from packets behind a hole, whose ACK the hole delays), syncs each arm's clock with PING, and
  fires a `Flush`: when both arms have
  received everything before it, it sends each arm `WRITE [T] = T; SLEEP_UNTIL [T]`, the `DRAW`s
  and `NOTIFY [T], [T]` (the event's time is when the eye showed it), then resets the word, T = now
  + 2 intervals + clock error + 15 ms in that arm's clock (VM.md §7). The `DRAW`s are built at
  `Flush` time; the pool words they use stay pinned while the present is built (with the pool full
  of them, a `DRAW` carries its own operands). An eye whose whole panel is black after the `Flush` gets
  `PANEL` = 2 (standby) at T instead of black `DRAW`s: a lit black panel costs ≈ 4.6 mA, standby ≈ 1 mA,
  and the next `DRAW` wakes it in ≈ 4 ms (VM.md §6.6). That also blanks what a program drew there. Still
  black `SCOUTER_G1_BLACK_OFF_MS` (30 s) after it went black, the eye goes off (≈ 1 mA less; the next
  `DRAW` then takes ≈ 71 ms): the Bluetooth thread sends `PANEL` = `PANEL` & 1, so a panel that a
  program lit meanwhile stays on. An eye that was off before a `Flush` that leaves it black stays off.
  An app that wants the 4 ms wake keeps its standby itself (it writes `PANEL` = 2, as `g1 -standby`
  does): an all-black `Flush` always ends in off after the delay.
- **The mirror** of each arm is a second instance of the arms' interpreter (`src/vm`) that runs only
  host code, on `Arm::arena`, with a virtual clock: it decides what the arm would refuse, and its
  panel is the expectation for `PanelCrc` and for restores. Programs are not mirrored (VM.md §5.6).
- **Link profiles** (review §3.3): *active*, 7.5 ms on the 2M PHY, while packets are queued or a
  `Flush` waits; *idle*, 50 ms, latency 0, after 5 s without traffic (once `Init` has returned). The
  switch is a `LINK` request; the arm's `NOTICE` reports the parameters in force. `PING` runs every
  second while active and every 30 s on the idle link. While active, a bulk transfer that finds the
  link slower asks again, at most every 5 s (an arm sends the request 5 s after connecting at the
  earliest). Both links share the PC's controller, so the arms take turns: while both have packets
  to send, an arm that sent more bytes than the other waits, and both uploads end together (an arm
  waiting in `SLEEP_UNTIL` or silent does not hold the other back).
- **The asset store** (VM.md §6.10): `Store(blob, type, &error)` puts a blob on each arm that lacks it
  (staged in free arena memory, `ASSET_PUT`, waits for the arm; blocks, call it from the app's thread),
  `Assets(arm)` lists an arm's store, `Forget(id)` deletes on both; `AssetIdOf(blob)` is its ID, the
  first 16 bytes of its SHA-256 (TinyCrypt, vendored). `Program::AssetLoad(id, dst, off, n)` lets a
  program load assets by itself. The library reads each arm's list at every session setup, before it
  restores the arena.
- **Reconnect and epochs:** same epoch → resend from `rx_next`. New epoch (the arm rebooted) → the
  library resets the session, uploads the allocated memory again from the mirror and redraws the
  panel as the mirror shows it (only its lit bands, through an 8 KB staging area; the panel is off
  until then), then goes on; `Flush`es it had queued are shown by the other arm without waiting for
  it, and `EVENT_RESET` tells the application.

### Configuration (define before compiling)
| Macro | Default | |
|---|---|---|
| `SCOUTER_G1_EMULATOR` | off | emulated arms instead of BlueZ; adds `g1::emulator` (panels, loss, link drops, reboots, sensor values) |
| `SCOUTER_G1_MAX_PACKET` | 236 | packet size cap (236 = 9 HCI fragments of 27 bytes, this PC's dongle) |
| `SCOUTER_G1_IN_FLIGHT` | 12 | packets in flight per arm (4…16 all keep this PC's controller full; 12 leaves room for faster ones) |
| `SCOUTER_G1_STAGING_BYTES` | 8000 | arena kept for black DRAWs and panel restores |
| `SCOUTER_G1_POOL_WORDS` | 1024 | arena words for the host code's constants |
| `SCOUTER_G1_LINK_MIN` / `_MAX` | 6 / 6 | active link: connection interval asked for (× 1.25 ms), on the 2M PHY |
| `SCOUTER_G1_IDLE_LINK` | 40 | idle link: 50 ms |
| `SCOUTER_G1_IDLE_AFTER_MS` | 5000 | without traffic, then the idle link |
| `SCOUTER_G1_BLACK_OFF_MS` | 30000 | an all-black eye in standby this long, then off (0: never) |
| `SCOUTER_G1_MATCH_BRIGHTNESS` | 1 | one eye's panel guard cap (`NOTICE` 7, VM.md §6.6) becomes the other's `BRIGHTNESS_MAX`, so both show the lower level, 42 again when that eye's link drops and at `Deinit`; 0: each eye on its own (firmware 0.5.52+; never `BRIGHTNESS` itself) |
| `SCOUTER_G1_MIC` | off | the microphone: `MicStart` decodes with liblc3 (link `third_party/liblc3`); without it `MicStart` fails and the emulated arms have no microphone |

Arena layout: words 0…14 are the programs' registers (allocations start at 64); the top 12 388
bytes are the library's: the constants pool from 0x3CF9C, a DRAW's own operands at 0x3DF9C, the
present time word 0x3DFBC, glyph text from 0x3DFC0 and the zero staging rows from 0x3E0C0 to the end.
Host code may draw over them (destinations clip at the arena's end): the library checks its words
against the mirror and writes them again.

### Emulated arms
Built with `SCOUTER_G1_EMULATOR`, the library talks to two emulated arms that run the arms'
interpreter for host code and programs, behind the window, ACKs, events and control requests of
VM.md §6 (HELLO of g1v0 0.5.0: triggers 0xF063, no microphone, so `MIC` is refused with 12;
`PANEL_READ` refused with 11 while the panel is off; `EVENTS` = request ∧ triggers). Programs run
on with the link down. `emulator::SetProperty(arm, id, value)` sets sensor values; `BATT_PCT`
follows with the firmware's hysteresis (a 1 % change only after 30 s, VM.md §2.5). Built with
`SCOUTER_G1_MIC` too, the right arm has a microphone as g1v0 0.5.10 runs it (`HELLO.board` bit 2;
the left arm refuses `MIC` with 12): the indicator, the first 40 ms dropped, then 5 LC3 frames
every 50 ms of the arm's clock from counter 0, encoded with liblc3 from a sine that
`emulator::SetMicSignal(arm, hz, peak)` sets (440 Hz at a quarter of full scale at first); `PANEL` =
0 stops it with `NOTICE` 5, a link drop or a reboot without one. `emulator::NewHost(keep)` makes
the library forget the arms' sessions and connect again as a new process would.

## C layer (`c/scouter_kit_c.h`)
The whole API with an `sk_` prefix (`sk_load_font`, `sk_create_bitmap`, `sk_font_draw_line`,
`sk_draw`, `sk_flush`, …) and `sk_g1_` for the G1 (`sk_g1_init`, `sk_g1_fill`, `sk_g1_get_state`,
`sk_g1_load`, `sk_g1_vm_start`, `sk_g1_next_event`, …). Fonts, bitmaps and programs are opaque
handles; loads return NULL on failure; arms are 0 (left) and 1 (right). The program builder is
`sk_g1_program_new` … `sk_g1_program_free`, with labels as ints and one call for every instruction:
`sk_g1_program_op(program, opcode, operands, n, label)`. The microphone: `sk_g1_mic_start(arm, rate)`,
`sk_g1_mic_stop`, `sk_g1_mic_read(arm, &frame, timeout_ms)` and `sk_g1_mic_get_state`.
`sk_g1_init_config(&config, error, size)` takes every option as a struct (`sk_g1_config`, with
`keep_sessions` and `log`); `sk_g1_init` is the same without them.

## Fonts: `font_converter.py`
```
python3 font_converter/font_converter.py FONT.ttf --size 21 -o font.skf            # outline font
python3 font_converter/font_converter.py third_party/departure_mono/DepartureMono-Regular.otf \
    --size 11 --supersample 8 --c-array src/base/departure_mono.inc --name kDepartureMonoBlob \
    --comment "Departure Mono 1.500, (c) 2022-2024 Helena Zhang, SIL Open Font License 1.1: third_party/departure_mono/LICENSE"
```
Needs Pillow. `--supersample K` is for pixel fonts (exact pixels); `--chars`/`--ranges` select
characters, `--kern` adds kerning pairs for ASCII. The blob format is VM.md §4.2 (format 1); the glyphs come
in the order above (`0`–`9`, `A`–`Z`, `a`–`z`, then from index 62 the others by codepoint).

## Tests
```
VM="src/vm/vm.c src/vm/vm_gfx.c"
g++ -std=c++20 -O2 -DSCOUTER_G1_EMULATOR tests/g1_test.cpp src/base/scouter_kit.cpp $VM -o g1_test -lpthread
./g1_test [quick]   # byte code vs VM.md, the pool and mirror, end to end on emulated arms (quick: no 30 s wait)
gcc -std=c11 -DSCOUTER_G1_EMULATOR -c tests/c_test.c -o c_test.o
g++ -std=c++20 -DSCOUTER_G1_EMULATOR c_test.o src/c/scouter_kit_c.cpp src/base/scouter_kit.cpp \
    src/g1/scouter_kit_g1.cpp $VM -o c_test -lpthread && ./c_test
g++ -std=c++20 -O2 tests/dk_smoke.cpp src/base/scouter_kit.cpp src/g1/scouter_kit_g1.cpp $VM \
    $(pkg-config --cflags --libs libsystemd) -lpthread -o dk_smoke
./dk_smoke DK_ADDRESS [--drop] [--log]     # one board without a panel: arena CRCs, reconnect
g++ -std=c++20 -O2 tests/glasses_check.cpp src/base/scouter_kit.cpp src/g1/scouter_kit_g1.cpp $VM \
    $(pkg-config --cflags --libs libsystemd) -lpthread -o glasses_check
./glasses_check LEFT_ADDRESS RIGHT_ADDRESS [--power S] [--log]   # panels read back, clock updates, the idle link
# the microphone on the emulated arms (liblc3 objects as under Compile)
g++ -std=c++20 -O2 -DSCOUTER_G1_EMULATOR -DSCOUTER_G1_MIC tests/mic_test.cpp src/base/scouter_kit.cpp \
    src/g1/scouter_kit_g1.cpp $VM lc3_*.o -o mic_test -lpthread -lm && ./mic_test
```
`mic_test`: a 440 Hz and a 1 kHz tone decoded at their level (liblc3 decodes a pure tone 0.7 dB
louder) and frequency, at 32 and 16 kbps, 100.00 frames/s by the arm's clock, counters from 0; 10 %
of the notifications lost: the gaps concealed and the counters continuous; the indicator in the
emulated panel; `MicStart` twice (nothing changes) and with another rate (the arm starts again from
0); `MicStop` (off, nothing more read, the panel off); `PANEL` = 0, a link drop and a reboot (stopped,
the reason, nothing after the reconnect, the indicator gone); the left arm refused; kept sessions: a
counting TIMER program on the right arm runs on (same epoch, ticks going on) while a new host adopts
the session and records for 1 s, the panel is off again after, a `PANEL` = 0 from the program stops
the microphone, and a session kept from `Init` owns no memory. 2026-10-27: passes, also under TSan and
ASan + UBSan.
`g1_test` checks the builder against VM.md's bytes (the `ADD r3, r1, [0x0120]` example, the §9.2 clock
and §9.3 battery programs with their tables and pool upload, `RAISE C12`, setup writes), the library's
present head and tail, the pool (one word per value, a full ring without evicting a word in use),
and on the emulated arms: the gray display through loss, link drops and reboots (panels and whole
arenas equal to the expectation, host code over the library's own memory heals), the §9.2 clock
(ticks, the USER0 correction, midnight, running on while the link is down) and §9.3 battery
programs at their spec addresses, their pixels compared with text drawn by the host, the `BATT_PCT`
hysteresis and `INPUT` events, `RAISE` → `NOTIFY` round trips (about 8 ms in the emulator), `HALT` and
error halts with `VM_STATUS`, host `CALL` and `Run`, refused `MIC` (error 12), `PANEL_READ` while off
(error 11), `EVENTS` ∧ triggers, the idle and active link profiles, glyph indices, pair and adapter
choice, one arm, no case hold (0.5.60), the eyes matched on a panel guard cap
(`NOTICE` 7 → the other eye's `BRIGHTNESS_MAX`, 42 again); and 3000 random host-code calls (`./g1_test quick SEED`: every drawing and memory
call with valid and invalid operands, mixed with `Flush`es, 10 % loss both ways, link drops and
reboots), after which both arenas and panels equal the mirrors and no arm reported an `ERROR`. Run with
`-DSCOUTER_G1_POOL_WORDS=16` and `=64` too, so the pool wraps around inside presents. 2026-10-28: all
pass, also under ASan + UBSan and under TSan; `c_test` under ASan; `mic_test` under ASan + UBSan and TSan. The random test found three bugs on
the way: a present's pool words reused inside the same present (now pinned), the present time word
left stale in the mirror by host code that drew over it, and inflated RTT samples (a hole delays
the ACK of the packets after it) with an RTO of up to 32 s after a lost last packet (since v4).

Earlier results (protocol 4, g1v0 0.4.0 and 0.4.2): on the DK, bonding through the library's agent,
uploads with the arena CRC equal to the mirror, and recovery from link drops; on the glasses (in the
case), a 75 KB test screen in 4.4–4.5 s with both panels and arenas equal to the expectation, clock
updates 39.9 ms each (longest `Flush` 65 ms), eyes 0.03 ms apart. On protocol 5 (g1v0 0.5.6, in the
case, hci0, 2026-10-27, `logs/library/m2/incase-056/`): `glasses_check` passed: the 76 KB test screen
uploaded, shown and read back in 4.5 s (both panels and arenas equal to the expectation), clock updates
41.4 ms each (longest `Flush` 77 ms), eyes 0.04 ms apart; both links went to the idle 50 ms profile
after 5 s and back to 7.5 ms with the next update, which the eyes showed 376 ms after `Flush`, 0.05 ms
apart (a parameter update on running links works with two links up; no reconnect needed). The Go
`image` example (702 × 402): shown after 13.3 s, both panels as expected. `vmclock -away 10m`: both
arms ran the clock for 10 minutes with the host's links dropped (11 tasks, digits and the band's
panel rows read back as drawn by the host), and the correction was redrawn 16 / 19 ms after the
request.

On g1v0 0.5.30 (2026-10-28, in the case, `logs/final/reg/14-…`): `glasses_check` passed (0 failures; both panels and
arenas equal to the expectation before and after the idle link), and `g1_test`, `c_test` and `mic_test` pass.

**Upload speed** (g1v0 0.4.2, the Go `image` example: 131.7 KB of packets per arm, btmon captures):
before (7.5–15 ms asked; the left link ran at 15 ms, the right at 7.5 ms) left 6.8 KB/s, right
9.0 KB/s, shown after 19.4 s (29.9 s in the session that reported it); now (7.5 ms / 2M on both)
8.5–10.9 KB/s per arm, shown after 12–16 s, in 8 of 15 sessions, and 5.4–6.5 KB/s, 21–25 s, in the
other 7. The limit is this PC's RTL8761B: 8 LE buffers of 27 bytes shared by both links (a 236-byte
packet is 9 fragments), so throughput is how fast the buffers turn over; one link alone reaches
17.8 KB/s. The group a session falls in is decided when the controller places the second
connection: its events 2.5–4 ms from the first link's (fast) or 1.25 ms (each link then loses about
every other event). A connection update with the same interval keeps that placement; one with
another interval places it anew. Sending to one arm at a time does not help (the idle link's events
still cut the other's short). A controller with larger LE buffers removes this limit: with the
nRF5340 DK as a second adapter (251-byte × 10 LE buffers, `docs/dk-adapter.md`, g1v0 0.4.10) one arm
alone took 43–47 KB/s instead of 21–22 KB/s and showed the picture after 3.0–3.3 s instead of 6.0–6.2 s;
at 7.5 ms the arm then receives 2 packets per connection event, its own net core's limit.

## Changes from the first header drafts
- `std::mdspan` → `View2D<T>` (pointer, width, height, stride); `gray::BitmapView` is `View2D<uint8_t>`.
- `Font::DrawLine` always draws into a `gray::Bitmap`; new `gray::CreateBitmap` and `gray::Draw`
  place bitmaps on the display; `Flush` shows them on both eyes in sync.
- `LoadFontDepartureMono` is gone: include `base/departure_mono.inc` and pass `kDepartureMonoBlob`
  to `LoadFont` (a `uint8_t` overload was added).
- `Arm` commands return nothing; refused commands count in `State::errors`.
- Glasses addresses come only from `Connection`; the emulator is chosen at compile time.
- Added: `ERROR_NONE`, `ERROR_G1_FIRMWARE`, `Connection`, `gray::Write/Fill/Clear/Unload`,
  `gray::response`, `Unload(Font&)`, and the `Arm` extras (`Alloc`, `GetState`, control requests).
- Protocol 5 (2026-10-27): `Arm` addresses are arena bytes (`Address`) and coordinates are `int32_t`,
  as in VM.md; `Draw` takes the source bitmap, stride and pixel instead of a bit address; `Alloc`
  returns an address, the same on both arms (also `g1::Alloc`); `WaitUntil` is `SLEEP_UNTIL`,
  `Fence` a `NOTIFY`; `ArenaCrc(address, length)`. New: `Unpack`, `Line`, `SysWrite`, `Notify`,
  `Raise`, `Call`, `Run`, `Load`/`Start`/`Stop`/`Status`, `Props`/`Prop`, `ReportInputs`,
  `NextEvent`, `ReadArena`, `Disconnect`, `Program`, `AddressOf`, `GlyphIndices`, and the
  properties, triggers and events of VM.md.
- The microphone (2026-10-27): `MicStart`, `MicStop`, `MicRead`, `GetMicState`, `MicFrame`,
  `MicState`, `emulator::SetMicSignal` (`SCOUTER_G1_MIC`); `Connection::keep_sessions`,
  `emulator::NewHost`, `sk_g1_init_config`. `GlyphIndices` now decodes UTF-8 with the
  base library's decoder (`device::NextCodepoint`): it turned every 3- and 4-byte character into two
  U+FFFD before.
- 2026-11-05: `Connection::case_panel` (and the C, Go and Python options, `--case-panel`) removed with g1v0
  0.5.60's case hold; `Program::PanelPolicy` is the programs' display policy instead.
- 2026-10-28: `Connection::case_panel` and `Connection::log` (`sk_g1_config`'s `case_panel` and `log`)
  replace the environment variables `SCOUTER_G1_CASE_PANEL` and `SCOUTER_G1_LOG` and the macro
  `SCOUTER_G1_LOG`.

## Third-party code
- moodycamel ConcurrentQueue v1.0.5 (github.com/cameron314/concurrentqueue, release tarball sha256
  `4d6368a27492d86011fde5ca0cf386dce7c49cd425aa3d9b063ca6ec373a6ef3`): `concurrentqueue.h` and its
  `LICENSE.md` (simplified BSD).
- Departure Mono 1.500 (github.com/rektdeckard/departure-mono, `DepartureMono-1.500.zip` sha256
  `bf3e48059aeef4617ec585bdea81dcc3491c576b3e7a472f52faf40e09ee5c3a`): the OTF and its OFL `LICENSE`;
  `base/departure_mono.inc` is derived from it and under the same licence.
- liblc3 1.1.3 (github.com/google/liblc3, tag v1.1.3; vendored for the firmware, see
  `../g1_firmware/README.md`): `src/`, `include/` and its `LICENSE` (Apache-2.0). The library uses the
  decoder (and the emulated arms the encoder), built with `LC3_PLUS=0`, `LC3_PLUS_HR=0`.
