package scouterkit.chewedfeed

// libchewedfeed.so (native/jni.cpp). A stub until the JNI session ticket.
object Native {
    // native/boundary.hpp kAbiVersion: checked at start so a stale library fails loudly.
    const val ABI_VERSION = 1

    init {
        System.loadLibrary("chewedfeed")
    }

    @JvmStatic external fun abiVersion(): Int

    // "glasses" or "emulated": the native backend, which must match BuildConfig.FLAVOR.
    @JvmStatic external fun backend(): String
}
