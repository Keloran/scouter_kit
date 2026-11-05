// The terminal on the emulated arms (T1): nothing shows while the head is down, looking up shows the
// screen on both eyes (the same pixels at each eye's offset), a lowered head hides it after the hold,
// keyer's force shows it whatever the pitch, and one eye's decision is mirrored to the other.
#include <chrono>
#include <clocale>
#include <cstdio>
#include <fstream>
#include <iterator>
#include <thread>

#include "../terminal.hpp"

namespace g1 = scouter_kit::g1;

static int failures = 0;
#define CHECK(cond)                                                       \
  do {                                                                    \
    if (!(cond)) {                                                        \
      std::printf("%s:%d: CHECK(%s) failed\n", __FILE__, __LINE__, #cond); \
      failures++;                                                         \
    }                                                                     \
  } while (0)

static bool WaitFor(auto done, int ms = 5000) {
  auto end = std::chrono::steady_clock::now() + std::chrono::milliseconds(ms);
  while (!done()) {
    if (std::chrono::steady_clock::now() > end) return false;
    std::this_thread::sleep_for(std::chrono::milliseconds(10));
  }
  return true;
}

static int Lit(int arm) {
  const uint8_t *p = g1::emulator::Panel(arm);
  int n = 0;
  for (int i = 0; i < 640 * 480; i++) n += p[i] != 0;
  return n;
}

static uint32_t PanelProp(int arm) {
  uint32_t v = 99;
  (arm ? g1::right : g1::left).Prop(g1::PROP_PANEL, &v);
  return v;
}

static void Pitch(int arm, int deg) { g1::emulator::SetProperty(arm, g1::PROP_PITCH, uint32_t(deg)); }

static std::shared_ptr<ssh::Snapshot> Screen(int cols, int rows, const std::string &line0) {
  auto s = std::make_shared<ssh::Snapshot>();
  s->cols = cols;
  s->rows = rows;
  s->cells.resize(size_t(cols) * rows);
  for (size_t i = 0; i < line0.size() && int(i) < cols; i++) s->cells[i].text = std::string(1, line0[i]);
  const char *red = "red bold";
  for (int i = 0; red[i]; i++) {
    ssh::Cell &c = s->cells[size_t(2) * cols + i];
    c.text = std::string(1, red[i]);
    c.fg = 2;
    c.bold = true;
  }
  s->cursor_row = 0;
  s->cursor_col = int(line0.size());
  return s;
}

// Lit pixels of an arm's panel in columns [x0, x1) of the rows around the battery band.
static int Band(int arm, int x0, int x1) {
  const uint8_t *p = g1::emulator::Panel(arm);
  int n = 0;
  for (int y = 200; y < 280; y++)
    for (int x = x0; x < x1; x++) n += p[y * 640 + x] != 0;
  return n;
}
// The left eye's area at its offset equals the right eye's at its own.
static bool SameArea(int lx, int ly, int rx, int ry, int w, int h) {
  const uint8_t *l = g1::emulator::Panel(0), *r = g1::emulator::Panel(1);
  for (int y = 0; y < h; y++)
    for (int x = 0; x < w; x++)
      if (l[(ly + y) * 640 + lx + x] != r[(ry + y) * 640 + rx + x]) return false;
  return true;
}

int main(int argc, char **argv) {
  setlocale(LC_ALL, "C.UTF-8");
  std::ifstream in(argc > 1 ? argv[1] : TERM_FONT, std::ios::binary);
  std::vector<uint8_t> font((std::istreambuf_iterator<char>(in)), {});
  CHECK(font.size() > 100000);
  g1::GrayConfig gray{100, 9};
  std::string error;
  CHECK(g1::Init(&gray, &error, nullptr) == g1::ERROR_NONE);
  for (int arm = 0; arm < 2; arm++) g1::emulator::SetProperty(arm, g1::PROP_WORN, 1);
  Pitch(0, 0);
  Pitch(1, 0);

  ssh::GlassesTerminal term;
  int visibility = 0;
  CHECK(term.Start({100, 9, 20}, font, [&] { visibility++; }, &error));
  if (!error.empty()) std::printf("start: %s\n", error.c_str());
  CHECK(term.cols() == 77 && term.rows() == 33);
  term.Show(Screen(77, 33, "hello glasses"));
  std::this_thread::sleep_for(std::chrono::milliseconds(300));
  CHECK(!term.Shown() && Lit(0) == 0 && Lit(1) == 0);

  Pitch(0, 30); // looking up
  Pitch(1, 30);
  CHECK(WaitFor([&] { return term.Shown(); }));
  CHECK(WaitFor([&] { return Lit(0) > 200 && Lit(1) > 200; }));
  // dx 100, dy 9: the 539 x 462 area at (100, 4) on the left panel and at (0, 13) on the right.
  int w = 77 * 7, h = 33 * 14, x = (740 - 540) / 2, y = (471 - h) / 2;
  CHECK(SameArea(x, y, x - 100, y + 9, w, h));
  // Each eye's battery level beside the terminal: the left eye's on its left, the right eye's on its right;
  // BATTERY redraws it.
  int band = Band(0, 0, 100);
  CHECK(band > 0 && Band(1, 540, 640) > 0);
  g1::emulator::SetProperty(0, g1::PROP_BATT_PCT, 7);
  CHECK(WaitFor([&] { return Band(0, 0, 100) != band; }));
  int lit = Lit(0);
  std::string check = term.Check(); // rows 0 (text and cursor) and 2 have ink, on both eyes
  CHECK(check.rfind("ok", 0) == 0);
  CHECK(check.find("L: 2 of 30 text rows inked") != std::string::npos);
  CHECK(check.find("R: 2 of 30 text rows inked") != std::string::npos);
  if (check.rfind("ok", 0) != 0) std::printf("%s", check.c_str());
  if (argc > 2) { // the left panel as a PGM, to look at
    std::ofstream out(argv[2], std::ios::binary);
    out << "P5 640 480 15\n";
    out.write(reinterpret_cast<const char *>(g1::emulator::Panel(0)), 640 * 480);
  }

  term.Show(Screen(77, 33, "hello glasses, again"));
  CHECK(WaitFor([&] { return Lit(0) > lit && Lit(1) > lit; }));
  CHECK(SameArea(x, y, x - 100, y + 9, w, h));

  Pitch(0, 10); // head down: hidden after the 1 s hold
  Pitch(1, 10);
  std::this_thread::sleep_for(std::chrono::milliseconds(500));
  CHECK(term.Shown());
  CHECK(WaitFor([&] { return !term.Shown(); }, 3000));
  CHECK(WaitFor([&] { return PanelProp(0) == 0 && PanelProp(1) == 0; }));
  CHECK(term.Check().find("panel off") != std::string::npos); // hidden: nothing to read back

  term.Force(true); // keyer held: shown whatever the pitch
  CHECK(WaitFor([&] { return term.Shown() && Lit(0) > 200 && Lit(1) > 200; }));
  // The battery band only while looking up: none with the head level, with the terminal when it looks up, gone
  // again after the hold when it is level, the terminal staying.
  std::this_thread::sleep_for(std::chrono::milliseconds(300));
  CHECK(Band(0, 0, 100) == 0 && Band(1, 540, 640) == 0);
  Pitch(0, 30);
  Pitch(1, 30);
  CHECK(WaitFor([&] { return Band(0, 0, 100) > 0 && Band(1, 540, 640) > 0; }));
  Pitch(0, 10);
  Pitch(1, 10);
  std::this_thread::sleep_for(std::chrono::milliseconds(500));
  CHECK(Band(0, 0, 100) > 0); // the hold
  CHECK(WaitFor([&] { return Band(0, 0, 100) == 0 && Band(1, 540, 640) == 0; }, 3000));
  CHECK(term.Shown() && Lit(0) > 200 && Lit(1) > 200);
  Pitch(0, -30);
  std::this_thread::sleep_for(std::chrono::milliseconds(1500));
  CHECK(term.Shown());
  term.Force(false); // the pitch rule again: hidden
  CHECK(WaitFor([&] { return !term.Shown(); }, 3000));

  Pitch(1, 30); // one eye looks up: the other follows it
  CHECK(WaitFor([&] { return term.Shown() && PanelProp(0) != 0 && PanelProp(1) != 0; }));
  CHECK(visibility >= 4);

  // PanelPolicy: folded (WORN 0) the panel goes off, also with keyer's force; unfolded the terminal comes back
  // (redraw); in the case off, out of it back.
  term.Force(true);
  CHECK(WaitFor([&] { return PanelProp(0) != 0 && PanelProp(1) != 0; }));
  auto worn = [](int v) {
    for (int arm = 0; arm < 2; arm++) g1::emulator::SetProperty(arm, g1::PROP_WORN, uint32_t(v));
  };
  worn(0);
  CHECK(WaitFor([&] { return PanelProp(0) == 0 && PanelProp(1) == 0; }, 1000));
  worn(1);
  CHECK(WaitFor([&] { return PanelProp(0) != 0 && PanelProp(1) != 0 && Lit(0) > 200 && Lit(1) > 200; }, 2000));
  CHECK(SameArea(x, y, x - 100, y + 9, w, h));
  g1::emulator::SetProperty(0, g1::PROP_IN_CASE, 1);
  g1::emulator::SetProperty(1, g1::PROP_IN_CASE, 1);
  CHECK(WaitFor([&] { return PanelProp(0) == 0 && PanelProp(1) == 0; }, 1000));
  g1::emulator::SetProperty(0, g1::PROP_IN_CASE, 0);
  g1::emulator::SetProperty(1, g1::PROP_IN_CASE, 0);
  CHECK(WaitFor([&] { return PanelProp(0) != 0 && PanelProp(1) != 0 && Lit(0) > 200; }, 2000));
  term.Force(false);

  Pitch(0, 30); // the left arm reboots: its program starts again and redraws the screen
  g1::emulator::Reboot(0);
  CHECK(WaitFor([&] { return PanelProp(0) != 0 && Lit(0) > 200; }, 10000));
  term.Show(Screen(77, 33, "after the reboot"));
  CHECK(WaitFor([&] { return SameArea(x, y, x - 100, y + 9, w, h) && Lit(0) > 200; }, 5000));

  // The left arm's link drops while keyer's force holds: the arm's own watch ends the force within 5 s, and
  // the lowered head hides the terminal there; the right arm, still linked, stays forced.
  Pitch(0, -30);
  Pitch(1, -30);
  CHECK(WaitFor([&] { return PanelProp(0) == 0 && PanelProp(1) == 0; }, 3000));
  term.Force(true); // on again only through the force
  CHECK(WaitFor([&] { return PanelProp(0) != 0 && PanelProp(1) != 0; }));
  auto back = std::chrono::steady_clock::now() + std::chrono::milliseconds(10000);
  g1::emulator::Disconnect(0, 9000);
  CHECK(WaitFor([&] { return Lit(0) == 0; }, 8000)); // its panel, unlinked: no PanelProp
  CHECK(PanelProp(1) != 0);
  std::this_thread::sleep_until(back); // the link back
  term.Force(false);
  CHECK(WaitFor([&] { return PanelProp(0) == 0 && PanelProp(1) == 0; }, 3000));

  // keyer off, the phone lets go (Stop(keep)): the program and the last screen stay on the arms, which show
  // them with the battery band when the head looks up and hide them when it goes down, without the host.
  term.Show(Screen(77, 33, "the last screen"));
  term.Force(true);
  CHECK(WaitFor([&] { return PanelProp(0) != 0 && PanelProp(1) != 0 && Lit(0) > 200; }));
  std::this_thread::sleep_for(std::chrono::milliseconds(500));
  term.Stop(true); // the force ends with it
  CHECK(WaitFor([&] { return PanelProp(0) == 0 && PanelProp(1) == 0; }, 3000));
  Pitch(0, 30);
  Pitch(1, 30);
  CHECK(WaitFor([&] { return Lit(0) > 200 && Lit(1) > 200 && Band(0, 0, 100) > 0 && Band(1, 540, 640) > 0; }));
  CHECK(SameArea(x, y, x - 100, y + 9, w, h));
  Pitch(0, 10);
  Pitch(1, 10);
  CHECK(WaitFor([&] { return PanelProp(0) == 0 && PanelProp(1) == 0; }, 3000));
  g1::left.Stop();
  g1::right.Stop();

  // The debug setting: folded, the terminal still shows (keyer's force), for tests on the table.
  for (int arm = 0; arm < 2; arm++) g1::emulator::SetProperty(arm, g1::PROP_WORN, 0);
  ssh::GlassesTerminal table;
  CHECK(table.Start({100, 9, 20, true}, font, [] {}, &error));
  table.Show(Screen(77, 33, "on the table"));
  table.Force(true);
  CHECK(WaitFor([&] { return table.Shown() && PanelProp(0) != 0 && PanelProp(1) != 0; }));
  table.Stop();
  g1::Deinit();
  std::printf("%s (%d failures)\n", failures ? "FAILED" : "all tests passed", failures);
  return failures ? 1 : 0;
}
