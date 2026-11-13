// Host test: the boundary builds against ScouterKit's library and its emulated arms start and stop.
#include <cstdio>
#include <cstring>
#include <string>

#include "boundary.hpp"
#include "g1/scouter_kit_g1.hpp"

namespace g1 = scouter_kit::g1;

int main() {
  int failures = 0;
  auto check = [&](bool ok, const char *what) {
    if (!ok) {
      std::fprintf(stderr, "FAIL: %s\n", what);
      failures++;
    }
  };
  check(std::strcmp(chewedfeed::Backend(), "emulated") == 0, "host build is emulated");
  check(chewedfeed::kAbiVersion == 4, "ABI version");
  for (int i = 0; i < 2; i++) {
    std::string error;
    check(g1::Init(nullptr, &error) == g1::ERROR_NONE, "emulated Init");
    g1::Deinit();
  }
  if (failures == 0) std::puts("boundary_test: ok");
  return failures != 0;
}
