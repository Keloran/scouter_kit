// The built-in font for the Python package: the C layer has no accessor for it.
#include <cstddef>

#include "../../src/base/departure_mono.inc"

extern "C" const unsigned char *sk_py_departure_mono(size_t *size) {
  *size = sizeof kDepartureMonoBlob;
  return kDepartureMonoBlob;
}
