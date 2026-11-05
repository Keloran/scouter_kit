// Check on the glasses: a test screen on both eyes, verified pixel by pixel by
// reading both panels back (PANEL_READ CRC) and both arenas (ARENA_CRC), then
// timed clock updates, then one update from the idle link profile.
//   g++ -std=c++20 -O2 tests/glasses_check.cpp src/base/scouter_kit.cpp src/g1/scouter_kit_g1.cpp
//       src/vm/vm.c src/vm/vm_gfx.c $(pkg-config --cflags --libs libsystemd) -lpthread -o glasses_check
//   ./glasses_check LEFT_ADDRESS RIGHT_ADDRESS [--updates N] [--power SECONDS] [--black-off] [--log]
// --power: then the arms' battery current (BATT_MA) on the idle link profile,
// and on the active one with a NOTIFY every 250 ms, SECONDS each. --black-off:
// then a black screen, in standby at once and off SCOUTER_G1_BLACK_OFF_MS later.
// --log: the library logs to stderr.
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

#include "../src/base/departure_mono.inc"
#include "../src/g1/scouter_kit_g1.hpp"

#ifndef SCOUTER_G1_BLACK_OFF_MS
#define SCOUTER_G1_BLACK_OFF_MS 30000 // as the library's default
#endif

namespace sk = scouter_kit;
namespace g1 = scouter_kit::g1;

static int failures = 0;

static void Check(bool ok, const std::string &what) {
  std::printf("%s %s\n", ok ? "ok  " : "FAIL", what.c_str());
  if (!ok) failures++;
}

static double Ms(int64_t from) { return double(g1::NowUs() - from) / 1000.0; }

static void Verify(const char *when) {
  const char *names[2] = {"left", "right"};
  g1::Arm *arms[2] = {&g1::left, &g1::right};
  for (int i = 0; i < 2; i++) {
    uint32_t panel = 0, expected = 1, arena = 0, mirror = 1;
    bool p = arms[i]->PanelCrc(0, 480, &panel, &expected);
    bool a = arms[i]->ArenaCrc(0, 0, &arena, &mirror);
    char text[200];
    std::snprintf(text, sizeof(text), "%s, %s eye: panel %08x / expected %08x, arena %08x / mirror %08x",
                  when, names[i], panel, expected, arena, mirror);
    Check(p && a && panel == expected && arena == mirror, text);
  }
}

static void PrintState(const char *label, g1::Arm &arm) {
  g1::Arm::State s = arm.GetState();
  std::printf("%s: %s, firmware %d.%d.%d, max packet %d, interval %.2f ms, clock error %d us, "
              "%s, packets %u, resent %u, reconnects %u, errors %u%s%s\n",
              label, s.connected ? "connected" : "DOWN", s.firmware_major, s.firmware_minor,
              s.firmware_patch, s.max_packet, s.interval_us / 1000.0, s.clock_error_us,
              s.idle ? "idle profile" : "active profile", s.packets,
              s.resent, s.reconnects, s.errors, s.last_error.empty() ? "" : ", last: ",
              s.last_error.c_str());
}

// Average battery current per arm (BATT_MA, mA, positive while discharging).
static void Power(const char *label, int seconds, bool traffic) {
  g1::Arm *arms[2] = {&g1::left, &g1::right};
  double sum[2] = {0, 0};
  int n[2] = {0, 0};
  for (int k = 0; k < 120; k++) { // until both links run the profile
    if (traffic)
      for (g1::Arm *a : arms) a->Notify(1, 2);
    g1::Arm::State ls = g1::left.GetState(), rs = g1::right.GetState();
    int want = traffic ? 7500 : 50000;
    if (ls.interval_us == want && rs.interval_us == want) break;
    std::this_thread::sleep_for(std::chrono::milliseconds(250));
  }
  int64_t end = g1::NowUs() + int64_t(seconds) * 1000000, next = 0;
  while (g1::NowUs() < end) {
    if (traffic)
      for (g1::Arm *a : arms) a->Notify(1, 2);
    if (g1::NowUs() >= next) {
      next = g1::NowUs() + 5000000;
      for (int i = 0; i < 2; i++) {
        uint32_t ma = g1::UNKNOWN;
        if (arms[i]->Prop(g1::PROP_BATT_MA, &ma) && ma != g1::UNKNOWN) {
          sum[i] += int32_t(ma);
          n[i]++;
        }
      }
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(250));
  }
  g1::Arm::State ls = g1::left.GetState(), rs = g1::right.GetState();
  std::printf("%s: left %.2f mA (%d samples, %.2f ms link), right %.2f mA (%d samples, %.2f ms link)\n", label,
              n[0] ? sum[0] / n[0] : 0.0, n[0], ls.interval_us / 1000.0, n[1] ? sum[1] / n[1] : 0.0, n[1],
              rs.interval_us / 1000.0);
  g1::Event e{};
  for (g1::Arm *a : arms)
    while (a->NextEvent(&e, 0)) {
    }
}

int main(int argc, char **argv) {
  std::setvbuf(stdout, nullptr, _IOLBF, 0);
  if (argc < 3 || std::strlen(argv[1]) != 17 || std::strlen(argv[2]) != 17) {
    std::fprintf(stderr,
                 "usage: %s LEFT_ADDRESS RIGHT_ADDRESS [--updates N] [--power SECONDS] [--black-off] "
                 "[--log]\n",
                 argv[0]);
    return 2;
  }
  int updates = 30, power = 0;
  bool black_off = false;
  g1::Connection connection;
  for (int i = 3; i < argc; i++) {
    if (!std::strcmp(argv[i], "--updates") && i + 1 < argc) updates = std::atoi(argv[i + 1]);
    if (!std::strcmp(argv[i], "--power") && i + 1 < argc) power = std::atoi(argv[i + 1]);
    if (!std::strcmp(argv[i], "--black-off")) black_off = true;
    if (!std::strcmp(argv[i], "--log")) connection.log = true;
  }
  connection.left = argv[1];
  connection.right = argv[2];
  std::string error;
  int64_t t0 = g1::NowUs();
  g1::InitError e = g1::Init(nullptr, &error, &connection);
  std::printf("Init: %d (%s) after %.0f ms\n", int(e), error.c_str(), Ms(t0));
  if (e != g1::ERROR_NONE) return 1;
  PrintState("left", g1::left);
  PrintState("right", g1::right);

  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "", &error);
  sk::Font &title = sk::LoadFont(kDepartureMonoBlob, "ScouterKit V1 on the G1, both eyes", &error, 2);
  sk::Font &digits = sk::LoadFont(kDepartureMonoBlob, "0123456789:", &error, 4);
  Check(font.pimpl && title.pimpl && digits.pimpl, "fonts loaded");
  const int w = sk::gray::display.width, h = sk::gray::display.height;

  sk::gray::Bitmap &top = sk::gray::CreateBitmap(w, 40);
  title.DrawLine(top, "ScouterKit V1 on the G1, both eyes", w / 2, 30, sk::ALIGN_CENTER);
  sk::gray::Bitmap &seams = sk::gray::CreateBitmap(w, 24);
  for (int x : {0, 99, 100, 639, 640, w - 1}) sk::gray::Fill(seams, x, 0, 1, 24, 255);
  font.DrawLine(seams, "left only | both eyes from x=100 to x=639, this text crosses both seams | right only",
                w / 2, 16, sk::ALIGN_CENTER, nullptr, 200);
  std::vector<uint8_t> ramp(size_t(w) * 40);
  for (int y = 0; y < 40; y++)
    for (int x = 0; x < w; x++) ramp[size_t(y) * size_t(w) + size_t(x)] = uint8_t(x * 255 / (w - 1));
  sk::gray::Bitmap &gradient = sk::gray::LoadBitmap({ramp.data(), w, 40, w});
  sk::gray::Bitmap &clock = sk::gray::CreateBitmap(300, 70);
  sk::gray::Bitmap &across = sk::gray::CreateBitmap(w, 1);
  sk::gray::Bitmap &down = sk::gray::CreateBitmap(1, h);
  sk::gray::Fill(across, 0, 0, w, 1, 255);
  sk::gray::Fill(down, 0, 0, 1, h, 255);
  Check(top.pimpl && seams.pimpl && gradient.pimpl && clock.pimpl && across.pimpl && down.pimpl,
        "bitmaps created");
  sk::gray::Draw(across, 0, 0);
  sk::gray::Draw(across, 0, h - 1);
  sk::gray::Draw(down, 0, 0);
  sk::gray::Draw(down, w - 1, 0);
  sk::gray::Draw(top, 0, 10);
  sk::gray::Draw(seams, 0, 70);
  sk::gray::Draw(gradient, 0, 120);
  sk::gray::Draw(clock, (w - 300) / 2, 220);
  std::printf("memory used %d of %d bytes per arm\n", sk::total_memory - sk::free_memory,
              sk::total_memory);
  t0 = g1::NowUs();
  sk::Flush();
  Verify("test screen");
  std::printf("test screen uploaded, shown and read back in %.0f ms\n", Ms(t0));

  t0 = g1::NowUs();
  double worst = 0;
  for (int k = 0; k < updates; k++) {
    char text[16];
    std::snprintf(text, sizeof(text), "%02d:%02d", 12 + k / 60, k % 60);
    sk::gray::Fill(clock, 0, 0, 300, 70, 0);
    digits.DrawLine(clock, text, 150, 58, sk::ALIGN_CENTER);
    sk::gray::Draw(clock, (w - 300) / 2, 220);
    int64_t t = g1::NowUs();
    sk::Flush();
    worst = std::max(worst, Ms(t));
  }
  double total = Ms(t0);
  std::printf("%d clock updates in %.0f ms: %.1f ms each, longest Flush %.1f ms\n", updates, total,
              total / updates, worst);
  sk::Flush();
  uint32_t ignored;
  g1::left.ArenaCrc(0, 1, &ignored, &ignored);
  g1::right.ArenaCrc(0, 1, &ignored, &ignored);
  g1::Arm::State ls = g1::left.GetState(), rs = g1::right.GetState();
  if (ls.shown_update == rs.shown_update)
    std::printf("last update shown by both eyes %.2f ms apart (right minus left, clock error "
                "bound %d + %d us)\n",
                double(rs.shown_us - ls.shown_us) / 1000.0, ls.clock_error_us, rs.clock_error_us);
  Verify("after the clock updates");
  PrintState("left", g1::left);
  PrintState("right", g1::right);

  // Idle link after 5 s without traffic; the next update switches back.
  std::this_thread::sleep_for(std::chrono::seconds(8));
  PrintState("left, idle", g1::left);
  PrintState("right, idle", g1::right);
  uint64_t shown = g1::left.GetState().shown_update;
  sk::gray::Fill(clock, 0, 0, 300, 70, 0);
  digits.DrawLine(clock, "13:00", 150, 58, sk::ALIGN_CENTER);
  sk::gray::Draw(clock, (w - 300) / 2, 220);
  t0 = g1::NowUs();
  sk::Flush();
  while (Ms(t0) < 5000 && (g1::left.GetState().shown_update == shown || g1::right.GetState().shown_update == shown))
    std::this_thread::sleep_for(std::chrono::milliseconds(2));
  ls = g1::left.GetState();
  rs = g1::right.GetState();
  std::printf("update from the idle link shown after %.0f / %.0f ms (left / right), %.2f ms apart\n",
              double(ls.shown_us - t0) / 1000.0, double(rs.shown_us - t0) / 1000.0,
              double(rs.shown_us - ls.shown_us) / 1000.0);
  std::this_thread::sleep_for(std::chrono::seconds(2));
  PrintState("left, active again", g1::left);
  PrintState("right, active again", g1::right);
  Verify("after the idle link");
  if (power > 0) {
    Power("idle profile", power, false);
    Power("active profile, a NOTIFY every 250 ms", power, true);
  }
  if (black_off) {
    auto panels = [](uint32_t want) {
      uint32_t l = 9, r = 9;
      return g1::left.Prop(g1::PROP_PANEL, &l) && g1::right.Prop(g1::PROP_PANEL, &r) && l == want && r == want;
    };
    sk::gray::Clear();
    sk::Flush();
    std::this_thread::sleep_for(std::chrono::seconds(1));
    Check(panels(2), "all black: both panels in standby (PANEL 2)");
    std::this_thread::sleep_for(std::chrono::milliseconds(SCOUTER_G1_BLACK_OFF_MS + 2000));
    Check(panels(0), "still black " + std::to_string(SCOUTER_G1_BLACK_OFF_MS / 1000) + " s later: both panels off");
  }
  g1::Deinit();
  std::printf("%s (%d failures)\n", failures ? "FAILED" : "glasses check passed", failures);
  return failures ? 1 : 0;
}
