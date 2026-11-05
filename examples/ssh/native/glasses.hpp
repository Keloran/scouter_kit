#pragma once

#include <string>

#include "g1/scouter_kit_g1.hpp"

namespace ssh {

struct GlassesConfig {
  std::string left, right; // bonded addresses
  int dx = 100, dy = 9;
  int brightness = 21;
};


// ScouterKit's session on both arms: through the app's transport, or the
// emulated arms when `transport` is null in an emulator build. Init blocks
// until both arms are ready (at most 30 s).
bool GlassesStart(const GlassesConfig &config, scouter_kit::g1::Transport *transport, std::string *error);
void GlassesStop();

} // namespace ssh
