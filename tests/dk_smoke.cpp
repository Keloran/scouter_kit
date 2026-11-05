// Smoke test against one board (an nRF5340 DK running the scouter firmware)
// over BlueZ. The address is required, so it never looks for the glasses.
//   g++ -std=c++20 -O2 tests/dk_smoke.cpp src/base/scouter_kit.cpp src/g1/scouter_kit_g1.cpp src/vm/vm.c src/vm/vm_gfx.c
//       $(pkg-config --cflags --libs libsystemd) -lpthread -o dk_smoke
//   ./dk_smoke AA:BB:CC:DD:EE:FF [--left] [--updates N] [--drop] [--log]
// The board plays the right arm (--left: the left one). --drop disconnects it
// once with bluetoothctl during the run to test the reconnect. --log: the
// library logs to stderr.
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

#include "../src/base/departure_mono.inc"
#include "../src/g1/scouter_kit_g1.hpp"

namespace sk = scouter_kit;
namespace g1 = scouter_kit::g1;

static int failures = 0;

static void Check(bool ok, const char *what) {
  std::printf("%s %s\n", ok ? "ok  " : "FAIL", what);
  if (!ok) failures++;
}

static double Ms(int64_t from) { return double(g1::NowUs() - from) / 1000.0; }

static void PrintState(const char *label, g1::Arm &arm) {
  g1::Arm::State s = arm.GetState();
  std::printf("%s: connected %d, firmware %d.%d.%d, epoch %08x, max packet %d, interval %.2f ms, "
              "clock error %d us, packets %u, resent %u, reconnects %u, epoch changes %u, "
              "errors %u%s%s\n",
              label, s.connected, s.firmware_major, s.firmware_minor, s.firmware_patch, s.epoch,
              s.max_packet, s.interval_us / 1000.0, s.clock_error_us, s.packets, s.resent,
              s.reconnects, s.epoch_changes, s.errors, s.last_error.empty() ? "" : ", last: ",
              s.last_error.c_str());
}

int main(int argc, char **argv) {
  std::setvbuf(stdout, nullptr, _IOLBF, 0);
  if (argc < 2 || std::strlen(argv[1]) != 17) {
    std::fprintf(stderr, "usage: %s BOARD_ADDRESS [--left] [--updates N] [--drop] [--log]\n", argv[0]);
    return 2;
  }
  std::string address = argv[1];
  bool as_left = false, drop = false;
  int updates = 40;
  g1::Connection connection;
  for (int i = 2; i < argc; i++) {
    if (!std::strcmp(argv[i], "--left")) as_left = true;
    if (!std::strcmp(argv[i], "--drop")) drop = true;
    if (!std::strcmp(argv[i], "--log")) connection.log = true;
    if (!std::strcmp(argv[i], "--updates") && i + 1 < argc) updates = std::atoi(argv[++i]);
  }
  connection.left = as_left ? address : "none";
  connection.right = as_left ? "none" : address;
  connection.timeout_ms = 40000;
  g1::Arm &arm = as_left ? g1::left : g1::right;

  std::string error;
  int64_t t0 = g1::NowUs();
  g1::InitError e = g1::Init(nullptr, &error, &connection);
  std::printf("Init: %d (%s) after %.0f ms\n", int(e), error.c_str(), Ms(t0));
  if (e != g1::ERROR_NONE) return 1;
  PrintState("after Init", arm);

  t0 = g1::NowUs();
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "", &error);
  Check(font.pimpl != nullptr, "Departure Mono loaded (all glyphs)");
  sk::Font &big = sk::LoadFont(kDepartureMonoBlob, "0123456789:", &error, 4);
  Check(big.pimpl != nullptr, "digits loaded at scale 4");
  std::printf("fonts queued in %.1f ms, free memory %d of %d bytes\n", Ms(t0), sk::free_memory,
              sk::total_memory);

  sk::gray::Bitmap &line = sk::gray::CreateBitmap(720, 20);
  sk::gray::Bitmap &clock = sk::gray::CreateBitmap(240, 60);
  std::vector<uint8_t> ramp(256 * 48);
  for (int y = 0; y < 48; y++)
    for (int x = 0; x < 256; x++) ramp[size_t(y) * 256 + size_t(x)] = uint8_t(x);
  sk::gray::Bitmap &picture = sk::gray::LoadBitmap({ramp.data(), 256, 48, 256});
  Check(line.contents.data && clock.contents.data && picture.contents.data, "bitmaps created");
  font.DrawLine(line, "ScouterKit on the DK: one line of Departure Mono, glyph runs on the arm.", 4,
                14);
  sk::gray::Draw(line, 10, 60);
  sk::gray::Draw(picture, 240, 200);
  t0 = g1::NowUs();
  sk::Flush();
  sk::Flush();
  std::printf("first screen queued in %.1f ms\n", Ms(t0));

  int64_t at = arm.Alloc(256);
  arm.Fill(g1::Address(at), 16, 0, 0, 32, 16, 9);
  uint32_t arm_crc = 0, expected = 1;
  t0 = g1::NowUs();
  bool crc = arm.ArenaCrc(0, 0, &arm_crc, &expected);
  std::printf("arena CRC %08x, mirror %08x (%.0f ms until the arm had run everything)\n", arm_crc,
              expected, Ms(t0));
  Check(crc && arm_crc == expected, "the arm's whole arena equals the library's mirror");
  arm.Free(g1::Address(at));

  t0 = g1::NowUs();
  int64_t dropped_at = 0;
  for (int k = 0; k < updates; k++) {
    char text[32];
    std::snprintf(text, sizeof(text), "%02d:%02d", k / 60, k % 60);
    sk::gray::Fill(clock, 0, 0, 240, 60, 0);
    big.DrawLine(clock, text, 120, 50, sk::ALIGN_CENTER);
    sk::gray::Draw(clock, 250, 300);
    sk::Flush();
    if (drop && k == updates / 2) {
      std::string cmd = "bluetoothctl disconnect " + address + " > /dev/null";
      std::printf("disconnecting the board (%s)\n", system(cmd.c_str()) == 0 ? "ok" : "failed");
      dropped_at = g1::NowUs();
    }
  }
  double total = Ms(t0);
  std::printf("%d clock updates in %.0f ms (%.1f ms each, Flush waits for the previous one)\n",
              updates, total, total / updates);
  if (dropped_at) {
    for (int k = 0; k < 300 && !arm.GetState().connected; k++)
      std::this_thread::sleep_for(std::chrono::milliseconds(50));
    Check(arm.GetState().connected, "reconnected after the drop");
  }
  crc = arm.ArenaCrc(0, 0, &arm_crc, &expected);
  Check(crc && arm_crc == expected, "arena still equals the mirror after the updates");

  int mv = 0, percent = 0;
  bool charging = false;
  bool battery = arm.Battery(&mv, &percent, &charging);
  std::printf("battery: %s (%d mV, %d %%)\n", battery ? "answered" : "no answer", mv, percent);
  uint32_t panel_crc = 0;
  bool panel = arm.PanelCrc(0, 480, &panel_crc, &expected);
  std::printf("panel read-back: %s\n", panel ? (panel_crc == expected ? "equal" : "DIFFERENT")
                                             : "refused (a board without a panel)");
  PrintState("at the end", arm);
  sk::UnloadAll();
  Check(sk::free_memory == sk::total_memory, "all memory free after UnloadAll");
  g1::Deinit();
  std::printf("%s (%d failures)\n", failures ? "FAILED" : "smoke test passed", failures);
  return failures ? 1 : 0;
}
