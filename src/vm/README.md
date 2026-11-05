# ScouterKit VM core (protocol 5)

The portable interpreter of `../../g1_firmware/docs/VM.md`: one implementation for the g1v0 firmware, its
Renode and native builds, and the C++ library's emulated arms. It runs host code (packets) and programs on
one 256 KiB arena and draws through a platform interface.

| File | Content |
|---|---|
| `vm.h`, `vm.c` | interpreter and executor: decoding, all instructions, host packets and host `CALL`, programs, triggers, jump budget, run-time cap, `ERROR`/`HALT`/`NOTIFY` events, properties, `VM_START`/`VM_STOP`/`VM_STATUS`/`PROPS`/`RESET_SESSION`, the microphone indicator |
| `vm_gfx.h`, `vm_gfx.c` | memory and drawing primitives on a plain arena: PackBits, `FILL`, `BLIT`, `GLYPHS`, `LINE`, `DRAW` clipping and row packing; usable alone (an arena mirror) |
| `../../tests/vm_test.c` | tests; `vm_v4_ref.c` beside it is the protocol 4 drawing code of g1v0, copied verbatim, as the pixel reference |

Properties: freestanding C99 that also compiles as C++17/20 (so `g++` can build `vm.c` with the library);
needs `<stdint.h>` and `memcpy`/`memmove`/`memset` (GCC's and Clang's builtins speed up `VIDEO`); no libm, no allocation, no globals, no OS calls (each
`struct vm` is independent: two emulated arms are two instances). Cortex-M33 `-Os`: 15 KB of code, no writable
static data (`VIDEO`'s tables: 1 KB of constants), `struct vm` 5.8 KB (5.1 KB of it `VIDEO`'s rows) plus the
caller's arena. Not thread-safe: one thread calls
the `vm_*` functions.

## API

```c
void     vm_init(struct vm *vm, uint8_t *arena, const struct vm_host *host, int safe); /* boot: arena zeroed */
uint32_t vm_run(struct vm *vm);         /* run until nothing can run now; returns µs to block (VM_FOREVER, 0 = yield) */
int      vm_start(struct vm *vm, uint32_t table);      /* VM_START: 0, 3 (table not in the arena) or 7 (safe mode) */
void     vm_stop(struct vm *vm);                       /* VM_STOP */
void     vm_reset_session(struct vm *vm, int clear_arena); /* RESET_SESSION (VM part) */
void     vm_set_mic(struct vm *vm, int on);            /* the microphone went on/off: draw or clear the indicator */
int      vm_prop(const struct vm *vm, uint32_t id, uint32_t *value); /* PROPS: 0 or 5 */
void     vm_status(const struct vm *vm, uint8_t *out); /* VM_STATUS body, 32 bytes */
void     vm_set_events(struct vm *vm, uint16_t mask);   /* EVENTS: triggers reported as INPUT events */
void     vm_trigger(struct vm *vm, uint32_t id, uint32_t value); /* a platform trigger (MOTION) with its INPUT value */
int      vm_moved(const int32_t *prev, const int32_t *cur, uint32_t mg); /* MOTION test for the IMU code */
uint32_t vm_distance(const int32_t *prev, const int32_t *cur);         /* MOTION's INPUT value, mg */
/* vm_gfx.h: vm_fill, vm_blit, vm_glyphs, vm_line (operand arrays in instruction order),
 * vm_draw_clip + vm_draw_row, vm_packbits_ok + vm_packbits_decode */
```
`vm->stats` (packets, instructions, draws, jumps, errors, late) feeds the `STATUS` reply; the other members of
`struct vm` are private.

## The platform interface (`struct vm_host`)

All members are required except `prop_write`, `panel_span` and `asset_read`. The VM calls them only from inside
`vm_*` functions, on the caller's thread.

| Member | Called | The platform |
|---|---|---|
| `now` | before and after each program instruction, before each host-code instruction while a program's timer is armed, between packets, `SLEEP_UNTIL`, `TIME_US`, and every 16 rows inside `VIDEO` | returns device time, µs (u32, wraps); keep it cheap |
| `packet` | before each instruction while no packet is active | returns the packet at exec_next if it has arrived (u16 seq + host code, len ≥ 2), else NULL; it stays valid until `packet_done` |
| `packet_done` | the packet ended (normally, by `END`, or on an error) | frees the slot, exec_next++, schedules the ACK |
| `poll` | before each instruction | returns triggers it fired since the last call (bits 0–15: `MOTION`, `TOUCH`, tests) and `VM_YIELD` to make `vm_run` return now (control requests, watchdog); `poll` and `now` together can be the platform's liveness signal, `now` alone cannot (host code may run for seconds without reading the clock) |
| `prop` | `SYS_READ`, `PROPS`, sensor checks | value of a platform property: `ARM`, `LINK`, `LINK_INTERVAL`, `BATT_*`, `CHARGE`, `IN_CASE`, `WORN`, `ACC_*`, `PITCH`, `ROLL`, `LIGHT`, `BRIGHTNESS`, `PANEL`, `CASE_*`, and the reads of the later milestones' `GYR_*`, `GYRO_RATE`, `TOUCH_GESTURE`, `AUTO_BRIGHTNESS`, `PEER_LINK`, `PEER_MSG`, `PEER_T_US`; `VM_UNKNOWN` if none |
| `event` | `ERROR`, `HALT`, `NOTIFY`, `INPUT` | sends type + epoch + t_us + the given body (bytes from offset 9); returns 0 if queued (else `NOTIFY` counts a drop; `INPUT` counts either way) |
| `panel_begin` | a `DRAW` with a visible part, a `VIDEO` that changes a visible pixel, the indicator | powers the panel on if off (all pixels 0); gets the clipped rectangle (w, h > 0); nonzero = no panel, nothing is drawn |
| `panel_row` | each row of that rectangle, top to bottom | writes w pixels: `(w + 1) / 2` bytes, left pixel in the high nibble; the buffer is reused, copy it |
| `panel_end` | after the last row | latches (JBD `97`), so a `DRAW` is never seen half done |
| `panel_power` | `PANEL` written (0, 1 or 2) | 0: rails off, image lost; 1: on if off; 2: standby (image lost, the next `DRAW` wakes it fast); while the microphone is on, 0 and 2 blank the panel to the indicator and wait for the microphone to stop (VM.md §6.5) |
| `brightness` | `BRIGHTNESS` written (0–42) | applies or stores it (applied at power-on while off) |
| `imu_rate` | `IMU_RATE` written (0/13/26/52/104), `RESET_SESSION` | reconfigures the accelerometer |
| `prop_write` | `GYRO_RATE` (0/13/26/52/104) or `AUTO_BRIGHTNESS` (0/1) written, values checked by the core; `PEER_LINK`, `PEER_SEND` | applies it; returns 0, or an error code (5: not available here). Optional: `NULL` makes these writes error 5 |
| `panel_span` | `VIDEO`: each changed row, top to bottom, between `panel_begin` (the clipped frame) and `panel_end` | writes w pixels from x as `panel_row`, a part of a row; rows are skipped. Optional: `NULL` calls `panel_row` with the same arguments |
| `asset_read` | `ASSET_LOAD`, after its arena ranges passed (id + 16, dst + n) | copies bytes off…off + n − 1 of the stored asset with that 16-byte ID (a copy, not the arena's bytes) to dst; returns 0, 13 (unknown ID, a corrupt body, no store) or 3 (off + n past the asset's end, detail off) and writes nothing unless 0. Optional: `NULL` makes every `ASSET_LOAD` error 13 |

Owned by the core: `TRIGGER`, `PENDING`, `TIMER`, `MOTION_MG`, `PITCH_LO/HI`, `LIGHT_LO/HI`, `IMU_RATE` (the
IMU code reads `MOTION_MG` with `vm_prop`). `GYRO_RATE`, `AUTO_BRIGHTNESS`, `PEER_LINK` are the platform's (read
through `prop`, written through `prop_write`; the platform resets its sensor settings at `RESET_SESSION`);
`PEER_SEND` is write-only, so reading it is error 5. `vm_wants` tells the platform which triggers a running
program can take now (a handler in its table, or its task waiting for them), so a sensor nothing wants can sleep.

Triggers: `START` (`vm_start`), `TIMER` and `USER0`–`3` (`RAISE`) are the core's. `BATTERY`, `CASE` (any change of
`IN_CASE`, `CASE_LID`, `CASE_CHARGING` or `CASE_BATT_PCT`), `WEAR`,
`TILT` and `LIGHT` are detected by the core from `prop` values whenever the executor is idle, so **the platform
wakes the executor (calls `vm_run`) after any sensor value changes**. `MOTION` (the platform sees every sample:
`vm_moved(prev, cur, MOTION_MG)`) comes through `vm_trigger` with the change in mg, `TOUCH` and test triggers
through `poll`. `HELLO.triggers` is the platform's: the core's set plus the sensors it has.

`INPUT` events (§6.3, the `EVENTS` mask): `vm_set_events` takes the mask in force (the platform's request ∧
`HELLO.triggers`, 0 at boot and at a disconnection) and samples the sensor values. From then on the core reports
each firing of a masked trigger whether or not a program runs: `BATTERY`, `CASE`, `WEAR`, `TILT` and `LIGHT`
with baselines of their own (evaluated at every idle point while the mask is not 0, so a program's `VM_START`
does not reset them), `MOTION`/`TOUCH`/`PEER` from `vm_trigger` or `poll`, and `TIMER`, `RAISE` and `START`
while a program runs (as the program sees them).

## Call pattern

**Firmware (Zephyr).** The executor thread owns the `struct vm` and the arena; proto.c keeps the window,
ACKs, epoch, event header and control parsing.
```c
static uint8_t arena[VM_ARENA_SIZE] __aligned(4);
static struct vm vm;
static const struct vm_host host = { NULL, fw_now, fw_packet, fw_packet_done, fw_poll, fw_prop, fw_event,
	fw_panel_begin, fw_panel_row, fw_panel_end, fw_panel_power, fw_brightness, fw_imu_rate };

static void executor(void)
{
	vm_init(&vm, arena, &host, safe_level() != 0);
	for (;;) {
		apply_control_requests(); /* posted by the control thread: vm_start, vm_stop, vm_reset_session, vm_set_mic */
		uint32_t wait = vm_run(&vm);
		if (wait) {
			k_sem_take(&wake, K_USEC(MIN(wait, 500000))); /* given on packet, sensor change, control request */
		}
	}
}
```
- `fw_poll` returns `atomic_clear(&fired)` (MOTION from the IMU thread) plus `VM_YIELD` while a control request
  waits; a control request then takes effect between two instructions (§6.4). The control thread can reply
  to `VM_START` at once after the pure checks (table in the arena, safe mode); `VM_STATUS` and `PROPS` may call
  `vm_status`/`vm_prop` directly (they only read; on the single-core M33 a torn snapshot is the worst case).
- `fw_packet` checks `qslots[q_exec_next % QSLOTS].full`; `fw_packet_done` frees it, `q_exec_next++`,
  `proto_ack(false)`. `RESET_SESSION`: flush the window as today, then `vm_reset_session(&vm, flags & 1)`.
- `fw_panel_row` copies the row into the staging buffer of `panel_draw`'s pipeline (the clipped rectangle
  from `fw_panel_begin` tells it whether a full-width row is followed by another); `fw_panel_end` sends `97`.
- Platform rules that stay outside the core: the unattended-panel rule (30 s after the last `panel_end`, no
  host connected, `VM_STATUS` byte 0 not 1 = running), `MIC` and its checks (then `vm_set_mic`), advertising,
  safe mode detection.
- Stack: about 0.7 KB at the deepest (`vm_run` → `step` → `vm_glyphs`, Cortex-M33 `-Os`).

**Library emulator.** Per emulated arm: a `struct vm`, its arena and a `vm_host` whose `ctx` is the `Arm`.
In `Execute(a, now)`: `uint32_t w = vm_run(&a.vm);` then `a.vm_at = w == VM_FOREVER ? -1 : now + w` and
`NextEvent()` includes `a.vm_at`. `now` is `DeviceNow(a, NowUs())`, `packet`/`packet_done` read and free
`a.slot[a.exec_next % 32]`, `panel_row` writes `a.panel`, `event` prepends the header and queues it with
`Notify`. Since it is single-threaded, `poll` returns 0 (or injected triggers). An exact arena mirror can be a
second `struct vm` that only ever runs host code: its `packet` returns the packets as sent, `now` is a virtual
clock advanced by `vm_run`'s return value, `panel_row` updates the expected panel.

## Tests

```
cd scouter_kit/tests
cc -std=c11 -O1 -g -fsanitize=address,undefined -fno-sanitize-recover=all -I../src/vm \
   vm_test.c vm_v4_ref.c ../src/vm/vm.c ../src/vm/vm_gfx.c -o /tmp/vm_test
/tmp/vm_test              # all tests + 2 s of fuzzing
/tmp/vm_test fuzz 3600 7  # all tests + 1 h of fuzzing, seed 7
/tmp/vm_test vectors FILE [N [SEED]]   # only: write replayable cases for a firmware (below)
```
**Vectors for a firmware** (`vectors` mode): a directed `DRAW` case (a full frame, full-width bands, odd x, w and
source x, every panel edge, panel off and on again) and N fuzz cases, made only of host traffic: packets
(the setup as `WRITE`s too), `VM_START`, `VM_STOP`. Each case runs twice here, with device times 10 s and
2000 s and different battery and link values, and is kept only if both runs give the same text, so its
results do not depend on time or sensors (`TIMER` and `SLEEP_UNTIL` in the generated code are then harmless:
a case where they matter differs and is dropped); `BATTERY`/`CASE` handlers and masks are left out, since an
arm sees those change. The IMU is described as the emulator's model at rest (`ACC_*`, `PITCH`, `ROLL` 0) and
`IMU_RATE` is never written (0 would make the arm's values `UNKNOWN`). Per case: the packets, `VM_STATUS` at each point where nothing can run (bytes 26–27 and
29–31 zeroed), every `ERROR`, `HALT` and `NOTIFY` event body, the arena's CRC-32 and the panel's (`PANEL_READ`
rows, or `off`). `g1_firmware/emulator/tests/v5_tests.py vectors` replays them through g1v0 in the emulator.
Coverage: every opcode (ALU edge values against a reference, the a = (a/b)·b + a%b identity, all six branches
on signed/unsigned edges, LD/ST of every width at the arena's end and with 32-bit wrapping indices), the
encoding example `11 31 f0 20 01`, host rules (program-only ops, unknown ops, cut-off instructions, padding,
`ERROR` byte for byte), host `CALL` (`RET` to the packet, `END`, `WAIT_EVENT`/`HALT` there, depth 16, budget,
`SLEEP_UNTIL`), programs (`HALT`, errors, stack, budget 1000 and when it restarts), triggers (`TIMER`,
coalescing, order, `WAIT_EVENT`, pending without handler, stopped VM, settings across `VM_START`, poll,
`TILT`/`LIGHT` ranges evaluated at idle, `BATTERY`/`CASE`/`WEAR`), the run-time cap (30 ms per 100 ms, a 53 ms
`DRAW` loop held to ~30 %, host code first and uncounted), `VM_STATUS`, properties, VM.md §4.3 (digits, at most
9 jumps) and the §9.2 clock and §9.3 battery programs from their printed bytes and addresses (setup packets,
`RAISE C12` = `42 f0 28 00`, minute tick, midnight, a past `N'`). Drawing: FILL, BLIT (4/1 bpp, all modes,
overlapping scrolls, the byte fast path), GLYPH_RUN (kerning, 2-byte varints, bad fonts) and WRITE_RLE are
byte-identical to the v4 code on the arena wherever v4 accepts the operands (~7 700 cases); clipped and
protocol 5 operands (negative, i32/u32 extremes, byte addresses, rows ending in the arena) match a
per-pixel reading of §3 written separately in the test; LINE matches a closed form; DRAW matches a
per-pixel panel model including the microphone indicator. Fuzzing: random programs whose operands suit
their roles (addresses, coordinates, levels, property IDs, times...), jumps and handlers on instruction
boundaries, random packets that `CALL` into them, triggers, control calls and fonts; about 300 instructions
and 4 `DRAW`s per run (`VM_FUZZ_HIST=1` prints what halts the random programs).

2026-10-27 (M3): `INPUT` events (`vm_set_events`, `vm_trigger`, `vm_distance`; `test_input`: mask 0, baselines at
the mask, every sensor trigger without a program, `TILT` once per crossing with `UNKNOWN` in between, `MOTION` from
`vm_trigger`, `TOUCH` from `poll`, `TIMER`/`RAISE`/`START` only with a program, one report per change with a program,
the count through drops and `RESET_SESSION`, `vm_distance` saturating). gcc C11, C++20 and clang -O2 under ASan +
UBSan: 0 failures; Cortex-M33 `-Os` 5.6 KB for `vm.c`.

2026-10-27 (M1): the `CASE` trigger now watches all four case values, `CASE_*` and the later milestones' readable
properties go to the platform (tests in `test_triggers` and `test_props`), and the fuzz's `RAISE` operand has a
role of its own. Same three builds, 0 failures; 10 min of fuzzing under ASan + UBSan (seed 0x5EED2027) without a
finding. The 41 default vectors (seed 0x5EED, 40 fuzz cases) replay on g1v0 0.5.0 without a difference.

Results (2026-10-26): 76 920 deterministic checks (a pixel comparison covers a whole 64 KiB window or the
panel), 0 failures, with gcc 14 (C11), with the core compiled as C++20 and linked to the C tests, and with
clang 19 `-O2`, all under ASan + UBSan. Fuzzing under ASan + UBSan: 10 min (2.9 M runs, ~840 M instructions,
~10.7 M `DRAW`s, seed 0x5EED2026) plus an earlier 10 min with a shallower generator, no finding. Cortex-M33
build clean with `-Wall -Wextra -pedantic`.

## Spec points resolved here

Encoding and errors
- A nonzero padding nibble (odd operand count) is error 1. An unknown opcode or one not allowed where it is
  fetched is error 2 before any length check (`30` alone in a packet is 2, not 1).
- An instruction cut off by its packet's end or by the arena's end is error 1. A fetch at an address ≥ 262 144
  (a handler entry, code running past the end) is error 3, detail = that address, op 0. `JMP`/`CALL`/branch
  targets ≥ 262 144 fail at the jump (error 3, detail = target). `CALL` checks the budget (10), then the
  depth (9), then the target (3).
- Address arithmetic is exact, never wrapped to 32 bits: `LD`/`ST` element base + i·w/8, `COPY`/`UNPACK`
  ranges, `text + n`, the trigger table. A wrapped index is error 3, not an alias.
- `detail` of error 3: the start of the failing range (`WRITE`/`WRITE_RLE` addr, `COPY`/`UNPACK` src or dst,
  the source bitmap of `BLIT`/`DRAW`, `GLYPHS` text), the element address for `LD`/`ST`, the jump target or
  fetch address. Error 5: the offending value (property ID for an unknown or read-only property, mode, level,
  mask, trigger, coordinate, rate).
- `ERROR` for an error in called code: offset = the packet's `CALL`, op and address = the failing called
  instruction.
- `WRITE_RLE` stored in the arena copies its data first, so it may overwrite itself.

Programs and triggers
- Every trigger, `RAISE` included, is ignored while no program runs.
- The jump budget starts again only after a real suspension: not after a `SLEEP_UNTIL` already past, a
  `WAIT_EVENT` satisfied at once, or a cap pause. Host packets follow the same rule.
- `TIMER` is due when (i32)(t − now) ≤ 0, the rule of `SLEEP_UNTIL`: a time 2^31 µs or more ahead counts as
  past and fires at once; no error.
- "The executor goes idle" = neither a packet nor a task is between two of its instructions (a sleeping packet
  and a sleeping or waiting task are idle; a task paused by the cap is not). The sensor triggers are
  evaluated there, so a range moved with two `SYS_WRITE`s in one packet or task never fires spuriously.
- `TILT`/`LIGHT`: an `UNKNOWN` reading changes nothing; the inside/outside state is sampled at `VM_START` (a
  range already left does not fire); LO > HI is an empty range. `BATTERY`/`CASE`/`WEAR` fire on any change of
  the raw value (to or from `UNKNOWN` too) against a baseline taken at `VM_START`.
- `MOTION`: Euclidean distance of consecutive samples > `MOTION_MG` (`vm_moved`, computed in 64 bits).
- Counters: tasks started, coalesced triggers and cap pauses restart at `VM_START`; `NOTIFY.count` and the
  dropped count restart at `RESET_SESSION` only (they span host code); the first `NOTIFY` has count 1. So does
  `INPUT.count` (it also counts events the platform could not queue).
- `INPUT` values: `TILT` the `PITCH` that left the range, `LIGHT` the reading, `BATTERY` `BATT_PCT`, `CASE` bits 0
  `IN_CASE`, 1 `CASE_LID` (1 = closed), 2 `CASE_CHARGING`, 8–15 `CASE_BATT_PCT` (0xFF unknown), `WEAR` `WORN`,
  `MOTION` the platform's value (`vm_distance`, rounded down; 0 for a `RAISE`d one), `TOUCH` `TOUCH_GESTURE`,
  `PEER` `PEER_MSG`; 0 for `START`, `TIMER` and `USER`. `RAISE` reports while a program runs only, like the trigger
  it sets; a running program and the mask see a sensor change once each.
- `VM_STATUS`: next-instruction address and jumps left are 0 without a task; the trigger is 0xFF after a
  halt (`HALT` carries it); halt code and address stay until the next `VM_START`. `PROPS` of `TRIGGER` gives
  the current task's trigger (0xFF without one).

Run-time cap
- Periods are a fixed 100 ms grid from `vm_init`. A program instruction (or a new task) starts only while the
  time charged to the current period is below 30 ms; time beyond 30 ms (one long `DRAW`) is charged to the
  following periods, so back-to-back 53 ms `DRAW`s get ~30 %, not 53 %. Each instruction is timed with `now`.
  `busy_ms` is the run time of the last complete period.

Drawing
- A source rectangle must exist completely, also where the destination clips. With w = 0 or h = 0 only the
  error 5 checks apply (and error 7 for `DRAW`).
- `BLIT`'s direction compares the first drawn pixels after clipping (identical to the unclipped rule when
  nothing is clipped, and the same direction for equal depth and stride).
- `GLYPHS` checks in this order: mode/level (5), text range (3), font (8), then per index in text order:
  varint (1), index (8), bitmap range (8). The pen does not wrap. If the destination overlaps the text or the
  font, the drawing pass checks each glyph again and stops or skips it.
- `LINE`, exactly: the ends are ordered so the longer axis increases (x when |dx| = |dy|); pixel j (0…n) is at
  major + j and minor + sign·⌈(2·j·d_minor − n) / (2n)⌉, i.e. the exact line rounded to the nearest pixel with
  ties toward the first end; a line and its reverse set the same pixels.
- `DRAW` with nothing visible (empty, or entirely off the panel) does nothing: no power-on, no latch. Without
  a panel it does nothing and is no error. `PANEL` = 1 without a panel is no error either.
- Microphone indicator: drawn into the `DRAW` rows before the latch (never seen missing); `vm_set_mic(1)`
  powers the panel on and draws it; `vm_set_mic(0)` clears it to level 0 if `PANEL` reads 1 (with 0 or 2
  pending the platform applies it after the stop).

## For the reviewer

Spec issues and open points:
1. `VM_STATUS` byte 13 "u8 jumps left" cannot hold the new budget of 1000; it is clamped to 255 here. A u16
   would need a layout change (the body is full at 32 bytes).
2. §3.3's byte fast path ("source 4 bpp, mode COPY, even src_x, dst_x, w") must also require level 15: with
   a 4-bpp source `COPY` gives (a·L + 7)/15, which is a only for L = 15. The core requires it.
3. §5.4 does not say what happens to time beyond 30 ms in one instruction; carrying it over (above) is a
   choice that makes the stated bound hold. Please confirm.
4. §2.5 `TIMER` "at most 2^31 − 1 µs ahead" reads as a limit, but a wrapping clock cannot tell a larger value
   from the past; treated as past, no error.
5. §6.4 says the control thread answers on arrival "also while host code sleeps or draws". `VM_START`/`VM_STOP`
   must still be applied by the executor between instructions (here via `poll` → `VM_YIELD`), and reading
   `VM_STATUS` concurrently is a data race in C (benign on one core). An explicit rule ("the reply comes after
   the pure checks; the action follows within one instruction") would help.
6. Points decided above that the spec leaves open and the host library must mirror: the padding-nibble error,
   ERROR.op for called code, error 3 details, the budget-restart rule, `TILT`/`LIGHT` with `UNKNOWN` and at
   `VM_START`, the `MOTION` metric, counter lifetimes and `NOTIFY.count` starting at 1, `DRAW` with nothing
   visible, the indicator at `MIC` 0, the `LINE` rounding.

Protocol 4 issues found on the way (g1v0 0.4.x `gfx.c`, not changed here):
- `GLYPH_RUN` re-reads glyph entries in its drawing pass without checking them again. A destination that
  overlaps the font's glyph table can rewrite a later glyph's `bits`, and the next glyph then reads far past
  the arena: one host packet makes ASan report a read 31.6 MB past the arena in the copied code (on the arm,
  outside RAM: a BusFault). The core here re-checks each glyph.
- `FILL` with w = 0 and a large h walks its rows and forms pointers past the arena (undefined behaviour in C,
  nothing written).
