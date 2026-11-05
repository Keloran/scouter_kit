// The microphone path on the emulated arms: LC3 frames of a known tone, decoded
// by the library (level, frequency, counters), concealment of lost events, the
// indicator, and every way the microphone stops (VM.md §6.5).
//   for f in third_party/liblc3/src/*.c; do gcc -std=c11 -O2 -DLC3_PLUS=0 -DLC3_PLUS_HR=0
//       -Ithird_party/liblc3/include -c $f -o lc3_$(basename $f .c).o; done
//   g++ -std=c++20 -O2 -DSCOUTER_G1_EMULATOR -DSCOUTER_G1_MIC tests/mic_test.cpp src/base/scouter_kit.cpp
//       src/g1/scouter_kit_g1.cpp src/vm/vm.c src/vm/vm_gfx.c lc3_*.o -o mic_test -lpthread -lm
#if !defined(SCOUTER_G1_EMULATOR) || !defined(SCOUTER_G1_MIC)
#error "build with -DSCOUTER_G1_EMULATOR -DSCOUTER_G1_MIC"
#endif

#include <chrono>
#include <cmath>
#include <cstdio>
#include <string>
#include <thread>
#include <vector>

#include "../src/g1/scouter_kit_g1.hpp"

namespace g1 = scouter_kit::g1;

static int failures = 0;

#define CHECK(cond)                                                       \
  do {                                                                    \
    if (!(cond)) {                                                        \
      std::printf("FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);         \
      failures++;                                                         \
    }                                                                     \
  } while (0)

static void Sleep(int ms) { std::this_thread::sleep_for(std::chrono::milliseconds(ms)); }

struct Run {
  std::vector<g1::MicFrame> frames;
  int concealed = 0;
  bool in_order = true;
};

// Reads `n` frames (at most 3 s per frame), checking that the counters follow on.
static Run Read(g1::Arm &arm, int n) {
  Run r;
  g1::MicFrame f;
  while (int(r.frames.size()) < n && arm.MicRead(&f, 3000)) {
    if (!r.frames.empty() && f.counter != r.frames.back().counter + 1) r.in_order = false;
    r.concealed += f.concealed;
    r.frames.push_back(f);
  }
  return r;
}

// RMS (dBFS, full-scale sine = -3 dB) and frequency (upward zero crossings) of
// frames [from, end). liblc3 decodes a pure tone about 0.7 dB louder.
static void Measure(const Run &r, size_t from, double *dbfs, double *hz) {
  double sum = 0;
  long n = 0, up = 0;
  int prev = 0;
  for (size_t k = from; k < r.frames.size(); k++)
    for (int16_t v : r.frames[k].pcm) {
      sum += double(v) * v;
      if (n > 0 && prev < 0 && v >= 0) up++;
      prev = v;
      n++;
    }
  *dbfs = n ? 10 * std::log10(sum / double(n) / (32768.0 * 32768.0)) : -200;
  *hz = n ? double(up) * g1::kMicSampleRate / double(n) : 0;
}

static bool Near(double a, double b, double tolerance) { return std::fabs(a - b) <= tolerance; }

static bool Indicator(int arm) {
  const uint8_t *p = g1::emulator::Panel(arm);
  int x = arm == 1 ? 620 : 4;
  for (int y = 4; y < 20; y++)
    for (int i = 0; i < 16; i++)
      if (p[y * 640 + x + i] != 15) return false;
  return p[10 * 640 + x - 1] == 0 && p[10 * 640 + x + 16] == 0 && p[3 * 640 + x] == 0 && p[20 * 640 + x] == 0;
}

static bool Blank(int arm) {
  const uint8_t *p = g1::emulator::Panel(arm);
  for (int i = 0; i < 640 * 480; i++)
    if (p[i]) return false;
  return true;
}

static bool WaitConnected(g1::Arm &arm) {
  for (int k = 0; k < 100 && !arm.GetState().connected; k++) Sleep(50);
  return arm.GetState().connected;
}

static bool WaitReconnect(g1::Arm &arm, uint32_t reconnects) {
  for (int k = 0; k < 100; k++) {
    g1::Arm::State s = arm.GetState();
    if (s.reconnects > reconnects && s.connected) return true;
    Sleep(50);
  }
  return false;
}

static uint32_t Word(g1::Arm &arm, g1::Address address) {
  uint8_t b[4] = {};
  arm.ReadArena(address, b);
  return uint32_t(b[0] | b[1] << 8 | b[2] << 16 | b[3] << 24);
}

// Connection::keep_sessions: a program on the right arm (a 100 ms TIMER that
// counts) runs on while a new host adopts the session and records; a PANEL =
// 0 from the program stops the microphone; a kept session owns no memory.
static void TestKeep() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  int64_t base = g1::Alloc(512);
  CHECK(base >= 64);
  g1::Address count = g1::Address(base) + 508;
  g1::Program p(g1::Address(base), 16);
  g1::Program::Label tick = p.NewLabel(), off = p.NewLabel();
  p.Bind(tick);
  p.Add(g1::ArenaWord(count / 4), g1::ArenaWord(count / 4), p.Const(1));
  p.SysRead(g1::R(0), p.Const(g1::PROP_TIME_US));
  p.Add(g1::R(0), g1::R(0), p.Const(100000));
  p.SysWrite(p.Const(g1::PROP_TIMER), g1::R(0));
  p.End();
  p.Bind(off);
  p.SysWrite(p.Const(g1::PROP_PANEL), p.Const(0));
  p.End();
  p.On(g1::TRIGGER_START, tick);
  p.On(g1::TRIGGER_TIMER, tick);
  p.On(g1::TRIGGER_USER1, off);
  p.Changes(count, 4);
  CHECK(p.Valid() && g1::right.Load(p) && g1::right.Start(g1::Address(base)));
  Sleep(350);
  uint32_t epoch = g1::right.GetState().epoch, ticks = Word(g1::right, count);
  CHECK(ticks >= 3);

  g1::emulator::NewHost(true);
  CHECK(WaitReconnect(g1::right, g1::right.GetState().reconnects));
  g1::VmStatus vs{};
  CHECK(g1::right.Status(&vs) && vs.state == 1);
  CHECK(g1::right.GetState().epoch == epoch && g1::right.GetState().epoch_changes == 0);
  uint32_t adopted = Word(g1::right, count);
  CHECK(adopted > ticks);
  CHECK(g1::right.MicStart());
  Run r = Read(g1::right, 100);
  CHECK(r.frames.size() == 100 && r.in_order && r.concealed == 0);
  CHECK(Indicator(1));
  CHECK(g1::right.MicStop());
  CHECK(Blank(1)); // the panel was off before: off again
  CHECK(g1::right.Status(&vs) && vs.state == 1);
  uint32_t after = Word(g1::right, count);
  std::printf("kept session: %u ticks before, %u adopted, %u after a 1 s burst\n", ticks, adopted, after);
  CHECK(after >= adopted + 8);

  // The program switches the panel off (VM.md §6.5): the microphone goes on, the panel shows only the
  // indicator and PANEL reads 0; when the microphone stops the panel goes off.
  CHECK(g1::right.MicStart());
  CHECK(Read(g1::right, 10).frames.size() == 10);
  g1::right.Raise(g1::TRIGGER_USER1);
  Sleep(300);
  uint32_t panel = 1;
  CHECK(g1::right.GetMicState().on && Read(g1::right, 20).frames.size() == 20);
  CHECK(Indicator(1) && g1::right.Prop(g1::PROP_PANEL, &panel) && panel == 0);
  CHECK(g1::right.Status(&vs) && vs.state == 1);
  CHECK(g1::right.MicStop());
  CHECK(Blank(1) && g1::right.GetMicState().stopped == g1::MIC_STOP_HOST);
  g1::Deinit();

  g1::Connection keep;
  keep.keep_sessions = true;
  CHECK(g1::Init(nullptr, &error, &keep) == g1::ERROR_NONE);
  CHECK(g1::Alloc(64) == -1 && scouter_kit::total_memory == 0);
  CHECK(scouter_kit::gray::CreateBitmap(8, 8).pimpl == nullptr);
  CHECK(g1::right.MicStart());
  CHECK(Read(g1::right, 20).frames.size() == 20);
  CHECK(g1::right.MicStop());
  CHECK(Blank(1));
  g1::Deinit();
}

// The lease (VM.md §6.5): the library renews it from the frames it receives, so a
// 3 s capture stays on; a host that stops renewing loses the microphone within
// 2 s, with MIC_STOP_LEASE, the indicator cleared and no frame after.
static void TestLease() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  CHECK(g1::right.MicStart());
  CHECK(Read(g1::right, 300).frames.size() == 300);
  CHECK(g1::right.GetMicState().on);
  g1::emulator::HoldMicRenewals(true);
  auto t0 = std::chrono::steady_clock::now();
  while (g1::right.GetMicState().on && std::chrono::steady_clock::now() - t0 < std::chrono::seconds(4)) Sleep(20);
  double s = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
  g1::MicState st = g1::right.GetMicState();
  std::printf("lease: off %.2f s after the renewals stopped (reason %d)\n", s, int(st.stopped));
  CHECK(!st.on && st.stopped == g1::MIC_STOP_LEASE && s >= 1.1 && s <= 2.3);
  CHECK(Blank(1));
  g1::emulator::HoldMicRenewals(false);
  CHECK(g1::right.MicStop());
  g1::Deinit();
}

int main() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  CHECK(g1::right.GetState().microphone && !g1::left.GetState().microphone);
  CHECK(!g1::right.GetMicState().on && g1::right.GetMicState().stopped == g1::MIC_STOP_HOST);

  // The left arm has no capture (HELLO.board bit 2 clear): refused with 12.
  CHECK(!g1::left.MicStart());
  CHECK(g1::left.GetState().last_error.find("MIC refused: mic") != std::string::npos);
  CHECK(!g1::left.GetMicState().on);

  // 440 Hz at a quarter of full scale: -15.1 dBFS.
  CHECK(g1::right.MicStart());
  g1::MicState s = g1::right.GetMicState();
  CHECK(s.on && s.frame_bytes == 40 && s.frames_per_event == 5);
  Run r = Read(g1::right, 150);
  CHECK(r.frames.size() == 150 && r.in_order && r.concealed == 0 && r.frames[0].counter == 0);
  double dbfs = 0, hz = 0;
  Measure(r, 20, &dbfs, &hz);
  std::printf("32 kbps: %.2f dBFS, %.1f Hz\n", dbfs, hz);
  CHECK(Near(dbfs, -15.05, 1) && Near(hz, 440, 2));
  double span_s = double(r.frames[145].device_us - r.frames[5].device_us) / 1e6; // first frames of events
  double per_s = (r.frames[145].counter - r.frames[5].counter) / span_s;
  std::printf("frames per second by the arm's clock: %.2f\n", per_s);
  CHECK(Near(per_s, 100, 1));
  CHECK(Indicator(1));
  CHECK(g1::right.GetState().interval_us <= 15000); // the active link while it is on
  CHECK(g1::right.MicStart());                       // already on: nothing changes
  r = Read(g1::right, 10);
  CHECK(r.frames.size() == 10 && r.frames[0].counter == 150 && r.in_order);

  g1::emulator::SetMicSignal(1, 1000, 0.5);
  Read(g1::right, 20);
  r = Read(g1::right, 100);
  Measure(r, 0, &dbfs, &hz);
  std::printf("1 kHz: %.2f dBFS, %.1f Hz\n", dbfs, hz);
  CHECK(Near(dbfs, -9.03, 1) && Near(hz, 1000, 3));

  // Lost events: gaps in the counter, concealed, the timeline kept.
  g1::emulator::SetLoss(0, 0.1);
  r = Read(g1::right, 300);
  g1::emulator::SetLoss(0, 0);
  s = g1::right.GetMicState();
  std::printf("10%% loss: %zu frames, %d concealed, %llu gaps, %llu missing\n", r.frames.size(), r.concealed,
              (unsigned long long)s.gaps, (unsigned long long)s.missing);
  CHECK(r.frames.size() == 300 && r.in_order && r.concealed > 0 && s.gaps > 0);
  CHECK(s.missing % 5 == 0 && s.bad == 0 && s.dropped == 0 && uint64_t(r.concealed) <= s.missing);
  CHECK(s.frames + s.missing >= r.frames.back().counter + 1);

  // MicStop: off at once, the indicator gone, the panel off again.
  CHECK(g1::right.MicStop());
  s = g1::right.GetMicState();
  CHECK(!s.on && s.stopped == g1::MIC_STOP_HOST);
  g1::MicFrame f;
  CHECK(!g1::right.MicRead(&f, 300));
  uint32_t got = 0, want = 0;
  CHECK(!g1::right.PanelCrc(0, 1, &got, &want)); // the panel is off (and the queue has run)
  CHECK(Blank(1));

  // 16 kbps.
  g1::emulator::SetMicSignal(1, 440, 0.25);
  CHECK(g1::right.MicStart(g1::MIC_16KBPS));
  s = g1::right.GetMicState();
  CHECK(s.on && s.frame_bytes == 20 && s.frames_per_event == 5 && s.frames == 0 && s.gaps == 0);
  r = Read(g1::right, 100);
  Measure(r, 20, &dbfs, &hz);
  std::printf("16 kbps: %.2f dBFS, %.1f Hz\n", dbfs, hz);
  CHECK(r.frames.size() == 100 && r.in_order && r.concealed == 0 && r.frames[0].counter == 0);
  CHECK(Near(dbfs, -15.05, 1) && Near(hz, 440, 2));
  // Another rate while on: the arm starts again from 0 in the new format.
  CHECK(g1::right.MicStart(g1::MIC_32KBPS));
  r = Read(g1::right, 30);
  s = g1::right.GetMicState();
  CHECK(r.frames.size() == 30 && r.frames[0].counter == 0 && r.in_order && s.frame_bytes == 40 && s.gaps == 0);

  // PANEL = 0 from host code while on (VM.md §6.5): the microphone goes on, the panel shows only the
  // indicator; MicStop then takes the panel off.
  g1::right.SetPanel(false);
  Sleep(300);
  s = g1::right.GetMicState();
  CHECK(s.on && Read(g1::right, 20).frames.size() == 20 && Indicator(1));
  CHECK(g1::right.MicStop());
  CHECK(Blank(1));
  while (g1::right.MicRead(&f, 0)) {
  }
  CHECK(!g1::right.MicRead(&f, 200));

  // A dropped link stops it on both ends; nothing comes after the reconnect.
  CHECK(g1::right.MicStart());
  CHECK(Read(g1::right, 20).frames.size() == 20);
  g1::emulator::Disconnect(1, 400);
  for (int k = 0; k < 40 && g1::right.GetMicState().on; k++) Sleep(25);
  s = g1::right.GetMicState();
  CHECK(!s.on && s.stopped == g1::MIC_STOP_LINK);
  CHECK(WaitConnected(g1::right));
  while (g1::right.MicRead(&f, 0)) {
  }
  uint64_t frames = g1::right.GetMicState().frames;
  Sleep(300);
  CHECK(!g1::right.MicRead(&f, 0) && g1::right.GetMicState().frames == frames && !g1::right.GetMicState().on);
  CHECK(!Indicator(1));
  CHECK(g1::right.MicStop());

  // A reboot as well.
  CHECK(g1::right.MicStart());
  CHECK(Read(g1::right, 10).frames.size() == 10);
  g1::emulator::Reboot(1);
  for (int k = 0; k < 40 && g1::right.GetMicState().on; k++) Sleep(25);
  CHECK(g1::right.GetMicState().stopped == g1::MIC_STOP_LINK);
  CHECK(WaitConnected(g1::right));
  Sleep(300);
  CHECK(!g1::right.GetMicState().on && !Indicator(1));
  CHECK(g1::right.GetState().microphone);

  g1::Deinit();
  TestKeep();
  TestLease();
  std::printf("%s (%d failures)\n", failures ? "FAILED" : "all passed", failures);
  return failures ? 1 : 0;
}
