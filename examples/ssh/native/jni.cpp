// The app's JNI surface (Native.kt).
#include <jni.h>

#include <algorithm>
#include <atomic>
#include <clocale>
#include <cstdint>
#include <cstring>
#include <memory>
#include <mutex>
#include <string>

#include "android_transport.hpp"
#include "glasses.hpp"
#include "keys.hpp"
#include "mosh_client.hpp"
#include "terminal.hpp"

namespace {

JavaVM *g_vm = nullptr;
jclass g_native = nullptr; // scouterkit.ssh.Native
jmethodID g_changed = nullptr;
// Lock order: g_glasses_mutex, g_mutex, g_term_mutex.
std::mutex g_glasses_mutex; // guards the glasses' objects (Init holds it up to 30 s)
std::mutex g_link_mutex; // guards g_transport for the link callbacks
std::shared_ptr<ssh::AndroidTransport> g_transport;
bool g_glasses = false;
ssh::GlassesConfig g_config;
int g_angle = 20;
bool g_ignore_worn = false;
std::vector<uint8_t> g_font;
std::mutex g_mutex; // guards g_mosh
std::unique_ptr<ssh::MoshClient> g_mosh;
std::atomic<ssh::MoshClient *> g_mosh_live{nullptr}; // between Start and Stop, for its own thread
std::mutex g_term_mutex;                          // guards g_term and g_force
std::shared_ptr<ssh::GlassesTerminal> g_term;
bool g_force = false;

// The JNIEnv of a native thread, attached on first use and detached when the
// thread ends.
JNIEnv *Env() {
  thread_local struct Attached {
    JNIEnv *env = nullptr;
    ~Attached() {
      if (env) g_vm->DetachCurrentThread();
    }
  } attached;
  if (!attached.env) {
    JavaVMAttachArgs args{JNI_VERSION_1_6, "scouter-mosh", nullptr};
    g_vm->AttachCurrentThread(&attached.env, &args);
  }
  return attached.env;
}

std::string Text(JNIEnv *env, jstring s) {
  if (!s) return {};
  const char *c = env->GetStringUTFChars(s, nullptr);
  std::string out(c);
  env->ReleaseStringUTFChars(s, c);
  return out;
}

template <typename T> void Put(std::string &out, T v) { out.append(reinterpret_cast<const char *>(&v), sizeof v); }

void Changed() { Env()->CallStaticVoidMethod(g_native, g_changed); }

std::shared_ptr<ssh::AndroidTransport> Link() {
  std::lock_guard<std::mutex> lock(g_link_mutex);
  return g_transport;
}

void SetLink(std::shared_ptr<ssh::AndroidTransport> t) {
  std::lock_guard<std::mutex> lock(g_link_mutex);
  g_transport = std::move(t);
}

// The terminal on the arms, with the latest screen (g_glasses_mutex held).
std::string StartTerminal() {
  auto term = std::make_shared<ssh::GlassesTerminal>();
  std::string error;
  if (!term->Start({g_config.dx, g_config.dy, g_angle, g_ignore_worn}, g_font, Changed, &error)) return error;
  std::shared_ptr<const ssh::Snapshot> s;
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    if (g_mosh) s = g_mosh->Latest();
  }
  std::lock_guard<std::mutex> lock(g_term_mutex);
  term->Force(g_force);
  if (s) term->Show(s);
  g_term = term;
  return {};
}

void StopTerminal(bool keep = false) {
  std::shared_ptr<ssh::GlassesTerminal> term;
  {
    std::lock_guard<std::mutex> lock(g_term_mutex);
    term.swap(g_term);
  }
  if (term) term->Stop(keep);
}

} // namespace

extern "C" {

JNIEXPORT jint JNI_OnLoad(JavaVM *vm, void *) {
  g_vm = vm;
  setlocale(LC_ALL, "C.UTF-8"); // mosh's UTF-8 parser uses mbrtowc
  JNIEnv *env = nullptr;
  vm->GetEnv(reinterpret_cast<void **>(&env), JNI_VERSION_1_6);
  jclass c = env->FindClass("scouterkit/ssh/Native");
  g_native = static_cast<jclass>(env->NewGlobalRef(c));
  g_changed = env->GetStaticMethodID(c, "changed", "()V");
  return JNI_VERSION_1_6;
}

JNIEXPORT jstring JNICALL Java_scouterkit_ssh_Native_version(JNIEnv *env, jclass) {
  return env->NewStringUTF("scouter_ssh 0.1, mosh 1.4.0");
}

// ------------------------------------------------ glasses

// Init through the link (null: the emulated arms), then the terminal. Blocks up to 30 s; an error or null.
JNIEXPORT jstring JNICALL Java_scouterkit_ssh_Native_glassesStart(JNIEnv *env, jclass, jobject link, jstring left,
                                                                   jstring right, jint dx, jint dy, jint brightness,
                                                                   jint angle, jbyteArray font, jboolean ignore_worn) {
  std::lock_guard<std::mutex> lock(g_glasses_mutex);
  if (g_glasses) return env->NewStringUTF("already started");
  g_config = {Text(env, left), Text(env, right), dx, dy, brightness};
  g_angle = angle;
  g_ignore_worn = ignore_worn;
  g_font.resize(size_t(env->GetArrayLength(font)));
  env->GetByteArrayRegion(font, 0, jsize(g_font.size()), reinterpret_cast<jbyte *>(g_font.data()));
  if (link) SetLink(std::make_shared<ssh::AndroidTransport>(g_vm, env->NewGlobalRef(link)));
  std::string error;
  if (!ssh::GlassesStart(g_config, Link().get(), &error)) {
    SetLink(nullptr);
    return env->NewStringUTF(error.c_str());
  }
  g_glasses = true;
  error = StartTerminal();
  return error.empty() ? nullptr : env->NewStringUTF(error.c_str());
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_glassesStop(JNIEnv *, jclass, jboolean keep) {
  StopTerminal(keep);
  std::lock_guard<std::mutex> lock(g_glasses_mutex);
  if (g_glasses) ssh::GlassesStop();
  g_glasses = false;
  SetLink(nullptr);
}


JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_force(JNIEnv *, jclass, jboolean on) {
  std::lock_guard<std::mutex> lock(g_term_mutex);
  g_force = on;
  if (g_term) g_term->Force(on);
}

// The emulated arm's panel, 640 x 480 levels 0..15 (emulator builds; null otherwise).
JNIEXPORT jbyteArray JNICALL Java_scouterkit_ssh_Native_emulatedPanel(JNIEnv *env, jclass, jint arm) {
#ifdef SCOUTER_G1_EMULATOR
  const uint8_t *p = scouter_kit::g1::emulator::Panel(arm);
  if (!p) return nullptr;
  jbyteArray out = env->NewByteArray(640 * 480);
  env->SetByteArrayRegion(out, 0, 640 * 480, reinterpret_cast<const jbyte *>(p));
  return out;
#else
  (void)env;
  (void)arm;
  return nullptr;
#endif
}

// The terminal's read-back check (T3), a few lines; blocks while the arms answer.
JNIEXPORT jstring JNICALL Java_scouterkit_ssh_Native_check(JNIEnv *env, jclass) {
  std::shared_ptr<ssh::GlassesTerminal> term;
  {
    std::lock_guard<std::mutex> lock(g_term_mutex);
    term = g_term;
  }
  return env->NewStringUTF(term ? term->Check().c_str() : "no terminal on the glasses\n");
}

// A sensor value of an emulated arm, such as PITCH (emulator builds).
JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_emulatedProperty(JNIEnv *, jclass, jint arm, jint id, jint value) {
#ifdef SCOUTER_G1_EMULATOR
  scouter_kit::g1::emulator::SetProperty(arm, scouter_kit::g1::Property(id), uint32_t(value));
#else
  (void)arm, (void)id, (void)value;
#endif
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_linkUp(JNIEnv *, jclass, jint arm, jint mtu) {
  if (auto t = Link()) t->LinkUp(arm, mtu);
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_linkDown(JNIEnv *, jclass, jint arm) {
  if (auto t = Link()) t->LinkDown(arm);
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_linkData(JNIEnv *env, jclass, jint arm, jbyteArray data) {
  auto t = Link();
  if (!t) return;
  jsize n = env->GetArrayLength(data);
  jbyte *b = env->GetByteArrayElements(data, nullptr);
  t->Data(arm, reinterpret_cast<const uint8_t *>(b), size_t(n));
  env->ReleaseByteArrayElements(data, b, JNI_ABORT);
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_linkFound(JNIEnv *env, jclass, jint arm, jstring address) {
  if (auto t = Link()) t->Found(arm, Text(env, address));
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_linkProblem(JNIEnv *env, jclass, jint code, jstring text) {
  if (auto t = Link()) t->Problem(scouter_kit::g1::InitError(code), Text(env, text));
}

// ------------------------------------------------ mosh

JNIEXPORT jstring JNICALL Java_scouterkit_ssh_Native_moshStart(JNIEnv *env, jclass, jstring ip, jint port,
                                                                jstring key, jint cols, jint rows) {
  std::lock_guard<std::mutex> lock(g_mutex);
  if (g_mosh) return env->NewStringUTF("already started");
  auto mosh = std::make_unique<ssh::MoshClient>([] {
    if (ssh::MoshClient *m = g_mosh_live.load()) {
      std::lock_guard<std::mutex> lock(g_term_mutex);
      if (g_term)
        if (auto s = m->Latest()) g_term->Show(s);
    }
    Changed();
  });
  g_mosh_live = mosh.get();
  std::string error;
  if (!mosh->Start(Text(env, ip), port, Text(env, key), cols, rows, &error)) {
    g_mosh_live = nullptr;
    return env->NewStringUTF(error.c_str());
  }
  g_mosh = std::move(mosh);
  return nullptr;
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_moshPause(JNIEnv *, jclass, jboolean on) {
  std::lock_guard<std::mutex> lock(g_mutex);
  if (g_mosh) g_mosh->Pause(on);
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_moshStop(JNIEnv *, jclass) {
  std::unique_ptr<ssh::MoshClient> mosh;
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    mosh = std::move(g_mosh);
  }
  if (mosh) mosh->Stop();
  g_mosh_live = nullptr;
}

JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_resize(JNIEnv *, jclass, jint cols, jint rows) {
  std::lock_guard<std::mutex> lock(g_mutex);
  if (g_mosh) g_mosh->Resize(cols, rows);
}

// A key from keyer (R17): encoded with the screen's cursor-key mode, then to mosh.
JNIEXPORT void JNICALL Java_scouterkit_ssh_Native_keyEvent(JNIEnv *, jclass, jint kind, jint mods, jint value) {
  std::lock_guard<std::mutex> lock(g_mutex);
  if (!g_mosh) return;
  auto s = g_mosh->Latest();
  std::string bytes = ssh::EncodeKey(kind, uint8_t(mods), uint32_t(value), s && s->application_cursor_keys);
  if (!bytes.empty()) g_mosh->Keys(bytes);
}

// heard, ended, silent ms, then the arms: connected left, right, and the terminal shown on an eye.
JNIEXPORT jintArray JNICALL Java_scouterkit_ssh_Native_status(JNIEnv *env, jclass) {
  jint v[6] = {0, 0, 0, 0, 0, 0};
  {
    std::unique_lock<std::mutex> glasses(g_glasses_mutex, std::try_to_lock); // not while Init runs
    if (glasses.owns_lock() && g_glasses) {
      v[3] = scouter_kit::g1::left.GetState().connected;
      v[4] = scouter_kit::g1::right.GetState().connected;
    }
  }
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    if (g_mosh) {
      ssh::Status s = g_mosh->GetStatus();
      v[0] = s.heard;
      v[1] = s.ended;
      v[2] = jint(std::min<int64_t>(s.silent_ms, INT32_MAX));
    }
  }
  {
    std::lock_guard<std::mutex> lock(g_term_mutex);
    if (g_term) v[5] = g_term->Shown();
  }
  jintArray out = env->NewIntArray(6);
  env->SetIntArrayRegion(out, 0, 6, v);
  return out;
}

// The mosh session's last error and the terminal's title.
JNIEXPORT jobjectArray JNICALL Java_scouterkit_ssh_Native_texts(JNIEnv *env, jclass) {
  std::string error, title;
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    if (g_mosh) {
      error = g_mosh->GetStatus().error;
      if (auto s = g_mosh->Latest()) title = s->title;
    }
  }
  jobjectArray out = env->NewObjectArray(2, env->FindClass("java/lang/String"), nullptr);
  env->SetObjectArrayElement(out, 0, env->NewStringUTF(error.c_str()));
  env->SetObjectArrayElement(out, 1, env->NewStringUTF(title.c_str()));
  return out;
}

// The latest screen for the phone's mirror (TerminalView.parse): little-endian u16 cols, rows, cursor row,
// cursor column, u8 flags (1 cursor visible, 2 application cursor keys), u16 title length and the title;
// then per cell u32 fg, u32 bg, u8 attributes (1 bold, 2 faint, 4 italic, 8 underline, 16 inverse,
// 32 invisible, 64 wide), u8 length and the UTF-8 text.
JNIEXPORT jbyteArray JNICALL Java_scouterkit_ssh_Native_snapshot(JNIEnv *env, jclass) {
  std::shared_ptr<const ssh::Snapshot> s;
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    if (g_mosh) s = g_mosh->Latest();
  }
  if (!s) return nullptr;
  std::string out;
  Put<uint16_t>(out, s->cols);
  Put<uint16_t>(out, s->rows);
  Put<uint16_t>(out, s->cursor_row);
  Put<uint16_t>(out, s->cursor_col);
  Put<uint8_t>(out, (s->cursor_visible ? 1 : 0) | (s->application_cursor_keys ? 2 : 0));
  std::string title = s->title.substr(0, 255);
  Put<uint16_t>(out, title.size());
  out += title;
  for (const ssh::Cell &c : s->cells) {
    Put<uint32_t>(out, c.fg);
    Put<uint32_t>(out, c.bg);
    Put<uint8_t>(out, c.bold | c.faint << 1 | c.italic << 2 | c.underline << 3 | c.inverse << 4 | c.invisible << 5 |
                          c.wide << 6);
    std::string text = c.text.substr(0, 255);
    Put<uint8_t>(out, text.size());
    out += text;
  }
  jbyteArray a = env->NewByteArray(jsize(out.size()));
  env->SetByteArrayRegion(a, 0, jsize(out.size()), reinterpret_cast<const jbyte *>(out.data()));
  return a;
}

} // extern "C"
