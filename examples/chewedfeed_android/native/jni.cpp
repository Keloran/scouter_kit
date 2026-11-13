// The app's JNI surface (Native.kt). Every blocking call (start, stop, status,
// drawing) is made from the session's own thread in GlassesSession.kt, never the
// UI thread. Text arrives as UTF-8 bytes (JNI's modified UTF-8 would mangle
// characters outside the BMP). No native lock is held while calling into Java.
#include <jni.h>

#include <climits>
#include <memory>
#include <mutex>
#include <string>
#include <string_view>
#include <vector>

#include "boundary.hpp"
#include "link_transport.hpp"
#include "session.hpp"

namespace g1 = scouter_kit::g1;

namespace {

JavaVM *g_vm = nullptr;
chewedfeed::Session g_session;
std::mutex g_mutex; // guards g_link and g_last_error
std::shared_ptr<chewedfeed::LinkTransport> g_link;
std::string g_last_error;
bool g_starting = false; // a start owns g_link until it returns

std::shared_ptr<chewedfeed::LinkTransport> Link() {
  std::lock_guard<std::mutex> lock(g_mutex);
  return g_link;
}

std::string Text(JNIEnv *env, jstring s) {
  if (!s) return {};
  const char *c = env->GetStringUTFChars(s, nullptr);
  if (!c) return {};
  std::string out(c);
  env->ReleaseStringUTFChars(s, c);
  return out;
}

// UTF-8 bytes from Kotlin; false if the array could not be read.
bool Bytes(JNIEnv *env, jbyteArray a, std::string *out) {
  if (!a) return false;
  out->resize(size_t(env->GetArrayLength(a)));
  env->GetByteArrayRegion(a, 0, jsize(out->size()), reinterpret_cast<jbyte *>(out->data()));
  return !env->ExceptionCheck();
}

uint64_t Gen(jlong gen) { return gen > 0 ? uint64_t(gen) : 0; }

} // namespace

extern "C" {

JNIEXPORT jint JNI_OnLoad(JavaVM *vm, void *) {
  g_vm = vm;
  return JNI_VERSION_1_6;
}

JNIEXPORT jint JNICALL Java_scouterkit_chewedfeed_Native_abiVersion(JNIEnv *, jclass) {
  return chewedfeed::kAbiVersion;
}

JNIEXPORT jstring JNICALL Java_scouterkit_chewedfeed_Native_backend(JNIEnv *env, jclass) {
  return env->NewStringUTF(chewedfeed::Backend());
}

// ------------------------------------------------ session

// Blocks up to 30 s. A generation (> 0), or minus the error (lastError says more).
JNIEXPORT jlong JNICALL Java_scouterkit_chewedfeed_Native_start(JNIEnv *env, jclass, jobject link, jstring left,
                                                                 jstring right, jint dx, jint dy, jint brightness) {
  chewedfeed::StartConfig config{Text(env, left), Text(env, right), dx, dy, brightness};
  std::shared_ptr<chewedfeed::LinkTransport> transport;
  if (link) transport = std::make_shared<chewedfeed::LinkTransport>(g_vm, env->NewGlobalRef(link));
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    if (g_starting || g_session.Generation() != 0) {
      g_last_error = "a session is already running";
      return -chewedfeed::kBusy;
    }
    g_starting = true;
    g_link = transport;
  }
  std::weak_ptr<chewedfeed::LinkTransport> weak = transport;
  auto r = g_session.Start(config, transport.get(), [weak] {
    if (auto t = weak.lock()) t->Problem(g1::ERROR_BLUETOOTH_ERROR, "cancelled");
  });
  std::lock_guard<std::mutex> lock(g_mutex);
  g_starting = false;
  if (r.generation == 0) {
    g_link = nullptr; // Init failed and let go of the transport
    g_last_error = r.message;
    return -jlong(r.error == 0 ? chewedfeed::kBusy : r.error);
  }
  g_last_error.clear();
  return jlong(r.generation);
}

JNIEXPORT jstring JNICALL Java_scouterkit_chewedfeed_Native_lastError(JNIEnv *env, jclass) {
  std::lock_guard<std::mutex> lock(g_mutex);
  return env->NewStringUTF(g_last_error.c_str());
}

JNIEXPORT void JNICALL Java_scouterkit_chewedfeed_Native_cancel(JNIEnv *, jclass) { g_session.Cancel(); }

// Blank, panels off, Deinit (threads joined, no reconnect); then the link goes.
JNIEXPORT void JNICALL Java_scouterkit_chewedfeed_Native_stop(JNIEnv *, jclass) {
  g_session.Stop();
  std::shared_ptr<chewedfeed::LinkTransport> old;
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    old.swap(g_link);
  }
  // `old` releases its global reference here, on this attached thread.
}

JNIEXPORT jint JNICALL Java_scouterkit_chewedfeed_Native_loadFont(JNIEnv *, jclass, jlong gen, jint scale) {
  std::string error;
  return g_session.LoadFont(Gen(gen), scale, &error);
}

JNIEXPORT jintArray JNICALL Java_scouterkit_chewedfeed_Native_fontMetrics(JNIEnv *env, jclass, jlong gen, jint font) {
  int m[3];
  if (!g_session.FontMetrics(Gen(gen), font, m)) return nullptr;
  jintArray out = env->NewIntArray(3);
  if (out) env->SetIntArrayRegion(out, 0, 3, m);
  return out;
}

JNIEXPORT jint JNICALL Java_scouterkit_chewedfeed_Native_measure(JNIEnv *env, jclass, jlong gen, jint font,
                                                                 jbyteArray utf8) {
  std::string text;
  if (!Bytes(env, utf8, &text)) return -1;
  return g_session.Measure(Gen(gen), font, text);
}

JNIEXPORT jboolean JNICALL Java_scouterkit_chewedfeed_Native_canvas(JNIEnv *, jclass, jlong gen, jint w, jint h) {
  return g_session.Canvas(Gen(gen), w, h);
}

JNIEXPORT jboolean JNICALL Java_scouterkit_chewedfeed_Native_clear(JNIEnv *, jclass, jlong gen, jint gray) {
  return g_session.Clear(Gen(gen), uint8_t(gray));
}

JNIEXPORT jboolean JNICALL Java_scouterkit_chewedfeed_Native_fill(JNIEnv *, jclass, jlong gen, jint x, jint y, jint w,
                                                                  jint h, jint gray) {
  return g_session.Fill(Gen(gen), x, y, w, h, uint8_t(gray));
}

JNIEXPORT jboolean JNICALL Java_scouterkit_chewedfeed_Native_text(JNIEnv *env, jclass, jlong gen, jint font,
                                                                  jbyteArray utf8, jint x, jint baseline, jint align,
                                                                  jint gray) {
  std::string text;
  if (!Bytes(env, utf8, &text)) return false;
  return g_session.Text(Gen(gen), font, text, x, baseline, align, uint8_t(gray));
}

JNIEXPORT jboolean JNICALL Java_scouterkit_chewedfeed_Native_present(JNIEnv *, jclass, jlong gen, jint x, jint y) {
  return g_session.Present(Gen(gen), x, y);
}

JNIEXPORT jboolean JNICALL Java_scouterkit_chewedfeed_Native_blank(JNIEnv *, jclass, jlong gen) {
  return g_session.Blank(Gen(gen));
}

// [width, height, pitch, roll, case battery, then per arm: present, connected, battery, in case];
// Int.MIN_VALUE for unknown; null for another generation.
JNIEXPORT jintArray JNICALL Java_scouterkit_chewedfeed_Native_status(JNIEnv *env, jclass, jlong gen) {
  auto s = g_session.GetStatus(Gen(gen));
  if (s.generation == 0) return nullptr;
  jint v[13] = {s.width, s.height, s.pitch, s.roll, s.case_battery};
  for (int i = 0; i < 2; i++) {
    v[5 + 4 * i] = s.arms[i].present;
    v[6 + 4 * i] = s.arms[i].connected;
    v[7 + 4 * i] = s.arms[i].battery;
    v[8 + 4 * i] = s.arms[i].in_case;
  }
  jintArray out = env->NewIntArray(13);
  if (out) env->SetIntArrayRegion(out, 0, 13, v);
  return out;
}

// Five ints per event: arm, type, trigger, a, b (a and b as raw bits).
JNIEXPORT jintArray JNICALL Java_scouterkit_chewedfeed_Native_events(JNIEnv *env, jclass, jlong gen) {
  auto events = g_session.Events(Gen(gen), 256);
  std::vector<jint> v;
  v.reserve(events.size() * 5);
  for (auto &e : events) v.insert(v.end(), {e.arm, e.type, e.trigger, jint(e.a), jint(e.b)});
  jintArray out = env->NewIntArray(jsize(v.size()));
  if (out) env->SetIntArrayRegion(out, 0, jsize(v.size()), v.data());
  return out;
}

// ------------------------------------------------ link callbacks (ArmLink implementations, any thread)

JNIEXPORT void JNICALL Java_scouterkit_chewedfeed_Native_linkUp(JNIEnv *, jclass, jint arm, jint mtu) {
  if (auto t = Link(); t && (arm == 0 || arm == 1)) t->LinkUp(arm, mtu);
}

JNIEXPORT void JNICALL Java_scouterkit_chewedfeed_Native_linkDown(JNIEnv *, jclass, jint arm) {
  if (auto t = Link(); t && (arm == 0 || arm == 1)) t->LinkDown(arm);
}

JNIEXPORT void JNICALL Java_scouterkit_chewedfeed_Native_linkData(JNIEnv *env, jclass, jint arm, jbyteArray data) {
  auto t = Link();
  if (!t || !data || (arm != 0 && arm != 1)) return;
  jsize n = env->GetArrayLength(data);
  std::vector<uint8_t> bytes(static_cast<size_t>(n));
  env->GetByteArrayRegion(data, 0, n, reinterpret_cast<jbyte *>(bytes.data()));
  if (!env->ExceptionCheck()) t->Data(arm, bytes.data(), bytes.size());
}

JNIEXPORT void JNICALL Java_scouterkit_chewedfeed_Native_linkFound(JNIEnv *env, jclass, jint arm, jstring address) {
  if (auto t = Link(); t && (arm == 0 || arm == 1)) t->Found(arm, Text(env, address));
}

JNIEXPORT void JNICALL Java_scouterkit_chewedfeed_Native_linkProblem(JNIEnv *env, jclass, jint code, jstring text) {
  if (auto t = Link()) t->Problem(g1::InitError(code), Text(env, text));
}

// ------------------------------------------------ emulated arms (emulated flavor only)

JNIEXPORT jbyteArray JNICALL Java_scouterkit_chewedfeed_Native_emulatedPanel(JNIEnv *env, jclass, jint arm) {
#ifdef SCOUTER_G1_EMULATOR
  const uint8_t *p = g1::emulator::Panel(arm);
  if (!p) return nullptr;
  jbyteArray out = env->NewByteArray(640 * 480);
  if (out) env->SetByteArrayRegion(out, 0, 640 * 480, reinterpret_cast<const jbyte *>(p));
  return out;
#else
  (void)env, (void)arm;
  return nullptr;
#endif
}

JNIEXPORT void JNICALL Java_scouterkit_chewedfeed_Native_emulatedProperty(JNIEnv *, jclass, jint arm, jint id,
                                                                          jint value) {
#ifdef SCOUTER_G1_EMULATOR
  g1::emulator::SetProperty(arm, g1::Property(id), uint32_t(value));
#else
  (void)arm, (void)id, (void)value;
#endif
}

} // extern "C"
