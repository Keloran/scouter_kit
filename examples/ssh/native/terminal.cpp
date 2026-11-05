#include "terminal.hpp"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>

#include <zlib.h>

#ifdef __ANDROID__
#include <android/log.h>
#define LOG(...) __android_log_print(ANDROID_LOG_INFO, "scouter", __VA_ARGS__)
#else
#define LOG(...) (std::fprintf(stderr, __VA_ARGS__), std::fputc('\n', stderr))
#endif

namespace sk = scouter_kit;
namespace g1 = scouter_kit::g1;
using g1::ArenaWord;
using g1::R;

namespace ssh {

namespace {

// State words from Layout::state. The host writes wTop ... wBattFont with each frame, wForce and wPeer.
enum {
  wTop, wFull, wCurSlot, wCurX, wShowAt, wFrame, wFont, wBattFont, wNext, wShown, wForce, wPeer, wHideAt, wUp,
  kStateWords
};

constexpr int kRecordBytes = 512; // u8 runs; each u8 x, u8 cells, u8 bg<<4|fg, u8 1: underline, u8 n, n varint bytes
constexpr int kMaxRuns = 32;
constexpr int kRowsPerTask = 6; // rows rendered per activation, within the jump budget
constexpr int kCodeBytes = 2048;
constexpr int kPoolWords = 96;
constexpr uint32_t kNoCursor = 0xFFFFFFFF;
constexpr int kHysteresis = 5;      // degrees below the angle at which the terminal hides again
constexpr int kHoldUs = 1000000;    // shown this long after the head went down
constexpr uint32_t kByHost = 16;    // NOTIFY a: a visibility change the host asked for (wForce, wPeer)
constexpr int kForceWatchUs = 5000000; // while forced, how often the arm checks that the host is still linked
constexpr int kBattPad = 3, kBattPadY = 1; // the battery band: pixels between its frame and the text
constexpr int kDefaultLevel = 12;

const int kAnsi[16][3] = {{0, 0, 0},       {205, 0, 0},   {0, 205, 0},     {205, 205, 0}, {0, 0, 238},
                          {205, 0, 205},   {0, 205, 205}, {229, 229, 229}, {127, 127, 127}, {255, 0, 0},
                          {0, 255, 0},     {255, 255, 0}, {92, 92, 255},   {255, 0, 255}, {0, 255, 255},
                          {255, 255, 255}};

// Symbols that TUIs use and Departure Mono has not (main.go).
const std::map<uint32_t, uint32_t> kSimilar = {
    {U'⏺', U'•'}, {U'●', U'•'}, {U'◉', U'•'}, {U'○', U'o'}, {U'◦', U'o'}, {U'◯', U'o'}, {U'⎿', U'└'},
    {U'✻', U'*'}, {U'✱', U'*'}, {U'✳', U'*'}, {U'✶', U'*'}, {U'✢', U'*'}, {U'✽', U'*'}, {U'✔', U'√'},
    {U'✓', U'√'}, {U'✅', U'√'}, {U'✘', U'×'}, {U'✗', U'×'}, {U'❌', U'×'}, {U'▶', U'►'}, {U'▸', U'►'},
    {U'⏵', U'►'}, {U'◀', U'◁'}, {U'⏴', U'◁'}, {U'◆', U'♦'}, {U'◇', U'♦'}, {U'⋯', U'…'}, {U'⚠', U'!'},
    {U'☐', U'□'}, {U'▢', U'□'}, {U'☑', U'■'}, {U'☒', U'■'}, {U'⏹', U'■'}, {U'⭐', U'★'}, {U'⇒', U'→'},
    {U'⇐', U'←'}, {U'⏎', U'↵'}};

// The characters that go to the arms at the start, as far as the font has them (font.go).
const uint32_t kBaseRanges[][2] = {{0x20, 0x7e},     {0xa0, 0x24f},   {0x370, 0x4ff}, {0x2000, 0x23ff},
                                   {0x2500, 0x27bf}, {0x2800, 0x28ff}, {0xe0a0, 0xe0d7}};

std::string Utf8(uint32_t c) {
  std::string out;
  if (c < 0x80) {
    out += char(c);
  } else if (c < 0x800) {
    out += char(0xC0 | c >> 6);
    out += char(0x80 | (c & 0x3F));
  } else if (c < 0x10000) {
    out += char(0xE0 | c >> 12);
    out += char(0x80 | (c >> 6 & 0x3F));
    out += char(0x80 | (c & 0x3F));
  } else {
    out += char(0xF0 | c >> 18);
    out += char(0x80 | (c >> 12 & 0x3F));
    out += char(0x80 | (c >> 6 & 0x3F));
    out += char(0x80 | (c & 0x3F));
  }
  return out;
}

uint32_t FirstCodePoint(const std::string &s) {
  if (s.empty()) return ' ';
  auto b = reinterpret_cast<const uint8_t *>(s.data());
  if (b[0] < 0x80) return b[0];
  int n = b[0] >= 0xF0 ? 3 : b[0] >= 0xE0 ? 2 : 1;
  if (int(s.size()) <= n) return 0xFFFD;
  uint32_t c = b[0] & (0x3F >> n);
  for (int i = 1; i <= n; i++) c = c << 6 | (b[i] & 0x3F);
  return c;
}

void Rgb(uint32_t c, int *r, int *g, int *b) {
  if (c & kRgb) {
    *r = c >> 16 & 255, *g = c >> 8 & 255, *b = c & 255;
    return;
  }
  int i = int(c) - 1;
  if (i < 16) {
    *r = kAnsi[i][0], *g = kAnsi[i][1], *b = kAnsi[i][2];
  } else if (i < 232) {
    const int steps[6] = {0, 95, 135, 175, 215, 255};
    i -= 16;
    *r = steps[i / 36], *g = steps[i / 6 % 6], *b = steps[i % 6];
  } else {
    *r = *g = *b = 8 + 10 * (i - 232);
  }
}

int Luma(uint32_t c) {
  int r, g, b;
  Rgb(c, &r, &g, &b);
  return (2126 * r + 7152 * g + 722 * b) / 10000;
}

struct Levels {
  int fg, bg;
  bool line;
};

// A style as panel levels (screen.go): the text's from its colour's brightest channel (5 ... 15), the
// background's from its luma (0 ... 9), then reverse video, and enough contrast between the two.
Levels LevelsOf(const Cell &c) {
  int fg = kDefaultLevel, bg = 0;
  if (c.fg) {
    int r, g, b;
    Rgb(c.fg, &r, &g, &b);
    fg = 5 + (std::max({r, g, b}) * 10 + 127) / 255;
  }
  if (c.bg) bg = (Luma(c.bg) * 9 + 127) / 255;
  if (c.bold) fg = std::min(15, fg + 3);
  if (c.faint) fg = std::max(4, fg * 2 / 3);
  if (c.inverse) std::swap(fg, bg);
  if (c.invisible) {
    fg = bg;
  } else if (bg > 0 && std::abs(fg - bg) < 6) {
    fg = bg >= 8 ? 0 : std::min(15, bg + 6);
  }
  return {fg, bg, c.underline};
}

void Varints(std::vector<uint8_t> &out, uint32_t g) {
  while (g >= 0x80) {
    out.push_back(uint8_t(g | 0x80));
    g >>= 7;
  }
  out.push_back(uint8_t(g));
}

// The characters of a font blob (VM.md §4.2).
std::set<uint32_t> Codepoints(const std::vector<uint8_t> &blob) {
  std::set<uint32_t> out;
  if (blob.size() < 16) return out;
  auto u32 = [&](size_t at) {
    uint32_t v;
    std::memcpy(&v, blob.data() + at, 4);
    return v;
  };
  size_t n = blob[8] | blob[9] << 8;
  if (16 + 16 * n > blob.size()) return out;
  for (size_t i = 0; i < n; i++)
    if (uint32_t cp = u32(16 + 12 * n + 4 * i)) out.insert(cp);
  return out;
}

std::string Chars(const std::set<uint32_t> &set) {
  std::string out;
  for (uint32_t c : set) out += Utf8(c);
  return out;
}

std::vector<uint8_t> Words(std::initializer_list<uint32_t> words) {
  std::vector<uint8_t> out;
  for (uint32_t w : words)
    for (int i = 0; i < 4; i++) out.push_back(uint8_t(w >> (8 * i)));
  return out;
}

// The battery band's top on the panel: centred vertically.
int BattY(const GlassesTerminal::Geometry &g) { return (480 - g.bh) / 2; }

GlassesTerminal::Layout MakeLayout(uint32_t base, const GlassesTerminal::Geometry &g) {
  GlassesTerminal::Layout l;
  l.base = base;
  l.state = base + kCodeBytes;
  l.dirty = l.state + 4 * kStateWords;
  l.ring = l.dirty + uint32_t((g.rows + 3) & ~3);
  l.area = l.ring + uint32_t(g.rows * kRecordBytes);
  l.batt = l.area + uint32_t(g.W() / 2 * g.rows * g.ch);
  l.batt_text = l.batt + uint32_t(g.bw / 2 * g.bh); // 3 digits and %
  l.end = l.batt_text + 4;
  return l;
}

// The terminal at panel (px, py) (program.go), and its visibility. USER0 (the host wrote a frame) renders
// the dirty slots, kRowsPerTask per activation, then RAISEs USER1, which waits for wShowAt, DRAWs the dirty
// rows or the whole area (not while hidden) and NOTIFYs (wFrame, 0). TILT, TIMER, USER2 and START decide
// whether the terminal shows and NOTIFY (shown | kByHost when the host caused it, PITCH). The battery band
// shows at (bx, by) with the terminal; BATTERY redraws it.
g1::Program MakeProgram(const GlassesTerminal::Layout &l, const GlassesTerminal::Geometry &g, int px, int py,
                        int bx, int by, int angle, bool ignore_worn) {
  g1::Program p(l.base, kPoolWords);
  auto c = [&](uint32_t v) { return p.Const(v); };
  auto n = [&](int v) { return p.Const(uint32_t(int32_t(v))); };
  auto w = [&](int i) { return ArenaWord(l.state / 4 + i); };
  ArenaWord top = w(wTop), full = w(wFull), cur_slot = w(wCurSlot), cur_x = w(wCurX), show_at = w(wShowAt),
            frame = w(wFrame), font = w(wFont), batt_font = w(wBattFont), next = w(wNext), shown = w(wShown),
            force = w(wForce), peer = w(wPeer), hide_at = w(wHideAt), up = w(wUp);
  ArenaWord area = c(l.area), stride = n(g.W() / 2);
  ArenaWord pitch = c(g1::PROP_PITCH), lo = c(g1::PROP_PITCH_LO), hi = c(g1::PROP_PITCH_HI);
  ArenaWord time_us = c(g1::PROP_TIME_US), timer = c(g1::PROP_TIMER), panel = c(g1::PROP_PANEL);
  ArenaWord link = c(g1::PROP_LINK);

  // The shared display policy (VM.md §6.6): the panel off while in the case or folded (WORN 0), also with
  // wForce; afterwards `redraw` puts the terminal back if it is shown. Before the first DRAW. The debug
  // setting leaves it out, for tests with the glasses folded on the table.
  auto redraw = p.NewLabel();
  if (!ignore_worn) p.PanelPolicy(redraw);

  auto render = p.NewLabel(), runs = p.NewLabel(), no_bg = p.NewLabel(), no_line = p.NewLabel(),
       runs_done = p.NewLabel(), no_cur = p.NewLabel();
  p.Bind(render); // r1 = the slot; uses r2 ... r13
  p.Mul(R(2), R(1), n(kRecordBytes));
  p.Add(R(2), R(2), c(l.ring));
  p.Mul(R(3), R(1), n(g.ch));
  p.Fill(area, stride, c(0), R(3), n(g.W()), n(g.ch), c(0));
  p.Load(8, R(4), R(2), c(0));
  p.Add(R(5), R(2), c(1));
  p.Bind(runs);
  p.Beq(R(4), c(0), runs_done);
  p.Load(8, R(6), R(5), c(0));
  p.Mul(R(6), R(6), n(g.cw));
  p.Load(8, R(7), R(5), c(1));
  p.Mul(R(7), R(7), n(g.cw));
  p.Load(8, R(8), R(5), c(2));
  p.Shr(R(9), R(8), c(4));
  p.And(R(8), R(8), c(15));
  p.Beq(R(9), c(0), no_bg);
  p.Fill(area, stride, R(6), R(3), R(7), n(g.ch), R(9));
  p.Bind(no_bg);
  p.Load(8, R(10), R(5), c(3));
  p.Load(8, R(11), R(5), c(4));
  p.Add(R(12), R(5), c(5));
  p.Add(R(13), R(3), n(g.ascent));
  p.Glyphs(font, area, stride, R(6), R(13), c(2), R(8), R(12), R(11));
  p.Beq(R(10), c(0), no_line);
  p.Add(R(13), R(3), n(g.ch - 1));
  p.Fill(area, stride, R(6), R(13), R(7), c(1), R(8));
  p.Bind(no_line);
  p.Add(R(5), R(12), R(11));
  p.Sub(R(4), R(4), c(1));
  p.Jmp(runs);
  p.Bind(runs_done);
  p.Bne(R(1), cur_slot, no_cur);
  p.Mul(R(6), cur_x, n(g.cw));
  p.Add(R(13), R(3), n(g.ch - 2));
  p.Fill(area, stride, R(6), R(13), n(g.cw), c(2), c(15));
  p.Bind(no_cur);
  p.Ret();

  auto draw_all = p.NewLabel(); // the whole area, row 0 from slot wTop
  p.Bind(draw_all);
  p.Sub(R(1), n(g.rows), top);
  p.Mul(R(1), R(1), n(g.ch));
  p.Mul(R(2), top, n(g.ch));
  p.Draw(area, stride, c(0), R(2), n(px), n(py), n(g.W()), R(1));
  p.Add(R(3), R(1), n(py));
  p.Draw(area, stride, c(0), c(0), n(px), R(3), n(g.W()), R(2));
  p.Ret();

  // The battery band (VM.md §9.3): BATT_PCT and % in the 2x font, centred in a 1 px white frame on black,
  // drawn at (bx, by). Nothing before the host has sent that font's address (wBattFont); an UNKNOWN reading
  // draws the band as it was. Uses r9 ... r14.
  ArenaWord batt = c(l.batt), bstride = n(g.bw / 2), text = c(l.batt_text);
  auto label = p.NewLabel(), digit = p.NewLabel(), label_draw = p.NewLabel(), label_done = p.NewLabel();
  p.Bind(label);
  p.Beq(batt_font, c(0), label_done);
  p.SysRead(R(9), c(g1::PROP_BATT_PCT));
  p.BltU(n(100), R(9), label_draw); // above 100: unknown
  p.Mov(R(10), c(3)); // i = 3, the % byte
  p.Bind(digit);
  p.Sub(R(10), R(10), c(1));
  p.ModU(R(11), R(9), c(10)); // a digit's glyph index is its value (VM.md §4.1)
  p.Store(8, text, R(10), R(11));
  p.DivU(R(9), R(9), c(10));
  p.Bne(R(9), c(0), digit);
  p.Add(R(12), R(10), text);  // the first digit
  p.Sub(R(13), c(4), R(10)); // the digits and %
  p.Mul(R(14), R(13), n(g.bcw));
  p.Add(R(14), R(14), n(2 + 2 * kBattPad)); // the frame's width
  p.Sub(R(11), n(g.bw), R(14));
  p.Shr(R(11), R(11), c(1)); // the frame's x in the band
  p.Fill(batt, bstride, c(0), c(0), n(g.bw), n(g.bh), c(0));
  p.Fill(batt, bstride, R(11), c(0), R(14), n(g.bh), c(15));
  p.Add(R(11), R(11), c(1));
  p.Sub(R(14), R(14), c(2));
  p.Fill(batt, bstride, R(11), c(1), R(14), n(g.bh - 2), c(0));
  p.Add(R(11), R(11), n(kBattPad));
  p.Glyphs(batt_font, batt, bstride, R(11), n(1 + kBattPadY + g.bascent), c(2), c(15), R(12), R(13));
  p.Bind(label_draw);
  p.Draw(batt, bstride, c(0), c(0), n(bx), n(by), n(g.bw), n(g.bh));
  p.Bind(label_done);
  p.Ret();

  // The head, by the looking-up rule, also while forced: wUp 0 down, 1 up (from `angle`), 2 the hold of kHoldUs
  // after it went kHysteresis lower. The terminal shows while the head is up or wForce holds it; the battery band
  // only while the head is up. show / hide: r6 = 0 (the arm's own decision) or kByHost.
  auto up_range = p.NewLabel(), down_range = p.NewLabel(), show = p.NewLabel(), on_panel = p.NewLabel(),
       no_band = p.NewLabel(), hide = p.NewLabel(), band_off = p.NewLabel();
  p.Bind(up_range);
  p.SysWrite(lo, n(angle - kHysteresis));
  p.SysWrite(hi, n(90));
  p.Ret();
  p.Bind(down_range);
  p.SysWrite(lo, n(-90));
  p.SysWrite(hi, n(angle - 1));
  p.Ret();
  p.Bind(show);
  p.Bne(shown, c(0), on_panel);
  p.Call(draw_all);
  p.Mov(shown, c(1));
  p.SysRead(R(7), pitch);
  p.Or(R(8), shown, R(6));
  p.Notify(R(8), R(7));
  p.Bind(on_panel);
  p.Beq(up, c(0), no_band);
  p.Call(label);
  p.Bind(no_band);
  p.Ret();
  p.Bind(hide);
  p.SysWrite(panel, c(0));
  p.Mov(shown, c(0));
  p.SysRead(R(7), pitch);
  p.Or(R(8), shown, R(6));
  p.Notify(R(8), R(7));
  p.Ret();
  // The band off while the terminal stays (forced): black over it, then the terminal where they overlap.
  p.Bind(band_off);
  p.Fill(batt, bstride, c(0), c(0), n(g.bw), n(g.bh), c(0));
  p.Draw(batt, bstride, c(0), c(0), n(bx), n(by), n(g.bw), n(g.bh));
  if (bx < px + g.W() && bx + g.bw > px && by < py + g.rows * g.ch && by + g.bh > py) p.Call(draw_all);
  p.Ret();

  auto chunk = p.NewLabel(), scan = p.NewLabel(), rendered = p.NewLabel();
  p.Bind(chunk);
  p.Mov(R(0), c(0));
  p.Bind(scan);
  p.BgeU(next, n(g.rows), rendered);
  p.Mov(R(1), next);
  p.Add(next, next, c(1));
  p.Load(8, R(2), c(l.dirty), R(1));
  p.Beq(R(2), c(0), scan);
  p.Call(render);
  p.Add(R(0), R(0), c(1));
  p.BltU(R(0), n(kRowsPerTask), scan);
  p.Raise(c(g1::TRIGGER_USER0));
  p.End();
  p.Bind(rendered);
  p.Mov(next, c(0));
  p.Raise(c(g1::TRIGGER_USER1));
  p.End();

  auto present = p.NewLabel(), slots = p.NewLabel(), skip = p.NewLabel(), drawn = p.NewLabel(),
       presented = p.NewLabel();
  p.Bind(present);
  p.Beq(shown, c(0), presented); // hidden: the panel stays off
  p.SleepUntil(show_at);
  p.Beq(full, c(0), slots);
  p.Call(draw_all);
  p.Jmp(drawn);
  p.Bind(slots);
  p.Mov(R(1), c(0));
  p.Bind(skip);
  p.BgeU(R(1), n(g.rows), drawn);
  p.Load(8, R(2), c(l.dirty), R(1));
  p.Add(R(1), R(1), c(1));
  p.Beq(R(2), c(0), skip);
  p.Sub(R(3), R(1), c(1));
  p.Mul(R(4), R(3), n(g.ch));
  p.Add(R(3), R(3), n(g.rows));
  p.Sub(R(3), R(3), top);
  p.ModU(R(3), R(3), n(g.rows));
  p.Mul(R(3), R(3), n(g.ch));
  p.Add(R(3), R(3), n(py));
  p.Draw(area, stride, c(0), R(4), n(px), R(3), n(g.W()), n(g.ch));
  p.Jmp(skip);
  p.Bind(drawn);
  p.Beq(up, c(0), presented);
  p.Call(label); // again on top, where a row overlaps it
  p.Bind(presented);
  p.Notify(frame, c(0));
  p.End();

  // TILT: PITCH left its range. Up: the head is up; down from up: the hold (looking up again ends it).
  auto tilt = p.NewLabel(), head_up = p.NewLabel(), done = p.NewLabel();
  p.Bind(tilt);
  p.Mov(peer, c(0));
  p.SysRead(R(0), pitch);
  p.Beq(R(0), c(g1::UNKNOWN), done);
  p.BgeS(R(0), n(angle), head_up);
  p.Call(down_range);
  p.Bne(up, c(1), done);
  p.Mov(up, c(2));
  p.SysRead(R(1), time_us);
  p.Add(hide_at, R(1), n(kHoldUs));
  p.SysWrite(timer, hide_at);
  p.End();
  p.Bind(head_up);
  p.Call(up_range);
  p.Mov(up, c(1));
  p.Mov(R(6), c(0));
  p.Call(show);
  p.Bind(done);
  p.End();

  // TIMER: the hold's end (the terminal goes, or while forced only the band), and while forced the force's
  // watch: once the host is gone (LINK 0: the phone let go, keyer with it), the head alone decides.
  auto tick = p.NewLabel(), held = p.NewLabel(), watch = p.NewLabel(), rearm = p.NewLabel(),
       unforce = p.NewLabel(), own = p.NewLabel(), own_show = p.NewLabel();
  p.Bind(tick);
  p.Bne(up, c(2), watch);
  p.SysRead(R(1), time_us);
  p.Sub(R(2), R(1), hide_at);
  p.BltS(R(2), c(0), rearm);
  p.Mov(up, c(0));
  p.Beq(force, c(1), held);
  p.Mov(R(6), c(0));
  p.Beq(shown, c(0), done);
  p.Call(hide);
  p.End();
  p.Bind(held);
  p.Beq(shown, c(0), watch);
  p.Call(band_off);
  p.Bind(watch);
  p.Bne(force, c(1), done);
  p.SysRead(R(0), link);
  p.Beq(R(0), c(0), unforce);
  p.SysRead(R(1), time_us);
  p.Add(R(1), R(1), n(kForceWatchUs));
  p.SysWrite(timer, R(1));
  p.End();
  p.Bind(rearm);
  p.SysWrite(timer, hide_at);
  p.End();

  // USER2: the host changed wForce or wPeer (the other eye's decision). START: the same, from hidden with the
  // panel off and the head's state from PITCH.
  auto user2 = p.NewLabel(), forced = p.NewLabel(), peer_up = p.NewLabel(), peer_down = p.NewLabel(),
       start = p.NewLabel(), start_down = p.NewLabel();
  p.Bind(user2);
  p.Mov(R(6), c(kByHost));
  p.Beq(force, c(1), forced);
  p.Beq(peer, c(1), peer_up);
  p.Beq(peer, c(2), peer_down);
  p.Bind(own);
  p.Mov(R(6), c(0));
  p.Bne(up, c(0), own_show);
  p.Beq(shown, c(0), done);
  p.Call(hide);
  p.End();
  p.Bind(own_show);
  p.Call(show);
  p.End();
  p.Bind(peer_up);
  p.Mov(up, c(1));
  p.Call(up_range);
  p.Call(show);
  p.End();
  p.Bind(peer_down);
  p.Mov(up, c(0));
  p.Call(down_range);
  p.Beq(shown, c(0), done);
  p.Call(hide);
  p.End();
  p.Bind(forced);
  p.Call(show);
  p.Beq(up, c(2), done); // the hold's TIMER first; the watch follows it
  p.SysRead(R(1), time_us);
  p.Add(R(1), R(1), n(kForceWatchUs));
  p.SysWrite(timer, R(1));
  p.End();
  p.Bind(unforce);
  p.Mov(force, c(0));
  p.Mov(peer, c(0));
  p.Jmp(own);
  p.Bind(start);
  p.SysWrite(panel, c(0));
  p.Mov(shown, c(0));
  p.Mov(next, c(0));
  p.Mov(up, c(0));
  p.SysRead(R(0), pitch);
  p.Beq(R(0), c(g1::UNKNOWN), start_down);
  p.BltS(R(0), n(angle), start_down);
  p.Mov(up, c(1));
  p.Call(up_range);
  p.Jmp(user2);
  p.Bind(start_down);
  p.Call(down_range);
  p.Jmp(user2);

  // The policy's hold ended (unfolded, out of the case): the terminal again where it is shown.
  p.Bind(redraw);
  p.Beq(shown, c(0), done);
  p.Call(draw_all);
  p.Beq(up, c(0), done);
  p.Call(label);
  p.End();

  // BATTERY: the band again where it shows.
  auto battery = p.NewLabel();
  p.Bind(battery);
  p.Beq(shown, c(0), done);
  p.Beq(up, c(0), done);
  p.Call(label);
  p.End();

  p.On(g1::TRIGGER_START, start);
  p.On(g1::TRIGGER_USER0, chunk);
  p.On(g1::TRIGGER_USER1, present);
  p.On(g1::TRIGGER_USER2, user2);
  p.On(g1::TRIGGER_TILT, tilt);
  p.On(g1::TRIGGER_TIMER, tick);
  p.On(g1::TRIGGER_BATTERY, battery);
  p.Changes(l.state + 4 * wNext, 4 * (kStateWords - wNext));
  p.Changes(l.area, l.end - l.area);
  return p;
}

} // namespace

std::string GlassesTerminal::Row::Key() const {
  return std::to_string(cursor) + ":" + std::string(rec.begin(), rec.end());
}

// The frame that brings the arms to these rows (screen.go). The ring turns by k when the screen scrolled
// by k and that leaves fewer rows to send. False when nothing changed.
bool GlassesTerminal::Screen::Next(const std::vector<Row> &rows, Frame *f) {
  int n = int(rows.size());
  if (int(slots.size()) != n) {
    slots.assign(n, std::string());
    fresh = true;
  }
  std::vector<std::string> keys(n);
  for (int y = 0; y < n; y++) keys[y] = rows[y].Key();
  auto changed = [&](int k) {
    int d = 0;
    for (int y = 0; y < n; y++) d += slots[(y + top + k) % n] != keys[y];
    return d;
  };
  int k = 0;
  if (!fresh) {
    int best = changed(0);
    for (int kk = 1; kk < n && best > 0; kk++)
      if (int d = changed(kk); d < best) best = d, k = kk;
  }
  top = (top + k) % n;
  *f = Frame{};
  f->top = top;
  f->dirty.assign(n, 0);
  for (int y = 0; y < n; y++) {
    int slot = (y + top) % n;
    if (fresh || slots[slot] != keys[y]) {
      f->dirty[slot] = 1;
      f->recs[slot] = rows[y].rec;
      slots[slot] = keys[y];
    }
    if (rows[y].cursor >= 0) f->cur_slot = slot, f->cur_x = rows[y].cursor;
  }
  f->full = fresh || k != 0 || int(f->recs.size()) > n / 2;
  fresh = false;
  return k != 0 || !f->recs.empty();
}

bool GlassesTerminal::LoadGlyphs(const std::set<uint32_t> &chars) {
  std::string error;
  sk::Font &font = sk::LoadFont(std::span<const uint8_t>(blob_), Chars(chars), &error);
  if (!font.pimpl) return false;
  font_ = &font;
  loaded_ = chars;
  glyphs_.clear();
  auto space = g1::GlyphIndices(font, " ");
  auto q = g1::GlyphIndices(font, "?");
  space_ = space.empty() ? 0 : space[0];
  unknown_ = q.size() == 1 ? q[0] : space_;
  return true;
}

// The wanted characters go to the arms too: the font again with them (the glyph indices change, so the
// next frame redraws everything), or, if that does not fit the arena, as it was, and those show as "?".
bool GlassesTerminal::Grow() {
  std::set<uint32_t> more = loaded_, old = loaded_;
  more.insert(want_.begin(), want_.end());
  sk::Unload(*font_);
  font_ = nullptr;
  if (!LoadGlyphs(more)) {
    never_.insert(want_.begin(), want_.end());
    if (!LoadGlyphs(old)) return false;
  }
  want_.clear();
  screen_.fresh = true;
  return true;
}

// A character's glyph, one cell wide: a similar one or "?" for one the font has not, a space for blanks.
uint32_t GlassesTerminal::Glyph(uint32_t cp) {
  if (auto it = glyphs_.find(cp); it != glyphs_.end()) return it->second;
  uint32_t g = unknown_;
  if (cp == 0 || cp == ' ') {
    g = space_;
  } else if (loaded_.count(cp)) {
    std::string text = Utf8(cp);
    auto gl = g1::GlyphIndices(*font_, text);
    int width = 0;
    font_->Measure(text, &width);
    if (gl.size() == 1 && width == geo_.cw) g = gl[0];
  } else if (has_.count(cp) && !never_.count(cp)) {
    want_.insert(cp);
    return g;
  }
  if (g == unknown_)
    if (auto s = kSimilar.find(cp); s != kSimilar.end()) g = Glyph(s->second);
  glyphs_[cp] = g;
  return g;
}

// A row's record (screen.go): runs of cells with equal levels, blank cells (spaces without a background or
// underline) left out between runs.
std::vector<uint8_t> GlassesTerminal::Encode(const Cell *cells, int n) {
  std::vector<uint8_t> out{0};
  auto blank = [&](const Cell &c, int bg, bool line) {
    Levels l = LevelsOf(c);
    return FirstCodePoint(c.text) == ' ' && l.bg == 0 && !l.line && bg == 0 && !line;
  };
  for (int x = 0; x < n && out[0] < kMaxRuns;) {
    Levels lv = LevelsOf(cells[x]);
    if (blank(cells[x], lv.bg, lv.line)) {
      x++;
      continue;
    }
    bool last = out[0] == kMaxRuns - 1; // takes the rest of the row as it is
    int end = x, ink = x;
    while (end < n && end - x < 80) {
      const Cell &c = cells[end];
      if (blank(c, lv.bg, lv.line)) {
        if (end - ink >= 7 && !last) break;
        end++;
        continue;
      }
      Levels l = LevelsOf(c);
      if (!last && (l.fg != lv.fg || l.bg != lv.bg || l.line != lv.line)) break;
      end++;
      ink = end;
    }
    end = ink;
    std::vector<uint8_t> v;
    for (int i = x; i < end; i++) Varints(v, Glyph(cells[i].text.empty() ? ' ' : FirstCodePoint(cells[i].text)));
    out.insert(out.end(), {uint8_t(x), uint8_t(end - x), uint8_t(lv.bg << 4 | lv.fg), uint8_t(lv.line), uint8_t(v.size())});
    out.insert(out.end(), v.begin(), v.end());
    out[0]++;
    x = end;
  }
  return out;
}

std::vector<GlassesTerminal::Row> GlassesTerminal::Rows(const Snapshot &s) {
  std::vector<Row> rows(geo_.rows);
  int cols = std::min(s.cols, geo_.cols);
  for (int y = 0; y < geo_.rows; y++) {
    if (y < s.rows) rows[y].rec = Encode(&s.cells[size_t(y) * s.cols], cols);
    else rows[y].rec = {0};
    if (s.cursor_visible && y == s.cursor_row && s.cursor_col < geo_.cols) rows[y].cursor = s.cursor_col;
  }
  return rows;
}

bool GlassesTerminal::Start(const Config &config, std::vector<uint8_t> font, std::function<void()> changed,
                            std::string *error) {
  blob_ = std::move(font);
  changed_ = std::move(changed);
  angle_ = config.angle;
  arms_ = {&g1::left, &g1::right};
  for (int i = 0; i < 2; i++) present_[i] = arms_[i]->GetState().present;
  has_ = Codepoints(blob_);
  std::set<uint32_t> base;
  for (uint32_t c : has_)
    for (auto &r : kBaseRanges)
      if (c >= r[0] && c <= r[1]) base.insert(c);
  if (has_.empty() || !LoadGlyphs(base)) {
    *error = "cannot load the terminal font";
    return false;
  }
  int vw = 640 + std::abs(config.dx), vh = 480 - std::abs(config.dy);
  int cw = 0;
  font_->Measure("M", &cw);
  geo_.cw = cw;
  geo_.ch = font_->line_spacing;
  geo_.ascent = font_->ascent;
  geo_.cols = std::min(255, (vw - 2 * std::abs(config.dx)) / geo_.cw);
  geo_.rows = std::min(255, vh / geo_.ch);
  geo_.x = (vw - geo_.W()) / 2;
  geo_.y = (vh - geo_.rows * geo_.ch) / 2;
  // The battery band's font: the digits (glyphs 0 ... 9, VM.md §4.1) and % at 2x.
  sk::Font &batt = sk::LoadFont(std::span<const uint8_t>(blob_), "0123456789%", nullptr, 2);
  auto percent = batt.pimpl ? g1::GlyphIndices(batt, "%") : std::vector<uint32_t>{};
  if (percent.size() != 1 || percent[0] > 127 || g1::GlyphIndices(batt, "0") != std::vector<uint32_t>{0}) {
    if (batt.pimpl) sk::Unload(batt);
    *error = "cannot load the battery font";
    return false;
  }
  batt_font_ = &batt;
  batt.Measure("0", &geo_.bcw);
  geo_.bascent = batt.ascent;
  geo_.bw = (4 * geo_.bcw + 2 + 2 * kBattPad + 1) & ~1;
  geo_.bh = batt.line_spacing + 2 + 2 * kBattPadY;
  int64_t base_address = g1::Alloc(MakeLayout(0, geo_).end);
  if (base_address < 0) {
    *error = "the terminal does not fit the arena";
    return false;
  }
  l_ = MakeLayout(uint32_t(base_address), geo_);
  for (int i = 0; i < 2; i++) {
    if (!present_[i]) continue;
    pos_[i] = i == 0 ? std::array<int, 2>{geo_.x - std::max(0, -config.dx), geo_.y + std::max(0, -config.dy)}
                     : std::array<int, 2>{geo_.x - std::max(0, config.dx), geo_.y + std::max(0, config.dy)};
    // The battery band in the panel's margin beside the terminal (outside the shared area while there is
    // room), centred vertically: the left eye's on the left, the right eye's on the right.
    int right = pos_[i][0] + geo_.W();
    int bx = i == 0 ? std::max(2, (pos_[i][0] - geo_.bw) / 2) : std::min(640 - geo_.bw - 2, right + (640 - right - geo_.bw) / 2);
    g1::Program p = MakeProgram(l_, geo_, pos_[i][0], pos_[i][1], bx, BattY(geo_), angle_, config.ignore_worn);
    if (!p.Valid(error)) return false;
    if (p.Here() > l_.state) {
      *error = "the terminal program overlaps its state";
      return false;
    }
    arms_[i]->WriteArena(l_.state, std::vector<uint8_t>(4 * kStateWords + geo_.rows, 0));
    std::vector<uint8_t> band(l_.end - l_.batt, 0);
    band.back() = uint8_t(percent[0]);
    arms_[i]->WriteArena(l_.batt, band);
    if (!arms_[i]->Load(p) || !arms_[i]->Start(l_.base)) {
      *error = "the terminal program did not start";
      return false;
    }
  }
  LOG("terminal: %d x %d at %u, angle %d", geo_.cols, geo_.rows, l_.base, angle_);
  stop_ = false;
  thread_ = std::thread([this] { Run(); });
  for (int i = 0; i < 2; i++)
    if (present_[i]) event_threads_[i] = std::thread([this, i] { Events(i); });
  return true;
}

void GlassesTerminal::Stop(bool keep) {
  {
    std::lock_guard<std::mutex> lock(mutex_);
    stop_ = true;
  }
  cv_.notify_all();
  if (thread_.joinable()) thread_.join();
  for (auto &t : event_threads_)
    if (t.joinable()) t.join();
  for (int i = 0; i < 2; i++) {
    if (!present_[i]) continue;
    if (!keep) {
      arms_[i]->Stop();
      continue;
    }
    WriteWord(i, wForce, 0);
    WriteWord(i, wPeer, 0);
    arms_[i]->Raise(g1::TRIGGER_USER2);
  }
  if (font_) sk::Unload(*font_);
  if (batt_font_) sk::Unload(*batt_font_);
  font_ = batt_font_ = nullptr;
  if (l_.base) g1::Free(l_.base);
  l_ = {};
}

void GlassesTerminal::Show(std::shared_ptr<const Snapshot> s) {
  int64_t now = g1::NowUs();
  {
    std::lock_guard<std::mutex> lock(mutex_);
    if (!pending_) first_us_ = now;
    last_us_ = now;
    pending_ = std::move(s);
  }
  cv_.notify_all();
}

void GlassesTerminal::Force(bool on) {
  {
    std::lock_guard<std::mutex> lock(mutex_);
    force_ = on;
  }
  cv_.notify_all();
}

bool GlassesTerminal::Shown() {
  std::lock_guard<std::mutex> lock(mutex_);
  return shown_[0] || shown_[1];
}

void GlassesTerminal::Events(int arm) {
  while (true) {
    {
      std::lock_guard<std::mutex> lock(mutex_);
      if (stop_) return;
    }
    g1::Event e;
    if (!arms_[arm]->NextEvent(&e, 500)) continue;
    {
      std::lock_guard<std::mutex> lock(mutex_);
      events_.push_back({arm, e});
    }
    cv_.notify_all();
  }
}

void GlassesTerminal::WriteWord(int arm, int word, uint32_t value) {
  arms_[arm]->WriteArena(l_.state + 4 * word, Words({value}));
}

// Both eyes follow the latest decision an arm took itself (the first NOTIFY wins near the threshold, R24).
void GlassesTerminal::SyncPeers(int from) {
  for (int i = 0; i < 2; i++) {
    if (!present_[i] || i == from || bool(shown_[i]) == desired_ || force_sent_) continue;
    WriteWord(i, wPeer, desired_ ? 1 : 2);
    arms_[i]->Raise(g1::TRIGGER_USER2);
  }
}

// A stopped program (the arm rebooted, or it halted) starts again and the next frame redraws everything.
void GlassesTerminal::Restart(int arm) {
  LOG("terminal: arm %d program stopped, starting it again", arm);
  WriteWord(arm, wNext, 0);
  WriteWord(arm, wForce, force_sent_);
  WriteWord(arm, wPeer, 0);
  shown_[arm] = 0;
  screen_.fresh = true;
  if (!pending_ && last_) { // the screen again, in full (R5)
    pending_ = last_;
    first_us_ = last_us_ = g1::NowUs();
  }
  if (inflight_) done_[arm] = true;
  arms_[arm]->Start(l_.base);
  SyncPeers(-1); // the restarted eye follows the other's state
}

void GlassesTerminal::Handle(const ArmEvent &ae) {
  const g1::Event &e = ae.e;
  if (e.type != g1::EVENT_NOTIFY) LOG("terminal: arm %d event %d trigger %d code %d", ae.arm, e.type, e.trigger, e.code);
  if (e.type == g1::EVENT_RESET || e.type == g1::EVENT_HALT) return Restart(ae.arm);
  if (e.type != g1::EVENT_NOTIFY) return;
  if (e.trigger == g1::TRIGGER_USER1) {
    if (e.a == inflight_) done_[ae.arm] = true;
    return;
  }
  bool was = shown_[0] || shown_[1];
  LOG("terminal: arm %d %s (%s), pitch %d", ae.arm, e.a & 1 ? "shown" : "hidden", e.a & kByHost ? "host" : "own",
      int(e.b));
  shown_[ae.arm] = e.a & 1;
  if (!(e.a & kByHost)) desired_ = e.a & 1;
  SyncPeers(ae.arm);
  if (bool(shown_[0] || shown_[1]) != was) notify_ = true;
}

// The rows that differ from what the arms hold (also after a hidden spell, R26), to both arms.
void GlassesTerminal::Send() {
  std::shared_ptr<const Snapshot> s;
  s.swap(pending_);
  if (!font_) return;
  last_ = s;
  std::vector<Row> rows = Rows(*s);
  if (!want_.empty()) {
    if (!Grow()) return;
    rows = Rows(*s);
  }
  Frame f;
  if (!screen_.Next(rows, &f)) return;
  frame_++;
  size_t bytes = f.dirty.size() + 4 * kStateWords;
  for (auto &[slot, rec] : f.recs) bytes += rec.size();
  int64_t lead = 30000;
  for (int i = 0; i < 2; i++) {
    if (!present_[i]) continue;
    auto st = arms_[i]->GetState();
    lead = std::max<int64_t>(lead, 2 * st.interval_us + st.clock_error_us + 15000);
  }
  lead += int64_t(bytes / 200 + 1) * 10000 + int64_t(f.recs.size()) * 2000;
  uint32_t cur = f.cur_slot >= 0 ? uint32_t(f.cur_slot) : kNoCursor;
  uint32_t font = g1::AddressOf(*font_), batt_font = batt_font_ ? g1::AddressOf(*batt_font_) : 0;
  for (int i = 0; i < 2; i++) {
    done_[i] = !present_[i];
    if (!present_[i]) continue;
    g1::Arm &arm = *arms_[i];
    for (auto &[slot, rec] : f.recs) arm.WriteArena(l_.ring + uint32_t(slot * kRecordBytes), rec);
    arm.WriteArena(l_.dirty, f.dirty);
    arm.WriteArena(l_.state, Words({uint32_t(f.top), uint32_t(f.full), cur, uint32_t(f.cur_x),
                                    arm.DeviceTime(lead), frame_, font, batt_font}));
    arm.Raise(g1::TRIGGER_USER0);
  }
  inflight_ = frame_;
  inflight_us_ = g1::NowUs();
}

void GlassesTerminal::Run() {
  std::unique_lock<std::mutex> lock(mutex_);
  while (!stop_) {
    int64_t now = g1::NowUs();
    bool visible = shown_[0] || shown_[1];
    int64_t wake = now + 1000000;
    if (pending_ && !inflight_ && visible) wake = std::min(last_us_ + 10000, first_us_ + 40000);
    if (inflight_) wake = std::min(wake, inflight_us_ + 10000000);
    if (events_.empty() && force_ == force_sent_ && wake > now)
      cv_.wait_for(lock, std::chrono::microseconds(wake - now));
    if (stop_) break;
    while (!events_.empty()) {
      ArmEvent ae = events_.front();
      events_.pop_front();
      Handle(ae);
    }
    if (force_ != force_sent_) {
      force_sent_ = force_;
      for (int i = 0; i < 2; i++) {
        if (!present_[i]) continue;
        arms_[i]->WriteArena(l_.state + 4 * wForce, Words({uint32_t(force_sent_), 0}));
        arms_[i]->Raise(g1::TRIGGER_USER2);
      }
    }
    now = g1::NowUs();
    if (inflight_ && ((done_[0] && done_[1]) || now - inflight_us_ > 10000000)) {
      if (!(done_[0] && done_[1])) screen_.fresh = true;
      inflight_ = 0;
      cv_.notify_all(); // Check waits for this
    }
    if (pending_ && !inflight_ && (shown_[0] || shown_[1]) &&
        (now - last_us_ >= 10000 || now - first_us_ >= 40000))
      Send();
    if (notify_ && changed_) {
      notify_ = false;
      lock.unlock();
      changed_();
      lock.lock();
    }
  }
}

std::string GlassesTerminal::Check() {
  std::vector<Row> rows;
  {
    std::unique_lock<std::mutex> lock(mutex_);
    cv_.wait_for(lock, std::chrono::seconds(3), [&] { return !inflight_ && !pending_; });
    if (last_) rows = Rows(*last_);
  }
  std::string out;
  bool ok = true;
  auto fail = [&](const std::string &line) {
    out += line + "\n";
    ok = false;
  };
  int ch = geo_.ch;
  std::vector<uint8_t> zeros(size_t(320) * ch, 0);
  uint32_t blank = uint32_t(crc32(0, zeros.data(), uInt(zeros.size())));
  for (int i = 0; i < 2; i++) {
    if (!present_[i]) continue;
    std::string arm = i ? "R" : "L";
    // The records, dirty bytes and frame words as the host sent them (the library's mirror).
    const std::pair<uint32_t, uint32_t> regions[] = {
        {l_.ring, uint32_t(geo_.rows * kRecordBytes)}, {l_.dirty, uint32_t(geo_.rows)}, {l_.state, 4 * wNext}};
    for (auto [address, length] : regions) {
      uint32_t got = 0, want = 1;
      if (!arms_[i]->ArenaCrc(address, length, &got, &want) || got != want)
        fail(arm + ": arena at " + std::to_string(address) + " differs from what was sent");
    }
    // Each text row on the panel: ink exactly where the screen has some (a run or the cursor). The rows beside
    // the battery band are left out: it has ink of its own.
    int shown = 0, wrong = 0, compared = 0;
    for (int y = 0; y < int(rows.size()); y++) {
      int top = pos_[i][1] + y * ch;
      if (top < BattY(geo_) + geo_.bh && top + ch > BattY(geo_)) continue;
      compared++;
      uint32_t got = 0, library = 0;
      if (!arms_[i]->PanelCrc(top, ch, &got, &library)) {
        fail(arm + ": no panel read-back (panel off?)");
        break;
      }
      bool ink = got != blank, want = rows[y].rec[0] > 0 || rows[y].cursor >= 0;
      shown += ink;
      wrong += ink != want;
    }
    if (wrong) fail(arm + ": " + std::to_string(wrong) + " text rows differ in ink from the screen");
    out += arm + ": " + std::to_string(shown) + " of " + std::to_string(compared) + " text rows inked\n";
  }
  return ok ? "ok\n" + out : out;
}

} // namespace ssh
