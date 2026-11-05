#include "keys.hpp"

namespace ssh {

namespace {

bool Ctrl(uint8_t m) { return m & 0x11; }
bool Shift(uint8_t m) { return m & 0x22; }
bool Alt(uint8_t m) { return m & 0x44; }
bool Super(uint8_t m) { return m & 0x88; }

// xterm's modifier parameter: 1 + Shift 1 + Alt 2 + Ctrl 4 + Super 8.
int Param(uint8_t m) { return 1 + Shift(m) + 2 * Alt(m) + 4 * Ctrl(m) + 8 * Super(m); }

void Utf8(std::string &out, uint32_t c) {
  if (c < 0x80) {
    out += char(c);
  } else if (c < 0x800) {
    out += char(0xC0 | c >> 6);
    out += char(0x80 | (c & 0x3F));
  } else if (c < 0x10000) {
    out += char(0xE0 | c >> 12);
    out += char(0x80 | (c >> 6 & 0x3F));
    out += char(0x80 | (c & 0x3F));
  } else {
    out += char(0xF0 | c >> 18);
    out += char(0x80 | (c >> 12 & 0x3F));
    out += char(0x80 | (c >> 6 & 0x3F));
    out += char(0x80 | (c & 0x3F));
  }
}

uint32_t Shifted(uint32_t c) {
  if (c >= 'a' && c <= 'z') return c - 32;
  const char *from = "1234567890-=[]\;',./`", *to = "!@#$%^&*()_+{}|:\"<>?~";
  for (int i = 0; from[i]; i++)
    if (c == uint32_t(from[i])) return uint32_t(to[i]);
  return c;
}

std::string Unicode(uint8_t m, uint32_t c) {
  if (Shift(m) && c < 0x80) c = Shifted(c);
  std::string out = Alt(m) ? "\x1b" : "";
  if (Ctrl(m)) {
    if (c >= 'a' && c <= 'z') return out + char(c - 'a' + 1);
    if (c >= '@' && c <= '_') return out + char(c - '@');
    if (c == ' ' || c == '2') return out + '\0';
    if (c == '?' || c == '8') return out + '\x7f';
  }
  Utf8(out, c);
  return out;
}

// A HID usage that types a character on a US layout, or 0.
uint32_t Character(uint32_t usage) {
  if (usage >= 0x04 && usage <= 0x1D) return 'a' + (usage - 0x04);
  if (usage >= 0x1E && usage <= 0x26) return '1' + (usage - 0x1E);
  if (usage == 0x27) return '0';
  const char *punct = "-=[]\\#;'`,./"; // 0x2D ... 0x38
  if (usage >= 0x2D && usage <= 0x38) return uint32_t(punct[usage - 0x2D]);
  return 0;
}

} // namespace

std::string EncodeKey(int kind, uint8_t m, uint32_t v, bool app) {
  if (kind == KEY_UNICODE) return Unicode(m, v);
  if (kind != KEY_HID) return "";
  if (uint32_t c = Character(v)) return Unicode(m, c);
  std::string p = std::to_string(Param(m));
  bool plain = Param(m) == 1;
  auto letter = [&](char l) {
    if (!plain) return "\x1b[1;" + p + l;
    return std::string(app ? "\x1bO" : "\x1b[") + l;
  };
  auto tilde = [&](const char *n) { return "\x1b[" + std::string(n) + (plain ? "" : ";" + p) + "~"; };
  auto ss3 = [&](char l) { return plain ? std::string("\x1bO") + l : "\x1b[1;" + p + l; };
  std::string alt = Alt(m) ? "\x1b" : "";
  switch (v) {
  case 0x28: return alt + "\r";                                // Enter
  case 0x29: return alt + "\x1b";                              // Escape
  case 0x2A: return alt + (Ctrl(m) ? "\x08" : "\x7f");         // Backspace
  case 0x2B: return Shift(m) ? "\x1b[Z" : alt + "\t";          // Tab
  case 0x2C: return Ctrl(m) ? alt + '\0' : alt + " ";          // Space
  case 0x3A: return ss3('P');                                  // F1 ... F4
  case 0x3B: return ss3('Q');
  case 0x3C: return ss3('R');
  case 0x3D: return ss3('S');
  case 0x3E: return tilde("15");                               // F5 ... F12
  case 0x3F: return tilde("17");
  case 0x40: return tilde("18");
  case 0x41: return tilde("19");
  case 0x42: return tilde("20");
  case 0x43: return tilde("21");
  case 0x44: return tilde("23");
  case 0x45: return tilde("24");
  case 0x49: return tilde("2");                                // Insert
  case 0x4A: return letter('H');                               // Home
  case 0x4B: return tilde("5");                                // Page Up
  case 0x4C: return tilde("3");                                // Delete
  case 0x4D: return letter('F');                               // End
  case 0x4E: return tilde("6");                                // Page Down
  case 0x4F: return letter('C');                               // Right
  case 0x50: return letter('D');                               // Left
  case 0x51: return letter('B');                               // Down
  case 0x52: return letter('A');                               // Up
  }
  return "";
}

} // namespace ssh
