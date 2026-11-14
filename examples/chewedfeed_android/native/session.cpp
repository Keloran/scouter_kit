#include "session.hpp"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <thread>

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
  dx_ = config.dx, dy_ = config.dy;
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
      // TILT too: the arm streams head angles only while a program runs or a TILT event can be reported (firmware
      // exec.c imu_need). The desktop's standby program covers it; this app runs none.
      ArmAt(i).ReportInputs(1u << g1::TRIGGER_TOUCH | 1u << g1::TRIGGER_CASE | 1u << g1::TRIGGER_TILT);
      Step(i ? "right: inputs" : "left: inputs", t);
      ArmAt(i).SetBrightness(std::clamp(config.brightness, 0, 42));
      Step(i ? "right: brightness" : "left: brightness", t);
    }
    // Head angles for showing and hiding the dashboard (desktop session.go): a TILT range narrower than ±90 makes
    // the firmware stream every sample; without it PITCH stays at its first value. Both arms report the same
    // angles, so one arm is enough; restored on Close (sensor settings outlive the session).
    pitch_arm_ = present_[0] ? 0 : present_[1] ? 1 : -1;
    if (pitch_arm_ >= 0) ArmAt(pitch_arm_).SysWrite(g1::PROP_PITCH_LO, uint32_t(kPitchLo));
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
  if (pitch_arm_ >= 0) ArmAt(pitch_arm_).SysWrite(g1::PROP_PITCH_LO, uint32_t(kPitchLo - 1));
  pitch_arm_ = -1;
  for (int i = 0; i < 2; i++)
    if (present_[i]) {
      ArmAt(i).ReportInputs(0);
      ArmAt(i).SetPanel(false);
    }
  calibration_ = {};
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

namespace {
constexpr int kCalW = 560, kCalH = 360;
constexpr g1::Address kCalPosition = 3840; // the program's x, y words, after its code

// demo.PanelAt: where an image at virtual (x, y) lands on one arm's panel.
void PanelAt(int dx, int dy, int arm, int *x, int *y) {
  if (arm == 0) *x -= std::max(0, -dx), *y += std::max(0, -dy);
  else *x -= std::max(0, dx), *y += std::max(0, dy);
}

void Words(g1::Arm &arm, g1::Address at, int x, int y) {
  uint8_t b[8];
  for (int i = 0; i < 4; i++) b[i] = uint8_t(uint32_t(x) >> (8 * i)), b[4 + i] = uint8_t(uint32_t(y) >> (8 * i));
  arm.WriteArena(at, b);
}

void Paint(gray::Bitmap &b, sk::Font &font, sk::Font &numbers, int arm, int major) {
  const int cx = kCalW / 2, cy = kCalH / 2;
  auto line = [&](int x, int y, int w, int h, uint8_t g) { gray::Fill(b, x, y, w, h, g); };
  auto text = [&](const std::string &t, int x, int y, sk::TextAlign a) { font.DrawLine(b, t, x, y, a, nullptr, 204); };
  auto number = [&](const std::string &t, int x, int y, sk::TextAlign a) { numbers.DrawLine(b, t, x, y, a, nullptr, 255); };
  auto signed_ = [](int v) { char s[16]; std::snprintf(s, sizeof s, "%+d", v); return std::string(s); };
  line(0, 0, kCalW, kCalH, 0);
  if (arm == 0) {
    text("LEFT: pixel ruler", cx, 18, sk::ALIGN_CENTER);
    text("X: right + / left -    Y: down + / up -", cx, 34, sk::ALIGN_CENTER);
    for (int y = -120; y <= 120; y += 10)
      for (int x = -240; x <= 240; x += 10) line(cx + x, cy + y, 1, 1, 102);
    line(cx - 240, cy, 481, 1, 153);
    line(cx, cy - 120, 1, 241, 153);
    for (int x = -240; x <= 240; x += 10) {
      int n = x % major == 0 ? 7 : 3;
      if (x % major == 0 && x != 0) number(signed_(x), cx + x, cy + 12 + numbers.ascent, sk::ALIGN_CENTER);
      line(cx + x, cy - n, 1, 2 * n + 1, 204);
    }
    for (int y = -120; y <= 120; y += 10) {
      int n = y % major == 0 ? 7 : 3;
      if (y % major == 0 && y != 0) number(signed_(y), cx + 12, cy + y + numbers.ascent / 2, sk::ALIGN_LEFT);
      line(cx - n, cy + y, 2 * n + 1, 1, 204);
    }
    // A hollow square at the left eye's origin: the right plus should sit inside it.
    line(cx - 5, cy - 5, 11, 1, 255);
    line(cx - 5, cy + 5, 11, 1, 255);
    line(cx - 5, cy - 5, 1, 11, 255);
    line(cx + 5, cy - 5, 1, 11, 255);
    text("L 0,0", cx - 12, cy - 13, sk::ALIGN_RIGHT);
    text("Small marks: 10 px / numbers: " + std::to_string(major) + " px", cx, kCalH - 8, sk::ALIGN_CENTER);
  } else {
    text("RIGHT: crosshair", cx, 18, sk::ALIGN_CENTER);
    line(cx - 18, cy, 37, 1, 255);
    line(cx, cy - 18, 1, 37, 255);
    text("R", cx + 24, cy - 13, sk::ALIGN_LEFT);
  }
  number("0123456789", cx, kCalH - 30, sk::ALIGN_CENTER);
}
} // namespace

bool Session::CalibrationShow(uint64_t gen, const std::vector<uint8_t> &numbers_blob, int scale, int major) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || calibration_.on || scale < 1 || scale > 4 || major < 10) return false;
  if (!present_[0] || !present_[1]) return false; // two eyes to compare
  // Labels at scale 1 and only the ruler's digits at the dashboard size: two 560 x 360 patterns take most of the
  // arena (desktop calibration.go does the same).
  std::span<const uint8_t> builtin(kDepartureMonoBlob, sizeof kDepartureMonoBlob);
  sk::Font &font = sk::LoadFont(builtin, "", nullptr, 1);
  if (!font.pimpl) return false;
  fonts_.push_back(&font);
  sk::Font &numbers = numbers_blob.empty() ? sk::LoadFont(builtin, "+-0123456789", nullptr, scale)
                                           : sk::LoadFont(std::span<const uint8_t>(numbers_blob), "+-0123456789", nullptr, scale);
  if (!numbers.pimpl) return false;
  fonts_.push_back(&numbers);
  int64_t base = g1::Alloc(4096);
  if (base < 0) return false;
  gray::Bitmap *bitmaps[2];
  for (int i = 0; i < 2; i++) {
    bitmaps[i] = &gray::CreateBitmap(kCalW, kCalH);
    if (!bitmaps[i]->contents.data) return false;
    Paint(*bitmaps[i], font, numbers, i, major);
  }
  gray::Clear();
  sk::Flush(); // uploads both patterns; the programs draw them
  const int vw = gray::display.width, vh = gray::display.height;
  for (int i = 0; i < 2; i++) {
    int x = (vw - kCalW) / 2, y = (vh - kCalH) / 2;
    PanelAt(dx_, dy_, i, &x, &y);
    g1::Program p(g1::Address(base), 32);
    auto show = p.NewLabel();
    p.PanelPolicy(show);
    p.Bind(show);
    int stride = 0;
    g1::Address addr = g1::AddressOf(*bitmaps[i], &stride);
    g1::ArenaWord pos = g1::ArenaWord((base + kCalPosition) / 4);
    p.Draw(p.Const(addr), p.Const(uint32_t(stride)), p.Const(0), p.Const(0), pos, g1::ArenaWord(pos + 1), p.Const(kCalW),
           p.Const(kCalH));
    p.Notify(p.Const(1), p.Const(0));
    p.End();
    p.On(g1::TRIGGER_START, show);
    p.On(g1::TRIGGER_USER0, show);
    if (p.Here() > base + kCalPosition) return false; // the program would overlap its position words
    Words(ArmAt(i), g1::Address(base + kCalPosition), x, y);
    if (!ArmAt(i).Load(p) || !ArmAt(i).Start(g1::Address(base))) return false;
    if (i == 1) calibration_ = {true, g1::Address(base), dx_, dy_, x, y};
  }
  return true;
}

bool Session::CalibrationMove(uint64_t gen, int dx, int dy) {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!Live(gen) || !calibration_.on || dx < -240 || dx > 240 || dy < -120 || dy > 120) return false;
  // The left ruler holds still; dx moves the right image left, dy down (as the offsets would).
  Calibration &c = calibration_;
  int x = c.x + c.dx - dx, y = c.y + dy - c.dy;
  Words(g1::right, c.base + kCalPosition, x, y);
  g1::right.Raise(g1::TRIGGER_USER0);
  c.dx = dx, c.dy = dy, c.x = x, c.y = y;
  return true;
}

} // namespace chewedfeed
