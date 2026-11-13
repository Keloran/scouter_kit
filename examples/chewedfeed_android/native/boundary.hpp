// The native side of the app, below JNI (jni.cpp), so the host tests can use it.
#pragma once

namespace chewedfeed {

// Bumped when Native.kt and jni.cpp change incompatibly; Native.ABI_VERSION must match.
constexpr int kAbiVersion = 2;

// "glasses" (SCOUTER_G1_NO_BLUEZ: the app supplies the Bluetooth link) or "emulated" (SCOUTER_G1_EMULATOR).
const char *Backend();

} // namespace chewedfeed
