# SSH Scouter

An Android app that keeps a [mosh](https://mosh.org) session to your server and mirrors its terminal on the
Even Realities G1 running the ScouterKit firmware, with keys from
[keyer](https://github.com/mafik/keyer) over its key-stream service. GPL-3.0 (`LICENSE`), because it
includes mosh; third-party notices in `NOTICE` and `licenses/`.

- The terminal fills the binocular area: (640 − dx) / 7 × (480 − dy) / 14 cells (77 × 33 at dx 100, dy 9).
- It shows when you look up past the angle in Settings and hides 5° lower after a second. While the app
  holds keyer, it shows whatever the head's angle, also with the phone in a pocket.
- The phone shows the same terminal while the app is in the foreground. It has no keyboard: keys come from
  keyer only.
- While the terminal shows, each eye shows its arm's battery level beside it (left eye on the left, right eye
  on the right). The arms do this themselves, also without the phone.

## Build

    native/third_party/build.sh          # OpenSSL 3.5.9, protobuf 21.12, mosh 1.4.0 at pinned SHA-256
    ./gradlew assembleGlassesDebug       # -PthirdPartyDir=OUT if build.sh got another OUT

A signed release (application ID `eu.mrogalski.scouter`; no R8, since JNI and JSch find classes by name):

    ./gradlew assembleGlassesRelease

signed with the keystore that `keystore.properties` in this directory names (storeFile, storePassword,
keyAlias, keyPassword; storeFile absolute or relative to this directory; the file and `*.jks` are
git-ignored). Keep that keystore: updates must be signed with the same key. Without the file the release is
unsigned.

Needs the Android SDK (platform 34, NDK 27.3.13750724, CMake 3.31.6), JDK 17 or later, and for
`build.sh` curl, cmake, ninja, make, perl and a host C++ compiler. The `emulated` flavour
(`assembleEmulatedDebug`) runs two emulated arms inside the app and shows their panels: no glasses needed.

Host tests (Linux, against this machine's `mosh-server` and the emulated arms):

    cmake -S native -B BUILD -G Ninja && cmake --build BUILD && ctest --test-dir BUILD

## Set up

Settings is the app's only screen with controls; everything else follows keyer. It first asks for Android's
Bluetooth ("Nearby devices") and notification permissions; without Bluetooth the app starts nothing.

1. Settings, Server: host, port, user, your OpenSSH private key (file or paste, with its passphrase if it
   has one; it is kept encrypted with a key in Android's keystore), the command (empty: the login shell) and
   mosh's UDP ports.
2. Glasses: pair each arm in Android's Bluetooth settings with the glasses in the open charging case (the
   phone takes one of each arm's 3 bond slots), then "Use the bonded glasses" (or "Pair glasses", a scan in the
   open case) picks the pair. The app connects only to
   arms Android lists as bonded and never pairs by itself: if a bond is lost or encryption fails, the
   notification says "pair again in the case". dx and dy align the eyes (they set the overlap and the
   terminal's size); the looking-up angle sets when the terminal shows.
3. keyer: pair it in Android's Bluetooth settings, then "Use the bonded keyer".
4. Open the app once with keyer on and allow it as keyer's companion (Android's dialog; it also exempts the
   app from battery optimisation). The first session shows the server's host key fingerprint to confirm.

keyer on: the app starts by itself, connects the glasses, takes keyer's keys and resumes the mosh session,
with one notification showing the state. keyer off: the app lets go of the glasses and keyer, mosh goes
quiet and the phone can sleep; looking up still shows the last screen, which the arms keep. The glasses stay
connected only while keyer's own link is up. Bluetooth switched off (also when Android keeps Bluetooth Low
Energy on for other apps) ends the session, and the app then does nothing: Android stops reporting keyer to
it until Bluetooth is on again. The mosh session
continues at the next keyer on while Android keeps the app's process; after that a new one starts (and ends
the old one), so run tmux on the server (command `tmux new -A -s main`) to keep your work.
