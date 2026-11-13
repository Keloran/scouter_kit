// Two host sessions on the same retained emulated arms; no radio, bonds or encryption are emulated here.
// g++ -std=c++20 -O2 -DSCOUTER_G1_EMULATOR tests/g1_handover_test.cpp src/base/scouter_kit.cpp
//     src/vm/vm.c src/vm/vm_gfx.c -o /tmp/g1_handover_test -lpthread
#include "../src/g1/scouter_kit_g1.cpp"

#include <atomic>
#include <cstdio>
#include <thread>

namespace g1 = scouter_kit::g1;
using namespace std::chrono_literals;

static int failures;
#define CHECK(condition) do { if (!(condition)) { \
  std::printf("FAIL %d: %s\n", __LINE__, #condition); failures++; \
} } while (0)

// EmulatorTransport::Start boots its arms. Keep one transport running behind a stable callback proxy so
// changing the library's host session does not accidentally erase the device state being tested.
class RetainedArms : public g1::Transport::Events {
public:
  struct Port : g1::Transport {
    RetainedArms &pair;
    std::atomic<int> writes{0}, resets{0};
    explicit Port(RetainedArms &pair) : pair(pair) {}
    bool Start(Events *events, const bool present[2]) override {
      if (pair.active) return false;
      pair.active = this;
      pair.sink = events;
      if (!pair.booted) {
        pair.booted = true;
        return pair.device.Start(&pair, present);
      }
      for (int arm = 0; arm < 2; arm++) {
        pair.device.DropLink(arm, 20);
        events->Found(arm, arm == 0 ? "LEFT_ADDR" : "RIGHT_ADDR");
      }
      return true;
    }
    bool Write(int arm, int channel, const uint8_t *data, size_t n) override {
      if (pair.active != this) return false;
      writes++;
      if (channel == g1::CH_CTRL && n && data[0] == g1::CTRL_RESET_SESSION) resets++;
      return pair.device.Write(arm, channel, data, n);
    }
    void Poll(int timeout_ms) override { pair.device.Poll(timeout_ms); }
    void Wake() override { pair.device.Wake(); }
    void Disconnect(int arm) override { pair.device.Disconnect(arm); }
    void Hold(int arm, int ms) override { pair.device.Hold(arm, ms); }
    void Stop() override {
      for (int arm = 0; arm < 2; arm++) pair.device.DropLink(arm, 60000);
      pair.sink = nullptr;
      pair.active = nullptr;
    }
  };

  g1::EmulatorTransport device{""};
  void LinkUp(int arm, int mtu) override { if (sink) sink->LinkUp(arm, mtu); }
  void LinkDown(int arm) override { if (sink) sink->LinkDown(arm); }
  void Data(int arm, const uint8_t *data, size_t n) override { if (sink) sink->Data(arm, data, n); }
  void Found(int arm, const std::string &address) override { if (sink) sink->Found(arm, address); }
  void Problem(g1::InitError error, const std::string &text) override { if (sink) sink->Problem(error, text); }

private:
  Port *active = nullptr;
  Events *sink = nullptr;
  bool booted = false;
};

static bool Connect(RetainedArms::Port &port, bool keep = false) {
  g1::Connection connection;
  connection.transport = &port;
  connection.keep_sessions = keep;
  std::string error;
  bool ok = g1::Init(nullptr, &error, &connection) == g1::ERROR_NONE;
  if (!ok) std::printf("Init: %s\n", error.c_str());
  CHECK(ok);
  return ok;
}

static void Settled() {
  auto done = [] {
    auto *core = g1::g->core.get();
    std::lock_guard<std::mutex> lock(core->mutex);
    for (int arm = 0; arm < 2; arm++)
      if (!core->links[arm].ready || !core->Settled(arm, core->links[arm].emitted)) return false;
    return true;
  };
  auto until = std::chrono::steady_clock::now() + 5s;
  while (!done() && std::chrono::steady_clock::now() < until) std::this_thread::sleep_for(10ms);
  CHECK(done());
}

static bool PanelOn(g1::Arm &arm) {
  uint32_t value = 0;
  CHECK(arm.Prop(g1::PROP_PANEL, &value));
  return value == 1;
}

static constexpr g1::Address marker_at = 0x18000;
static const std::vector<uint8_t> marker(32, 0x5A);

static void InstallOldSession() {
  g1::Program program(0x1000, 16);
  auto handler = program.NewLabel();
  program.On(g1::TRIGGER_USER0, handler);
  program.Bind(handler);
  program.Notify(program.Const(0xAA), program.Const(0xBB));
  program.End();
  for (auto *arm : {&g1::left, &g1::right}) {
    arm->WriteArena(marker_at, marker);
    arm->Fill(marker_at + 64, 16, 0, 0, 32, 8, 9);
    arm->Draw(marker_at + 64, 16, 0, 0, 40, 80, 32, 8);
    CHECK(arm->Load(program) && arm->Start(program.table_address));
    uint16_t mask = 0;
    CHECK(arm->ReportInputs(1u << g1::TRIGGER_BATTERY, &mask));
    CHECK(mask == (1u << g1::TRIGGER_BATTERY));
  }
  Settled();
  CHECK(PanelOn(g1::left) && PanelOn(g1::right));
}

static void TestFreshHostClearsRetainedState() {
  RetainedArms pair;
  RetainedArms::Port laptop(pair), phone(pair);
  if (!Connect(laptop)) return;
  InstallOldSession();
  uint32_t old_epoch = g1::left.GetState().epoch;
  CHECK(pair.device.Panel(0)[80 * 640 + 40] == 9);
  g1::Deinit();
  int old_writes = laptop.writes;

  if (!Connect(phone)) return;
  CHECK(phone.resets == 2); // one RESET_SESSION per retained arm, not a fresh emulator boot
  CHECK(g1::left.GetState().epoch != old_epoch);
  for (auto *arm : {&g1::left, &g1::right}) {
    std::vector<uint8_t> bytes(marker.size(), 1);
    CHECK(arm->ReadArena(marker_at, bytes));
    CHECK(bytes == std::vector<uint8_t>(marker.size(), 0));
    g1::VmStatus status{};
    CHECK(arm->Status(&status) && status.state == 0);
    CHECK(!PanelOn(*arm));
    auto events = g1::Control(arm == &g1::left ? 0 : 1, g1::CTRL_EVENTS, {});
    CHECK(events.first == 0 && events.second.size() == 2 && g1::Get16(events.second.data()) == 0);
  }
  // Render different content after the fresh session: the former host must not restore its old mirror.
  g1::left.Fill(marker_at, 16, 0, 0, 32, 8, 3);
  g1::left.Draw(marker_at, 16, 0, 0, 40, 80, 32, 8);
  Settled();
  std::this_thread::sleep_for(100ms);
  CHECK(pair.device.Panel(0)[80 * 640 + 40] == 3);
  CHECK(laptop.writes == old_writes);
  g1::Deinit();
}

static void TestKeepSessionsIsNotFreshTakeover() {
  RetainedArms pair;
  RetainedArms::Port laptop(pair), phone(pair);
  if (!Connect(laptop)) return;
  InstallOldSession();
  uint32_t old_epoch = g1::left.GetState().epoch;
  g1::Deinit();
  if (!Connect(phone, true)) return;
  CHECK(phone.resets == 0);
  CHECK(g1::left.GetState().epoch == old_epoch);
  std::vector<uint8_t> bytes(marker.size());
  CHECK(g1::left.ReadArena(marker_at, bytes) && bytes == marker);
  g1::VmStatus status{};
  CHECK(g1::left.Status(&status) && status.state == 1);
  g1::Deinit();
}

static void TestReconnectKeepsEpochAndRestoresRequestedInputs() {
  RetainedArms pair;
  RetainedArms::Port laptop(pair);
  if (!Connect(laptop)) return;
  InstallOldSession();
  uint32_t old_epoch = g1::left.GetState().epoch;
  int resets = laptop.resets;
  pair.device.DropLink(0, 100);
  std::this_thread::sleep_for(30ms);
  Settled();
  CHECK(g1::left.GetState().epoch == old_epoch && laptop.resets == resets);
  auto events = g1::Control(0, g1::CTRL_EVENTS, {});
  CHECK(events.first == 0 && events.second.size() == 2 &&
        g1::Get16(events.second.data()) == (1u << g1::TRIGGER_BATTERY));
  std::vector<uint8_t> bytes(marker.size());
  CHECK(g1::left.ReadArena(marker_at, bytes) && bytes == marker);
  g1::Deinit();
}

// A live host cannot tell a reset by another client from a reboot. This is raw reset fault injection,
// not two concurrent BLE links: it documents why the outgoing controller must Deinit before yielding.
static void TestLiveHostRestoresAfterForeignReset() {
  RetainedArms pair;
  RetainedArms::Port laptop(pair);
  if (!Connect(laptop)) return;
  InstallOldSession();
  uint32_t old_epoch = g1::left.GetState().epoch;
  int resets = laptop.resets;
  const uint8_t reset[] = {g1::CTRL_RESET_SESSION, 0xE7, 1};
  CHECK(pair.device.Write(0, g1::CH_CTRL, reset, sizeof(reset)));
  std::this_thread::sleep_for(100ms);
  pair.device.DropLink(0, 100);
  std::this_thread::sleep_for(30ms);
  Settled();
  CHECK(g1::left.GetState().epoch != old_epoch && laptop.resets > resets);
  CHECK(pair.device.Panel(0)[80 * 640 + 40] == 9);
  // Unallocated raw writes are not preserved by Restore; its retained display mirror is enough to expose
  // old content even though this scratch marker was discarded.
  std::vector<uint8_t> bytes(marker.size());
  CHECK(g1::left.ReadArena(marker_at, bytes) && bytes == std::vector<uint8_t>(marker.size(), 0));
  g1::Deinit();
}

int main() {
  TestFreshHostClearsRetainedState();
  TestKeepSessionsIsNotFreshTakeover();
  TestReconnectKeepsEpochAndRestoresRequestedInputs();
  TestLiveHostRestoresAfterForeignReset();
  std::printf("retained-arm host takeover: %d failures (no BLE bonding/encryption coverage)\n", failures);
  return failures ? 1 : 0;
}
