#include "boundary.hpp"

namespace chewedfeed {

const char *Backend() {
#if defined(SCOUTER_G1_EMULATOR)
  return "emulated";
#elif defined(SCOUTER_G1_NO_BLUEZ)
  return "glasses";
#else
#error "build with SCOUTER_G1_EMULATOR or SCOUTER_G1_NO_BLUEZ"
#endif
}

} // namespace chewedfeed
