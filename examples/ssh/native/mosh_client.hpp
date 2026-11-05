#pragma once

#include <atomic>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

namespace ssh {

// Colours: 0 is the default, 1..256 a palette index + 1, kRgb | 0xRRGGBB.
constexpr uint32_t kRgb = 0x1000000;

struct Cell {
  std::string text; // UTF-8; empty is blank
  uint32_t fg = 0, bg = 0;
  bool bold = false, faint = false, italic = false, underline = false;
  bool inverse = false, invisible = false, wide = false;
  bool operator==(const Cell &) const = default;
};

struct Snapshot {
  int cols = 0, rows = 0;
  std::vector<Cell> cells; // rows x cols
  int cursor_row = 0, cursor_col = 0;
  bool cursor_visible = true;
  bool application_cursor_keys = false;
  std::string title;
  const Cell &At(int row, int col) const { return cells[size_t(row) * cols + col]; }
};

struct Status {
  bool heard = false;        // the server has answered
  bool ended = false;        // the server ended the session (its command exited)
  int64_t silent_ms = 0;     // since the server was last heard
  std::string error;         // the last network or crypto error, if any
};

// mosh's client without a tty (in place of STMClient): one thread on the UDP
// socket. `changed` runs on that thread after each new snapshot or status.
class MoshClient {
public:
  explicit MoshClient(std::function<void()> changed);
  ~MoshClient();
  bool Start(const std::string &ip, int port, const std::string &key, int cols, int rows,
             std::string *error);
  void Keys(const std::string &bytes);
  void Resize(int cols, int rows);
  std::shared_ptr<const Snapshot> Latest() const;
  Status GetStatus() const;
  // Paused: no traffic and no timers (keyer off: the phone may sleep), the session's state kept; mosh-server
  // keeps the session too, so resuming carries on with it (from a new address if it has changed).
  void Pause(bool on);
  void Stop();

private:
  struct Session;
  void Run();
  void Wake();

  std::function<void()> changed_;
  std::unique_ptr<Session> session_;
  std::thread thread_;
  std::atomic<bool> stop_{false}, paused_{false};
  int wake_fd_ = -1;
  mutable std::mutex mutex_; // guards the fields below
  std::string keys_;
  int resize_cols_ = 0, resize_rows_ = 0;
  std::shared_ptr<const Snapshot> latest_;
  Status status_;
};

} // namespace ssh
