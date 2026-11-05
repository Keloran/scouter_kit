# ScouterKit for Go

Go wrapper around the ScouterKit C++ library (`../src`, through its C layer `../src/c/scouter_kit_c.h`)
for Even Realities G1 glasses running the scouter firmware (g1v0 0.5.x, protocol 5 with the on-arm VM,
`../g1_firmware/docs/VM.md`). Linux + BlueZ.

- Module path: **`scouterkit`** (local, until the repository has a public home such as
  `github.com/OWNER/scouter_kit/go`). The package compiles the C++ sources from `../src` itself
  (`scouter_kit.cpp`, `scouter_kit_g1.cpp`, `scouter_kit_c.cpp`, `departure_mono.cpp`, `vm.c` and
  `vm_gfx.c` here only include them), so use it from a checkout of `scouter_kit`. From another module:
  ```
  require scouterkit v0.0.0
  replace scouterkit => /path/to/scouter_kit/go
  ```
  (or `go work use /path/to/scouter_kit/go`).
- Needs Go 1.22+, g++ with C++20 (tested: Go 1.24.4, GCC 14.2), and for the real build pkg-config and
  libsystemd (Debian: `libsystemd-dev`; tested with 257) plus BlueZ (5.82). The microphone's LC3 decoder
  is the vendored liblc3 (`../third_party/liblc3`, compiled by `internal/lc3`; the library is built with
  `SCOUTER_G1_MIC`). The `micsource` example also needs libpipewire (Debian: `libpipewire-0.3-dev`;
  tested with 1.4.2) and a running PipeWire.

## Build

```
cd scouter_kit/go
go build ./examples/image                  # real build: BlueZ through sd-bus (libsystemd)
go build -tags emulator ./examples/image   # two emulated arms (-DSCOUTER_G1_EMULATOR), no libsystemd
go test -tags emulator ./...               # conversion, the builder vs VM.md, end to end on the emulated arms
go test -race -tags emulator ./...
go test -run Program .                     # the builder's golden bytes alone (needs libsystemd, no arms)
```
2026-10-28: all pass, with `-race` too (the emulated arms run the arms' interpreter, `../src/vm`).
`Config.Log` (the examples' `-log`) makes the library log to stderr. The demos with a program use
`Program.PanelPolicy` (`../src/README.md`): their panels stay off while the glasses lie in the case or are
folded, so their `-check` read-backs need the glasses out of the case and unfolded.

The `.cpp`/`.c` files here and in `internal/lc3` only include the library and liblc3 from `../src` and
`../third_party`, and Go's build cache hashes only a package's own files, so it misses changes there: after
changing library sources, run `go build -a ./...` here. It rebuilds every package and rewrites the shared build
cache, so later plain `go build` and `go run` use the new code. Other tags and test builds have their own cache
entries: give them `-a` once too (`go build -a -tags emulator ./...`, `go test -a -tags emulator ./...`).

## API

```go
cfg := scouterkit.DefaultConfig()               // DX 100, DY 9: a 740 x 471 display, bonded glasses
if err := scouterkit.Init(cfg); err != nil {   // errors.Is: ErrNoG1Found, ErrBonding, ErrFirmware, ErrBluetooth
	log.Fatal(err)
}
defer scouterkit.Deinit()                       // waits until the last update is shown

font, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "", 2)
line, err := scouterkit.CreateBitmap(700, 30)
font.DrawLine(line, "Hello, both eyes", 350, 24, scouterkit.AlignCenter, 255)
pic, err := scouterkit.LoadBitmap(img)          // any image.Image
scouterkit.Draw(line, 20, 200)
scouterkit.Draw(pic, 20, 240)
scouterkit.Flush()                              // both eyes change together
```

| | |
|---|---|
| `Init(Config)`, `Deinit()`, `DefaultConfig()` | `Config{DX, DY, Left, Right, Adapter, Timeout, KeepSessions, Log}`; empty addresses: the bonded glasses, `"none"`: leave the arm out; `Adapter`: the BlueZ adapter (`"hci1"` or its address), empty: the first powered one; `KeepSessions`: adopt the arms' sessions instead of resetting them, so a program on them runs on (the library then owns no arena memory and draws nothing; for control requests and events, such as the microphone); `CasePanel` (development): the panels work while the glasses lie in the charging case; `Log`: the library logs to stderr |
| `Size()`, `Draw(b, x, y)`, `Clear()`, `Flush()` | the gray virtual display; `Flush` returns once queued (after the previous update is shown) |
| `Display() *image.Gray` | read-back of what the eyes show, as of the last `Flush` |
| `CreateBitmap(w, h)`, `LoadBitmap(img)` | bitmaps in the glasses' memory |
| `b.Write(img, x, y)`, `b.Fill(rect, gray)`, `b.Image()`, `b.Unload()` | `Write`/`LoadBitmap`: luma (transparent = black), the response curve, Floyd–Steinberg to the 16 levels |
| `SetResponse`, `Response`, `GammaResponse(g)` | the library's level response curve (identity by default) |
| `LoadFont(blob, glyphs, scale)`, `DepartureMono()` | font blobs from `../font_converter`; the built-in Departure Mono 11 px |
| `f.Ascent`, `f.Descent`, `f.LineSpacing`, `f.Measure(text)`, `f.DrawLine(b, text, x, baseline, align, gray)`, `f.Unload()` | text, drawn by the arms as glyph runs |
| `TotalMemory()`, `FreeMemory()`, `UnloadAll()` | bytes per arm |
| `Left`, `Right`: `State()`, `SetBrightness(0…42)`, `SetPanel(on)`, `Battery()`, `PanelCRC(y0, rows)` | per-arm escape hatch; `PanelCRC` reads the panel back and gives the expected CRC too (`ErrNoReply` while the panel is off) |
| `NewProgram(table, pool, poolWords, code)`, `NewProgramAt(address, poolWords)` | the program builder (below) |
| `a.Load(p)`, `a.Start(table)`, `a.Stop()`, `a.VMStatus()` | programs on an arm (VM.md §5) |
| `a.WriteArena(addr, b)`, `a.ReadArena(addr, n)`, `a.ArenaCRC(addr, n)`, `a.Raise(t)`, `a.Notify(x, y)`, `a.SysWrite(p, v)`, `a.Call(addr)`, `a.Run(code)` | host code and read-back; arena addresses are bytes |
| `a.Props(ids...)`, `a.ReportInputs(mask)`, `a.NextEvent(timeout)` | properties (`Unknown` for a missing value), `INPUT` reports (in force: the request and `State().Triggers`), events: `EventNotify`, `EventHalt`, `EventError`, `EventInput`, `EventReset` |
| `a.DeviceTime(in)`, `a.Disconnect(d)`, `NowUS()` | the arm's clock; drop the link for d (the session and the program stay) |
| `Alloc(n)`, `Free(addr)`, `b.Address()`, `f.Address()`, `f.GlyphIndices(text)` | arena memory, the same address on both arms; where bitmaps and fonts are; glyph indices for programs |
| `a.Mirror(addr, n)` | the library's copy of what the host wrote to the arena (a font blob as loaded, a table), without a round trip |
| `a.MicStart(rate)`, `a.MicStop()`, `a.MicState()` | the microphone (VM.md §6.5; on the G1 the right arm, `State().Microphone`), below |
| `LoadFontStored(blob, glyphs, scale)`, `LoadBitmapStored(img)` | as `LoadFont`/`LoadBitmap`, and kept in each arm's flash (VM.md §6.10): a reboot or a later session loads them from there |
| `Store(blob, t)`, `Assets(arm)`, `Forget(id)`, `AssetIDOf(blob)`, `p.AssetLoad(id, dst, off, n)` | the arms' asset stores: put a blob on both arms (blocks), list one, delete on both; a program loads an asset with `AssetLoad`; the CLI: `g1 assets` |
| `Left.EmulatedPanel()`, `Left.SetEmulatedProperty(p, v)`, `Right.SetEmulatedMic(hz, peak)`, `EmulateNewHost(keep)` (emulator builds) | the emulated panel, 640 x 480; sensor values (`BATT_PCT` with the firmware's 30 s hysteresis); what the emulated microphone hears (a sine; 440 Hz at a quarter of full scale at first) |

Pixels are 0 (off, see-through) … 255; the G1 shows 16 levels, so bitmaps and the display hold multiples
of 17. Every function is safe for concurrent use. A bitmap or font handle becomes inert (its methods do
nothing) after `Unload`, `UnloadAll` or `Deinit`.

### Programs

```go
base, _ := scouterkit.Alloc(1024)               // the same address on both arms
p := scouterkit.NewProgramAt(base, 16)          // trigger table, 16 pool words, then the code
box := scouterkit.Word((base + 1020) / 4)       // operands are arena words; R(0)...R(14) are registers
echo := p.Label()
p.Bind(echo)
p.Add(scouterkit.R(1), box, p.Const(1))         // Const: a pool word holding the value
p.Notify(scouterkit.R(1), p.Const(0))
p.End()
p.On(scouterkit.TriggerUser0, echo)
p.Changes(base+1020, 4)                         // what the program writes: left out of ArenaCRC
if err := scouterkit.Left.Load(p); err != nil { // p.Err(): unbound labels, a full pool, ...
	log.Fatal(err)
}
scouterkit.Left.Start(base)                     // after everything queued before has run
scouterkit.Left.WriteArena(base+1020, []byte{41, 0, 0, 0})
scouterkit.Left.Raise(scouterkit.TriggerUser0)
e, _ := scouterkit.Left.NextEvent(time.Second) // EventNotify, e.A == 42
```
The builder (C++ `g1::Program` through the C layer) emits VM.md's byte code: every instruction as a method
(`Mov`, `Add` … `Sar`, `Load(bits, …)`/`Store(bits, …)`, `Jmp`, `Call`, the six branches, `SleepUntil`,
`WaitEvent`, `Raise`, `Notify`, `SysRead`/`SysWrite`, `Fill`, `Blit`, `Glyphs`, `Draw`, `Line`, `Write`,
`WriteRLE`, `Op` by opcode), labels (`Label`, `Bind`, `At`), triggers (`On`) and its bytes (`Code`, `Pool`,
`Table`). `TestProgramSpecBytes` builds VM.md §9.2 and §9.3 and compares them with the spec byte for byte.

## Examples

```
go run ./examples/image [-gamma 2.2] [-save display.png] [-left ADDR -right ADDR] [-adapter hci1] FILE
go run ./examples/text [-save display.png] [-left ADDR -right ADDR] [-adapter hci1]
go run ./examples/vmclock [-away 10m] [-stop] [-left ADDR -right ADDR] [-adapter hci1]
go run ./examples/videoplay [-width 540] [-height 460] [-fps 15] [-levels 2|16] [-threshold 128] [-start D] [-for D] [-crop auto|none|W:H:X:Y] [-audio] [-audio-delay D] [-stream [-lead 300ms]] [-check] [-save PNG] FILE
go run ./examples/gifplay [-check] [-loops N] [-for D] [-leave] [-away D] [-power D] [-scale F] [-max-kb N] [-dither] [-gamma G] [-save PNG] FILE.gif
go run ./examples/bookscroll [-font FILE.skf] [-scale N] [-wpm 220] [-step ROWS] [-width 540 -height 400] [-ring N] [-hold 3s] [-check N] [-for D] [-leave] [-away D] [-power D] FILE.txt
go run ./examples/rsvp [-wpm 300] [-orp=false] [-side 7] [-font FILE.skf] [-scale N] [-width 540] [-ring N] [-hold 2s] [-check N] [-for D] [-leave] [-away D] [-power D] FILE.txt
go run ./examples/dashboard [-angle 20] [-hysteresis 5] [-hold 1s] [-show] [-check] [-power D] [-for D] [-stop]
go run ./examples/cube [-size 150] [-fps 15] [-spin 2] [-frames N] [-rate 52] [-master left|right] [-check N] [-for D] [-leave] [-power D]
go run ./examples/micsource [-arm right] [-rate 32|16] [-buffer 60ms] [-hold 2s] [-for D] [-check] [-name scouterkit-g1-mic] [-description TEXT]
go run ./examples/g1 mirror [-serve [-replace]] [-bus ADDR] [-angle 20] [-hysteresis 5] [-hold 1s] [-list-for 15s] [-popup 5s] [-longest 15s] [-holdback 500ms] [-icons=false] [-apps A,B] [-skip A,B] [-urgency low|normal|critical] [-quiet 22:00-07:00] [-standby] [-font FILE.skf] [-scale N] [-socket PATH] [-check] [-for D] [-power D]
go run ./examples/g1 notify [-app NAME] [-icon NAME] [-timeout 5s] [-direct] [-standby] [-icons=false] [-socket PATH] TITLE [BODY]
go run ./examples/stereo [-ref 0.8] [-hud 2] [-label 0.8] [-menu 0.6] [-near 0.6] [-far 0] [-cycle 4s] [-fps 25] [-cycles N] [-check] [-for D] [-leave] [-power D]
go run ./examples/gestures [-depth 8] [-depth2 5] [-start 3] [-motion 60] [-quiet 300ms] [-guard 240ms] [-min 100ms] [-max 800ms] [-second 500ms] [-double 400ms] [-list FILE] [-lines 5] [-rate 52] [-hold 6s] [-check] [-idle 1m] [-record FILE.csv] [-for D] [-leave] [-power D]
go run ./examples/gestures -replay FILE.csv
go run ./examples/term [-wide] [-cols N] [-rows N] [-scale 1] [-font FILE.skf] [-check] [-leave] [COMMAND [ARGS...]]
go run -tags sherpa ./examples/captions {-live DIR | -model FILE [-vad FILE] [-lang en] [-step 500ms]} [-threads 8] [-denoise=false] [-rate 32|16] [-audio FILE [-pace=false] [-codec 32|16] [-ref FILE]] [-lines 3] [-tail 8] [-font FILE.skf] [-scale N] [-width 540] [-y N] [-clear 8s] [-check] [-for D]
go run -tags sherpa ./examples/teleprompter {-live DIR | -model FILE [-vad FILE] [-lang en] [-step 400ms] [-prompt=false]} [-threads 8] [-denoise=false] [-rate 32|16] [-audio FILE [-pace=false] [-codec 32|16]] [-font FILE.skf] [-scale N] [-width 540] [-height 240] [-mark 0.3] [-rows 16] [-frame 125ms] [-dim 5] [-check N] [-for D] SCRIPT.txt
```
All take `-left ADDR -right ADDR`, `-adapter hci1`, `-log` (the library logs to stderr) and `--help`.
Add `-tags emulator` to run them against the emulated arms; `-save` writes the display read-back as a PNG.

- `image`: a PNG, JPEG or the first frame of a GIF: luma, dark margin trimmed (black is see-through),
  shrunk to fit the display if needed (else 1:1), centred, Floyd–Steinberg; then both panels are read back
  (`PanelCRC`) and compared with the expectation. `-gamma` sets `GammaResponse(g)` as the response curve:
  the panel's levels are roughly linear in luminance, so about 2.2 puts midtones on the low levels where
  the steps are visible. An 800 x 500 GIF is shown at 702 x 402 after trimming, both panels as
  expected: in the emulator after 0.6 s, on the glasses (g1v0 0.4.2) after 12–14 s (595 packets per
  arm; 21–25 s in sessions where the PC's controller places the two links' events 1.25 ms apart, see
  `../src/README.md`; 19–30 s before the 7.5 ms link fix); on protocol 5 (g1v0 0.5.0 and 0.5.6, in
  the case) after 13.0–13.3 s, both panels as expected. The example waits for `ShownUpdate` to time
  it; `PanelCRC` waits by itself until the arm ran everything. User's verdict (2026-10-23, worn): the
  plain version (default curve) looks good.
- `text`: a few lines of Departure Mono (scale 3 and 2, a dimmer line, a right-aligned clock) in the
  columns both eyes see; both panels are read back (`PanelCRC`) and compared with the expectation.
- `vmclock`: VM.md §9.2's clock built with the program builder, one per arm (the right eye's position
  moved by DX, DY), plus a `NOTIFY` after each redraw: the arms redraw HH:MM every minute by
  themselves, in their own clocks. `-away D` drops both links for D, then reconnects, reads each arm
  back (its digits and state with `ReadArena`, the band's panel rows against the text drawn here with
  `PanelCRC`), corrects the clock (mailbox + `RAISE USER0`) and times the redraw. `-stop` stops the
  programs and switches the panels off at the end; without it the clock keeps running after exit. On
  the emulator: correction redrawn in 7–9 ms. On the glasses (g1v0 0.5.6, in the case, `-away 10m`):
  both arms kept the time for the 10 minutes, read back as drawn here, correction redrawn in 16–19 ms.
- `videoplay`: a video file (anything ffmpeg reads) on both eyes. ffmpeg decodes it to gray frames that fit the
  columns both eyes see (`-crop auto` cuts black borders found in the key frames); each frame is written into one
  bitmap (`Flush` uploads only the changed pixels) and shown on both eyes together. A frame more than half a period
  late is skipped, so the picture keeps time; `-audio` plays the sound on the PC with ffplay (`-audio-delay` shifts
  it). `-levels 2` (the default) is black and white, the least data; `-levels 16` uses the panel's 16 levels. The
  link sets the pace; the report gives the frames shown and skipped and each arm's packets per second.
  `-stream` (with `-levels 2`) sends each frame as runs of its changed rows (about 2.6 times less data than the
  changed pixels) that stored code on the arms composes into a canvas and draws at the frame's time in each arm's
  clock; a frame composed more than 30 ms late is not drawn, so the eyes stay in step. On the glasses (g1v0
  0.5.40, both arms through the nRF5340 DK, a 480 x 360 black-and-white video at 30 frames/s, 2026-11-02): the
  default mode showed 5.3 frames/s on average (about 4.5 KB per frame, nearly every row changes), `-stream`
  4–5 frames/s: the arms compose a busy frame in about 35 ms and draw it in about 33 ms, but while they run
  that code the link carries about 44 packets/s per arm instead of about 105–111 (not explained yet). On the
  emulated arms (`go test -tags emulator ./examples/videoplay`): a generated clip at both level settings and
  streamed, both panels read back as expected.

### Microphone

```go
frames, err := scouterkit.Right.MicStart(scouterkit.Mic32kbps) // the arm's indicator lights up
for f := range frames {                                         // 10 ms each: f.PCM, 160 samples of 16 kHz mono
	use(f.PCM[:])                                               // f.Concealed: lost on the link, concealed (PLC)
}
fmt.Println(scouterkit.Right.MicState().Stopped)                // closed: MicStop, the link dropped, the panel went off
scouterkit.Right.MicStop()                                      // MIC 0, and the panel off again
```
`MicStart` sends `MIC` once the arm ran what was queued before. The arm captures, shows its indicator (a
16 x 16 block at level 15 in the panel's outer top corner; `MIC` powers the panel on) and streams LC3
frames (32 kbps: 40 B, 16 kbps: 20 B; 5 per `MIC_DATA` event every 50 ms). The library decodes them with
liblc3 on its Bluetooth thread, conceals a gap in the frame counter (up to 1 s) with the decoder's PLC, and
keeps the last 2 s for the channel. While the microphone is on, the link stays on the active profile
(7.5 ms; VM.md asks for at most 15 ms). The arm stops by itself at a disconnection, at a panel or capture
fault, and when its 2 s lease is not renewed (the library renews it from the frames it receives, so a host
that stops reading or dies loses the microphone); a `PANEL` = 0 meanwhile only blanks the panel to the
indicator and the off waits for the microphone to stop (VM.md §6.5). The library never starts it again: the
channel closes and `MicState().Stopped` says why (`MicStoppedLink`, `MicStoppedPanelOff` for a panel fault,
`MicStoppedFault`, `MicStoppedLease`). `MicState()` counts events,
frames, gaps and the frames they missed, undecodable frames and frames not read in time. `MicStop`
switches the panel off again if it was off before `MicStart` and the library drew nothing since. While
it is on, `PanelCRC` of rows 4–19 differs from the expectation by the indicator (the library's mirror
does not draw it). A program on the arm that writes `PANEL` = 0 while the microphone is on stops it
(`NOTICE` 5, `MicStoppedPanelOff`): the firmware never captures without the indicator.

`micsource` makes the microphone a PipeWire source: an `Audio/Source` node `scouterkit-g1-mic` ("G1
glasses microphone", 16 kHz mono S16) that any PipeWire, PulseAudio or JACK application records from
(`pw-record --target scouterkit-g1-mic out.wav`, or pick "G1 glasses microphone" in the app); PipeWire
resamples for 48 kHz consumers. It connects the microphone's arm only, keeps its session
(`KeepSessions`: a program on the arm, such as `g1 mirror`'s notifications or a clock, runs on; on that
arm a program's `PANEL` = 0 ends the run), and refuses an arm without capture (`HELLO.board` bit 2)
before anything starts. The microphone is on only while something records from the
node (the node is streaming): it starts with the first recording (the first ~150 ms of it are silence)
and stops `-hold` (2 s) after the last one ends, so the indicator is lit exactly while audio is taken
and the arm draws its ~9 mA (review §4) only then. Once a second it prints the microphone and indicator
state, frames per second, gaps, concealed frames, the level (dBFS and a meter) and the node's queue.
Ctrl-C, SIGTERM, SIGHUP and `-for D` end it: `MIC` 0 and the panel off first, then the node is
removed. If the microphone stops by itself (link, panel) it says why, removes the node and exits with
status 1; it never starts the microphone again after that.
- **Buffering:** the node keeps `-buffer` (60 ms) queued on average: it plays silence until that much
  has arrived, then a PI controller on the queue nudges the stream's adaptive resampler
  (`pw_stream_set_rate`, ±0.5 % at most), because the arm's clock and the graph's differ (by 0.2 % in the
  tests here, a few hundred ppm on real hardware). An underrun plays silence and fills up again; what
  arrives late after it (a stall, then the arm's backlog at once) is dropped, oldest first, down to the
  target, and so is a queue more than 150 ms above it. While nothing records from the node, the samples
  are dropped. End to end: the arm's 50 ms bundling and BLE, about 70 ms, plus the queue and a 20 ms
  quantum. A link that stalls needs a larger `-buffer`: in a 3-minute soak with jittered 50 ms events
  and a 150 ms stall every ~4 s, 60 ms gave one dropout per stall (about 0.1 s of audio lost each) and
  200 ms none.
- **`-check`** (default `-for 5s`): a capture stream records from the node by name (never from another
  source: `node.dont-fallback`), in memory, and the run fails unless the frames came at 100/s by the arm's
  clock with no gap, undecodable or dropped frame, the decoded audio is not all zero, PipeWire delivered
  the audio without underruns, and panel rows 4–19 read back as the lit block while on and black (or the
  panel off) after. Only sums and counts are kept: no audio is stored anywhere.
- If the process is killed (SIGKILL) it cannot switch the microphone off: the arm keeps it on, with the
  indicator lit, until the link drops (`bluetoothctl disconnect ADDRESS`).

On the glasses (g1v0 0.5.10, the right arm in the open case, hci0, 2026-10-27; two `-check -for 5s`
runs, aggregates only, logs in `logs/m5/micsource/` of the development tree): the node was listed by
`pw-cli` and `wpctl` ("G1 glasses microphone") with the check's capture linked; 450 frames at 100.00/s
by the arm's clock, no gap, no undecodable or dropped frame; level −69 and −71 dBFS RMS (a quiet room,
peaks −46 and −56 dBFS); about 73 000 samples recorded through PipeWire, no underrun while the
microphone was on; the indicator read back lit while on and gone after; the session kept (no reset).
How it sounds, speech recognition and the end-to-end latency need a person (`docs/STATUS.md` of the
development tree).

Tests (`go test -tags emulator . ./examples/micsource`): the emulated right arm encodes a sine with liblc3
(as g1v0 0.5.10 frames it: 40 ms dropped at the start, then 5 frames every 50 ms of its clock); a 1 kHz
tone at a quarter of full scale comes out of the node through PipeWire at 1000 Hz and −14.3 dBFS (liblc3
decodes a pure tone 0.7 dB louder than the −15.05 dBFS it got), 100.00 frames/s, no gap, no underrun; the
node is listed while it runs (`pw-cli ls Node`) and gone after; the microphone is off until a capture
links, on while it records and off again after `-hold`; a link drop ends the run with the reason; the left
arm is refused; a counting TIMER program on the right arm runs on through a new host's kept session and
burst (`EmulateNewHost(true)`). `../tests/mic_test.cpp` covers the library: both rates, 10 % event loss (gaps
concealed, the counters continuous), the indicator, `MicStop`, `PANEL` = 0, a link drop and a reboot,
also under TSan and ASan/UBSan. The PipeWire part alone (a C harness, the feed 0.2 % fast and slow): the
queue stays at the target without underruns or drops, the correction settles at the clock difference
(−0.23 % and +0.22 %).

### VM demos

`gifplay`, `bookscroll`, `rsvp`, `dashboard` and `cube` run their real-time part as a program on each
arm (VM.md §5): the host prepares the data, uploads it with the program, gives each arm its start time in
its own clock (`DeviceTime`), starts both and steps back; the eyes stay in step because both programs
draw on the same schedule. The right eye's program draws DX to the left and DY lower (`PanelAt` in
`examples/internal/demo`). Every demo:

- `-check` first runs the program one step at a time (`USER3`, or host code that `CALL`s the step) and
  reads both panels back (`PanelCRC`) against what the host computes itself (`examples/internal/demo`:
  the composed frame, the text drawn from the font blob as loaded, the cube's fixed-point maths and
  `LINE`'s pixels), then switches to the timed run and, at the end, stops the program and checks the last
  frame it drew.
- `-power D` reads `BATT_MA` from both arms (the firmware's average of 64 one-second samples) 66 s
  after the start and every 64 s until D. The right arm's reading is about 8–9 mA high (`docs/power.md`
  §8): compare differences. A case top-up (`CHARGE` not 0) makes a reading meaningless; it is flagged.
- `-away D` (`gifplay`, `bookscroll`, `rsvp`) drops both links for D while it runs, then reconnects and
  checks that the program went on; `-leave` exits with the program running (`dashboard` does so unless
  `-stop`); `-for D` sets how long the host stays.
- A bitmap reaches down to the end of the arena (VM.md §3.1), so text bands get guard rows below them:
  glyphs of a line that comes in at the bottom are drawn past the band.

Costs on the arms (g1v0 0.5.6, single instructions timed with `TIME_US` in host code): `DRAW` 46 µs per
row + 0.196 µs per byte for an odd width, 12.5 µs more per row for an even width short of 640 (two panel
windows); `BLIT` (byte path) 0.105 µs, `UNPACK` 0.123 µs (plain runs; about 0.2 µs on real frames) and
`FILL` 0.12 µs per byte. So a 540 × 400 area takes 44 ms to show, a 200 × 200 cube 15.6 ms; the
run-time cap (30 ms of every 100 ms, VM.md §5.4) is what limits size and frame rate.

- `gifplay` stores the GIF's frames in the arena as the changed rectangle against the previous frame,
  4 bpp at the nearest level (`-dither`: Floyd–Steinberg inside the rectangle; it packs worse), PackBits:
  a rectangle as wide as the canvas is one chunk unpacked straight into the canvas, others are bands of
  at most 8 KB unpacked into scratch rows and `BLIT`ted. On each `TIMER` the program first `DRAW`s
  the changed rectangle of the frame it composed in the canvas after the last one (one pixel wider when
  even: one panel window per row), arms `TIMER` = the previous time + the frame's delay, then
  `UNPACK`s the next frame's chunks into the canvas; at the end of a loop it takes the host's anchor
  for the next one (sent every minute while connected), so the arms keep to the host's clock. The host
  fits the animation to the free arena and to the cap: the largest scale (at most 1:1) at which every
  frame fits, using at most 25 % of any 100 ms by the costs above, down to 160 px wide; then every
  second, third, … frame (each shown for the delays of those it stands for). `-loops N`: the arms
  switch the panels off after N loops by themselves.
- `bookscroll` lays a text file out in lines (word wrap with `Measure`, a blank line between
  paragraphs) with Departure Mono at scale 2 or any blob from `font_converter.py` (`-font`), and puts
  the lines as glyph indices in a ring in the arena (128 bytes a line). The program scrolls the page by
  `-step` rows (default a whole line) per period at `-wpm`: at the step's time it `DRAW`s the area,
  then prepares the next step in the same bitmap: one `BLIT` moves it up, a `FILL` clears the rows that
  come in, `GLYPHS` draws the lines that reach them. It `NOTIFY`s each new line; the host refills the
  ring then, so a long text streams in while the program scrolls; it waits (and says so) if the host
  falls behind, and ends after a minute without the next line. At the end the last page stays 30 s,
  then the panels go off. The font of the
  results below: `font_converter.py /usr/share/fonts/truetype/dejavu/DejaVuSans.ttf --size 20 --ranges
  20-7E,A0-17F,2010-2027 --kern -o dejavu20.skf`.
- `rsvp` shows a text one word at a time: each word is three glyph runs around its optimal
  recognition point (about a third into the letters), which stays on two focus marks; the letters
  around it are drawn at `-side` (7; the user found levels above about 8 hard to tell apart), the ORP
  letter at 15 (`-orp=false`: centred words). Each word's time is 60 s / `-wpm`, ×1.3 for long words,
  ×1.5 after `,;:`, ×2 after `.!?`, ×2.5 at a paragraph's end; the program keeps the schedule exactly
  (`TIMER` = the previous time + the word's time) and at each word's time `DRAW`s the band it
  prepared after the last word, then prepares the next one in the other band. A ring of 48-byte words is
  refilled on `NOTIFY` every 32 words; after a minute with the ring empty the program ends.
- `dashboard` is a home screen like stock's: `HH:MM` in the middle (both eyes) and each arm's battery
  (`L 87%`, `R 64%`) in the strip only its own eye sees. With the head up past `-angle` (`TILT`, range
  [−90, angle]) the program draws it; below angle − `-hysteresis` it hides `-hold` later with
  `PANEL` = 0. A minute `TIMER` keeps the clock (redrawn while shown), `BATTERY` redraws the battery;
  `USER0` takes a new time from the host, `USER1` shows it until `USER2` (`-show`, `-check`, `-power`).
  The host sets the time and leaves; drift is the arms' clock (≤ 130 ppm, about 0.5 s an hour).
- `cube` turns a wireframe cube against the head's pitch and roll: the program reads `PITCH` and
  `ROLL` (`IMU_RATE` 52 while it runs), looks up sin and cos in a Q14 table of 450 entries in the arena,
  rotates the 8 corners (spin about the vertical axis by `-spin` degrees a frame, then pitch about x,
  roll about z; worn, 2026-11-02: −pitch and −roll turned the wrong way) with `MUL`/`SAR`, projects them with `DIVS`, clears the rows the last cube covered,
  draws the 12 edges with `LINE` into a 150 × 150 bitmap and `DRAW`s the rows of both cubes (151 wide:
  one panel window per row), 15 times a second; the host
  lowers `-fps` if a frame would take more of the cap than a quarter. Each arm uses its own
  accelerometer; `-master left` has the host forward one arm's angles to the other (`PROPS` + `WRITE`
  every frame, so the host stays).

Results (2026-10-27; emulator: `go test -tags emulator ./examples/...`, also with `-race`; glasses:
g1v0 0.5.6 on both arms (the last two checks 0.5.8), in the open case, hci0, logs in `logs/demos/` of
the development tree). The
current is one or two 64 s `BATT_MA` windows on battery: the same session's dashboard hidden (panel off,
program idle) read 2.0 mA left and 13.0 mA right, so the differences below are what the demo costs.

| Demo | Emulator | Glasses |
|---|---|---|
| `gifplay` | every frame and the wrap equal to the host's frames on both panels (full-width, banded and empty changes); two timed loops, the second moved 40 ms by an anchor (kept to 2 ms); panels off after `-loops 2`; a stopped arm shows the frame its state says | tunnel GIF (800×600, 42 frames, 100 ms): fitted to 348×197 (111 KB, uploaded in 8–14 s), every frame read back as expected; 15 ms per frame on average, 22 ms at most (the cost model 7–11 % above), no cap pause in 40 s; eyes 0.2 ms apart (median, 1.4 ms max) at the loop starts; host away 20 s: both arms played on, still in step; 10.0 / 20.0 mA (+8.0 / +7.0) |
| `bookscroll` | after each of 40 steps (whole lines, 5 rows) both panels equal the page laid out here; a 24-line ring: the program waits for the host and goes on to the last page, or gives up when no line comes | GPL-3 (5 644 words) in DejaVu Sans 20 px from `font_converter.py`: 866 lines, all in the ring; first page and 20 steps read back as expected; at 600 wpm (a line every 0.76 s) eyes 4.5 ms apart (median, 5.3 ms max over 38 steps; g1v0 0.5.8); 20.0 / 27.0 mA (+18 / +14) for a full page of text at level 15 |
| `rsvp` | 80 words each with and without ORP equal to the host's; 430 words at 3 000 wpm through a 64-word ring, each `NOTIFY` within 1 ms of the text's times; gives up when the ring stays empty | GPL-3 at 300 wpm: the empty band and 20 words read back as expected; 320 words in 70.539 s for 70.540 s; eyes 2.1 ms apart (median, 3.6 ms max); with the host away 20 s both arms went on; 7.0 / 16.0 mA (+5 / +3) |
| `dashboard` | pitch up, down, up within the hold: shown, hiding, hidden 0.3–0.6 s later, shown again; battery 100 % and unknown redrawn; the clock past midnight | shown and read back as drawn on both panels, hidden with the panels off; hidden 2.0 / 13.0 mA, shown +5.5 mA on both arms |
| `cube` | frames at pitch and roll −90…90 and a full spin equal to the Go model of the program's Q14 maths and of `LINE`; `-master`; 30 timed frames, then panels off and `IMU_RATE` 13 | 20 frames read back as expected with each arm's own angles (in the case: left −90°/90°, right −69°/42°), `-master left` drew the left's on both; 150 px at 15 fps keeps time (with only the cube's rows cleared and drawn, 2–8 cap pauses in 15 s on 0.5.8); 8.0 / 17.0 mA (+6 / +4) |

Found on the glasses and fixed: frame costs far above the spec's 0.35 µs per `DRAW` byte (now
measured; the GIF fit and the cube's frame rate obey the cap: before, the arms fell behind and the
eyes drifted 166 ms apart); the readers chose their start time before the ring upload had run (the arms
then raced through the first seconds); a `DRAW` that came after other work could wait behind a cap
pause on one arm only, so `gifplay`, `bookscroll` and `rsvp` now `DRAW` first at the step's time and
prepare the next step after it (RSVP in a second band, the others in the same bitmap, since the panel
keeps its own copy).

Rerun on g1v0 0.5.30 (2026-10-28, in the open case, hci0; logs `logs/final/reg/` of the development tree): every
demo's `-check` passed on both panels — `gifplay` all 42 frames and the wrap (uploaded and read back in 13.4 s, eyes
1.6 ms apart (median) over 35 loops), `bookscroll` (DejaVu Sans 20 px) the first page and 20 steps, `rsvp` the band and
20 words, `cube` 20 frames, each stopped state as expected, `dashboard` shown and hidden as drawn; `image` and `text`
read back as expected, `vmclock -away 2m` kept time on both arms and was corrected in 20 / 36 ms. The case topped the
arms up during most of it, so the `-power` windows were flagged; on battery the GIF drew 10.3 mA on the left for 20
minutes (the pack +0.6 °C) and the dashboard shown +7.0 / +8.0 mA (`docs/power.md` §14 of the development tree).

### Apps (`docs/ideas.md` A1, B1, B2)

- `g1` (A1): `g1 mirror` shows the desktop's freedesktop notifications on the glasses, `g1 notify TITLE
  BODY` one message now. By default the mirror is a D-Bus monitor
  (`org.freedesktop.DBus.Monitoring.BecomeMonitor` with match rules for `Notify`, the daemon's
  replies and `NotificationClosed`): it only reads, so the desktop's own daemon (here xfce4-notifyd)
  keeps its popups, history, actions and do-not-disturb, nothing races for the bus name, and no
  eavesdropping (deprecated) is involved. From the daemon's replies it learns each notification's ID,
  so a notification that replaces another (`replaces_id`, `notify-send -r`) takes its place, and one
  the desktop closes leaves the glasses too. It monitors only while a daemon runs: when none does
  and none starts within 3 s (`StartServiceByName`; for example xfce4-notifyd without a graphical
  session, after the desktop crashed: the bus then waits 25 s or more for it, `notify-send` hangs and
  no monitor ever sees the call), it says so and is the daemon itself, yielding to a desktop daemon
  that asks to take the name; it follows the daemon leaving (`NameOwnerChanged`) or replacing it
  (`NameLost`). `-serve` always makes it the bus's notification daemon
  (it owns `org.freedesktop.Notifications`, answers `Notify`, `CloseNotification`,
  `GetCapabilities`, `GetServerInformation` and sends `NotificationClosed`);
  it refuses while another daemon runs (`-replace` takes over from one that allows it). The
  D-Bus client is ours (`dbus.go`: SASL EXTERNAL, the wire format, no dependency). Each notification
  becomes three lines of glyph indices (`HH:MM app: summary` at level 15, the body wrapped over two
  lines at level 9, cut with `...`; markup and entities removed) in a ring of 8 in each arm's arena,
  with its app's icon on the left (`-icons=false`: none): Pebble's notification icons (Apache-2.0,
  `../third_party/pebble_icons`, converted to 50 × 50 at 4 bpp by `examples/g1/icons_gen.go`, which
  runs rsvg-convert), picked by the words of the desktop entry, app name, icon name and category
  hint (`icons.go`: chat apps, mail, calls, calendar, alarms, music, ...; else the warning sign for a
  critical one, else the generic bell); each icon goes to the arena once, and the program `BLIT`s it
  beside the text. Each notification is first held back `-holdback` (500 ms): one closed or replaced
  meanwhile never shows (its replacement shows as a new one); its icon is loaded during the wait.
  The program on each arm: a new notification (`USER0`) pops up at a common time T in each arm's own
  clock (lead: two connection intervals + clock error + 15 ms) for its `expire_timeout` (the
  server's default `-popup` when the sender leaves it to the server, never more than `-longest`, also
  for "never"); the head tilted up past `-angle` (`TILT`) shows the last four (two lines each) until
  it goes down again (`-hold` later), or `-list-for` passed, after which the list comes back only once
  the head has been down; the panel is off otherwise, with the host away too (`-standby` keeps it in
  standby instead, `PANEL` = 2: a popup then lights up in about 4 ms instead of about 75 ms, for about
  1 mA more while hidden, `docs/power.md` §14 of the development tree). A popup that reaches the
  arm more than 2 s after T (after a reconnection) only goes to the list. Filters: `-apps`, `-skip`,
  `-urgency`, quiet hours (`-quiet`: to the list without a popup, critical ones still pop up), equal
  notifications within 5 s once. `g1 notify` (not held back; `-icon` picks the icon by a word such
  as `mail`, `chat`, `warning` or `done`) hands its message to a running mirror over a Unix socket
  (`$XDG_RUNTIME_DIR/scouterkit-g1.sock`, one JSON line); without one it connects itself (3–5 s),
  loads the glyphs it needs, shows the message and leaves the program running (it hides by itself and
  shows again while the head is up).
- `stereo` (B1): elements at chosen distances through per-eye disparity. Disparity D(z) = DX · ref / z
  pixels (DX 100 at `-ref` 0.8 m, the user's calibration: `docs/virtual-display.md` §1; 0 at
  infinity); the change from DX is split between the eyes (the left eye D − DX − ⌊(D − DX)/2⌋ to the
  right of the virtual display's mapping, the right eye ⌊(D − DX)/2⌋ to the left), so an element
  stays straight ahead while its depth changes; DY stays fixed. Nearer than D = 150 px (0.53 m) is
  refused: fusion breaks there. The scene, in the columns both eyes see: a HUD line at `-hud` (2 m,
  40 px), a label on the display's own plane (0.8 m, 100 px), a framed menu with a highlighted item at
  `-menu` (0.6 m, 133 px), and a word that slides from `-far` (infinity) to `-near` (0.6 m) and back
  every `-cycle` (a cosine in disparity, 25 fps; the bitmap's black margins erase the last position).
  The text bitmaps are drawn with the library; a program on each arm `DRAW`s them at its own
  positions and moves the slider on `TIMER` from a table of x per frame, both arms from one start time;
  every 10 s the host gives both the start of a coming cycle in each one's clock (taken when the cycle
  wraps), so drift and better clock estimates do not part the eyes.
  `-check` reads both panels back and finds each element on each panel by CRC over a search of
  ±6 px in x and y: the measured left − right offset must be the element's D and the vertical one DY
  (the HUD, label, menu and four slider frames).
- `gestures` (B2): a nod detector on each arm and a list it pages. The program reads `PITCH` and
  `ACC_X/Y/Z` every 20 ms (`IMU_RATE` 52: slightly slower than the sensor, since the firmware reads
  it at most once per sample period) and runs the detector (`detector.go` is the same algorithm in Go,
  step for step): the pitch is averaged over 4 samples (the sensor sits a few cm from the neck's pivot,
  so a brisk nod's angular acceleration adds a spurious ±10–40° to the accelerometer's pitch for a
  sample or two, which averages out); motion is a change of more than `-motion` 60 mg over 80 ms; a
  reference follows the pitch while the head is still; a nod starts when the pitch drops `-start` 3°
  below it after `-quiet` 300 ms of stillness ending up to `-guard` 240 ms before, and is one when the
  dip reaches `-depth` 8° and the pitch comes half way back after `-min` 100 ms to `-max` 800 ms; a
  second dip of `-depth2` 5° that starts within `-double` 400 ms and comes back within `-second` 500 ms
  makes a double nod, else the nod is reported when the window closes (so a single nod is reported
  about 0.4 s after it, a double one at its end). The stillness gate is what keeps walking out: the
  head's bounce never leaves 300 ms of stillness, and nods while walking are not detected. Each
  gesture is `NOTIFY`d (gesture, sample). The app part (`USER1`): a nod shows the list or turns to
  the next page, a double nod to the page before, `-hold` after the last gesture the panel goes off.
  While the host is connected (`LINK`) the arms leave the paging to it, so both eyes turn the same page
  at the same time: it takes the first arm's report (the other's within 600 ms is the same gesture) and
  hands it to both with one time T; with the host away each arm pages on its own detections at once
  (the two can disagree, more so until the IMU head frame is calibrated, until the arm-to-arm link). A shake needs the gyroscope (M8): gesture 3 and its handler (hide) are there,
  and the sampler has the place for its detector. `-record` NOTIFYs every sample and writes a CSV;
  `-replay FILE.csv` runs the Go detector over one (no glasses), for tuning on real data.
  `-check` replays synthetic traces on each arm through host code (`CALL` of the program's replay
  routine, 27 samples a packet) and compares the detections with the Go model's, pages by host
  gestures with panel read-back, and watches `-idle` for false gestures and the sampling rate.

Synthetic traces (`traces.go`): head pitch as minimum-jerk movements (nods 10–25° in 0.3–0.7 s,
at most 250°/s; double nods of two dips 0.25–0.5 s each, the second 60–100 % as deep), the
accelerometer 2–10 cm above and 3–9 cm in front of the pivot (lever-arm accelerations included),
walking at 1.6–2.2 steps/s (vertical bounce 0.15–0.35 g with a harmonic, fore-aft and sideways
sway, a damped jolt at each heel strike, a 0.5–2.5° pitch bob, looks down at the path), rest with
sway and slow looks up and down, 2–5 mg of noise, samples at 52 Hz read every 20 ms, `PITCH`
rounded as the firmware does. On 2000 nods and 2000 double nods (fresh seeds, not those tuned on):
all nods, 1999 double nods, none confused; no false gesture in 300 minutes of walking and 300 at rest,
nor in 24 hours of each on other seeds.
Beyond the tuned ranges (`TestDetectorVariants`): nods of 6–10° 89 %, 25–40° 100 % (double 98 %),
8–12 mg of noise 96 %, the sensor 0–2 or 10–15 cm above the pivot, walking 0.6–1.5 times as hard and
looks every 1–3 s: no false gesture.

- `term`: a terminal on the glasses. It runs a command (default `$SHELL`) on a pseudo-terminal
  (`pty.go`: `/dev/ptmx`, no dependency) of `-cols` × `-rows` cells, by default as many as fit the
  columns both eyes see and the display's height (77 × 33 at `-scale` 1 with Departure Mono's 7 × 14 px
  cells, 38 × 16 at scale 2; `-wide`: the whole 740 × 471 virtual display, 105 × 33 or 52 × 16, each eye
  showing its 640-px window of it and the outer 14 columns or so on one eye only), but not more than this
  terminal's size (it then says what size would show the whole area); this terminal stays the
  command's (raw mode, keys in, output out as before), and its own VT100/xterm emulator (`vt.go`:
  cursor movement, erasing, scroll regions, insert/delete, SGR with 256 and direct colours, the
  alternate screen, DEC line drawing, UTF-8 and wide characters, synchronized output; status queries
  answered when there is no terminal to answer them) keeps the screen for the glasses. Each row
  becomes a record of runs of glyph indices with a level each (`screen.go`: the default text 12, a
  colour 5–15 by its brightest channel, bold +3, faint ×⅔, backgrounds 0–9 by luma, reverse video,
  underline; symbols the font lacks become similar ones or `?`). The font is Departure Mono Nerd Font Mono
  (Nerd Fonts v3.5.1, `../third_party/departure_mono_nerd`), converted by `font_converter.py --cell` at
  the built-in font's 7 × 14 cell (glyphs cut to the cell; it has all braille patterns, which the
  built-in font lacks, and block elements; `--cell` draws those a font lacks): 11 952 glyphs, of which only the base set goes to the arms at the start (Latin,
  Greek, Cyrillic, punctuation and symbols, line drawing, block elements, braille, powerline: 1 246
  glyphs, 27 KB, 49 KB at scale 2); an icon or other character goes there when the screen first shows it
  (the font is loaded again with it, the screen redrawn), as long as the arena has room. btop needs
  80 × 24: run it with `-wide`. The program on each arm (`program.go`)
  keeps the area as a ring of row slots: the host sends only the rows that changed, and when the
  screen scrolled it turns the ring (`wTop`) so only the rows that came in are sent; `USER0` renders
  the dirty slots (6 per activation, within the jump budget), `USER1` waits for the frame's time on
  both arms, `DRAW`s the dirty rows (or the whole area, 2 `DRAW`s, after a turn) and `NOTIFY`s. One
  frame is in flight; output is gathered until it pauses for 10 ms (40 ms at most). `-check` shows
  test screens (styles, a screenful and scrolls, the alternate screen) and reads both panels back.
  Tested: the VT against tmux 3.5a on the same output of sh, less, vi, nano, whiptail and top at
  77 × 33 and 38 × 16 (`vt_tmux_test.go`, skipped with `-short` or without tmux).

Results (2026-10-27; emulator: `go test -tags emulator ./examples/g1 ./examples/stereo ./examples/gestures`, also
with `-race`; glasses: g1v0 0.5.10 on both arms, in the open case, hci0, logs in `logs/demos/5x-*` of the
development tree).

| App | Emulator | Glasses |
|---|---|---|
| `g1` | serve and monitor mode on a private bus with `notify-send`: popups read back on both panels (`€`, `—`, `é` included), the list, hidden, expiry after `-t 1500`; `notify-send -r` in place; the desktop's close hides it; TILT: list, hold, hidden, `-list-for` to dismissed, back only after the head went down; new ones while the list, the hold or the dismissed list show; `g1 notify` through the socket; popup 34 ms after the bus delivered it on the 15 ms link, 119 ms on the 50 ms idle link | monitor mode on the session bus (xfce4-notifyd answering `notify-send`) and serve mode on a private bus: every popup (`€`, `—`, `é` included) and the list read back on both panels as drawn here, hidden with the panels off, the 1.5 s one gone by itself, `g1 notify` through the socket and directly (shown 0.5 s after connecting); the list (four entries, ~40 ms of drawing) takes one or two cap pauses; a popup shows 138–220 ms after the bus delivered it while the panel is on (at T: lead 125 ms on the 50 ms idle link), 390–490 ms when the panel was off, since powering it on adds ~250–300 ms (the 300 ms acceptance needs a faster power-on in the firmware: a black panel costs ~5 mA); nothing shown 2.0 / 10.5 mA (as the dashboard hidden). On g1v0 0.5.16 the program hides with `PANEL` = 2: 138 ms from standby on the 50 ms idle link, 167 ms with the panel on, 65 ms on the 7.5 ms link, 60 ms through `g1 notify`'s socket (306–327 ms from off on 0.5.14) |
| `stereo` | HUD 40 px (2.00 m), label 100 px (0.80 m), menu 133 px (0.60 m) and the slider at four frames found at their offsets on both panels (dy 9); the emulated panels equal the composed scene; frames every 40 ms, eyes 0.1 ms apart; `-cycles 1` switches the panels off | every element found at its offset on both panels (HUD 40 px = 2.00 m, label 100 px, menu 133 px, the slider at four frames, dy 9); one start time: eyes 2.9 ms apart (median, 3.8 ms max), with the cycle anchors 1.2 ms (median, 4.9 ms max) over 809 frames; slider moving 10.0 / 18.0 mA (+8.0 / +7.5) |
| `gestures` | on both arms the program detects exactly what the Go model does on 50 nods, 50 double nods, 10 min of walking and 4 min at rest (all found, none false); live through the 20 ms sampler: a nod shows page 1, a double nod turns back, read back; 50 samples/s at rest with nothing reported; `-record` and its reader round-trip | both arms detect exactly what the Go model does on the replayed traces (3 nods, 3 double nods, 60 s walking, 30 s at rest; 13–60 s of samples in 0.7–2.5 s); nod, nod, double nod page 1, 2, 1, read back on both panels; 5 minutes idle in the case: no gesture, 50.0 samples/s on both arms (pitch −70° / −62°), busy 5 ms of every 100 ms; running 2.0 / 11.0 mA (+0 / +0.5, within the readings' resolution) |

Rerun on g1v0 0.5.30 (2026-10-28, in the case, `logs/final/reg/`): `g1 mirror -check` (popups 58–138 ms after the
bus delivered them from standby, the list, hidden, the socket), `g1 notify`, `stereo -check` (every element at its
offset, eyes 1.2–1.9 ms apart over 3221 frames), `gestures -check` (replays as the model, no gesture in 1 min idle)
and `micsource -check` (475 frames at 100.00/s, no gap, no underrun, indicator lit then gone) all passed. Since then
the app hides with `PANEL` = 0 by default (standby costs ≈ 1 mA, not the 0.2 mA it was chosen for): the check then
reports 307–350 ms for a popup from off, because the run-time cap pauses the program ~150 ms after a `DRAW` that
powered the panel on (VM.md §5.4); the popup itself is up about 75 ms after T, ~200 ms after the bus on the idle link.
Two of the first four checks in that mode failed a read-back (a left row read three ways right after the power-on;
the 3 s socket popup hid before the second arm's read-back): `demo.CheckPanel` now reads once more when the arm could
not verify a read, and the socket popup shows 5 s; the next run passed.

Found on the glasses and fixed: the stereo check read the panels before the program had drawn (the
panel's power-on: it now waits for the program's `NOTIFY`); one start time let the eyes drift 2.9 ms
apart (now the cycle anchors); a `VM_STATUS` without a reply left the stereo programs running at exit
(now stopped anyway). Programs are stopped at exit except the notification program (by design) and
`-leave`.

To keep the mirror running, a systemd user unit (`~/.config/systemd/user/g1-mirror.service`, then
`systemctl --user enable --now g1-mirror`), with the binary from `go build ./examples/g1`:
```
[Unit]
Description=ScouterKit notification mirror
After=graphical-session.target

[Service]
ExecStart=%h/bin/g1 mirror
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
```

### Speech (`captions`, `teleprompter`)

Both recognise speech on the PC, with no network service. The default is a streaming recogniser:
sherpa-onnx (Apache-2.0, with ONNX Runtime, MIT) running NVIDIA's NeMo FastConformer streaming transducer
with 80 ms look-ahead (`-live`, English, CC-BY-4.0). It gives each word about 0.3 s after it is spoken, or
0.5 s once its last token is in, and never takes back a word it gave. It needs about 10 % of 4 threads. The
other choice is whisper.cpp (MIT, `-model`, any Whisper language) by local agreement: each utterance is
decoded again and again, and a word counts once two decodes in a row agree on it. That takes 1.5–3.5 s, and
under CPU load it falls behind. Setup, once:
```
sh examples/internal/speech/fetch_sherpa.sh ~/sherpa     # sherpa-onnx 1.13.8 prebuilt, SHA-256 checked
sh examples/internal/speech/fetch_model.sh ~/models      # FastConformer 80 ms (103 MB), Whisper small-q5_1, Silero
export PKG_CONFIG_PATH=~/sherpa/lib/pkgconfig
go run -tags sherpa ./examples/captions -live ~/models/sherpa-onnx-nemo-streaming-fast-conformer-transducer-en-80ms-int8
```
For Whisper, also run `fetch_whisper.sh ~/whisper`, add `~/whisper/lib/pkgconfig` to `PKG_CONFIG_PATH`, and
build with `-tags whisper` (or both tags). Without either tag the demos build but refuse to start.
- **Input.** Speech comes from the right arm's microphone, which is on only while the command runs: its
  indicator is lit, and it goes off at exit, Ctrl-C, SIGTERM, SIGHUP and when the link drops. It can instead
  come from a file (`-audio`, anything ffmpeg reads; `-codec 32` passes it through the link's LC3 first).
- **Before the recogniser.** The PC suppresses stationary noise (`-denoise`: a Wiener filter, 16 ms sqrt-Hann
  frames, minimum statistics, gain floor −6 dB) and tells speech from quiet by the level of 150 Hz–4 kHz
  against a noise floor. With Whisper that segmenter also cuts the utterances; `-vad` (Silero, MIT) then
  decodes only those with at least 250 ms of speech.
- **The encoder's context with Whisper.** It covers only the audio (`audio_ctx`), with flash attention off:
  with it, whisper.cpp 1.9.5's cross-attention also read the unwritten padding of a cut context. A phrase
  that Whisper repeats is dropped.
- **Privacy.** Nothing is written to disk, and with the microphone the terminal shows counts and levels only,
  never the text.

Both programs draw only what changed, because GLYPHS costs the arm about 6 ms a line (g1v0 0.5.42):
- The host plans each update from what the arms' bitmaps hold: a scroll by one `BLIT` (the memmove path),
  then the lines that came in or changed, each cleared in its own box and drawn again.
- The program prepares it on `USER0`, `DRAW`s the rows that changed at a time the host gives (`TIMER`, the same
  on both eyes), then `NOTIFY`s. The host sends the next update only after that; the newest wanted state wins
  and the ones between are dropped.
- Line boxes are `max(LineSpacing, Ascent + Descent)` high, so no glyph reaches into its neighbour's box.
- After a reconnection (`State().Reconnects`) the next update draws everything.

- `captions`: the last `-lines` lines of the recent segments. The words the recogniser may still change (the
  tail) are at `-tail`, the others at 15 and never change. An update scrolls the band by whole lines when that
  keeps lines (a wrap), then draws the lines that differ, about one per update. The band goes blank only after
  `-clear` with no speech and no new word, never while someone speaks; 30 s later the panel goes off. The
  program blanks the band by itself only a minute after the host's last update. `-check` reads both panels
  back after every update.
- `teleprompter SCRIPT.txt`: the script is laid out as in `bookscroll`, with all its lines in a ring in the
  arena (refilled around the position when it is longer).
  - **Tracking.** The words of the segment under way are aligned to the script around the position: a local
    alignment in which small misspellings and cut-off words match and function words count less. A move back,
    or ahead by more than 8 words, needs a strong match or the same place twice in a row. After three texts
    matching nothing near, the whole script is searched (strong matches only).
  - **Display.** The line being read sits at `-mark` (a bar at the left edge), and the words read are at
    `-dim`.
  - **Scrolling.** The area scrolls smoothly: every `-frame` (125 ms) it moves a third of the way to the
    reading position, at least a row and at most `-rows` (16). Each step is one BLIT and DRAW, with no line
    drawn unless one came in or changed, because the next line is drawn below the area before it scrolls into
    view. There are no jumps.
  - **Whisper only.** It gets the script before each utterance as its prompt (`-prompt`), and its utterances
    are cut at 5 s.
  - **TouchBar.** A tap on the right arm moves on a line, on the left back one; a double tap pauses the
    following.
  - **`-check N`** steps through N words with no speech, reading both panels back after every step.

Tests: `go test -tags emulator ./examples/internal/speech ./examples/captions ./examples/teleprompter` covers
the segmenter, the stream, the tracker, both planners against a model of their program, the display read-back,
blanking only in quiet, and the microphone's on and off, with a stand-in recogniser. Add `-tags sherpa` and
`-args -live DIR -corpus DIR` (natural test speech from Kokoro, made by the development tree's corpus
script) for both demos end to end, and `-tags whisper` with `-args -model FILE` for Whisper.

Results on Kokoro speech in real time (Ryzen 9 9900X, 4 threads, the denoiser on), the same with 16 more
busy threads:
- **FastConformer 80 ms:** word errors 0.9 % clean and 3.5 % at 5 dB of pink noise. Words were stable 0.51 s
  (median) after they were spoken, 0.85 s at 90 %, first shown at 0.3 s, and never taken back.
- **Whisper small:** 0 % errors, but words were stable only after 1.7–3.5 s (median, idle). Under the load,
  56 s.
- **On the emulated arms:** captions had 2.7 % errors, with the DRAWs within 0.2 ms of their time. The
  teleprompter followed all 272 words of the Gettysburg Address, passing each sentence's last word by 0.15 s
  at most after it ended.

Real speech and the glasses: see `docs/speech.md` of the development tree.
