// Native cancellation, with no Bluetooth or hardware.
#ifndef SCOUTER_G1_EMULATOR
#error "build with SCOUTER_G1_EMULATOR"
#endif
#include "../src/g1/scouter_kit_g1.cpp"

#include <cassert>
#include <chrono>
#include <thread>

namespace g1 = scouter_kit::g1;
namespace sk = scouter_kit;
using Clock = std::chrono::steady_clock;

template<class F> void Await(F ready) {
  auto end = Clock::now() + std::chrono::seconds(2);
  while (!ready() && Clock::now() < end) std::this_thread::sleep_for(std::chrono::milliseconds(1));
  assert(ready());
}

struct StalledTransport : g1::EmulatorTransport {
  StalledTransport() : EmulatorTransport("") {}
  std::atomic<bool> commands{false}, controls{false};
  int blocked_arm = -1; // configured before Start
  std::atomic<int> starts{0}, stops{0}, writes{0}, polls{0};
  bool Start(Events *events, const bool present[2]) override {
    starts++;
    return EmulatorTransport::Start(events, present);
  }
  bool Write(int arm, int channel, const uint8_t *data, size_t size) override {
    writes++;
    if (arm == blocked_arm || (channel == 0 ? commands.load() : controls.load())) return true;
    return EmulatorTransport::Write(arm, channel, data, size);
  }
  void Poll(int ms) override { polls++; EmulatorTransport::Poll(ms); }
  void Stop() override { stops++; EmulatorTransport::Stop(); }
};

static void CancelBeforeStart() {
  g1::Cancellation token;
  token.Cancel();
  StalledTransport transport;
  g1::Connection connection;
  connection.transport = &transport;
  assert(g1::InitCancelable(token, nullptr, nullptr, &connection) == g1::ERROR_CANCELLED);
  assert(transport.starts == 0 && transport.writes == 0);
  g1::Deinit();
}

static void CancelPendingInit() {
  for (bool partial : {false, true}) {
    g1::Cancellation token;
    StalledTransport transport;
    transport.blocked_arm = partial ? 1 : -1;
    if (!partial) transport.controls = true;
    g1::Connection connection;
    connection.transport = &transport;
    connection.timeout_ms = 30000;
    g1::InitError result = g1::ERROR_NONE;
    std::thread init([&] { result = g1::InitCancelable(token, nullptr, nullptr, &connection); });
    Await([&] { return transport.starts > 0; });
    if (partial) Await([&] { return g1::left.GetState().connected; });
    auto started = Clock::now();
    token.Cancel();
    init.join();
    assert(Clock::now() - started < std::chrono::milliseconds(1000));
    assert(result == g1::ERROR_CANCELLED && transport.stops == 1);
    assert(!g1::left.GetState().connected && !g1::right.GetState().connected);
    g1::Deinit();
  }
}

static void CancelWorkAndRetireToken() {
  StalledTransport transport;
  g1::Cancellation token;
  g1::Connection connection;
  connection.transport = &transport;
  assert(g1::InitCancelable(token, nullptr, nullptr, &connection) == g1::ERROR_NONE);
  transport.commands = true;
  // A fresh frame cannot be acknowledged while command writes are dropped.
  sk::gray::Clear();
  sk::Flush();
  std::vector<uint8_t> blob(8192, 0x55);
  std::future<int> asset;
  {
    std::lock_guard<std::mutex> lock(sk::device::Lock());
    asset = g1::StagedPut(0, g1::kText, blob, 1, true);
  }
  assert(asset.wait_for(std::chrono::milliseconds(20)) == std::future_status::timeout);
  transport.controls = true;
  std::atomic<bool> done{false};
  std::thread pending([&] {
    uint32_t value;
    assert(!g1::left.Prop(g1::PROP_BATT_PCT, &value));
    done = true;
  });
  std::this_thread::sleep_for(std::chrono::milliseconds(20));
  assert(!done);
  auto started = Clock::now();
  token.Cancel();
  // Native Deinit must skip the usual 30-second asset drain after cancellation.
  g1::Deinit();
  pending.join();
  assert(asset.get() == -1);
  assert(Clock::now() - started < std::chrono::milliseconds(1000));
  assert(transport.stops == 1);
  int writes = transport.writes, polls = transport.polls;
  token.Cancel();
  std::this_thread::sleep_for(std::chrono::milliseconds(20));
  assert(transport.writes == writes && transport.polls == polls);
  g1::Cancellation next;
  assert(g1::InitCancelable(next) == g1::ERROR_NONE);
  token.Cancel(); // an old signal cannot stop the fresh session
  std::this_thread::sleep_for(std::chrono::milliseconds(120));
  assert(g1::left.GetState().connected && g1::right.GetState().connected);
  next.Cancel();
  Await([&] { return !g1::left.GetState().connected; });
  g1::Deinit();
}

int main() {
  CancelBeforeStart();
  CancelPendingInit();
  for (int i = 0; i < 3; i++) CancelWorkAndRetireToken();
  std::puts("native cancellation: passed");
}
