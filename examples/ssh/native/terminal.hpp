#pragma once

#include <array>
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <set>
#include <string>
#include <thread>
#include <vector>

#include "base/scouter_kit.hpp"
#include "g1/scouter_kit_g1.hpp"
#include "mosh_client.hpp"

namespace ssh {

// The terminal on the arms (R22–R26), ported from go/examples/term: a ring of row slots in each arm's arena,
// run records that the arm renders (USER0) and presents in sync with the other eye (USER1), the font's base
// glyphs at the start and others as the screen shows them. The arms decide visibility themselves: TILT on
// PITCH (shown from `angle` up, hidden 5° lower after a 1 s hold), USER2 for the host's wForce (keyer held,
// obeyed while the host is linked) and wPeer (the other eye's decision), on top of the library's PanelPolicy
// (off while folded or in the case, also with wForce). The host sends rows only while an eye shows the
// terminal. While the head is up (the looking-up rule, also when forced), each arm adds its own battery level
// (BATT_PCT, redrawn on BATTERY): the left arm at its panel's centre left, the right arm at the centre right, in
// the font at 2x in a 1 px white frame on black.
class GlassesTerminal {
public:
  struct Config {
    int dx = 100, dy = 9, angle = 20;
    bool ignore_worn = false; // debug: without PanelPolicy, to show with the glasses folded on the table
  };
  // After g1::Init. `font`: a font blob (departure_mono_nerd.skf); `changed` runs when visibility changes.
  bool Start(const Config &, std::vector<uint8_t> font, std::function<void()> changed, std::string *error);
  // keep: the program and its last screen stay on the arms, which show them by themselves when the head
  // looks up (keyer off: the phone lets go); wForce and wPeer are cleared first.
  void Stop(bool keep = false);
  void Show(std::shared_ptr<const Snapshot>); // a new screen, from any thread
  void Force(bool on);
  bool Shown();
  // Read-back (T3): both arms' rendered areas are equal and not blank, and each eye's panel rows hold that
  // area at the eye's offset (PanelCrc). One line per check; "ok" when all pass.
  std::string Check();
  int cols() const { return geo_.cols; }
  int rows() const { return geo_.rows; }

  struct Geometry {
    int cols, rows, cw, ch, ascent, x, y;
    int bw, bh, bcw, bascent; // the battery band (even width) and its font's advance and ascent
    int W() const { return (cols * cw + 1) & ~1; } // even: 4-bpp bytes
  };
  struct Layout {
    uint32_t base, state, dirty, ring, area, batt, batt_text, end;
  };
  struct Row {
    std::vector<uint8_t> rec;
    int cursor = -1;
    std::string Key() const;
  };
  struct Frame {
    int top = 0, cur_slot = -1, cur_x = 0;
    bool full = false;
    std::vector<uint8_t> dirty;
    std::map<int, std::vector<uint8_t>> recs;
  };
  // What the arms hold: the row each slot was last rendered with, and the slot of row 0 (screen.go).
  struct Screen {
    std::vector<std::string> slots;
    int top = 0;
    bool fresh = true;
    bool Next(const std::vector<Row> &rows, Frame *f);
  };

private:
  struct ArmEvent {
    int arm;
    scouter_kit::g1::Event e;
  };
  void Run();
  void Events(int arm);
  void Handle(const ArmEvent &);
  void Send();
  void SyncPeers(int from);
  void Restart(int arm);
  void WriteWord(int arm, int word, uint32_t value);
  std::vector<Row> Rows(const Snapshot &);
  std::vector<uint8_t> Encode(const Cell *cells, int n);
  uint32_t Glyph(uint32_t cp);
  bool LoadGlyphs(const std::set<uint32_t> &chars);
  bool Grow();

  Geometry geo_{};
  Layout l_{};
  int angle_ = 20;
  std::array<std::array<int, 2>, 2> pos_{};
  std::array<scouter_kit::g1::Arm *, 2> arms_{};
  std::array<bool, 2> present_{};
  std::vector<uint8_t> blob_;
  scouter_kit::Font *font_ = nullptr;
  scouter_kit::Font *batt_font_ = nullptr; // digits and % at 2x, for the battery band
  std::set<uint32_t> has_, loaded_, want_, never_;
  std::map<uint32_t, uint32_t> glyphs_;
  uint32_t space_ = 0, unknown_ = 0;
  Screen screen_;
  uint32_t frame_ = 0;
  std::function<void()> changed_;

  std::mutex mutex_; // guards the fields below
  std::condition_variable cv_;
  bool stop_ = false;
  std::deque<ArmEvent> events_;
  std::shared_ptr<const Snapshot> pending_, last_; // the screen to send, and the one sent last
  int64_t first_us_ = 0, last_us_ = 0; // when the pending screen first and last changed
  bool force_ = false, force_sent_ = false;
  std::array<int, 2> shown_{}; // as the arms last said: 0 hidden, 1 shown
  bool desired_ = false;       // the latest decision an arm took itself
  uint32_t inflight_ = 0;      // the frame sent and not shown by every arm yet
  int64_t inflight_us_ = 0;
  std::array<bool, 2> done_{};
  bool notify_ = false; // visibility changed: call changed_ outside the lock

  std::thread thread_;
  std::array<std::thread, 2> event_threads_;
};

} // namespace ssh
