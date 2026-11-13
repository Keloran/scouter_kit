#include "session.hpp"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <thread>

namespace g1 = scouter_kit::g1;
namespace sk = scouter_kit;
namespace gray = scouter_kit::gray;

#include "base/departure_mono.inc"

namespace chewedfeed {

namespace {

g1::Arm &ArmAt(int i) { return i == 0 ? g1::left : g1::right; }

int Value(uint32_t v) { return v == g1::UNKNOWN ? kUnknown : int(int32_t(v)); }

// Bind the fallback to this session's signal, never to a global current session.
// Join the timer before Stop returns, even when graceful cleanup wins the race.
class StopDeadline {
public:
  explicit StopDeadline(std::optional<g1::Cancellation> token) : worker_([this, token] {
    std::unique_lock<std::mutex> lock(mutex_);
    if (!ended_.wait_for(lock, std::chrono::milliseconds(500), [&] { return done_; }) && token)
      token->Cancel();
  }) {}
  ~StopDeadline() {
    { std::lock_guard<std::mutex> lock(mutex_); done_ = true; }
    ended_.notify_all();
    worker_.join();
  }
private:
  std::mutex mutex_;
  std::condition_variable ended_;
  bool done_ = false;
  std::thread worker_;
};

bool g_trace = false; // the start's log: each step and how long it took (stderr)

void Step(const char *what, std::chrono::steady_clock::time_point &t) {
  if (!g_trace) return;
  auto now = std::chrono::steady_clock::now();
  std::fprintf(stderr, "[chewedfeed session] %s (%lld ms)\n", what,
               (long long)std::chrono::duration_cast<std::chrono::milliseconds>(now - t).count());
  t = now;
}

} // namespace

StartResult Session::Start(const StartConfig &config, g1::Transport *transport, std::function<void()> on_cancel) {
  g1::Cancellation token;
  {
    std::lock_guard<std::mutex> lock(mutex_);
    if (starting_ || generation_ != 0) return {0, kBusy, "a session is already running"};
    {
      std::lock_guard<std::mutex> guard(cancellation_mutex_);
      cancellation_ = token;
      pending_ = true;
      on_cancel_ = std::move(on_cancel);
    }
    starting_ = true;
  }
  g1::GrayConfig gray{config.dx, config.dy};
  g1::Connection connection;
  connection.left = config.left;
  connection.right = config.right;
  connection.transport = transport;
  connection.log = config.log;
  connection.keep_sessions = false; // a fresh session: never adopt the previous host's program or memory
  std::string error;
  g_trace = config.log;
  auto t = std::chrono::steady_clock::now();
  g1::InitError e = g1::InitCancelable(token, &gray, &error, &connection);
  Step("Init returned", t);

  std::unique_lock<std::mutex> lock(mutex_);
  StartResult result;
  if (e != g1::ERROR_NONE) {
    bool cancelled = token.Cancelled();
    result = {0, cancelled ? kCancelled : int(e), cancelled ? "cancelled" : error};
  } else if (token.Cancelled()) {
    g1::Deinit();
    result = {0, kCancelled, "cancelled"};
  } else {
    for (int i = 0; i < 2; i++) {
      present_[i] = ArmAt(i).GetState().present;
      if (!present_[i]) continue;
#ifdef SCOUTER_G1_EMULATOR
      g1::emulator::SetProperty(i, g1::PROP_WORN, 1); // worn, head level, as real sensors would say
      g1::emulator::SetProperty(i, g1::PROP_PITCH, 0);
#endif
      // The previous host's image may still be on the panel: off until our first full frame.
      ArmAt(i).SetPanel(false);
      Step(i ? "right: panel off" : "left: panel off", t);
      ArmAt(i).ReportInputs(1u << g1::TRIGGER_TOUCH | 1u << g1::TRIGGER_CASE);
      Step(i ? "right: inputs" : "left: inputs", t);
      ArmAt(i).SetBrightness(std::clamp(config.brightness, 0, 42));
      Step(i ? "right: brightness" : "left: brightness", t);
    }
    if (token.Cancelled()) {
      g1::Deinit();
      result = {0, kCancelled, "cancelled"};
    } else {
      generation_ = ++last_generation_;
      result = {generation_, 0, {}};
    }
  }
  {
    std::lock_guard<std::mutex> guard(cancellation_mutex_);
    pending_ = false;
    on_cancel_ = nullptr;
    if (!result.generation) cancellation_.reset();
  }
  starting_ = false;
  idle_.notify_all();
  return result;
}

void Session::Cancel() {
  std::function<void()> hook;
  {
    std::lock_guard<std::mutex> guard(cancellation_mutex_);
    if (!pending_) return;
    if (cancellation_) cancellation_->Cancel();
    hook = on_cancel_;
  }
  if (hook) hook();
}

void Session::Stop() {
  std::optional<g1::Cancellation> token;
  {
    std::lock_guard<std::mutex> guard(cancellation_mutex_);
    token = cancellation_;
  }
  StopDeadline deadline(token);
  Cancel();
  std::unique_lock<std::mutex> lock(mutex_);
  idle_.wait(lock, [&] { return !starting_; });
  Close();
  {
    std::lock_guard<std::mutex> guard(cancellation_mutex_);
    cancellation_.reset();
  }
}

void Session::Close() {
  if (generation_ == 0) return;
  // Leave nothing of ours on show: black, flushed, panels off. Then Deinit
  // stops reconnecting, joins the library's threads and frees every font and
  // bitmap (CF-30); our handles go with it.
  gray::Clear();
  sk::Flush();
  for (int i = 0; i < 2; i++)
    if (present_[i]) {
      ArmAt(i).ReportInputs(0);
      ArmAt(i).SetPanel(false);
    }
  g1::Deinit();
  fonts_.clear();
  canvas_ = nullptr;
  generation_ = 0;
}

uint64_t Session::Generation() {
  std::lock_guard<std::mutex> lock(mutex_);
  return generation_;
}

const std::vector<uint8_t> &DepartureMono() {
  static const std::vector<uint8_t> blob(kDepartureMonoBlob, kDepartureMonoBlob + sizeof kDepartureMonoBlob);
  return blob;
}

int Session::LoadFontBlob(uint64_t gen, const std::vector<uint8_t> &blob, std::string *error) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || blob.empty() || fonts_.size() >= 8) return -1;
  sk::Font &f = sk::LoadFont(std::span<const uint8_t>(blob.data(), blob.size()), "", error, 1);
  if (!f.pimpl) return -1;
  fonts_.push_back(&f);
  return int(fonts_.size()) - 1;
}

int Session::LoadFont(uint64_t gen, int scale, std::string *error) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || scale < 1 || scale > 4 || fonts_.size() >= 8) return -1;
  sk::Font &f = sk::LoadFont(std::span<const uint8_t>(kDepartureMonoBlob, sizeof kDepartureMonoBlob), "", error,
                             scale);
  if (!f.pimpl) return -1;
  fonts_.push_back(&f);
  return int(fonts_.size()) - 1;
}

bool Session::FontMetrics(uint64_t gen, int font, int out[3]) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || font < 0 || size_t(font) >= fonts_.size()) return false;
  out[0] = fonts_[font]->ascent;
  out[1] = fonts_[font]->descent;
  out[2] = fonts_[font]->line_spacing;
  return true;
}

int Session::Measure(uint64_t gen, int font, std::string_view text) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || font < 0 || size_t(font) >= fonts_.size()) return -1;
  int width = 0;
  fonts_[font]->Measure(text, &width);
  return width;
}

bool Session::Canvas(uint64_t gen, int width, int height) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || width <= 0 || height <= 0 || width > 2048 || height > 2048) return false;
  if (canvas_) gray::Unload(*canvas_);
  canvas_ = &gray::CreateBitmap(width, height);
  if (!canvas_->pimpl) canvas_ = nullptr;
  return canvas_ != nullptr;
}

bool Session::Clear(uint64_t gen, uint8_t gray) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || !canvas_) return false;
  gray::Fill(*canvas_, 0, 0, canvas_->contents.width, canvas_->contents.height, gray);
  return true;
}

bool Session::Fill(uint64_t gen, int x, int y, int w, int h, uint8_t gray) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || !canvas_) return false;
  gray::Fill(*canvas_, x, y, w, h, gray);
  return true;
}

bool Session::Text(uint64_t gen, int font, std::string_view text, int x, int baseline, int align, uint8_t gray) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || !canvas_ || font < 0 || size_t(font) >= fonts_.size() || align < 0 || align > 2) return false;
  fonts_[font]->DrawLine(*canvas_, text, x, baseline, sk::TextAlign(align), nullptr, gray);
  return true;
}

bool Session::Present(uint64_t gen, int x, int y) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || !canvas_) return false;
  gray::Clear();
  gray::Draw(*canvas_, x, y);
  sk::Flush();
  for (int i = 0; i < 2; i++)
    if (present_[i] && !ArmAt(i).GetState().panel_on) ArmAt(i).SetPanel(true);
  return true;
}

bool Session::Blank(uint64_t gen) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen)) return false;
  // Clear the compositor too, so presenting the same frame again sends its pixels.
  gray::Clear();
  sk::Flush();
  for (int i = 0; i < 2; i++)
    if (present_[i]) ArmAt(i).SetPanel(false);
  return true;
}

Status Session::GetStatus(uint64_t gen) {
  std::lock_guard<std::mutex> lock(mutex_);
  Status s;
  if (!Live(gen)) return s;
  s.generation = gen;
  s.width = gray::display.width;
  s.height = gray::display.height;
  const uint8_t ids[] = {g1::PROP_BATT_PCT, g1::PROP_IN_CASE, g1::PROP_PITCH, g1::PROP_ROLL, g1::PROP_CASE_BATT_PCT};
  for (int i = 0; i < 2; i++) {
    ArmStatus &a = s.arms[i];
    a.present = present_[i];
    if (!a.present) continue;
    a.connected = ArmAt(i).GetState().connected;
    uint32_t v[5];
    auto t = std::chrono::steady_clock::now();
    bool ok = a.connected && ArmAt(i).Props(ids, v);
    Step(i ? "right: status props" : "left: status props", t);
    if (!ok) continue;
    a.battery = Value(v[0]);
    a.in_case = Value(v[1]);
    if (s.pitch == kUnknown) s.pitch = Value(v[2]), s.roll = Value(v[3]); // both arms report the same angles
    if (int c = Value(v[4]); c != kUnknown && c <= 100) s.case_battery = c;
  }
  return s;
}

std::vector<InputEvent> Session::Events(uint64_t gen, size_t max) {
  std::lock_guard<std::mutex> lock(mutex_);
  std::vector<InputEvent> out;
  if (!Live(gen)) return out;
  for (int i = 0; i < 2 && out.size() < max; i++) {
    if (!present_[i]) continue;
    g1::Event e;
    while (out.size() < max && ArmAt(i).NextEvent(&e, 0)) out.push_back({i, int(e.type), e.trigger, e.a, e.b});
  }
  return out;
}

} // namespace chewedfeed
