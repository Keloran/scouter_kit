// Adapted from examples/ssh/native/android_transport.* (GPL-3.0-or-later, this subtree's licence).
#include "link_transport.hpp"

#include <chrono>

namespace chewedfeed {

LinkTransport::LinkTransport(JavaVM *vm, jobject link) : vm_(vm), link_(link) {
  JNIEnv *env = nullptr;
  vm_->GetEnv(reinterpret_cast<void **>(&env), JNI_VERSION_1_6);
  jclass c = env->GetObjectClass(link_);
  write_ = env->GetMethodID(c, "write", "(II[B)Z");
  connect_ = env->GetMethodID(c, "connect", "(II)V");
  disconnect_ = env->GetMethodID(c, "disconnect", "(II)V");
  hold_ = env->GetMethodID(c, "hold", "(II)V");
  stop_ = env->GetMethodID(c, "stop", "(II)V");
  env->DeleteLocalRef(c);
}

LinkTransport::~LinkTransport() {
  JNIEnv *env = nullptr;
  if (vm_->GetEnv(reinterpret_cast<void **>(&env), JNI_VERSION_1_6) == JNI_OK) env->DeleteGlobalRef(link_);
}

void LinkTransport::Call(const char *method, int arm, int value) {
  jmethodID id = method[0] == 'c' ? connect_ : method[0] == 'd' ? disconnect_ : method[0] == 'h' ? hold_ : stop_;
  env_->CallVoidMethod(link_, id, arm, value);
  if (env_->ExceptionCheck()) {
    env_->ExceptionDescribe();
    env_->ExceptionClear();
  }
}

bool LinkTransport::Start(Events *events, const bool present[2]) {
  events_ = events;
  JavaVMAttachArgs args{JNI_VERSION_1_6, "chewedfeed-bt", nullptr};
  if (vm_->AttachCurrentThread(&env_, &args) != JNI_OK) {
    events->Problem(scouter_kit::g1::ERROR_BLUETOOTH_ERROR, "cannot attach the Bluetooth thread to Java");
    return false;
  }
  for (int i = 0; i < 2; i++)
    if (present[i]) Call("connect", i, 0);
  return true;
}

bool LinkTransport::Write(int arm, int channel, const uint8_t *data, size_t n) {
  jbyteArray bytes = env_->NewByteArray(jsize(n));
  env_->SetByteArrayRegion(bytes, 0, jsize(n), reinterpret_cast<const jbyte *>(data));
  bool ok = env_->CallBooleanMethod(link_, write_, arm, channel, bytes);
  env_->DeleteLocalRef(bytes);
  if (env_->ExceptionCheck()) {
    env_->ExceptionDescribe();
    env_->ExceptionClear();
    return false;
  }
  return ok;
}

void LinkTransport::Poll(int timeout_ms) {
  std::deque<Item> items;
  {
    std::unique_lock<std::mutex> lock(mutex_);
    cv_.wait_for(lock, std::chrono::milliseconds(timeout_ms), [&] { return woken_ || !items_.empty(); });
    woken_ = false;
    items.swap(items_);
  }
  for (Item &item : items) {
    switch (item.kind) {
    case Item::UP: events_->LinkUp(item.arm, item.mtu); break;
    case Item::DOWN: events_->LinkDown(item.arm); break;
    case Item::DATA: events_->Data(item.arm, item.bytes.data(), item.bytes.size()); break;
    case Item::FOUND: events_->Found(item.arm, item.address); break;
    case Item::PROBLEM: events_->Problem(scouter_kit::g1::InitError(item.mtu), item.address); break;
    }
  }
}

void LinkTransport::Wake() {
  {
    std::lock_guard<std::mutex> lock(mutex_);
    woken_ = true;
  }
  cv_.notify_all();
}

void LinkTransport::Disconnect(int arm) { Call("disconnect", arm, 0); }

void LinkTransport::Hold(int arm, int ms) { Call("hold", arm, ms); }

void LinkTransport::Stop() {
  Call("stop", 0, 0);
  vm_->DetachCurrentThread();
  env_ = nullptr;
}

void LinkTransport::Push(Item item) {
  {
    std::lock_guard<std::mutex> lock(mutex_);
    items_.push_back(std::move(item));
  }
  cv_.notify_all();
}

void LinkTransport::LinkUp(int arm, int mtu) { Push({Item::UP, arm, mtu, {}, {}}); }

void LinkTransport::LinkDown(int arm) { Push({Item::DOWN, arm, 0, {}, {}}); }

void LinkTransport::Data(int arm, const uint8_t *data, size_t n) {
  Push({Item::DATA, arm, 0, std::vector<uint8_t>(data, data + n), {}});
}

void LinkTransport::Found(int arm, const std::string &address) { Push({Item::FOUND, arm, 0, {}, address}); }

void LinkTransport::Problem(scouter_kit::g1::InitError error, const std::string &text) {
  Push({Item::PROBLEM, 0, int(error), {}, text});
}

} // namespace chewedfeed
