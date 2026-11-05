// The key table (R17) against xterm's sequences.
#include <cstdio>
#include <string>

#include "../keys.hpp"

static int failures = 0;

static void Check(int kind, int mods, uint32_t value, bool app, const std::string &want) {
  std::string got = ssh::EncodeKey(kind, uint8_t(mods), value, app);
  if (got == want) return;
  std::printf("kind %d mods %02x value %x app %d: got", kind, mods, value, app);
  for (unsigned char c : got) std::printf(" %02x", c);
  std::printf(", want");
  for (unsigned char c : want) std::printf(" %02x", c);
  std::printf("\n");
  failures++;
}

int main() {
  using namespace ssh;
  Check(KEY_UNICODE, 0, 'a', false, "a");
  Check(KEY_UNICODE, 0x02, 'a', false, "A");
  Check(KEY_UNICODE, 0x20, '1', false, "!");
  Check(KEY_UNICODE, 0x01, 'c', false, "\x03");
  Check(KEY_UNICODE, 0x10, '[', false, "\x1b");
  Check(KEY_UNICODE, 0x01, ' ', false, std::string(1, '\0'));
  Check(KEY_UNICODE, 0x04, 'x', false, "\x1bx");
  Check(KEY_UNICODE, 0x05, 'x', false, "\x1b\x18");
  Check(KEY_UNICODE, 0, 0xe9, false, "\xc3\xa9");
  Check(KEY_UNICODE, 0, 0x1F600, false, "\xf0\x9f\x98\x80");
  Check(KEY_HID, 0, 0x04, false, "a");
  Check(KEY_HID, 0x01, 0x06, false, "\x03");
  Check(KEY_HID, 0x02, 0x1E, false, "!");
  Check(KEY_HID, 0, 0x28, false, "\r");
  Check(KEY_HID, 0, 0x29, false, "\x1b");
  Check(KEY_HID, 0, 0x2A, false, "\x7f");
  Check(KEY_HID, 0x04, 0x2A, false, "\x1b\x7f");
  Check(KEY_HID, 0x02, 0x2B, false, "\x1b[Z");
  Check(KEY_HID, 0, 0x52, false, "\x1b[A");
  Check(KEY_HID, 0, 0x52, true, "\x1bOA");
  Check(KEY_HID, 0x01, 0x50, true, "\x1b[1;5D");
  Check(KEY_HID, 0x02, 0x4F, false, "\x1b[1;2C");
  Check(KEY_HID, 0, 0x4A, true, "\x1bOH");
  Check(KEY_HID, 0, 0x4D, false, "\x1b[F");
  Check(KEY_HID, 0, 0x4C, false, "\x1b[3~");
  Check(KEY_HID, 0x01, 0x4B, false, "\x1b[5;5~");
  Check(KEY_HID, 0, 0x3A, false, "\x1bOP");
  Check(KEY_HID, 0x02, 0x3A, false, "\x1b[1;2P");
  Check(KEY_HID, 0, 0x3E, false, "\x1b[15~");
  Check(KEY_HID, 0x04, 0x45, false, "\x1b[24;3~");
  Check(KEY_HID, 0, 0xE0, false, "");
  std::printf("%s (%d failures)\n", failures ? "FAILED" : "all tests passed", failures);
  return failures ? 1 : 0;
}
