#include "mosh_client.hpp"

#include <poll.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include <algorithm>
#include <cstdlib>
#include <cstring>

#include "completeterminal.h"
#include "fatal_assert.h"
#include "networktransport-impl.h"
#include "terminaloverlay.h"
#include "timestamp.h"
#include "user.h"

namespace ssh {

using Transport = Network::Transport<Network::UserStream, Terminal::Complete>;

struct MoshClient::Session {
  std::unique_ptr<Transport> net;
  Overlay::OverlayManager overlays;
  Terminal::Framebuffer shown{1, 1};
  bool first = true;
};

namespace {

void AppendUtf8(std::string &out, uint32_t c) {
  if (c < 0x80) {
    out += char(c);
  } else if (c < 0x800) {
    out += char(0xC0 | (c >> 6));
    out += char(0x80 | (c & 0x3F));
  } else if (c < 0x10000) {
    out += char(0xE0 | (c >> 12));
    out += char(0x80 | ((c >> 6) & 0x3F));
    out += char(0x80 | (c & 0x3F));
  } else {
    out += char(0xF0 | (c >> 18));
    out += char(0x80 | ((c >> 12) & 0x3F));
    out += char(0x80 | ((c >> 6) & 0x3F));
    out += char(0x80 | (c & 0x3F));
  }
}

// The colours of a rendition, from its SGR string (mosh keeps them private).
void Colours(const Terminal::Renditions &r, uint32_t *fg, uint32_t *bg) {
  std::string sgr = r.sgr();
  std::vector<int> n;
  for (size_t i = 2; i < sgr.size();) {
    size_t end = sgr.find_first_of(";m", i);
    if (end == std::string::npos) break;
    n.push_back(std::atoi(sgr.c_str() + i));
    i = end + 1;
  }
  *fg = *bg = 0;
  for (size_t i = 1; i < n.size(); i++) {
    int v = n[i];
    uint32_t *target = v == 38 || (v >= 30 && v <= 37) ? fg : v == 48 || (v >= 40 && v <= 47) ? bg : nullptr;
    if (!target) continue;
    if ((v == 38 || v == 48) && i + 1 < n.size()) {
      if (n[i + 1] == 5 && i + 2 < n.size()) {
        *target = uint32_t(n[i + 2]) + 1;
        i += 2;
      } else if (n[i + 1] == 2 && i + 4 < n.size()) {
        *target = kRgb | uint32_t(n[i + 2]) << 16 | uint32_t(n[i + 3]) << 8 | uint32_t(n[i + 4]);
        i += 4;
      }
    } else if (v != 38 && v != 48) {
      *target = uint32_t(v % 10) + 1;
    }
  }
}

std::shared_ptr<Snapshot> MakeSnapshot(const Terminal::Framebuffer &fb) {
  auto s = std::make_shared<Snapshot>();
  s->cols = fb.ds.get_width();
  s->rows = fb.ds.get_height();
  s->cells.resize(size_t(s->cols) * s->rows);
  for (int r = 0; r < s->rows; r++) {
    for (int c = 0; c < s->cols; c++) {
      const Terminal::Cell *in = fb.get_cell(r, c);
      Cell &out = s->cells[size_t(r) * s->cols + c];
      if (!in->is_blank()) in->print_grapheme(out.text);
      const Terminal::Renditions &rd = in->get_renditions();
      Colours(rd, &out.fg, &out.bg);
      out.bold = rd.get_attribute(Terminal::Renditions::bold);
      out.faint = rd.get_attribute(Terminal::Renditions::faint);
      out.italic = rd.get_attribute(Terminal::Renditions::italic);
      out.underline = rd.get_attribute(Terminal::Renditions::underlined);
      out.inverse = rd.get_attribute(Terminal::Renditions::inverse);
      out.invisible = rd.get_attribute(Terminal::Renditions::invisible);
      out.wide = in->get_wide();
    }
  }
  s->cursor_row = fb.ds.get_cursor_row();
  s->cursor_col = fb.ds.get_cursor_col();
  s->cursor_visible = fb.ds.cursor_visible;
  s->application_cursor_keys = fb.ds.application_mode_cursor_keys;
  for (wchar_t c : fb.get_window_title()) AppendUtf8(s->title, uint32_t(c));
  return s;
}

} // namespace

MoshClient::MoshClient(std::function<void()> changed) : changed_(std::move(changed)) {}

MoshClient::~MoshClient() { Stop(); }

bool MoshClient::Start(const std::string &ip, int port, const std::string &key, int cols, int rows,
                       std::string *error) {
  if (session_) {
    *error = "already started";
    return false;
  }
  auto session = std::make_unique<Session>();
  try {
    freeze_timestamp();
    Network::UserStream blank;
    Terminal::Complete local(cols, rows);
    std::string port_text = std::to_string(port);
    session->net = std::make_unique<Transport>(blank, local, key.c_str(), ip.c_str(), port_text.c_str());
    session->net->set_send_delay(1);
    session->net->get_current_state().push_back(Parser::Resize(cols, rows));
    session->shown = Terminal::Framebuffer(cols, rows);
  } catch (const Network::NetworkException &e) {
    *error = e.what();
    return false;
  } catch (const Crypto::CryptoException &e) {
    *error = e.what();
    return false;
  }
  wake_fd_ = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
  if (wake_fd_ < 0) {
    *error = std::strerror(errno);
    return false;
  }
  session_ = std::move(session);
  stop_ = false;
  thread_ = std::thread([this] { Run(); });
  return true;
}

void MoshClient::Wake() {
  uint64_t one = 1;
  if (wake_fd_ >= 0) (void)::write(wake_fd_, &one, sizeof(one));
}

void MoshClient::Keys(const std::string &bytes) {
  {
    std::lock_guard<std::mutex> lock(mutex_);
    keys_ += bytes;
  }
  Wake();
}

void MoshClient::Resize(int cols, int rows) {
  {
    std::lock_guard<std::mutex> lock(mutex_);
    resize_cols_ = cols;
    resize_rows_ = rows;
  }
  Wake();
}

std::shared_ptr<const Snapshot> MoshClient::Latest() const {
  std::lock_guard<std::mutex> lock(mutex_);
  return latest_;
}

Status MoshClient::GetStatus() const {
  std::lock_guard<std::mutex> lock(mutex_);
  return status_;
}

void MoshClient::Pause(bool on) {
  paused_ = on;
  Wake();
}

void MoshClient::Stop() {
  stop_ = true;
  Wake();
  if (thread_.joinable()) thread_.join();
  session_.reset();
  if (wake_fd_ >= 0) close(wake_fd_);
  wake_fd_ = -1;
}

void MoshClient::Run() {
  Session &s = *session_;
  Transport &net = *s.net;
  uint64_t resumed = 0; // silence counts from here after a pause
  while (!stop_) {
    if (paused_) {
      pollfd fd{wake_fd_, POLLIN, 0};
      if (poll(&fd, 1, -1) < 0 && errno != EINTR) break;
      uint64_t n;
      (void)::read(wake_fd_, &n, sizeof(n));
      if (paused_) continue;
      freeze_timestamp();
      resumed = timestamp();
      s.overlays.get_notification_engine().server_heard(resumed); // no "last contact" bar for the pause
      Wake(); // keys or a resize that came meanwhile
    }
    std::string error;
    try {
      Terminal::Framebuffer frame = net.get_latest_remote_state().state.get_fb();
      s.overlays.apply(frame);
      bool fresh = s.first || !(frame == s.shown);
      s.shown = frame;
      s.first = false;
      Status status;
      status.heard = net.get_remote_state_num() != 0;
      status.silent_ms = int64_t(timestamp() - std::max(net.get_latest_remote_state().timestamp, resumed));
      status.ended = net.counterparty_shutdown_ack_sent();
      std::shared_ptr<Snapshot> snapshot = fresh ? MakeSnapshot(frame) : nullptr;
      bool notify = fresh;
      {
        std::lock_guard<std::mutex> lock(mutex_);
        if (snapshot) latest_ = snapshot;
        status.error = status_.error;
        notify |= status.heard != status_.heard || status.ended;
        status_ = status;
      }
      if (notify && changed_) changed_();
      if (status.ended) break;

      int wait = std::min(net.wait_time(), s.overlays.wait_time());
      std::vector<pollfd> fds;
      for (int fd : net.fds()) fds.push_back({fd, POLLIN, 0});
      fds.push_back({wake_fd_, POLLIN, 0});
      if (poll(fds.data(), fds.size(), std::max(wait, 0)) < 0 && errno != EINTR) break;
      freeze_timestamp();

      bool readable = false;
      for (size_t i = 0; i + 1 < fds.size(); i++) readable |= (fds[i].revents & POLLIN) != 0;
      if (readable) {
        net.recv();
        Overlay::NotificationEngine &ne = s.overlays.get_notification_engine();
        ne.server_heard(net.get_latest_remote_state().timestamp);
        ne.server_acked(net.get_sent_state_acked_timestamp());
        Overlay::PredictionEngine &pe = s.overlays.get_prediction_engine();
        pe.set_local_frame_acked(net.get_sent_state_acked());
        pe.set_send_interval(net.send_interval());
        pe.set_local_frame_late_acked(net.get_latest_remote_state().state.get_echo_ack());
      }
      if (fds.back().revents & POLLIN) {
        uint64_t n;
        (void)::read(wake_fd_, &n, sizeof(n));
        std::string keys;
        int cols, rows;
        {
          std::lock_guard<std::mutex> lock(mutex_);
          keys.swap(keys_);
          cols = resize_cols_;
          rows = resize_rows_;
          resize_cols_ = resize_rows_ = 0;
        }
        if (cols > 0 && rows > 0 && !net.shutdown_in_progress()) {
          net.get_current_state().push_back(Parser::Resize(cols, rows));
          s.overlays.get_prediction_engine().reset();
        }
        if (!keys.empty() && !net.shutdown_in_progress()) {
          Overlay::PredictionEngine &pe = s.overlays.get_prediction_engine();
          pe.set_local_frame_sent(net.get_sent_state_last());
          bool paste = keys.size() > 100;
          if (paste) pe.reset();
          for (char c : keys) {
            if (!paste) pe.new_user_byte(c, s.shown);
            net.get_current_state().push_back(Parser::UserByte(c));
          }
        }
      }
      net.tick();
      std::string &send_error = net.get_send_error();
      error = send_error;
      send_error.clear();
    } catch (const Network::NetworkException &e) {
      error = e.what();
      usleep(200000);
      freeze_timestamp();
    } catch (const Crypto::CryptoException &e) {
      error = e.what();
      if (e.fatal) stop_ = true;
    }
    bool notify;
    {
      std::lock_guard<std::mutex> lock(mutex_);
      notify = status_.error != error;
      status_.error = error;
    }
    if (notify && changed_) changed_();
  }
}

} // namespace ssh
