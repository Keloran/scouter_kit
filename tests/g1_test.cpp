// Tests without hardware: the byte code against VM.md, the library's host
// code and mirror, and the whole library against the emulated arms (which run
// the arms' interpreter, src/vm).
//   g++ -std=c++20 -O2 -DSCOUTER_G1_EMULATOR tests/g1_test.cpp src/base/scouter_kit.cpp
//       src/vm/vm.c src/vm/vm_gfx.c -o g1_test -lpthread
#ifndef SCOUTER_G1_EMULATOR
#error "build the tests with -DSCOUTER_G1_EMULATOR"
#endif

#include "../src/g1/scouter_kit_g1.cpp" // the internals are tested too

#include <dirent.h>

#include <atomic>
#include <cstdio>
#include <thread>
#include <vector>

#include "../src/base/departure_mono.inc"

namespace sk = scouter_kit;
namespace g1 = scouter_kit::g1;
using g1::R;

static int failures = 0;

#define CHECK(cond)                                                       \
  do {                                                                    \
    if (!(cond)) {                                                        \
      std::printf("FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);         \
      failures++;                                                         \
    }                                                                     \
  } while (0)

static std::vector<uint8_t> Hex(const char *text) {
  std::vector<uint8_t> out;
  for (const char *p = text; *p;) {
    if (*p == ' ') {
      p++;
      continue;
    }
    out.push_back(uint8_t(std::strtoul(std::string(p, 2).c_str(), nullptr, 16)));
    p += 2;
  }
  return out;
}

static std::vector<uint8_t> Bytes(const g1::Cmd &c) {
  return std::vector<uint8_t>(c.b.begin(), c.b.begin() + c.size);
}

static void Sleep(int ms) { std::this_thread::sleep_for(std::chrono::milliseconds(ms)); }

// ---------------------------------------------------------------- VM.md §9

// VM.md §9.1: word 32 + j holds the j-th value.
static const uint32_t kSpecPool[27] = {0,   1,   2,   3,   4,   5,   8,    10,    12,    15,     17,     20,      24, 32,
                                       34,  40,  60,  80,  100, 160, 240,  256,   560,   1440,   16384,  153600, 60000000};

static const char *kClockBytes =
    "10 ff 44 00 42 00 10 ff 45 00 43 00 30 2b c0 00 11 ff f0 44 00 44 00 21 00 15 ff f0 44 00 44 00 37 00 "
    "11 ff f0 45 00 45 00 3a 00 14 4f f0 44 00 30 00 15 5f f0 44 00 30 00 14 64 f0 27 00 26 ff 60 35 00 20 00 "
    "15 64 f0 27 00 26 ff 60 35 00 21 00 14 65 f0 27 00 26 ff 60 35 00 23 00 15 65 f0 27 00 26 ff 60 35 00 24 00 "
    "50 ff ff ff f0 38 00 31 00 20 00 20 00 33 00 2d 00 20 00 52 ff ff ff ff f0 39 00 38 00 31 00 24 00 2c 00 "
    "22 00 29 00 35 00 25 00 53 ff ff ff ff 38 00 31 00 20 00 20 00 34 00 26 00 33 00 2d 00 45 ff 2e 00 45 00 33";

static const char *kBatteryBytes =
    "44 0f 2a 00 37 f0 70 c0 00 32 00 10 1f 23 00 12 11 f0 21 00 15 20 f0 27 00 26 f1 20 35 00 14 00 f0 27 00 "
    "36 0f 0f c0 00 20 00 11 31 f0 35 00 12 4f 10 24 00 50 ff ff ff f0 38 00 2f 00 20 00 20 00 31 00 2c 00 20 00 "
    "52 ff ff ff f3 40 39 00 38 00 2f 00 22 00 2b 00 22 00 29 00 53 ff ff ff ff 38 00 2f 00 20 00 20 00 36 00 "
    "26 00 31 00 2c 00 33";

static g1::Program SpecLayout() {
  g1::Program p(0x40, 0x80, 32, 0xC000);
  for (uint32_t v : kSpecPool) p.Const(v);
  return p;
}

// VM.md §9.2: HH:MM in a 160 x 32 band at panel (240, 8); a tick per minute
// (or per `tick_us`).
static void BuildClock(g1::Program &p, uint32_t tick_us = 60000000) {
  auto C = [&](uint32_t v) { return p.Const(v); };
  const g1::ArenaWord M = 68, N = 69, M2 = 66, N2 = 67;
  auto sync = p.NewLabel(), tick = p.NewLabel(), show = p.NewLabel();
  p.Bind(sync);
  p.Mov(M, M2);
  p.Mov(N, N2);
  p.Jmp(show);
  p.Bind(tick);
  p.Add(M, M, C(1));
  p.ModU(M, M, C(1440));
  p.Add(N, N, C(tick_us));
  p.Bind(show);
  p.DivU(R(4), M, C(60));
  p.ModU(R(5), M, C(60));
  p.DivU(R(6), R(4), C(10));
  p.Store(8, C(256), C(0), R(6));
  p.ModU(R(6), R(4), C(10));
  p.Store(8, C(256), C(1), R(6));
  p.DivU(R(6), R(5), C(10));
  p.Store(8, C(256), C(3), R(6));
  p.ModU(R(6), R(5), C(10));
  p.Store(8, C(256), C(4), R(6));
  p.Fill(C(16384), C(80), C(0), C(0), C(160), C(32), C(0));
  p.Glyphs(C(153600), C(16384), C(80), C(4), C(24), C(2), C(15), C(256), C(5));
  p.Draw(C(16384), C(80), C(0), C(0), C(240), C(8), C(160), C(32));
  p.SysWrite(C(34), N);
  p.End();
  p.On(g1::TRIGGER_START, sync);
  p.On(g1::TRIGGER_TIMER, tick);
  p.On(g1::TRIGGER_USER0, sync);
  p.Changes(0x100, 0x18);      // text, mailbox, state
  p.Changes(0x4000, 80 * 32);  // the band
}

// VM.md §9.3: the battery percentage in an 80 x 24 band at panel (560, 8).
static void BuildBattery(g1::Program &p) {
  auto C = [&](uint32_t v) { return p.Const(v); };
  auto entry = p.NewLabel(), next = p.NewLabel(), done = p.NewLabel();
  p.Bind(entry);
  p.SysRead(R(0), C(17));
  p.BltU(C(100), R(0), done);
  p.Mov(R(1), C(3));
  p.Bind(next);
  p.Sub(R(1), R(1), C(1));
  p.ModU(R(2), R(0), C(10));
  p.Store(8, C(256), R(1), R(2));
  p.DivU(R(0), R(0), C(10));
  p.Bne(R(0), C(0), next);
  p.Add(R(3), R(1), C(256));
  p.Sub(R(4), C(4), R(1));
  p.Fill(C(16384), C(40), C(0), C(0), C(80), C(24), C(0));
  p.Glyphs(C(153600), C(16384), C(40), C(2), C(20), C(2), C(15), R(3), R(4));
  p.Draw(C(16384), C(40), C(0), C(0), C(560), C(8), C(80), C(24));
  p.Bind(done);
  p.End();
  p.On(g1::TRIGGER_START, entry);
  p.On(g1::TRIGGER_BATTERY, entry);
  p.Changes(0x100, 4);
  p.Changes(0x4000, 40 * 24);
}

static std::vector<uint8_t> Words(std::initializer_list<uint32_t> values) {
  std::vector<uint8_t> out;
  for (uint32_t v : values) g1::Put32(out, v);
  return out;
}

static void TestEncoding() {
  g1::Program e(0x1000);
  e.Add(R(3), R(1), 0x0120);
  CHECK(e.code == Hex("11 31 f0 20 01")); // VM.md §2.1
  e.code.clear();
  e.Mov(15, R(1));                         // word 15 is no register: an escape
  e.SleepUntil(R(3));                      // one operand: a 0 padding nibble
  e.Notify(R(1), 300);
  e.Load(4, R(2), R(9), R(10));
  e.Store(16, 0xFFFF, R(0), R(14));
  CHECK(e.code == Hex("10 f1 0f 00 40 30 43 1f 2c 01 23 29 a0 25 f0 e0 ff ff"));
  e.code.clear();
  auto back = e.NewLabel(), ahead = e.NewLabel();
  e.Bind(back);
  e.BgeS(R(1), 0x40, ahead);
  e.Jmp(back);
  e.Call(e.At(0x12345));
  e.Bind(ahead);
  e.Ret();
  CHECK(e.Valid());
  CHECK(e.code == Hex("3a 1f 4f 11 00 40 00 30 40 11 00 31 45 23 01 32"));
  CHECK(e.table_address == 0x1000 && e.pool_address == 0x1040 && e.code_address == 0x1140);

  // Every other instruction: opcode, nibbles, target.
  g1::Program a(0x1000);
  auto l = a.At(0x01C0DE);
  a.Copy(R(1), R(2), R(3));
  a.Unpack(R(1), R(2), R(3), R(4));
  a.Mul(R(1), R(2), R(3));
  a.DivS(R(1), R(2), R(3));
  a.ModS(R(1), R(2), R(3));
  a.And(R(1), R(2), R(3));
  a.Or(R(1), R(2), R(3));
  a.Xor(R(1), R(2), R(3));
  a.Shl(R(1), R(2), R(3));
  a.Shr(R(1), R(2), R(3));
  a.Sar(R(1), R(2), R(3));
  a.Load(16, R(1), R(2), R(3));
  a.Load(8, R(1), R(2), R(3));
  a.Load(32, R(1), R(2), R(3));
  a.Store(32, R(1), R(2), R(3));
  a.Store(8, R(1), R(2), R(3));
  a.Store(4, R(1), R(2), R(3));
  a.Beq(R(1), R(2), l);
  a.Bne(R(1), R(2), l);
  a.BltU(R(1), R(2), l);
  a.BgeU(R(1), R(2), l);
  a.BltS(R(1), R(2), l);
  a.End();
  a.Halt();
  a.WaitEvent(R(1), R(2));
  a.SysRead(R(1), R(2));
  a.Blit(R(1), R(2), R(3), R(4), R(5), R(6), R(7), R(8), R(9), R(10), R(11), R(12));
  a.Line(R(1), R(2), R(3), R(4), R(5), R(6), R(7));
  a.Fill(R(1), R(2), R(3), R(4), R(5), R(6), R(7));
  a.Glyphs(R(1), R(2), R(3), R(4), R(5), R(6), R(7), R(8), R(9));
  a.Draw(R(1), R(2), R(3), R(4), R(5), R(6), R(7), R(8));
  uint8_t packed[2] = {0xFB, 7};
  a.WriteRLE(0x23456, 6, packed);
  CHECK(a.Valid());
  CHECK(a.code == Hex("03 12 30 04 12 34 13 12 30 16 12 30 17 12 30 18 12 30 19 12 30 1a 12 30 1b 12 30 1c 12 30 "
                      "1d 12 30 21 12 30 22 12 30 20 12 30 24 12 30 26 12 30 27 12 30 35 12 de c0 01 36 12 de c0 01 "
                      "37 12 de c0 01 38 12 de c0 01 39 12 de c0 01 33 34 41 12 44 12 51 12 34 56 78 9a bc "
                      "54 12 34 56 70 50 12 34 56 70 52 12 34 56 78 90 53 12 34 56 78 02 56 34 02 06 00 02 fb 07"));

  g1::Program bad(0x2000);
  auto missing = bad.NewLabel();
  bad.Jmp(missing);
  std::string error;
  CHECK(!bad.Valid(&error) && error.find("not bound") != std::string::npos);
  g1::Program bad2(0x2000, 1);
  bad2.Const(1);
  bad2.Const(2);
  bad2.Load(12, R(0), R(1), R(2));
  CHECK(!bad2.Valid(&error) && error == "the pool is full");
  g1::Program bad3(0x2000);
  bad3.Op(0x99, {});
  bad3.Op(g1::OP_ADD, {R(1)});
  CHECK(!bad3.Valid(&error));

  // The library's host code: a present's head and tail.
  CHECK(Bytes(g1::PresentHead(0x11223344)) ==
        Hex("01 bc df 03 04 44 33 22 11 40 f0 ef f7"));
  CHECK(Bytes(g1::PresentTail()) == Hex("43 ff ef f7 ef f7 01 bc df 03 04 00 00 00 00"));
}

static void TestSpecPrograms() {
  g1::Program clock = SpecLayout();
  BuildClock(clock);
  std::string error;
  CHECK(clock.Valid(&error));
  CHECK(clock.code == Hex(kClockBytes));
  CHECK(clock.pool.size() == 27);
  std::array<g1::Address, 16> table{};
  table[0] = table[12] = 0xC000;
  table[1] = 0xC010;
  CHECK(clock.table == table);

  std::vector<uint8_t> pool, write;
  for (uint32_t v : clock.pool) g1::Put32(pool, v);
  g1::PutWrite(write, clock.pool_address, pool.data(), pool.size());
  CHECK(std::vector<uint8_t>(write.begin(), write.begin() + 5) == Hex("01 80 00 00 6c"));
  write.clear();
  std::vector<uint8_t> t;
  for (g1::Address a : clock.table) g1::Put32(t, a);
  g1::PutWrite(write, clock.table_address, t.data(), t.size());
  CHECK(std::vector<uint8_t>(write.begin(), write.begin() + 5) == Hex("01 40 00 00 40"));

  g1::Program raise = SpecLayout();
  raise.Raise(raise.Const(12));
  CHECK(raise.code == Hex("42 f0 28 00"));
  write.clear();
  uint8_t text[5] = {0, 0, 64, 0, 0};
  g1::PutWrite(write, 0x100, text, 5);
  CHECK(write == Hex("01 00 01 00 05 00 00 40 00 00"));

  g1::Program battery = SpecLayout();
  BuildBattery(battery);
  CHECK(battery.Valid(&error));
  CHECK(battery.code == Hex(kBatteryBytes));
  table = {};
  table[0] = table[5] = 0xC000;
  CHECK(battery.table == table);
}

// ---------------------------------------------------------------- end to end

static int Level(uint8_t v) { return (v * 15 + 127) / 255; }

// Every panel pixel is what the display says for that eye (black outside).
static bool PanelsMatch(const char *label) {
  bool ok = true;
  const auto &d = sk::gray::display;
  for (int arm = 0; arm < 2; arm++) {
    const uint8_t *p = g1::emulator::Panel(arm);
    int ox = g1::g->geo.ox[arm], oy = g1::g->geo.oy[arm], bad = 0;
    for (int y = 0; y < 480; y++)
      for (int x = 0; x < 640; x++) {
        int vy = y - oy;
        int want = vy >= 0 && vy < d.height ? Level(d(x + ox, vy)) : 0;
        if (p[y * 640 + x] != want && bad++ < 3)
          std::printf("  %s: arm %d pixel (%d, %d) is %d, expected %d\n", label, arm, x, y,
                      p[y * 640 + x], want);
      }
    if (bad) ok = false;
  }
  if (!ok) std::printf("panels differ: %s\n", label);
  return ok;
}

// Waits until both arms are connected, have run everything and the last
// present is on their panels.
static void WaitShown() {
  auto settled = [] {
    g1::Core *core = g1::g->core.get();
    std::lock_guard<std::mutex> lock(core->mutex);
    for (int i = 0; i < 2; i++)
      if (core->links[i].present && !core->Settled(i, core->links[i].emitted)) return false;
    return core->Presented() >= core->present_counter.load();
  };
  for (int k = 0; k < 1000 && !settled(); k++) Sleep(10);
  CHECK(settled());
}

static bool ArenasMatch() {
  bool ok = true;
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    uint32_t arm = 0, expected = 1;
    ok &= a->ArenaCrc(0, 0, &arm, &expected) && arm == expected;
  }
  return ok;
}

static void TestEndToEnd() {
  std::string error;
  g1::GrayConfig config;
  CHECK(g1::Init(&config, &error) == g1::ERROR_NONE);
  if (!error.empty()) std::printf("init: %s\n", error.c_str());
  CHECK(sk::gray::display.width == 740 && sk::gray::display.height == 471);
  CHECK(sk::total_memory == int(g1::kHostTop - g1::kHostBottom) && sk::free_memory == sk::total_memory);
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    g1::Arm::State s = a->GetState();
    uint16_t triggers = a == &g1::right ? 0xF073 : 0xF063; // the right arm's light sensor
    CHECK(s.connected && s.firmware_minor == 5 && s.triggers == triggers && !s.microphone && !s.panel_on);
    uint32_t crc = 0;
    CHECK(!a->PanelCrc(0, 480, &crc, &crc)); // the panel is off: nothing to read back
  }

  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "", &error);
  CHECK(font.pimpl != nullptr && error.empty());
  CHECK(font.ascent == 11 && font.descent == 3 && font.line_spacing == 14);
  int w = 0, h = 0, asc = 0, desc = 0;
  font.Measure("Hello", &w, &h, &asc, &desc);
  CHECK(w == 35 && asc >= 8 && desc == 0 && h == asc + desc);
  sk::Font &big = sk::LoadFont(kDepartureMonoBlob, "0123456789:", &error, 3);
  CHECK(big.pimpl != nullptr && big.ascent == 33);
  CHECK(sk::free_memory < sk::total_memory);

  sk::gray::Bitmap &line = sk::gray::CreateBitmap(720, 20);
  CHECK(line.contents.data != nullptr);
  int width = 0;
  std::string text = "Hello, world! Text across both seams of the virtual display.";
  font.DrawLine(line, text, 4, 14, sk::ALIGN_LEFT, &width);
  CHECK(width == 7 * int(text.size()));
  sk::gray::Draw(line, 10, 100);
  sk::gray::Bitmap &clock = sk::gray::CreateBitmap(200, 40);
  big.DrawLine(clock, "12:34", 100, 34, sk::ALIGN_CENTER, nullptr, 180);
  sk::gray::Draw(clock, 520, 300);
  sk::Flush();
  WaitShown();
  CHECK(PanelsMatch("text"));
  CHECK(ArenasMatch());
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    uint32_t arm_crc = 0, expected = 1;
    CHECK(a->PanelCrc(0, 480, &arm_crc, &expected) && arm_crc == expected);
    CHECK(a->GetState().panel_on && a->GetState().shown_update == 1);
  }

  std::vector<uint8_t> ramp(300 * 60);
  for (int y = 0; y < 60; y++)
    for (int x = 0; x < 300; x++) ramp[size_t(y) * 300 + size_t(x)] = uint8_t(x * 255 / 299);
  sk::gray::Bitmap &picture = sk::gray::LoadBitmap({ramp.data(), 300, 60, 300});
  CHECK(picture.contents.data != nullptr);
  int levels_used = 0;
  bool seen[256] = {};
  for (int x = 0; x < 300; x++) seen[picture.contents(x, 30)] = true;
  for (bool s : seen) levels_used += s;
  CHECK(levels_used == 16);
  sk::gray::Draw(picture, 220, 180);
  for (int x = 0; x < 50; x++) clock.contents(x, 5) = 255;
  sk::gray::Draw(clock, 520, 300);
  sk::Flush();
  WaitShown();
  CHECK(PanelsMatch("picture and pixels"));
  CHECK(ArenasMatch());

  sk::gray::Fill(line, 0, 0, 720, 20, 0);
  font.DrawLine(line, "second line, same place", 4, 14);
  sk::gray::Draw(line, 10, 100);
  sk::Flush();
  WaitShown();
  CHECK(PanelsMatch("redraw"));

  g1::emulator::SetLoss(0.15, 0.15);
  for (int k = 0; k < 12; k++) {
    sk::gray::Fill(line, 0, 0, 720, 20, 0);
    font.DrawLine(line, "lossy link " + std::to_string(k), 4 + 30 * k, 14);
    sk::gray::Draw(line, 10, 100 + 20 * (k % 6));
    sk::Flush();
  }
  WaitShown();
  g1::emulator::SetLoss(0, 0);
  CHECK(PanelsMatch("loss"));
  CHECK(ArenasMatch());
  CHECK(g1::left.GetState().resent + g1::right.GetState().resent > 0);

  std::vector<uint8_t> noise(400 * 200);
  std::mt19937 rng(3);
  for (auto &p : noise) p = uint8_t(rng());
  sk::gray::Bitmap &busy = sk::gray::LoadBitmap({noise.data(), 400, 200, 400}, sk::gray::DITHER_NONE);
  sk::gray::Draw(busy, 300, 40);
  sk::Flush();
  Sleep(40);
  g1::emulator::Disconnect(1, 400);
  Sleep(50);
  WaitShown();
  CHECK(PanelsMatch("link drop during an upload"));
  CHECK(ArenasMatch());
  CHECK(g1::right.GetState().reconnects == 1 && g1::right.GetState().epoch_changes == 0);

  // PanelCrc waits for the upload, through a link drop, before it reads back.
  for (auto &p : noise) p = uint8_t(rng());
  sk::gray::Write(busy, {noise.data(), 400, 200, 400}, 0, 0, sk::gray::DITHER_NONE);
  sk::Flush();
  Sleep(20);
  g1::emulator::Disconnect(1, 1500);
  uint32_t got = 0, want = 1;
  CHECK(g1::right.PanelCrc(0, 480, &got, &want) && got == want);
  CHECK(g1::right.GetState().interval_us == 7500 && !g1::right.GetState().idle);

  g1::emulator::Reboot(0);
  Sleep(100);
  font.DrawLine(line, " after reboot", 200, 14);
  sk::gray::Draw(line, 10, 400);
  sk::Flush();
  WaitShown();
  CHECK(g1::left.GetState().epoch_changes == 1);
  CHECK(PanelsMatch("reboot"));
  CHECK(ArenasMatch());
  g1::Event e{};
  CHECK(g1::left.NextEvent(&e, 1000) && e.type == g1::EVENT_RESET);

  // The left arm reboots while the right one's link is down, in an upload: the
  // right arm must not wait for the left's lost queue.
  for (auto &p : noise) p = uint8_t(rng());
  sk::gray::Write(busy, {noise.data(), 400, 200, 400}, 0, 0, sk::gray::DITHER_NONE);
  sk::Flush();
  Sleep(20);
  g1::emulator::Disconnect(1, 1500);
  Sleep(100);
  g1::emulator::Reboot(0);
  font.DrawLine(line, " reboot and drop", 300, 14);
  sk::gray::Draw(line, 10, 420);
  sk::Flush();
  WaitShown();
  CHECK(g1::left.GetState().epoch_changes == 2);
  CHECK(PanelsMatch("reboot during a link drop"));
  CHECK(ArenasMatch());

  uint32_t before = g1::left.GetState().errors;
  g1::left.Fill(0x30000, 4000, 0, 0, 100, 100, 16);
  g1::Arm::State s = g1::left.GetState();
  CHECK(s.errors == before + 1 && s.last_error.find("FILL") != std::string::npos);
  // Host code may draw over the library's own memory (destinations clip at
  // the arena's end): its constants and staging rows recover.
  g1::left.Fill(g1::kPool, 4, 0, 0, 8, 64, 3);
  g1::left.Fill(g1::kStaging, 320, 0, 0, 640, 25, 6);
  sk::gray::Clear();
  sk::gray::Draw(line, 10, 100);
  sk::Flush();
  WaitShown();
  CHECK(PanelsMatch("after host code over the library's memory"));
  g1::left.WriteArena(g1::kPool, std::vector<uint8_t>(4, 1));
  CHECK(g1::left.GetState().errors == before + 2);
  int64_t at = g1::Alloc(64);
  CHECK(at >= 0 && at % 4 == 0);
  g1::left.Fill(g1::Address(at), 8, 0, 0, 16, 8, 5);
  g1::left.Line(g1::Address(at), 8, 0, 0, 15, 7, 9);
  g1::Free(g1::Address(at));
  CHECK(g1::left.GetState().errors == before + 2);
  int mv = 0, percent = 0;
  bool charging = true;
  CHECK(g1::right.Battery(&mv, &percent, &charging) && mv == 3950 && percent == 72 && !charging);
  CHECK(g1::right.SetBrightness(30) && !g1::right.SetBrightness(43));
  uint32_t brightness = 0;
  CHECK(g1::right.PanelCrc(0, 1, &got, &want) && g1::right.Prop(g1::PROP_BRIGHTNESS, &brightness) &&
        brightness == 30);

  sk::gray::Clear();
  sk::Flush();
  WaitShown();
  CHECK(PanelsMatch("clear"));
  for (g1::Arm *a : {&g1::left, &g1::right}) { // all black: standby, not a lit black panel
    uint32_t panel = 0;
    CHECK(a->Prop(g1::PROP_PANEL, &panel) && panel == 2 && !a->GetState().panel_on);
  }
  sk::gray::Draw(line, 10, 100);
  sk::Flush();
  WaitShown();
  CHECK(PanelsMatch("from standby"));
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    uint32_t panel = 0;
    CHECK(a->Prop(g1::PROP_PANEL, &panel) && panel == 1 && a->GetState().panel_on);
  }
  sk::gray::Clear();
  sk::Flush();
  WaitShown();
  CHECK(PanelsMatch("clear again"));
  bool black = true;
  for (int i = 0; i < 740 * 471; i++) black &= sk::gray::display.data[i] == 0;
  CHECK(black);
  CHECK(ArenasMatch());

  sk::UnloadAll();
  CHECK(sk::free_memory == sk::total_memory);
  g1::Deinit();
  CHECK(sk::gray::display.data == nullptr);
}

// The host-code calls not used by the gray display.
static void TestHostCalls() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  int64_t at = g1::Alloc(1024);
  g1::Address base = g1::Address(at);
  std::vector<uint8_t> plain(300), packed;
  for (size_t i = 0; i < plain.size(); i++) plain[i] = uint8_t(i < 200 ? 0x11 : i);
  g1::PackBits(plain.data(), int(plain.size()), packed);
  g1::left.WriteArena(base, packed);
  g1::left.Unpack(base + 512, base, uint32_t(packed.size()), uint32_t(plain.size()));
  std::vector<uint8_t> back(plain.size());
  CHECK(g1::left.ReadArena(base + 512, back) && back == plain);

  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "", &error);
  std::vector<uint32_t> glyphs = g1::GlyphIndices(font, "Hi");
  uint8_t varints[2] = {uint8_t(glyphs[0]), uint8_t(glyphs[1])};
  g1::left.Fill(base, 16, 0, 0, 32, 16, 0);
  g1::left.GlyphRunCompressed(g1::AddressOf(font), base, 16, 1, 12, g1::MODE_MASK, varints, 9);
  uint32_t got = 0, want = 1;
  CHECK(g1::left.ArenaCrc(base, 256, &got, &want) && got == want);
  int ink = 0;
  for (uint32_t i = 0; i < 256; i++) ink += g1::ArmBytes(0)[base + i] != 0;
  CHECK(ink > 10);

  g1::Event e{};
  while (g1::left.NextEvent(&e, 0)) {
  }
  int64_t t0 = g1::NowUs();
  g1::left.WaitUntil(g1::left.DeviceTime(300000));
  g1::left.Fence(0x7E57);
  CHECK(g1::left.NextEvent(&e, 2000) && e.type == g1::EVENT_NOTIFY && e.a == 0x7E57 && e.b == 0 &&
        e.trigger == 0xFF && e.host_us - t0 >= 290000);

  g1::left.SetPanel(true);
  CHECK(g1::left.PanelCrc(0, 480, &got, &want) && got == want && g1::left.GetState().panel_on);
  g1::left.SetPanel(false);
  CHECK(!g1::left.PanelCrc(0, 480, &got, &want) && !g1::left.GetState().panel_on);
  CHECK(ArenasMatch());
  g1::Deinit();
}

// The pool: one word per value, written once, and rewritten when it changed.
static void TestPool() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  {
    std::lock_guard<std::mutex> lock(sk::device::Lock());
    uint32_t a[3] = {7, 9, 7};
    g1::ArenaWord w[3];
    g1::Words(0, a, 3, w);
    CHECK(w[0] == w[2] && w[0] != w[1] && w[0] >= g1::kPool / 4);
    CHECK(g1::Get32(g1::ArmBytes(0) + 4 * w[1]) == 9);
    g1::ArenaWord v[3];
    int next = g1::g->arms[0].pool.next;
    g1::Words(0, a, 3, v);
    CHECK(v[0] == w[0] && v[1] == w[1] && g1::g->arms[0].pool.next == next);
    // A full ring of other values: the value comes back, in another word, and
    // never in a word that the same instruction uses.
    for (uint32_t k = 0; k < g1::kPoolWords; k++) {
      uint32_t b[2] = {1000 + k, 7};
      g1::ArenaWord x[2];
      g1::Words(0, b, 2, x);
      CHECK(x[0] != x[1] && g1::Get32(g1::ArmBytes(0) + 4 * x[1]) == 7);
    }
  }
  CHECK(ArenasMatch());
  g1::Deinit();
}

static int64_t g_arena_base = -1;

// The spec's programs at the spec's addresses (a block taken from the
// library's allocator), on both emulated arms.
static void LoadSpecFont(sk::Font &font) {
  std::span<const uint8_t> blob = sk::device::FontBlob(font);
  for (g1::Arm *a : {&g1::left, &g1::right}) a->WriteArena(0x25800, blob);
}

// What the clock band should show: the text drawn by the host library.
static bool BandShows(int arm, sk::Font &font, const char *text, int x0, int y0, int w, int h, int pen_x,
                      int baseline) {
  sk::gray::Bitmap &band = sk::gray::CreateBitmap(w, h);
  font.DrawLine(band, text, pen_x, baseline);
  const uint8_t *p = g1::emulator::Panel(arm);
  int bad = 0, ink = 0;
  for (int y = 0; y < h; y++)
    for (int x = 0; x < w; x++) {
      int want = Level(band.contents(x, y));
      ink += want != 0;
      if (p[(y0 + y) * 640 + x0 + x] != want) bad++;
    }
  sk::gray::Unload(band);
  if (bad) std::printf("  band on arm %d: %d pixels differ from \"%s\"\n", arm, bad, text);
  return bad == 0 && ink > 0;
}

static void Mailbox(g1::Arm &arm, uint32_t minute, uint32_t at) {
  arm.WriteArena(0x108, Words({minute, at}));
}

static void TestPrograms() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  g_arena_base = g1::Alloc(0x25800 - 64 + 4096);
  CHECK(g_arena_base == 64);
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "0123456789%:", &error, 2);
  CHECK(font.pimpl != nullptr);
  CHECK(g1::GlyphIndices(font, "07:%") == std::vector<uint32_t>({0, 7, 64, 63}));
  CHECK(g1::GlyphIndices(font, "a 9") == std::vector<uint32_t>({62, 9})); // no 'a', no U+FFFD
  LoadSpecFont(font);
  uint8_t text[5] = {0, 0, 64, 0, 0};

  // The clock, ticking every second here.
  g1::Program clock = SpecLayout();
  BuildClock(clock, 1000000);
  CHECK(clock.Valid());
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    a->WriteArena(0x100, text);
    Mailbox(*a, 12 * 60 + 34, a->DeviceTime(1500000));
    CHECK(a->Load(clock));
  }
  CHECK(g1::left.Start(0x40) && g1::right.Start(0x40));
  Sleep(300);
  for (int arm = 0; arm < 2; arm++) CHECK(BandShows(arm, font, "12:34", 240, 8, 160, 32, 4, 24));
  g1::VmStatus st{};
  CHECK(g1::left.Status(&st) && st.state == 1 && st.table == 0x40 && st.timer_armed && st.tasks == 1);
  Sleep(1500);
  for (int arm = 0; arm < 2; arm++) CHECK(BandShows(arm, font, "12:35", 240, 8, 160, 32, 4, 24));
  uint8_t digits[5] = {};
  CHECK(g1::right.ReadArena(0x100, digits) && digits[3] == 3 && digits[4] == 5);

  // Correction: the mailbox, then USER0, in one stream (VM.md §9.2).
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    Mailbox(*a, 23 * 60 + 59, a->DeviceTime(800000));
    a->Raise(g1::TRIGGER_USER0);
  }
  Sleep(400);
  for (int arm = 0; arm < 2; arm++) CHECK(BandShows(arm, font, "23:59", 240, 8, 160, 32, 4, 24));
  Sleep(900);
  for (int arm = 0; arm < 2; arm++) CHECK(BandShows(arm, font, "00:00", 240, 8, 160, 32, 4, 24));

  // What the program changes is left out of the mirror's checks.
  uint32_t got = 0, want = 0;
  CHECK(!g1::left.ArenaCrc(0x100, 0x18, &got, &want) && !g1::left.ArenaCrc(0, 0, &got, &want));
  CHECK(g1::left.ArenaCrc(0xC000, uint32_t(clock.code.size()), &got, &want) && got == want);
  CHECK(g1::left.ArenaCrc(0x25800, 4096, &got, &want) && got == want);

  // The arm runs on with the link down, and the session goes on.
  g1::left.Disconnect(2500);
  Sleep(400);
  CHECK(!g1::left.GetState().connected);
  Sleep(2600);
  CHECK(g1::left.PanelCrc(8, 1, &got, &want) && g1::left.GetState().reconnects == 1 &&
        g1::left.GetState().epoch_changes == 0);
  CHECK(g1::left.ReadArena(0x100, digits) && digits[2] == 64 && digits[0] == 0 && digits[1] == 0 &&
        digits[4] >= 2);
  char shown[6] = {char('0' + digits[0]), char('0' + digits[1]), ':', char('0' + digits[3]),
                   char('0' + digits[4]), 0};
  CHECK(BandShows(0, font, shown, 240, 8, 160, 32, 4, 24));

  CHECK(g1::left.Stop() && g1::right.Stop());
  CHECK(g1::left.Status(&st) && st.state == 0);

  // The battery, on START and on BATTERY (the 1 % hysteresis of VM.md §2.5).
  g1::Program battery = SpecLayout();
  BuildBattery(battery);
  uint8_t pct[4] = {0, 0, 0, 63};
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    a->WriteArena(0x100, pct);
    CHECK(a->Load(battery));
    CHECK(a->Start(0x40));
  }
  Sleep(200);
  for (int arm = 0; arm < 2; arm++) CHECK(BandShows(arm, font, "72%", 560, 8, 80, 24, 2, 20));
  g1::emulator::SetProperty(0, g1::PROP_BATT_PCT, 75);
  Sleep(200);
  CHECK(BandShows(0, font, "75%", 560, 8, 80, 24, 2, 20));
  uint16_t inputs = 0;
  CHECK(g1::left.ReportInputs(0x0021, &inputs) && inputs == 0x0021);
  CHECK(g1::left.ReportInputs(0x0F21, &inputs) && inputs == 0x0021); // the request ∧ HELLO.triggers
  g1::emulator::SetProperty(0, g1::PROP_BATT_PCT, 74);
  Sleep(1500);
  uint32_t value = 0;
  CHECK(g1::left.Prop(g1::PROP_BATT_PCT, &value) && value == 75);
  CHECK(BandShows(0, font, "75%", 560, 8, 80, 24, 2, 20));
  std::printf("waiting 30 s for the battery hysteresis\n");
  Sleep(29500);
  CHECK(g1::left.Prop(g1::PROP_BATT_PCT, &value) && value == 74);
  CHECK(BandShows(0, font, "74%", 560, 8, 80, 24, 2, 20));
  g1::Event e{};
  bool input = false;
  while (g1::left.NextEvent(&e, 200))
    if (e.type == g1::EVENT_INPUT && e.trigger == g1::TRIGGER_BATTERY && e.a == 74) input = true;
  CHECK(input);
  CHECK(g1::left.Stop() && g1::right.Stop());

  // RAISE round trip, NOTIFY, HALT, host CALL and host code from a Program.
  g1::Program p(0x200, 16);
  auto C = [&](uint32_t v) { return p.Const(v); };
  auto echo = p.NewLabel(), fail = p.NewLabel(), stop = p.NewLabel(), sub = p.NewLabel();
  const g1::ArenaWord box = 0x300 / 4;
  p.Bind(echo);
  p.Add(R(1), box, C(1));
  p.Notify(R(1), C(0xEC40));
  p.End();
  p.Bind(fail);
  p.Load(32, R(2), C(0x40000), C(0)); // past the arena: error 3
  p.End();
  p.Bind(stop);
  p.Halt();
  p.Bind(sub);
  g1::Address sub_at = p.Here();
  p.Add(box + 1, box, C(5));
  p.Ret();
  p.On(g1::TRIGGER_USER1, echo);
  p.On(g1::TRIGGER_USER2, fail);
  p.On(g1::TRIGGER_USER3, stop);
  p.Changes(0x300, 8);
  CHECK(p.Valid());
  CHECK(g1::left.Load(p) && g1::left.Start(0x200));
  while (g1::left.NextEvent(&e, 0)) {
  }
  g1::left.WriteArena(0x300, Words({41}));
  int64_t t0 = g1::NowUs();
  g1::left.Raise(g1::TRIGGER_USER1);
  CHECK(g1::left.NextEvent(&e, 2000) && e.type == g1::EVENT_NOTIFY && e.trigger == g1::TRIGGER_USER1 &&
        e.a == 42 && e.b == 0xEC40);
  std::printf("RAISE round trip: %.1f ms\n", double(e.host_us - t0) / 1000.0);
  g1::left.Raise(g1::TRIGGER_USER2);
  CHECK(g1::left.NextEvent(&e, 2000) && e.type == g1::EVENT_HALT && e.code == 3 && e.op == 0x20 &&
        e.trigger == g1::TRIGGER_USER2 && e.detail == 0x40000);
  CHECK(g1::left.Status(&st) && st.state == 2 && st.halt_code == 3 && st.next == e.address);
  CHECK(g1::left.Start(0x200));
  g1::left.Raise(g1::TRIGGER_USER3);
  CHECK(g1::left.NextEvent(&e, 2000) && e.type == g1::EVENT_HALT && e.code == 0 && e.op == 0x34);
  g1::left.Call(sub_at);
  g1::Program host(0x200, 16);
  host.Write(0x308, Words({77}));
  host.Notify(0x308 / 4, 0x308 / 4);
  g1::left.Run(host.code);
  CHECK(g1::left.NextEvent(&e, 2000) && e.type == g1::EVENT_NOTIFY && e.trigger == 0xFF && e.a == 77);
  uint8_t word[4] = {};
  CHECK(g1::left.ReadArena(0x304, word) && word[0] == 46);
  host.code.clear();
  host.Write(0x30C, Words({99}));
  host.Raise(0x30C / 4); // trigger 99: error 5, so the whole packet is refused
  uint32_t before = g1::left.GetState().errors;
  g1::left.Run(host.code);
  CHECK(g1::left.GetState().errors == before + 1 && g1::left.GetState().last_error.find("Run") == 0);

  // MIC: no microphone on the emulated arms (HELLO.board bit 2 clear).
  auto r = g1::Control(0, g1::CTRL_MIC, {1, 0});
  CHECK(r.first == VM_E_MIC);
  CHECK(g1::left.Stop());
  g1::Free(g1::Address(g_arena_base));
  CHECK(g1::left.ArenaCrc(0x100, 0x18, &got, &want) && got != want); // known again (and stale)
  sk::UnloadAll();
  g1::Deinit();
}

// Random host code on a block of arena memory, through a lossy link: the
// mirror and the arms must agree, the arms never see code the mirror
// refused, and the pool wraps around many times.
static uint32_t g_seed = 11;

static void TestRandomHostCode() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  const uint32_t size = 0x8000;
  int64_t at = g1::Alloc(size);
  CHECK(at > 0);
  g1::Address base = g1::Address(at);
  std::mt19937 rng(g_seed);
  auto r = [&](uint32_t n) { return uint32_t(rng() % n); };
  g1::emulator::SetLoss(0.1, 0.1);
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "", &error);
  sk::gray::Bitmap &line = sk::gray::CreateBitmap(300, 16);
  uint32_t refused = 0;
  for (int k = 0; k < 3000; k++) {
    if (k % 50 == 49) {
      sk::gray::Fill(line, 0, 0, 300, 16, uint8_t(r(3) * 40));
      font.DrawLine(line, "random " + std::to_string(k), int(r(100)), 12);
      sk::gray::Draw(line, int(r(400)), int(r(450)));
      if (r(4) == 0) sk::gray::Clear();
      sk::Flush();
    }
    if (k % 700 == 350) g1::emulator::Disconnect(int(r(2)), 300);
    if (k % 1000 == 499) {
      g1::emulator::Reboot(int(r(2)));
      Sleep(700); // reconnected with its new epoch before anything else happens to it
    }
    g1::Arm &arm = r(2) ? g1::left : g1::right;
    uint32_t before = arm.GetState().errors;
    uint32_t stride = 8 + r(120), x = r(300), y = r(200), w = r(200), h = r(60);
    uint8_t level = uint8_t(r(17));
    switch (r(9)) {
    case 0: arm.Fill(base + r(size), stride, int32_t(x) - 20, int32_t(y) - 10, w, h, level); break;
    case 1:
      arm.Blit(base + r(size / 2), stride, int32_t(r(50)), int32_t(r(20)), base + r(size), stride, int32_t(x),
               int32_t(y), w / 4, h / 4, g1::Mode(r(4)), level);
      break;
    case 2:
      arm.BlitCompressed(base + r(size / 2), stride, int32_t(r(50)), int32_t(r(20)), base + r(size), stride,
                         int32_t(x), int32_t(y), w / 4, h / 4, g1::Mode(r(4)), level);
      break;
    case 3: arm.Line(base + r(size), stride, int32_t(r(400)) - 50, int32_t(r(300)) - 50, int32_t(r(400)), int32_t(r(300)), level); break;
    case 4: arm.CopyArena(base + r(size), base + r(size - 256), r(256)); break;
    case 5: {
      std::vector<uint8_t> bytes(r(600));
      for (auto &b : bytes) b = uint8_t(r(4) ? 0 : rng());
      arm.WriteArena(base + r(size - uint32_t(bytes.size())), bytes);
      break;
    }
    case 6: {
      std::vector<uint8_t> plain(1 + r(300)), packed;
      for (auto &b : plain) b = uint8_t(r(3) ? 0x55 : rng());
      g1::PackBits(plain.data(), int(plain.size()), packed);
      arm.WriteArenaRLE(base + r(size - uint32_t(plain.size())), uint16_t(plain.size()), packed);
      break;
    }
    case 7:
      arm.Draw(base + r(size / 2), stride, int32_t(r(30)), int32_t(r(30)), int32_t(r(700)) - 30, int32_t(r(500)) - 10,
               w, h / 2);
      break;
    default: arm.Notify(rng(), rng()); break;
    }
    refused += arm.GetState().errors - before;
  }
  g1::emulator::SetLoss(0, 0);
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    uint32_t got = 0, want = 1;
    CHECK(a->ArenaCrc(0, 0, &got, &want) && got == want);
    bool on = a->GetState().panel_on;
    CHECK(!on || (a->PanelCrc(0, 480, &got, &want) && got == want));
    g1::Event e{};
    int notifies = 0, errors = 0;
    while (a->NextEvent(&e, 100)) {
      notifies += e.type == g1::EVENT_NOTIFY;
      errors += e.type == g1::EVENT_ERROR;
    }
    CHECK(errors == 0 && notifies > 0);
  }
  CHECK(refused > 100 && refused < 2500); // some of the random code is invalid, most is not
  uint32_t epochs = g1::left.GetState().epoch_changes + g1::right.GetState().epoch_changes;
  CHECK(epochs >= 2 && epochs <= 6); // three reboots; a lost RESET_SESSION reply can look like one more
  std::printf("random host code: %u of 3000 refused by the mirror, pool at word %d\n", refused,
              g1::g->arms[0].pool.next);
  g1::Deinit();
}

static void TestLinkProfiles() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  CHECK(!g1::left.GetState().idle && g1::left.GetState().interval_us == 7500);
  Sleep(SCOUTER_G1_IDLE_AFTER_MS + 800);
  for (g1::Arm *a : {&g1::left, &g1::right})
    CHECK(a->GetState().idle && a->GetState().interval_us == 50000);
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "idle", &error);
  sk::gray::Bitmap &b = sk::gray::CreateBitmap(100, 16);
  font.DrawLine(b, "idle", 0, 12);
  sk::gray::Draw(b, 300, 20);
  sk::Flush();
  Sleep(200);
  for (g1::Arm *a : {&g1::left, &g1::right})
    CHECK(!a->GetState().idle && a->GetState().interval_us == 7500);
  WaitShown();
  CHECK(PanelsMatch("after the idle link"));
  g1::Deinit();
}

// An all-black eye: off where it was off, else standby and off
// SCOUTER_G1_BLACK_OFF_MS later, unless a program lit it meanwhile (the arm
// checks PANEL).
static void TestBlackOff() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "black", &error);
  sk::gray::Bitmap &b = sk::gray::CreateBitmap(100, 16);
  font.DrawLine(b, "black", 0, 12);
  auto panels = [](uint32_t left, uint32_t right) {
    uint32_t l = 9, r = 9;
    return g1::left.Prop(g1::PROP_PANEL, &l) && g1::right.Prop(g1::PROP_PANEL, &r) && l == left && r == right;
  };
  auto show = [&](bool lit) {
    sk::gray::Clear();
    if (lit) sk::gray::Draw(b, 300, 20);
    sk::Flush();
    WaitShown();
  };
  show(false);
  CHECK(panels(0, 0));
  show(true);
  show(false);
  CHECK(panels(2, 2));
  if (SCOUTER_G1_BLACK_OFF_MS == 0) { // never off
    Sleep(1000);
    CHECK(panels(2, 2));
    g1::Deinit();
    return;
  }
  Sleep(SCOUTER_G1_BLACK_OFF_MS / 2);
  CHECK(panels(2, 2));
  Sleep(SCOUTER_G1_BLACK_OFF_MS / 2 + 600);
  CHECK(panels(0, 0) && !g1::left.GetState().panel_on);
  CHECK(PanelsMatch("off after the standby"));
  show(false);
  CHECK(panels(0, 0));

  show(true);
  show(false);
  int at = g1::Alloc(256);
  g1::Program p{g1::Address(at)};
  g1::Program::Label on = p.NewLabel();
  p.Bind(on);
  p.SysWrite(p.Const(g1::PROP_PANEL), p.Const(1));
  p.End();
  p.On(g1::TRIGGER_USER0, on);
  CHECK(p.Valid() && g1::right.Load(p) && g1::right.Start(g1::Address(at)));
  g1::right.Raise(g1::TRIGGER_USER0);
  Sleep(SCOUTER_G1_BLACK_OFF_MS + 600);
  CHECK(panels(0, 1));
  CHECK(g1::left.GetState().errors == 0 && g1::right.GetState().errors == 0);
  g1::Deinit();
}

static std::vector<uint32_t> Codepoints(const uint8_t *blob) {
  int n = blob[8] | blob[9] << 8;
  std::vector<uint32_t> cps(static_cast<size_t>(n));
  std::memcpy(cps.data(), blob + 16 + 12 * n, 4 * size_t(n));
  return cps;
}

// Glyph order (VM.md §4.1): 0-9, A-Z, a-z first, so a digit's value is its
// glyph index, then from index 62 the others by codepoint; subsets keep the
// slots in place.
static void TestGlyphOrder() {
  const char *slots = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  std::vector<uint32_t> full = Codepoints(kDepartureMonoBlob);
  bool ordered = full.size() > 62;
  for (size_t i = 0; i < full.size() && ordered; i++)
    ordered = i < 62 ? full[i] == uint8_t(slots[i]) : i == 62 || full[i] > full[i - 1];
  CHECK(ordered);

  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  sk::Font &all = sk::LoadFont(kDepartureMonoBlob, "", &error);
  sk::Font &some = sk::LoadFont(kDepartureMonoBlob, "7:", &error);
  CHECK(all.pimpl && some.pimpl);
  std::vector<uint32_t> subset = Codepoints(g1::ArmBytes(0) + g1::AddressOf(some));
  std::vector<uint32_t> expected(62, 0);
  expected[7] = '7';
  expected.insert(expected.end(), {' ', ':'}); // the font has no U+FFFD
  CHECK(subset == expected);
  CHECK(g1::GlyphIndices(some, "7 :7") == std::vector<uint32_t>({7, 62, 63, 7}));

  // UTF-8 of 1 to 4 bytes; a stray continuation byte, an invalid lead byte or
  // a cut-off sequence is one U+FFFD (the bytes after it are read again).
  CHECK(g1::Decode("A\xc3\xa9\xe2\x82\xac\xe2\x80\xa6\xf0\x9f\x98\x80") ==
        std::vector<uint32_t>({'A', 0xE9, 0x20AC, 0x2026, 0x1F600}));
  CHECK(g1::Decode("\x80" "a" "\xff" "\xe2\x82" "b" "\xf0\x9f\x98") ==
        std::vector<uint32_t>({0xFFFD, 'a', 0xFFFD, 0xFFFD, 'b', 0xFFFD}));
  auto indices = [&](std::initializer_list<uint32_t> cps) {
    std::vector<uint32_t> out;
    for (uint32_t cp : cps) {
      auto it = std::find(full.begin(), full.end(), cp);
      if (it == full.end()) it = std::find(full.begin(), full.end(), 0xFFFDu);
      if (it != full.end()) out.push_back(uint32_t(it - full.begin()));
    }
    return out;
  };
  CHECK(std::find(full.begin(), full.end(), 0x20ACu) != full.end());
  CHECK(g1::GlyphIndices(all, "\xc3\xa9\xe2\x82\xac\xe2\x80\xa6") == indices({0xE9, 0x20AC, 0x2026}));
  CHECK(g1::GlyphIndices(all, "\xf0\x9f\x98\x80" "x" "\xe2\x82") == indices({0x1F600, 'x', 0xFFFD}));

  // The same pixels from either font, on the host and in the arm's GLYPHS.
  sk::gray::Bitmap &a = sk::gray::CreateBitmap(40, 16), &b = sk::gray::CreateBitmap(40, 16);
  all.DrawLine(a, "7:7", 0, 12);
  some.DrawLine(b, "7:7", 0, 12);
  int stride = 0;
  g1::Address address = g1::AddressOf(b, &stride);
  CHECK(stride == 20 && address >= g1::kHostBottom);
  for (g1::Arm *arm : {&g1::left, &g1::right}) {
    uint32_t got = 0, want = 1;
    CHECK(arm->ArenaCrc(address, 20 * 16, &got, &want) && got == want);
  }
  const uint8_t *arena = g1::ArmBytes(0) + address;
  bool same = true, ink = false;
  for (int y = 0; y < 16; y++) {
    for (int x = 0; x < 40; x++) {
      uint8_t v = b.contents(x, y), cell = arena[y * stride + x / 2];
      same &= v == a.contents(x, y) && Level(v) == (x % 2 ? cell & 15 : cell >> 4);
      ink |= v != 0;
    }
  }
  CHECK(same && ink);
  g1::Deinit();
}

// Discovery by pair ID (VM.md §6.1): boards without a panel and arms of
// other glasses are passed over.
static void TestPairChoice() {
  int flags = 0;
  std::string id;
  CHECK(g1::ParsePairRecord({1, 0x05, 'S', '1'}, &flags, &id) && flags == 5 && id == "S1");
  CHECK(!g1::ParsePairRecord({2, 0x05, 'S'}, &flags, &id) && !g1::ParsePairRecord({1, 0x05}, &flags, &id));
  auto arm = [](const char *path, int flags, const char *pair_id, int rssi) {
    g1::Candidate c;
    c.path = c.address = path;
    c.paired = true;
    c.rssi = rssi;
    c.flags = flags;
    c.pair_id = pair_id;
    return c;
  };
  std::set<std::string> none;
  std::vector<g1::Candidate> lefts = {arm("L1", 5, "PAIR1", -70), arm("L2", 5, "PAIR2", -40)};
  std::vector<g1::Candidate> rights = {arm("DK", 2, "CHIP", -30), arm("R1", 4, "PAIR1", -60)};
  CHECK(g1::PickCandidate(lefts, rights, "", none) == 0);       // L2 is stronger but has no right arm
  CHECK(g1::PickCandidate(rights, lefts, "PAIR1", none) == 1);  // the DK has no panel
  CHECK(g1::PickCandidate({arm("R2", 4, "PAIR2", -30)}, lefts, "PAIR1", none) == -1);
  CHECK(g1::PickCandidate({arm("R0", -1, "", -50)}, lefts, "PAIR1", none) == 0); // no record
  CHECK(g1::PickCandidate(rights, lefts, "PAIR1", {"R1"}) == -1);
}

static void TestAdapterChoice() {
  std::vector<g1::AdapterInfo> two = {{"/org/bluez/hci0", "0A:00:00:00:00:01", true},
                                      {"/org/bluez/hci1", "CA:FE:00:00:00:02", true}};
  std::string problem;
  CHECK(g1::PickAdapter(two, "", &problem) == 0);                  // as before: the first powered one
  CHECK(g1::PickAdapter(two, "hci1", &problem) == 1);
  CHECK(g1::PickAdapter(two, "ca:fe:00:00:00:02", &problem) == 1); // by address, any case
  CHECK(g1::PickAdapter(two, "hci2", &problem) == -1 && problem == "no Bluetooth adapter hci2");
  CHECK(g1::PickAdapter(two, "hci", &problem) == -1);
  two[0].powered = false;
  CHECK(g1::PickAdapter(two, "", &problem) == 1);
  CHECK(g1::PickAdapter(two, "hci0", &problem) == -1 && problem == "Bluetooth is off (/org/bluez/hci0)");
  CHECK(g1::PickAdapter({}, "", &problem) == -1 && problem == "no Bluetooth adapter");
}

// Through the emulated transport (one emulated adapter, hci0).
static void TestAdapterSelection() {
  std::string error;
  g1::Connection connection;
  connection.adapter = "hci7";
  CHECK(g1::Init(nullptr, &error, &connection) == g1::ERROR_BLUETOOTH_ERROR);
  CHECK(error == "no Bluetooth adapter hci7");
  connection.adapter = "hci0";
  CHECK(g1::Init(nullptr, &error, &connection) == g1::ERROR_NONE);
  g1::Deinit();
  connection.adapter = "00:00:00:00:00:00";
  CHECK(g1::Init(nullptr, &error, &connection) == g1::ERROR_NONE);
  CHECK(g1::left.GetState().connected && g1::right.GetState().connected);
  g1::Deinit();
}

static void TestSingleArm() {
  std::string error;
  g1::Connection connection;
  connection.left = "none";
  CHECK(g1::Init(nullptr, &error, &connection) == g1::ERROR_NONE);
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "right only", &error);
  sk::gray::Bitmap &b = sk::gray::CreateBitmap(100, 16);
  font.DrawLine(b, "right only", 0, 12);
  sk::gray::Draw(b, 600, 20);
  sk::Flush();
  WaitShown();
  const uint8_t *p = g1::emulator::Panel(1);
  int lit = 0;
  for (int i = 0; i < 640 * 480; i++) lit += p[i] != 0;
  CHECK(lit > 50);
  CHECK(!g1::left.GetState().present && g1::right.GetState().connected);
  g1::Deinit();
}

// An application's own transport (Connection::transport, as on Android) carries the session, and Deinit leaves
// it to the application.
static void TestOwnTransport() {
  std::string error;
  auto own = std::make_unique<g1::EmulatorTransport>("");
  g1::g_emulator = own.get();
  g1::Connection connection;
  connection.transport = own.get();
  CHECK(g1::Init(nullptr, &error, &connection) == g1::ERROR_NONE);
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "own", &error);
  sk::gray::Bitmap &b = sk::gray::CreateBitmap(100, 16);
  font.DrawLine(b, "own transport", 0, 12);
  sk::gray::Draw(b, 300, 20);
  sk::Flush();
  WaitShown();
  for (int arm = 0; arm < 2; arm++) {
    const uint8_t *p = own->Panel(arm);
    int lit = 0;
    for (int i = 0; i < 640 * 480; i++) lit += p[i] != 0;
    CHECK(lit > 50);
  }
  g1::Deinit();
  CHECK(own->Panel(0) != nullptr);
}

// The settings an arm owns follow its HELLO.triggers (VM.md §2.5, §6.7): the left arm has no light sensor, so
// its AUTO_BRIGHTNESS is refused here and nothing is sent; the right arm's is taken. The emulated arms have no
// arm-to-arm link, so PEER_LINK is refused too.
static void TestArmSettings() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  uint32_t before = g1::left.GetState().errors, v = 0;
  g1::left.SysWrite(g1::PROP_AUTO_BRIGHTNESS, 1);
  g1::Arm::State s = g1::left.GetState();
  CHECK(s.errors == before + 1 && s.last_error.find("SYS_WRITE refused") != std::string::npos);
  g1::left.SysWrite(g1::PROP_PEER_LINK, 1);
  CHECK(g1::left.GetState().errors == before + 2);
  g1::left.SysWrite(g1::PROP_BRIGHTNESS, 30);
  g1::right.SysWrite(g1::PROP_AUTO_BRIGHTNESS, 1);
  WaitShown();
  CHECK(g1::right.Prop(g1::PROP_AUTO_BRIGHTNESS, &v) && v == 1);
  CHECK(g1::left.Prop(g1::PROP_AUTO_BRIGHTNESS, &v) && v == 0x80000000u);
  CHECK(g1::left.Prop(g1::PROP_BRIGHTNESS, &v) && v == 30);
  CHECK(g1::left.GetState().errors == before + 2); // no ERROR from the arm: the refused writes never reached it
  g1::Deinit();
}

static int Lit(int arm) {
  const uint8_t *p = g1::emulator::Panel(arm);
  int n = 0;
  for (int i = 0; i < 640 * 480; i++) n += p[i] != 0;
  return n;
}

// No case hold since g1v0 0.5.60 (VM.md §6.6): in the case the panel keeps its image and draws as anywhere.
static void TestInCase() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "in the case", &error);
  sk::gray::Bitmap &b = sk::gray::CreateBitmap(120, 16);
  font.DrawLine(b, "in the case", 0, 12);
  sk::gray::Draw(b, 300, 200);
  sk::Flush();
  WaitShown();
  int before = Lit(1);
  CHECK(before > 50);
  g1::emulator::SetProperty(1, g1::PROP_IN_CASE, 1);
  Sleep(300);
  CHECK(Lit(1) == before);
  sk::gray::Draw(b, 300, 240);
  sk::Flush();
  WaitShown();
  CHECK(Lit(1) > before);
  g1::emulator::SetProperty(1, g1::PROP_IN_CASE, 0);
  g1::Deinit();
}

// The panel guard (VM.md §6.6): a NOTICE 7 cap on one eye becomes the other eye's BRIGHTNESS_MAX, 42 again with
// it; BRIGHTNESS stays as written. (The emulated arm sends NOTICE 7 when a test sets its BRIGHTNESS_CAP.)
static void TestBrightnessMatch() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  uint32_t v = 0;
  g1::left.SysWrite(g1::PROP_BRIGHTNESS, 30);
  g1::right.SysWrite(g1::PROP_BRIGHTNESS, 30);
  WaitShown();
  Sleep(300);
  CHECK(g1::right.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 42);
  g1::emulator::SetProperty(0, g1::PROP_BRIGHTNESS_CAP, 15);
  Sleep(300);
  CHECK(g1::right.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 15);
  CHECK(g1::right.Prop(g1::PROP_BRIGHTNESS, &v) && v == 30);
  CHECK(g1::left.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 42);
  CHECK(g1::left.Prop(g1::PROP_BRIGHTNESS_CAP, &v) && v == 15);
  g1::emulator::SetProperty(1, g1::PROP_BRIGHTNESS_CAP, 9);
  Sleep(300);
  CHECK(g1::left.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 9);
  g1::emulator::SetProperty(0, g1::PROP_BRIGHTNESS_CAP, 42);
  g1::emulator::SetProperty(1, g1::PROP_BRIGHTNESS_CAP, 42);
  Sleep(300);
  CHECK(g1::right.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 42);
  CHECK(g1::left.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 42);
  CHECK(g1::left.GetState().errors == 0 && g1::right.GetState().errors == 0);
  g1::emulator::SetProperty(0, g1::PROP_BRIGHTNESS_CAP, 12); // the capped eye's link drops: the other one is free
  Sleep(300);
  CHECK(g1::right.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 12);
  g1::emulator::Disconnect(0, 1500);
  Sleep(500);
  CHECK(g1::right.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 42);
  Sleep(2500); // back: its setup reads the cap again, the other eye follows it again
  CHECK(g1::right.Prop(g1::PROP_BRIGHTNESS_MAX, &v) && v == 12);
  g1::emulator::SetProperty(0, g1::PROP_BRIGHTNESS_CAP, 42);
  Sleep(300);
  g1::Deinit();
}

// VIDEO records from VideoEncoder on both emulated arms: the bitmap follows the frames, the panels show
// them, only changed rows are drawn, the mirror stays exact, and a reboot of an arm changes nothing.
static void TestVideo() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  const int w = 203, h = 117, stride = 28, x[2] = {37, 61}, y = 90;
  g1::Address bmp = g1::Address(g1::Alloc(uint32_t(stride * h))), st = g1::Address(g1::Alloc(g1::kVideoState)),
              rec = g1::Address(g1::Alloc(16384));
  g1::VideoEncoder enc(w, h);
  CHECK(enc.Stride() == 26);
  CHECK(g1::VideoEncoder(0, 10).Encode(std::vector<uint8_t>(10)).empty() &&
        g1::VideoEncoder(641, 1).Encode(std::vector<uint8_t>(641)).empty() &&
        g1::VideoEncoder(1, 481).Encode(std::vector<uint8_t>(481)).empty());
  std::vector<uint8_t> zero(size_t(stride * h), 0), pix(size_t(w * h));
  std::mt19937 rng(7);
  for (g1::Arm *a : {&g1::left, &g1::right}) a->WriteArena(bmp, zero);
  for (int k = 0; k < 40; k++) {
    for (int py = 0; py < h; py++)
      for (int px = 0; px < w; px++) {
        int dx = px - 60 - 3 * k, dy = py - 50 - k, ex = px - 150 + 2 * k, ey = py - 70;
        pix[size_t(py * w + px)] = dx * dx + dy * dy < 900 || ex * ex + 3 * ey * ey < 1500 ||
                                   (k % 7 == 3 && py == 100 && rng() % 3 == 0);
      }
    if (k == 25) {
      g1::emulator::Reboot(0); // the library restores the arena (bitmap, state) and the panel
      Sleep(300);
    }
    std::vector<uint8_t> r = enc.Encode(pix, k == 0);
    CHECK(enc.Encode(pix, false, 0).empty());
    CHECK(r.size() < 16384 && (r[0] & 1) == (k == 0));
    for (int i = 0; i < 2; i++) {
      g1::Arm *a = i ? &g1::right : &g1::left;
      a->WriteArena(rec, r);
      a->Video(bmp, stride, w, h, x[i], y, 11, rec, uint32_t(r.size()), st);
    }
    bool same = true;
    for (int i = 0; i < 2; i++) {
      const uint8_t *m = g1::ArmBytes(i) + bmp;
      const std::vector<uint8_t> &panel = g1::g->arms[i].mirror.panel;
      for (int py = 0; py < h; py++)
        for (int px = 0; px < w; px++) {
          bool ink = pix[size_t(py * w + px)] != 0;
          same &= ((m[py * stride + px / 8] >> (7 - px % 8)) & 1) == ink;
          same &= panel[size_t(y + py) * 640 + size_t(x[i] + px)] == (ink ? 11 : 0);
        }
    }
    CHECK(same);
  }
  uint32_t got = 0, want = 1;
  for (g1::Arm *a : {&g1::left, &g1::right}) CHECK(a->PanelCrc(0, 480, &got, &want) && got == want);
  CHECK(ArenasMatch());
  CHECK(g1::left.GetState().epoch_changes == 1);
  g1::Deinit();
}

// The asset store (VM.md §6.10) on the emulated arms: Store, a second Store
// sends nothing, Assets, Forget; a Keep::kStored font and bitmap come back after
// a reboot by ASSET_LOAD with almost nothing sent; an arm without the store
// (an older firmware) gets them by WRITE.
static uint64_t Sent(int arm) { return g1::g->core->links[arm].sent_bytes; }

static bool JobsDone() {
  for (int k = 0; k < 500; k++) {
    {
      std::lock_guard<std::mutex> lock(g1::g->core->mutex);
      if (g1::g->core->links[0].asset_jobs.empty() && g1::g->core->links[1].asset_jobs.empty()) return true;
    }
    Sleep(10);
  }
  return false;
}

static bool Holds(g1::Arm &arm, const g1::AssetId &id) {
  for (auto &e : g1::Assets(arm))
    if (e.id == id) return true;
  return false;
}

// The kept objects' memory on both arms is what the library expects (the
// left arm ran a program, so its whole arena is not checked).
static bool KeptMatch() {
  bool ok = !g1::g->kept.empty();
  for (const g1::Kept *k : g1::g->kept)
    for (g1::Arm *a : {&g1::left, &g1::right}) {
      uint32_t arm = 0, expected = 1;
      ok &= a->ArenaCrc(k->address, uint32_t(k->blob.size()), &arm, &expected) && arm == expected;
    }
  return ok;
}

static void TestAssets() {
  std::string error;
  CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
  std::vector<uint8_t> blob(5000);
  for (size_t i = 0; i < blob.size(); i++) blob[i] = uint8_t(i * 13 + (i >> 7));
  g1::AssetId id = g1::AssetIdOf(blob);
  CHECK(g1::Store(blob, g1::ASSET_RAW, &error));
  for (g1::Arm *a : {&g1::left, &g1::right}) {
    auto list = g1::Assets(*a);
    CHECK(list.size() == 1 && list[0].id == id && list[0].size == blob.size() && list[0].type == g1::ASSET_RAW &&
          list[0].crc == g1::Crc32(0, blob.data(), blob.size()));
  }
  uint64_t sent[2] = {Sent(0), Sent(1)};
  CHECK(g1::Store(blob, g1::ASSET_RAW, &error));
  CHECK(Sent(0) == sent[0] && Sent(1) == sent[1]);

  // a program pages through the asset with ASSET_LOAD (its memory freed after: known again)
  int64_t area = g1::Alloc(8192);
  CHECK(area >= 0);
  g1::Address base = g1::Address(area), data = base + 4096;
  g1::Program p(base);
  g1::Program::Label start = p.NewLabel();
  p.On(g1::TRIGGER_START, start);
  p.Bind(start);
  p.Write(data, std::span<const uint8_t>(id.b, 16));
  p.AssetLoad(p.Const(data), p.Const(data + 16), p.Const(4000), p.Const(1000));
  p.Notify(p.Const(1), p.Const(2));
  p.End();
  p.Changes(data, 1016);
  CHECK(g1::left.Load(p) && g1::left.Start(p.table_address));
  g1::Event e{};
  bool notified = false;
  while (!notified && g1::left.NextEvent(&e, 2000))
    if (e.type == g1::EVENT_NOTIFY && e.a == 1) notified = true;
  CHECK(notified);
  std::vector<uint8_t> got(1000);
  CHECK(g1::left.ReadArena(data + 16, got) && std::memcmp(got.data(), blob.data() + 4000, 1000) == 0);
  CHECK(g1::left.Stop());
  g1::Free(base);

  // kept: the first load sends the font and stores it in the background
  sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "", &error, 1, sk::Keep::kStored);
  std::vector<uint8_t> pixels(64 * 40);
  for (size_t i = 0; i < pixels.size(); i++) pixels[i] = uint8_t(i * 7);
  sk::gray::Bitmap &bitmap =
      sk::gray::LoadBitmap({pixels.data(), 64, 40, 64}, sk::gray::DITHER_NONE, sk::Keep::kStored);
  sk::gray::Bitmap &line = sk::gray::CreateBitmap(300, 16);
  font.DrawLine(line, "kept font", 0, 12);
  sk::gray::Draw(line, 20, 100);
  sk::gray::Draw(bitmap, 20, 140);
  sk::Flush();
  WaitShown();
  CHECK(JobsDone());
  g1::AssetId font_id = g1::AssetIdOf(sk::device::FontBlob(font));
  auto *gb = static_cast<g1::G1Bitmap *>(sk::device::DeviceData(bitmap));
  CHECK(gb->kept && gb->header == 12 && gb->kept->blob.size() == 12 + 32 * 40);
  for (g1::Arm *a : {&g1::left, &g1::right}) CHECK(Holds(*a, font_id) && Holds(*a, gb->kept->id));

  // after a reboot, ASSET_LOAD: far fewer bytes than the font
  sent[1] = Sent(1);
  g1::emulator::Reboot(1);
  Sleep(100);
  sk::gray::Draw(line, 20, 200);
  sk::Flush();
  WaitShown();
  CHECK(Sent(1) - sent[1] < 2000 && sk::device::FontBlob(font).size() > 10000);
  CHECK(KeptMatch());
  CHECK(PanelsMatch("kept after reboot"));

  // an arm without the store gets them by WRITE again
  g1::emulator::SetAssetStore(0, false);
  sent[0] = Sent(0);
  g1::emulator::Reboot(0);
  Sleep(100);
  sk::gray::Draw(line, 20, 220);
  sk::Flush();
  WaitShown();
  CHECK(Sent(0) - sent[0] > sk::device::FontBlob(font).size());
  CHECK(KeptMatch());
  CHECK(g1::Assets(g1::left).empty());
  CHECK(!g1::Store(blob, g1::ASSET_RAW, &error) && !error.empty());
  g1::emulator::SetAssetStore(0, true);

  CHECK(g1::Forget(id, &error));
  CHECK(!Holds(g1::right, id) && Holds(g1::right, font_id));
  g1::Deinit();
}

// The host-session boundary: Init and Deinit repeat in one process; after Deinit the old session makes no
// writes, polls or reconnects and every API call fails at once; waits on other threads end within a bounded
// time; a failed Init or a missing arm leaves no thread behind.
static int Threads() {
  int n = 0;
  if (DIR *d = opendir("/proc/self/task")) {
    while (dirent *e = readdir(d)) n += e->d_name[0] != '.';
    closedir(d);
  }
  return n;
}

namespace {
struct CountingTransport : g1::EmulatorTransport {
  CountingTransport() : g1::EmulatorTransport("") {}
  std::atomic<int> writes{0}, polls{0}, starts{0}, stops{0};
  bool Start(Events *events, const bool present[2]) override {
    starts++;
    return EmulatorTransport::Start(events, present);
  }
  bool Write(int arm, int channel, const uint8_t *data, size_t n) override {
    writes++;
    return EmulatorTransport::Write(arm, channel, data, n);
  }
  void Poll(int timeout_ms) override {
    polls++;
    EmulatorTransport::Poll(timeout_ms);
  }
  void Stop() override {
    stops++;
    EmulatorTransport::Stop();
  }
};
} // namespace

static void TestLifecycle() {
  using clock = std::chrono::steady_clock;
  auto ms = [](clock::time_point since) {
    return std::chrono::duration_cast<std::chrono::milliseconds>(clock::now() - since).count();
  };
  std::string error;
  std::thread([] {}).join(); // a runtime's helper threads (a sanitizer's) start with the first thread
  int base = Threads();
  for (int cycle = 0; cycle < 5; cycle++) {
    CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
    sk::Font &font = sk::LoadFont(kDepartureMonoBlob, "cycle", &error);
    sk::gray::Bitmap &b = sk::gray::CreateBitmap(100, 16);
    font.DrawLine(b, "cycle", 0, 12);
    sk::gray::Draw(b, 100, 20);
    sk::Flush();
    WaitShown();
    uint32_t v = 0;
    CHECK(g1::left.Prop(g1::PROP_PANEL, &v) && g1::right.GetState().connected);
    g1::Deinit();
    g1::Deinit(); // idempotent
    CHECK(Threads() == base);
    auto t0 = clock::now();
    g1::Event e;
    CHECK(!g1::left.Prop(g1::PROP_PANEL, &v) && !g1::left.NextEvent(&e, 2000) && !g1::right.Stop());
    CHECK(!g1::right.GetState().connected && g1::Alloc(64) < 0 && ms(t0) < 100);
    g1::left.SysWrite(g1::PROP_PANEL, 0); // no session: nothing happens
  }

  // A failed Init (no such adapter) leaves no thread, and the next one works.
  g1::Connection bad;
  bad.adapter = "hci7";
  for (int i = 0; i < 3; i++) CHECK(g1::Init(nullptr, &error, &bad) == g1::ERROR_BLUETOOTH_ERROR);
  CHECK(Threads() == base);
  g1::Connection one;
  one.left = "none";
  for (int i = 0; i < 2; i++) {
    CHECK(g1::Init(nullptr, &error, &one) == g1::ERROR_NONE);
    CHECK(!g1::left.GetState().present && g1::right.GetState().connected);
    g1::Deinit();
  }
  CHECK(Threads() == base);

  // After Deinit the application's transport sees nothing from the old session: no write, poll or reconnect.
  auto own = std::make_unique<CountingTransport>();
  g1::g_emulator = own.get();
  g1::Connection conn;
  conn.transport = own.get();
  CHECK(g1::Init(nullptr, &error, &conn) == g1::ERROR_NONE);
  g1::left.SysWrite(g1::PROP_PANEL, 1);
  WaitShown();
  g1::Deinit();
  CHECK(own->starts == 1 && own->stops == 1);
  int writes = own->writes, polls = own->polls;
  g1::left.SysWrite(g1::PROP_PANEL, 0);
  g1::right.Disconnect(10);
  uint32_t v = 0;
  CHECK(!g1::right.Prop(g1::PROP_PANEL, &v));
  Sleep(300);
  CHECK(own->writes == writes && own->polls == polls && own->starts == 1);
  g1::g_emulator = nullptr;
  own.reset();

  // Waits on other threads end when the session does: Deinit returns within a bounded time and every waiting
  // call returns false soon after, whatever its timeout.
  for (int cycle = 0; cycle < 3; cycle++) {
    CHECK(g1::Init(nullptr, &error) == g1::ERROR_NONE);
    std::atomic<bool> run{true};
    std::atomic<int> ended{0};
    std::vector<std::thread> users;
    users.emplace_back([&] {
      g1::Event e;
      while (run) g1::left.NextEvent(&e, 60000);
      ended++;
    });
    users.emplace_back([&] {
      g1::MicFrame f;
      while (run) g1::right.MicRead(&f, 60000);
      ended++;
    });
    users.emplace_back([&] {
      uint32_t v = 0;
      while (run) g1::right.Prop(g1::PROP_BATT_PCT, &v);
      ended++;
    });
    users.emplace_back([&] {
      uint32_t a = 0, b = 0;
      while (run) g1::left.PanelCrc(0, 8, &a, &b);
      ended++;
    });
    Sleep(200);
    auto t0 = clock::now();
    g1::Deinit();
    CHECK(ms(t0) < 5000);
    run = false;
    auto t1 = clock::now();
    for (auto &t : users) t.join();
    CHECK(ended == 4 && ms(t1) < 1000);
    CHECK(Threads() == base);
  }
}

int main(int argc, char **argv) {
  bool quick = argc > 1 && std::string(argv[1]) == "quick"; // without the 30 s wait
  if (argc > 2) g_seed = uint32_t(std::atoi(argv[2]));         // of the random host code
  TestEncoding();
  TestSpecPrograms();
  TestEndToEnd();
  TestPool();
  TestHostCalls();
  TestVideo();
  TestRandomHostCode();
  if (!quick) TestPrograms();
  if (!quick || SCOUTER_G1_BLACK_OFF_MS < 5000) TestBlackOff();
  TestLinkProfiles();
  TestGlyphOrder();
  TestPairChoice();
  TestAdapterChoice();
  TestAdapterSelection();
  TestSingleArm();
  TestOwnTransport();
  TestArmSettings();
  TestInCase();
  TestBrightnessMatch();
  TestAssets();
  TestLifecycle();
  std::printf("%s (%d failures)\n", failures ? "FAILED" : "all tests passed", failures);
  return failures ? 1 : 0;
}
