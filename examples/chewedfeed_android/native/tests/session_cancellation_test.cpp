// Cooperative transport faults; no Bluetooth or hardware.
#include "g1/scouter_kit_g1.cpp"
#include "session.hpp"

#include <cassert>
#include <chrono>
#include <thread>

namespace g1 = scouter_kit::g1;
using Clock = std::chrono::steady_clock;

struct StalledTransport : g1::EmulatorTransport {
  StalledTransport() : EmulatorTransport("") {}
  std::atomic<bool> stall{false};
  std::atomic<int> starts{0}, stops{0}, writes{0};
  bool Start(Events *events, const bool present[2]) override {
    starts++;
    return EmulatorTransport::Start(events, present);
  }
  bool Write(int arm, int channel, const uint8_t *data, size_t size) override {
    writes++;
    return stall || EmulatorTransport::Write(arm, channel, data, size);
  }
  void Stop() override { stops++; EmulatorTransport::Stop(); }
};

template<class F> static void Await(F ready) {
  auto end = Clock::now() + std::chrono::seconds(2);
  while (!ready() && Clock::now() < end) std::this_thread::sleep_for(std::chrono::milliseconds(1));
  assert(ready());
}

int main() {
  chewedfeed::Session session;
  for (int cycle = 0; cycle < 3; cycle++) {
    StalledTransport transport;
    transport.stall = true;
    chewedfeed::StartResult result;
    std::thread starting([&] { result = session.Start({}, &transport); });
    Await([&] { return transport.starts > 0; });
    auto start = Clock::now();
    session.Cancel(); // no transport-specific cancellation hook
    starting.join();
    assert(Clock::now() - start < std::chrono::seconds(1));
    assert(result.error == chewedfeed::kCancelled && !result.generation && transport.stops == 1);
    session.Stop();

    transport.stall = false;
    result = session.Start({}, &transport);
    assert(result.generation && !result.error);
    transport.stall = true;
    std::atomic<bool> completed{false};
    std::thread status([&] { session.GetStatus(result.generation); completed = true; });
    std::this_thread::sleep_for(std::chrono::milliseconds(20));
    assert(!completed); // GetStatus holds the session mutex inside a pending SDK request
    start = Clock::now();
    session.Stop(); // fallback must signal independently of that mutex
    status.join();
    assert(Clock::now() - start < std::chrono::seconds(1));
    assert(transport.stops == 2 && !session.Generation());
    int writes = transport.writes;
    assert(!session.Blank(result.generation));
    session.Cancel();
    assert(transport.writes == writes);
    transport.stall = false;
    auto next = session.Start({}, &transport);
    assert(next.generation > result.generation);
    // A completed Stop's fallback cannot later cancel a fresh session.
    std::this_thread::sleep_for(std::chrono::milliseconds(600));
    assert(session.GetStatus(next.generation).arms[0].connected);
    session.Stop();
  }
}
