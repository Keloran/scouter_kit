# ScouterKit for Python

Python wrapper around the ScouterKit C++ library (`../src`, through its C layer
`../src/c/scouter_kit_c.h`) for Even Realities G1 glasses running the scouter firmware (g1v0 0.5.x,
protocol 5 with the on-arm VM, `../g1_firmware/docs/VM.md`). Linux + BlueZ. It follows the Go wrapper
(`../go`) call for call, with Python's conventions: exceptions, context managers, iterators,
snake_case, seconds for timeouts.

The package is plain Python on `ctypes` (no compiled extension). The library itself is built by
`build.sh` into two shared objects that sit inside the package:

| File | |
|---|---|
| `scouterkit/libscouterkit.so` | the glasses, through BlueZ (sd-bus from libsystemd) |
| `scouterkit/libscouterkit_emulator.so` | two emulated arms (`-DSCOUTER_G1_EMULATOR`): no Bluetooth, no glasses |

## Build and install

Needs Python 3.10+ with Pillow (numpy optional, for arrays), GCC with C++20, and for the glasses build
pkg-config and libsystemd (Debian: `libsystemd-dev`) plus BlueZ. Tested with Python 3.13.5, Pillow
13.0.0, numpy 2.5.4, GCC 14.2, libsystemd 257, BlueZ 5.82 (Debian 13).

```
cd scouter_kit/python
./build.sh                 # both shared objects, about 15 s; prints their SHA-256
pip install -e .           # or: pip install -e '.[test]' for pytest and numpy
```

`build.sh` compiles `../src/base/scouter_kit.cpp`, `../src/g1/scouter_kit_g1.cpp`,
`../src/c/scouter_kit_c.cpp`, `../src/vm/vm.c`, `../src/vm/vm_gfx.c`, the vendored liblc3
(`../third_party/liblc3`, the microphone's LC3 decoder) and `csrc/departure_mono.cpp` (the built-in
font, which the C layer has no accessor for) with `-O2 -fPIC -DSCOUTER_G1_MIC`, once per variant, and
links each with a version script that exports only the C layer (`sk_*`) and `-z defs` (no undefined
symbols). `CC` and `CXX` pick other compilers. The build is reproducible: it compiles from relative
paths with `-ffile-prefix-map`, embeds no time stamps or paths, and two builds give byte-identical
files. Run it again after changing `../src`. A wheel from `pip install .` carries the shared objects
built here, so it is for this platform only; the editable install is the intended one.

Which library: `scouterkit.init(emulator=True)` loads the emulator's, `init()` alone the one already
loaded, else the glasses'. One process uses one of them; the program builder works before `init()` too
(it loads the glasses' library unless `init(emulator=True)` came first). `init(log=True)` makes the
library log to stderr. The package reads no environment variables.

## API

```python
import scouterkit as sk
from PIL import Image

with sk.init() as session:                     # dx 100, dy 9: a 740 x 471 display, the bonded glasses
    font = sk.load_font(sk.departure_mono(), scale=2)
    line = sk.create_bitmap(700, 30)
    font.draw_line(line, "Hello, both eyes", 350, 24, sk.Align.CENTER)
    picture = sk.load_bitmap(Image.open("photo.png"))   # or a numpy array
    sk.draw(line, 20, 200)
    sk.draw(picture, 20, 240)
    sk.flush()                                 # both eyes change together
    print(sk.left.panel_crc())                 # (read back, expected)
                                               # leaving the block: deinit(), after the update is shown
```

| | |
|---|---|
| `init(dx=100, dy=9, left="", right="", adapter="", timeout=30, keep_sessions=False, log=False, emulator=None)` | connects and returns a `Session` (a context manager that calls `deinit()`); empty addresses: the bonded glasses, `"none"`: leave the arm out; `adapter`: `"hci1"` or its address, empty: the first powered one; `log`: the library logs to stderr. Raises `BluetoothError`, `NoG1FoundError`, `BondingError`, `FirmwareError` (all `ScouterKitError`) |
| `deinit()` | microphones off, waits until the last update is shown, disconnects, unloads everything; also runs at exit |
| `size()`, `draw(bitmap, x, y)`, `clear()`, `flush()` | the gray virtual display; `flush` returns once queued (after the previous update is shown) |
| `display()` | read-back of what the eyes show, as of the last `flush`: a Pillow `"L"` image |
| `create_bitmap(w, h)`, `load_bitmap(image, dither=True)` | bitmaps in the glasses' memory, from a Pillow image or an array (H x W gray, H x W x 3 RGB, H x W x 4 RGBA; uint8, bool or floats 0…1) |
| `b.write(image, x, y)`, `b.fill((x0, y0, x1, y1), gray)`, `b.image()`, `b.unload()`, `b.size`, `b.address`, `b.stride` | luma (transparent = black), the response curve, Floyd–Steinberg to the 16 levels; `with create_bitmap(...) as b:` unloads at the end |
| `to_gray(image)` | that luma conversion alone, as an `"L"` image |
| `set_response(curve)`, `response()`, `gamma_response(g)` | the library's level response curve (256 bytes, identity by default) |
| `load_font(blob_or_path, glyphs="", scale=1)`, `departure_mono()` | font blobs from `../font_converter`; the built-in Departure Mono 11 px |
| `f.ascent`, `f.descent`, `f.line_spacing`, `f.measure(text)`, `f.draw_line(b, text, x, baseline, align, gray)`, `f.unload()` | text, drawn by the arms as glyph runs |
| `total_memory()`, `free_memory()`, `unload_all()`, `alloc(n)`, `free(address)` | bytes per arm; arena memory at the same address on both arms (`DeviceMemoryError` when full) |
| `left`, `right`: `state()`, `set_brightness(0…42)`, `set_panel(on)`, `battery()`, `panel_crc(y0, rows)`, `arena_crc(address, length)`, `read_arena(address, n)`, `mirror(address, n)` | the per-arm escape hatch; `panel_crc` waits until the arm ran everything queued (`NoReplyError` while the panel is off) |
| `a.write_arena(address, data)`, `a.raise_(trigger)`, `a.notify(x, y)`, `a.sys_write(prop, v)`, `a.call(address)`, `a.run(code)` | host code |
| `a.load(program)`, `a.start(program)`, `a.stop()`, `a.vm_status()` | programs on an arm (VM.md §5) |
| `a.props(*ids)`, `a.prop(id)`, `a.report_inputs(mask)`, `a.next_event(timeout)`, `a.events(timeout=None)` | control requests (they bypass the queue: `notify` and its event mark what has run), `INPUT` reports, events as an iterator: `EventType.NOTIFY`, `HALT`, `ERROR`, `INPUT`, `RESET` |
| `a.device_time(after)`, `a.disconnect(seconds)`, `now_us()` | the arm's clock; drop the link for a while (the session and the program stay) |
| `a.mic(rate)`, `a.mic_state()` | the microphone, below |
| `a.emulated_panel()`, `a.set_emulated_property(prop, v)`, `a.set_emulated_mic(hz, peak)`, `emulate_new_host(keep)` | emulator only: the panel (640 x 480), sensor values, the microphone's sine, a new host process |

Pixels are 0 (off, see-through) … 255; the G1 shows 16 levels, so bitmaps and the display hold multiples
of 17. Every function can be called from several threads (the C calls release the GIL; a reader-writer
lock keeps handles valid). A bitmap or font raises `ScouterKitError` once unloaded (`unload`,
`unload_all`, `deinit`).

`load_font(..., stored=True)` and `load_bitmap(..., stored=True)` also keep the font or bitmap in each
arm's flash (VM.md §6.10), so a reboot or a later session loads it from there instead of over Bluetooth.
`store(blob, type)` puts any blob on both arms and returns its 16-byte ID, `assets(arm)` lists an arm's
store, `forget(id)` deletes on both, and `Program.asset_load(id, dst, off, n)` loads one in a program.

`keep_sessions=True` adopts the arms' sessions as they are: a program on them (the g1 notification
program, a clock) runs on and their memory stays, but the library then owns no memory and draws nothing
(`create_bitmap`, `load_font` and `alloc` fail). Use it in tools that only read state, use events or the
microphone, so that they do not disturb the arms (`examples/status.py`).

### Programs

```python
base = sk.alloc(1024)                          # the same address on both arms
p = sk.Program(base, 16)                       # trigger table, 16 pool words, then the code
box = (base + 1020) // 4                       # operands are arena words; sk.R(0)...sk.R(14) the registers
echo = p.label()
p.bind(echo)
p.add(sk.R(1), box, p.const(1))                # const: a pool word holding the value
p.notify(sk.R(1), p.const(0))
p.end()
p.on(sk.Trigger.USER0, echo)
p.changes(base + 1020, 4)                      # what the program writes: left out of arena_crc
sk.left.load(p)                                # ProgramError: unbound labels, a full pool, ...
sk.left.start(p)                               # after everything queued before has run
sk.left.write_arena(base + 1020, (41).to_bytes(4, "little"))
sk.left.raise_(sk.Trigger.USER0)
event = sk.left.next_event(1.0)                # EventType.NOTIFY, event.a == 42
```

`Program(table, pool_words, pool=None, code=None)` fixes the layout: the table at `table`, the pool
and the code right after it unless given. Every instruction is a method (`mov`, `add` … `sar` with
`and_`/`or_`, `load(bits, …)`/`store(bits, …)`, `jmp`, `call`, `beq` … `bges`, `sleep_until`,
`wait_event`, `raise_`, `notify`, `sys_read`/`sys_write`, `fill`, `blit`, `glyphs`, `draw`, `line`,
`write`, `write_rle`, `op` by opcode), with labels (`label`, `bind`, `at`), triggers (`on`) and the
result (`code`, `pool`, `table`, `error`, `validate()`). `Trigger`, `Prop`, `Mode`, `UNKNOWN` and
`ONE_BPP` are VM.md's numbers. `tests/test_program.py` builds §9.2's clock and compares it with the
bytes, table and pool it reads from VM.md.

### Microphone

```python
with sk.right.mic(sk.MicRate.KBPS_32) as mic:  # MIC 1: the arm's indicator lights up
    for frame in mic:                          # 10 ms each: frame.pcm, 160 int16 samples of 16 kHz mono
        use(frame.pcm)                         # frame.concealed: lost on the link, concealed (PLC)
print(mic.state.stopped)                       # MicStopReason.HOST, LINK, PANEL_OFF or FAULT
```

The microphone follows the project's privacy rules: only an explicit host call switches it on
(`Microphone.start()` or the with-block), and the library never starts it again after the arm stopped
it (link drop, `PANEL` = 0, fault). It goes off at the end of the with-block (exceptions and Ctrl-C
included), at `deinit()`, at the interpreter's exit, and on SIGTERM or SIGHUP: while a microphone is
on, those signals raise `SystemExit` when no other handler is set. A SIGKILL cannot switch it off: the
arm then captures, with its indicator lit, until the link drops. The arm shows its indicator while it
captures (a 16 x 16 block in the panel's outer top corner; `MIC` powers the panel on) and streams LC3
frames, which the library decodes on its Bluetooth thread and keeps for 2 s; the iterator ends once the
microphone is off and every frame is read. On the G1 the right arm has the microphone
(`state().microphone`); the left refuses it (`ScouterKitError`). While it is on, `panel_crc` of rows
4–19 differs by the indicator.

## Examples

```
python examples/image.py [--gamma 2.2] [--save display.png] FILE
python examples/text.py [--save display.png]
python examples/vmclock.py [--away SECONDS] [--stop]
python examples/notify.py [--app NAME] [--timeout 5] [--direct] [--socket PATH] [--check] TITLE [BODY]
python examples/status.py [--full]
```
All take `--left ADDR --right ADDR`, `--adapter hci1`, `--emulator` (the emulated arms), `--log` (the library logs to
stderr) and `--help`.

- `image`: a PNG, JPEG, the first frame of a GIF, …: luma, dark margin trimmed (black is
  see-through), shrunk to fit the display if needed (box filter, else 1:1), centred, Floyd–Steinberg;
  then both panels are read back (`panel_crc`) and compared with the expectation. `--gamma` sets
  `gamma_response(g)` as the response curve.
- `text`: a few lines of Departure Mono (scale 3 and 2, a dimmer line, a right-aligned clock) in the
  columns both eyes see; both panels are read back (`panel_crc`) and compared with the expectation.
- `vmclock`: VM.md §9.2's clock built with the program builder, one per arm (the right eye's position
  moved by dx, dy), plus a `NOTIFY` after each redraw: the arms redraw HH:MM every minute by themselves,
  in their own clocks. Each arm is read back (the digits and the state with `read_arena`, the band's
  panel rows against the text drawn by the host with `panel_crc`). `--away S` drops both links for S
  seconds, reconnects, checks again, corrects the clock (mailbox + `RAISE USER0`) and times the redraw.
  `--stop` stops the programs and switches the panels off at the end; without it the clock keeps running.
- `notify`, like `g1 notify`: hands the message to a running `g1 mirror` over its socket
  (`$XDG_RUNTIME_DIR/scouterkit-g1.sock`, one JSON line), or else connects, loads the notification
  program on both arms (the Go `g1` app's, built here with the builder: a ring of 8, the popup at one
  time T in each arm's clock, the last four while the head is tilted up past 20°) and leaves it
  running: the message hides by itself after `--timeout` and shows again while the head is up. The text
  is `HH:MM app: title` at level 15 and the body over two lines at level 9, cut with `...`. `--check`
  reads the popup back from both panels against the lines drawn by the host and waits until it hides.
- `status`: each arm's firmware, link, battery, panel and program, with `keep_sessions` (it disturbs
  nothing: a program on the arms runs on); `--full` adds every field of `state()` and `vm_status()`
  and twelve properties (the panel, the timer, the `TILT` range, the case, the current, …).

## Tests

```
python -m pytest           # 43 tests on the emulated arms, about 11 s (the glasses' test skipped)
SCOUTERKIT_GLASSES=1 python -m pytest tests/test_glasses.py -s   # the image example on the glasses
```
`test_glasses.py` resets the arms' sessions, so a program on them stops: `g1 notify -direct` (or
`examples/notify.py`) starts the notification program again.

On the emulator: the ctypes structures against the C compiler's layout of the header; image conversion
(luma, alpha over black, palettes, 16-bit, arrays) and the library's dithering (levels kept exactly,
the mean kept, 16 levels on a ramp, the response curve); the builder byte for byte against VM.md §9.2
(code, table, pool, size, `RAISE C12`), the §2.1 encoding, labels and errors; the display end to end
(both panels read back, the emulated panels equal to the display read-back through dx, dy), handles,
`keep_sessions`, threads; programs, events, host code, properties, `emulate_new_host`; the microphone
(counters, level and frequency of the emulated sine, 16 kbps, the left arm refused, off after an
exception, a link drop, `deinit`, SIGTERM in a child process, kept sessions); and every example in its
own process. 2026-10-27: all pass, 15 runs in a row (before that, one failure in 48 runs: `vmclock`
read the clock across a minute's redraw, which its check now waits out).

On g1v0 0.5.30 (2026-10-28, `logs/final/reg/` of the development tree): `test_glasses.py` (shown 0.65 s after
`flush`, both panels as expected), `image`, `text`, `vmclock --away 60`, `notify --check` and `status --full` passed on
both arms. On the glasses (g1v0 0.5.10 on both arms, in the open case, hci0, 2026-10-27; logs in `logs/python/` of
the development tree): `test_glasses.py` showed the image example's picture (278 x 142 after trimming)
0.9 s after `flush`, both panels read back as expected, 14 s in all with connecting; `status` read both
arms with their sessions kept, before and after (the notification program running on both after it was
started again).
