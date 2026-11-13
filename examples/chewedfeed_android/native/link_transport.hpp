// Adapted from examples/ssh/native/android_transport.* (GPL-3.0-or-later, this subtree's licence).
#pragma once

#include <jni.h>

#include <condition_variable>
#include <cstdint>
#include <deque>
#include <mutex>
#include <string>
#include <vector>

#include "g1/scouter_kit_g1.hpp"

namespace chewedfeed {

// ScouterKit's Transport over the app's Kotlin GATT layer (ArmLink.kt):
// Kotlin's callbacks queue link events here; Poll delivers them to the
// library on its Bluetooth thread; writes go to Kotlin's per-arm queues.
class LinkTransport : public scouter_kit::g1::Transport {
public:
  LinkTransport(JavaVM *vm, jobject link); // a global reference to an ArmLink
  ~LinkTransport() override;

  bool Start(Events *events, const bool present[2]) override;
  bool Write(int arm, int channel, const uint8_t *data, size_t n) override;
  void Poll(int timeout_ms) override;
  void Wake() override;
  void Disconnect(int arm) override;
  void Hold(int arm, int ms) override;
  void Stop() override;

  // From Kotlin, any thread.
  void LinkUp(int arm, int mtu);
  void LinkDown(int arm);
  void Data(int arm, const uint8_t *data, size_t n);
  void Found(int arm, const std::string &address);
  void Problem(scouter_kit::g1::InitError error, const std::string &text);

private:
  struct Item {
    enum Kind { UP, DOWN, DATA, FOUND, PROBLEM } kind;
    int arm;
    int mtu = 0;
    std::vector<uint8_t> bytes;
    std::string address; // or the problem's text
  };
  void Push(Item item);
  void Call(const char *method, int arm, int value);

  JavaVM *vm_;
  jobject link_;
  JNIEnv *env_ = nullptr; // the Bluetooth thread's, between Start and Stop
  jmethodID write_ = nullptr, connect_ = nullptr, disconnect_ = nullptr, hold_ = nullptr, stop_ = nullptr;
  Events *events_ = nullptr;
  std::mutex mutex_;
  std::condition_variable cv_;
  std::deque<Item> items_;
  bool woken_ = false;
};

} // namespace chewedfeed
