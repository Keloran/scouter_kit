// MoshClient against this machine's mosh-server: the prompt arrives, typed
// keys reach the shell, its output comes back, and the size is the one asked.
#include <clocale>
#include <condition_variable>
#include <cstdio>
#include <mutex>
#include <regex>
#include <string>

#include "../mosh_client.hpp"

static int failures = 0;
#define CHECK(cond)                                                       \
  do {                                                                    \
    if (!(cond)) {                                                        \
      std::printf("%s:%d: CHECK(%s) failed\n", __FILE__, __LINE__, #cond); \
      failures++;                                                         \
    }                                                                     \
  } while (0)

static std::string Row(const ssh::Snapshot &s, int r) {
  std::string out;
  for (int c = 0; c < s.cols; c++) out += s.At(r, c).text.empty() ? " " : s.At(r, c).text;
  return out;
}

static int Count(const ssh::Snapshot &s, const std::string &text) {
  int n = 0;
  for (int r = 0; r < s.rows; r++) n += Row(s, r).find(text) != std::string::npos;
  return n;
}

int main() {
  setlocale(LC_ALL, "C.UTF-8");
  FILE *server = popen("mosh-server new -i 127.0.0.1 -c 256 -l LANG=C.UTF-8 -- "
                       "env PS1='mosh-test$ ' sh 2>/dev/null", "r");
  std::string text;
  char line[512];
  while (fgets(line, sizeof line, server)) text += line;
  pclose(server);
  std::smatch m;
  if (!std::regex_search(text, m, std::regex("MOSH CONNECT (\\d+) ([A-Za-z0-9/+]{22})"))) {
    std::printf("no MOSH CONNECT line from mosh-server\n");
    return 1;
  }

  std::mutex mutex;
  std::condition_variable cv;
  ssh::MoshClient client([&] { cv.notify_all(); });
  auto wait_for = [&](auto done) {
    std::unique_lock<std::mutex> lock(mutex);
    return cv.wait_for(lock, std::chrono::seconds(10), [&] {
      auto s = client.Latest();
      return s && done(*s);
    });
  };
  std::string error;
  CHECK(client.Start("127.0.0.1", std::stoi(m[1]), m[2], 77, 33, &error));
  CHECK(wait_for([](const ssh::Snapshot &s) { return Count(s, "mosh-test$") > 0; }));
  auto s = client.Latest();
  CHECK(s && s->cols == 77 && s->rows == 33);
  CHECK(client.GetStatus().heard);

  client.Keys("echo hello-$((6*7))\r");
  CHECK(wait_for([](const ssh::Snapshot &s) { return Count(s, "hello-42") == 1; }));
  client.Keys("printf '\\033[1;31mred\\033[0m\\n'\r");
  CHECK(wait_for([](const ssh::Snapshot &s) {
    for (int r = 0; r < s.rows; r++)
      if (Row(s, r).rfind("red", 0) == 0) return s.At(r, 0).bold && s.At(r, 0).fg == 2;
    return false;
  }));
  client.Resize(60, 20);
  CHECK(wait_for([](const ssh::Snapshot &s) { return s.cols == 60 && s.rows == 20; }));
  // Paused (keyer off): nothing moves, keys wait; resumed, the same session carries on with them.
  client.Pause(true);
  std::this_thread::sleep_for(std::chrono::seconds(2));
  client.Keys("echo resumed-$((6*7))\r");
  std::this_thread::sleep_for(std::chrono::milliseconds(500));
  CHECK(Count(*client.Latest(), "resumed-42") == 0);
  client.Pause(false);
  CHECK(wait_for([](const ssh::Snapshot &s) { return Count(s, "resumed-42") == 1; }));
  CHECK(client.GetStatus().silent_ms < 3000);
  client.Keys("exit\r");
  {
    std::unique_lock<std::mutex> lock(mutex);
    CHECK(cv.wait_for(lock, std::chrono::seconds(10), [&] { return client.GetStatus().ended; }));
  }
  client.Stop();
  std::printf("%s (%d failures)\n", failures ? "FAILED" : "all tests passed", failures);
  return failures ? 1 : 0;
}
