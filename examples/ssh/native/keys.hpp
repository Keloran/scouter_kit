#pragma once

#include <cstdint>
#include <string>

namespace ssh {

// keyer's modifier byte (the HID one): left Ctrl 0x01, Shift 0x02, Alt 0x04,
// Super 0x08, then the right ones 0x10 ... 0x80.
enum KeyKind { KEY_UNICODE = 1, KEY_HID = 2 };

// The bytes an xterm sends for a key (R17): Unicode as UTF-8 (Ctrl: the
// control byte, Alt: an ESC first), HID usages (keyboard page) as xterm's
// sequences, the cursor keys as SS3 in application mode (DECCKM).
std::string EncodeKey(int kind, uint8_t mods, uint32_t value, bool application_cursor_keys);

} // namespace ssh
