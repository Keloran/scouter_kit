// Host test with the emulated arms: repeated start/stop, stale generations,
// panels off until the first frame, partial arms, no leaked memory or threads.
#include <dirent.h>

#include <chrono>
#include <cstdio>
#include <cstring>
#include <string>
#include <thread>

#include "session.hpp"

namespace g1 = scouter_kit::g1;
namespace sk = scouter_kit;
using chewedfeed::Session;

static int failures = 0;
#define CHECK(cond)                                                                                                    \
  do {                                                                                                                 \
    if (!(cond)) {                                                                                                     \
      std::fprintf(stderr, "%s:%d: FAIL %s\n", __FILE__, __LINE__, #cond);                                            \
      failures++;                                                                                                      \
    }                                                                                                                  \
  } while (0)

static int Threads() {
  int n = 0;
  if (DIR *d = opendir("/proc/self/task")) {
    while (dirent *e = readdir(d))
      if (e->d_name[0] != '.') n++;
    closedir(d);
  }
  return n;
}

static int Lit(int arm) {
  const uint8_t *p = g1::emulator::Panel(arm);
  int n = 0;
  for (int i = 0; p && i < 640 * 480; i++) n += p[i] != 0;
  return n;
}

static uint32_t PanelPower(int arm) {
  uint32_t v = 0;
  (arm == 0 ? g1::left : g1::right).Prop(g1::PROP_PANEL, &v);
  return v;
}

// The emulated arms run queued commands on their own threads: wait for it.
template <typename F> static bool Eventually(F f) {
  for (int i = 0; i < 200; i++) {
    if (f()) return true;
    std::this_thread::sleep_for(std::chrono::milliseconds(10));
  }
  return false;
}

static void Draw(Session &s, uint64_t gen) {
  std::string err;
  int font = s.LoadFont(gen, 2, &err);
  CHECK(font >= 0);
  int m[3];
  CHECK(s.FontMetrics(gen, font, m) && m[2] > 0);
  CHECK(s.Measure(gen, font, "Hello") > 0);
  // A blob from Kotlin (fractional scales): the built-in one loads; a damaged one does not.
  const auto &blob = chewedfeed::DepartureMono();
  int scaled = s.LoadFontBlob(gen, blob, &err);
  int sm[3];
  CHECK(scaled >= 0 && s.FontMetrics(gen, scaled, sm) && sm[2] > 0 && s.Measure(gen, scaled, "Hello 世界") > 0);
  CHECK(s.LoadFontBlob(gen, std::vector<uint8_t>(blob.begin(), blob.begin() + 100), &err) < 0);
  CHECK(s.LoadFontBlob(gen, {}, &err) < 0);
  CHECK(s.Canvas(gen, 400, 200));
  CHECK(s.Clear(gen, 0));
  CHECK(s.Fill(gen, 0, 150, 400, 2, 153));
  CHECK(s.Text(gen, font, "Hello glasses", 4, m[0] + 4, 0, 255));
  CHECK(s.Present(gen, 100, 100));
}

int main() {
  { // a dummy thread first, so the baseline counts what the runtime keeps around
    std::thread t([] {});
    t.join();
  }
  // A joined thread can stay listed for a moment while the kernel reaps it.
  std::this_thread::sleep_for(std::chrono::milliseconds(100));
  int base = Threads();
  Session s;
  uint64_t last = 0;
  for (int cycle = 0; cycle < 5; cycle++) {
    auto r = s.Start({}, nullptr);
    CHECK(r.generation > last && r.error == 0);
    if (!r.generation) return 1;
    // Panels stay off until our first full frame.
    CHECK(PanelPower(0) == 0 && PanelPower(1) == 0);
    auto st = s.GetStatus(r.generation);
    CHECK(st.generation == r.generation && st.arms[0].present && st.arms[1].present);
    CHECK(st.arms[0].connected && st.arms[1].connected && st.width > 0 && st.height > 0);
    CHECK(st.pitch == 0);
    Draw(s, r.generation);
    CHECK(Eventually([] { return PanelPower(0) == 1 && PanelPower(1) == 1 && Lit(0) > 0 && Lit(1) > 0; }));
    // A stale generation does nothing.
    CHECK(!s.Present(last, 0, 0) && !s.Blank(last) && s.LoadFont(last, 1, nullptr) < 0);
    CHECK(s.LoadFontBlob(last, chewedfeed::DepartureMono(), nullptr) < 0);
    CHECK(s.GetStatus(last).generation == 0 && s.Events(last, 10).empty());
    CHECK(s.Blank(r.generation));
    CHECK(Eventually([] { return PanelPower(0) == 0 && PanelPower(1) == 0; }));
    CHECK(s.Present(r.generation, 100, 100));
    CHECK(Eventually([] { return PanelPower(0) == 1 && Lit(0) > 0; }));
    // A second start while running is refused.
    CHECK(s.Start({}, nullptr).error == chewedfeed::kBusy);
    s.Stop();
    s.Stop(); // idempotent
    CHECK(s.Generation() == 0);
    CHECK(!s.Present(r.generation, 0, 0) && !s.Clear(r.generation, 0) && s.Measure(r.generation, 0, "x") < 0);
    last = r.generation;
  }
  CHECK(Eventually([&] { return Threads() <= base; }));

  // The case shows in the status (the emulated arms raise no CASE input events).
  auto r = s.Start({}, nullptr);
  g1::emulator::SetProperty(1, g1::PROP_IN_CASE, 1);
  CHECK(Eventually([&] { return s.GetStatus(r.generation).arms[1].in_case == 1; }));
  g1::emulator::SetProperty(1, g1::PROP_IN_CASE, 0);
  CHECK(Eventually([&] { return s.GetStatus(r.generation).arms[1].in_case == 0; }));
  CHECK(s.Events(r.generation, 64).size() <= 64);
  // Fonts and the canvas are freed by Stop: a new session has all the memory.
  Draw(s, r.generation);
  s.Stop();
  r = s.Start({}, nullptr);
  CHECK(sk::free_memory == sk::total_memory);
  s.Stop();

  // Calibration: a ruler on the left, a crosshair on the right, the right moved without a restart.
  r = s.Start({.dx = -10, .dy = -8}, nullptr);
  {
    CHECK(!s.CalibrationMove(r.generation, 0, 0)); // nothing shown yet
    CHECK(s.CalibrationShow(r.generation, {}, 2, 80));
    CHECK(Eventually([] { return Lit(0) > 1000 && Lit(1) > 0; })); // the ruler has far more ink than the plus
    CHECK(Lit(0) > Lit(1));
    CHECK(s.CalibrationMove(r.generation, 20, -16));
    CHECK(!s.CalibrationMove(r.generation, 241, 0) && !s.CalibrationMove(r.generation, 0, -121));
    CHECK(!s.CalibrationShow(r.generation, {}, 2, 80)); // once per session
    uint64_t gen = r.generation;
    s.Stop();
    CHECK(!s.CalibrationMove(gen, 0, 0));
  }
  r = s.Start({}, nullptr);
  CHECK(sk::free_memory == sk::total_memory); // the patterns and programs went with the session
  s.Stop();

  // One arm only.
  r = s.Start({"", "none"}, nullptr);
  CHECK(r.generation != 0);
  auto st = s.GetStatus(r.generation);
  CHECK(st.arms[0].present && !st.arms[1].present && !st.arms[1].connected);
  Draw(s, r.generation);
  s.Stop();

  // Cancel before Init finishes: the start fails as cancelled and leaves nothing running.
  std::thread canceller([&] { s.Cancel(); });
  r = s.Start({}, nullptr);
  canceller.join();
  if (r.error == chewedfeed::kCancelled) CHECK(r.generation == 0 && s.Generation() == 0);
  s.Stop();
  CHECK(Eventually([&] { return Threads() <= base; })); // no thread outlives its session

  if (failures == 0) std::puts("session_test: ok");
  return failures != 0;
}
