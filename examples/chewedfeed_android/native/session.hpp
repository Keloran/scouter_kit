// The app's glasses session below JNI (jni.cpp), so the host tests can drive it
// with the emulated arms. One session at a time (ScouterKit is process-wide).
#pragma once

#include <condition_variable>
#include <cstdint>
#include <functional>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

#include "g1/scouter_kit_g1.hpp"

namespace chewedfeed {

constexpr int kUnknown = INT32_MIN; // a value the arm did not report

// The built-in Departure Mono blob (11 px), for scaling on the Kotlin side.
const std::vector<uint8_t> &DepartureMono();

struct StartConfig {
  std::string left, right; // bonded addresses; "" any bonded G1, "none" leave the arm out
  int dx = 0, dy = 0;      // eye alignment
  int brightness = 21;     // 0..42
  bool log = false;        // the library's log on stderr (jni.cpp forwards it to logcat)
};

struct StartResult {
  uint64_t generation = 0; // 0: failed
  int error = 0;           // scouter_kit::g1::InitError, or kCancelled / kBusy
  std::string message;
};

constexpr int kCancelled = 100;
constexpr int kBusy = 101;

struct ArmStatus {
  bool present = false, connected = false;
  int battery = kUnknown; // %
  int in_case = kUnknown; // 0/1
};

struct Status {
  uint64_t generation = 0; // 0: no session
  ArmStatus arms[2];
  int pitch = kUnknown, roll = kUnknown; // degrees: + looking up, + right ear down
  int case_battery = kUnknown;           // %, only while an arm is in the case
  int width = 0, height = 0;             // the virtual display
};

struct InputEvent {
  int arm;     // 0 left, 1 right
  int type;    // scouter_kit::g1::EventType
  int trigger; // INPUT: which trigger (TOUCH, CASE, ...)
  uint32_t a, b;
};

// Every call names the generation it belongs to; calls for any other one (a
// stopped or replaced session) change nothing and return false, so nothing is
// written to the glasses after Stop.
class Session {
public:
  // Connects both arms (through transport, or the emulated arms when it is
  // null in an emulator build) and prepares a fresh session: inputs reported,
  // panels off until the first Present. Blocks up to 30 s; Cancel ends it sooner.
  // on_cancel is an optional notification; native cancellation also works
  // without it. Transport callbacks must return promptly.
  StartResult Start(const StartConfig &config, scouter_kit::g1::Transport *transport,
                    std::function<void()> on_cancel = {});
  // From any thread while Start runs: it fails with kCancelled.
  void Cancel();
  // Blanks and powers off both panels, then deinitializes: the library's
  // threads are joined and reconnects stop before it returns. Idempotent.
  void Stop();

  uint64_t Generation();

  // The built-in Departure Mono at an integer scale: a font handle, or -1.
  int LoadFont(uint64_t gen, int scale, std::string *error);
  // A font blob (font_converter's format, e.g. DepartureMono() resampled to a fractional scale): a handle, or -1.
  int LoadFontBlob(uint64_t gen, const std::vector<uint8_t> &blob, std::string *error);
  // ascent, descent, line spacing.
  bool FontMetrics(uint64_t gen, int font, int out[3]);
  int Measure(uint64_t gen, int font, std::string_view text); // advance width, -1 on error

  // One canvas per session: draw into it, then Present.
  bool Canvas(uint64_t gen, int width, int height);
  bool Clear(uint64_t gen, uint8_t gray);
  bool Fill(uint64_t gen, int x, int y, int w, int h, uint8_t gray);
  bool Text(uint64_t gen, int font, std::string_view text, int x, int baseline, int align, uint8_t gray);
  // Shows the whole canvas at (x, y) on the virtual display (the first one of
  // a session switches the panels on).
  bool Present(uint64_t gen, int x, int y);
  // Black and off, until the next Present.
  bool Blank(uint64_t gen);

  Status GetStatus(uint64_t gen);
  // Events since the last call, oldest first (at most max).
  std::vector<InputEvent> Events(uint64_t gen, size_t max);

private:
  bool Live(uint64_t gen) const { return gen != 0 && gen == generation_; }
  void Close(); // mutex_ held

  std::mutex mutex_;
  // Independent of mutex_: cancellation must wake a call holding that mutex.
  std::mutex cancellation_mutex_;
  std::optional<scouter_kit::g1::Cancellation> cancellation_;
  bool pending_ = false;
  std::condition_variable idle_;
  bool starting_ = false;
  std::function<void()> on_cancel_;
  uint64_t generation_ = 0, last_generation_ = 0;
  std::vector<scouter_kit::Font *> fonts_;
  scouter_kit::gray::Bitmap *canvas_ = nullptr;
  bool present_[2] = {false, false};
};

} // namespace chewedfeed
