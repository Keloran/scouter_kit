#include <cstddef>

#include "../src/base/departure_mono.inc"

extern "C" const unsigned char *sk_go_departure_mono(size_t *size) {
  *size = sizeof kDepartureMonoBlob;
  return kDepartureMonoBlob;
}
