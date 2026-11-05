// The worst-case VIDEO records of the emulator test `video` (g1_firmware/emulator/tests/v5_tests.py), one hex
// line each, from the library's encoder: 640 x 480 one-pixel stripes (redraw), 12-pixel stripes moving 5 pixels a
// row (every transition V(+5)), the stripes shifted (redraw), the 12-pixel ones the other way (V(-5)), and the
// first stripes again (redraw).
//   g++ -std=c++20 -O2 -DSCOUTER_G1_EMULATOR tests/video_records.cpp src/base/scouter_kit.cpp src/vm/vm.c
//       src/vm/vm_gfx.c -o video_records -lpthread
#include "../src/g1/scouter_kit_g1.cpp"

#include <cstdio>

int main() {
  const int w = 640, h = 480;
  scouter_kit::g1::VideoEncoder enc(w, h);
  std::vector<uint8_t> pix(size_t(w * h));
  auto frame = [&](int kind) {
    for (int y = 0; y < h; y++)
      for (int x = 0; x < w; x++) {
        int v = kind == 0 ? x & 1 : kind == 1 ? (x + 1) & 1 : kind == 2 ? ((x - 5 * y) % 12 + 12) % 12 < 6
                                                                         : (x + 5 * y) % 12 < 6;
        pix[size_t(y * w + x)] = uint8_t(v);
      }
  };
  const int kinds[5] = {0, 2, 1, 3, 0};
  for (int k = 0; k < 5; k++) {
    frame(kinds[k]);
    std::vector<uint8_t> r = enc.Encode(pix, kinds[k] < 2);
    for (uint8_t b : r) std::printf("%02x", b);
    std::printf("\n");
  }
  return 0;
}
