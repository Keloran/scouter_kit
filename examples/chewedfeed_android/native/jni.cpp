// The app's JNI surface (Native.kt). A stub: the glasses session comes with the JNI ticket.
#include <jni.h>

#include "boundary.hpp"

extern "C" {

JNIEXPORT jint JNICALL Java_scouterkit_chewedfeed_Native_abiVersion(JNIEnv *, jclass) {
  return chewedfeed::kAbiVersion;
}

JNIEXPORT jstring JNICALL Java_scouterkit_chewedfeed_Native_backend(JNIEnv *env, jclass) {
  return env->NewStringUTF(chewedfeed::Backend());
}

} // extern "C"
