/* The C layer against the emulated arms.
     gcc -std=c11 -DSCOUTER_G1_EMULATOR -c tests/c_test.c -o c_test.o
     g++ -std=c++20 -DSCOUTER_G1_EMULATOR c_test.o src/c/scouter_kit_c.cpp src/base/scouter_kit.cpp \
         src/g1/scouter_kit_g1.cpp src/vm/vm.c src/vm/vm_gfx.c -o c_test -lpthread */
#include <stdio.h>
#include <string.h>

#include "../src/c/scouter_kit_c.h"
#include "../src/base/departure_mono.inc"

static int failures = 0;
#define CHECK(cond)                                                    \
  do {                                                                 \
    if (!(cond)) {                                                     \
      printf("FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);           \
      failures++;                                                      \
    }                                                                  \
  } while (0)

/* VM.md §9.3, from the generic instruction call. */
static const uint8_t kBattery[113] = {
    0x44, 0x0f, 0x2a, 0x00, 0x37, 0xf0, 0x70, 0xc0, 0x00, 0x32, 0x00, 0x10, 0x1f, 0x23, 0x00, 0x12, 0x11,
    0xf0, 0x21, 0x00, 0x15, 0x20, 0xf0, 0x27, 0x00, 0x26, 0xf1, 0x20, 0x35, 0x00, 0x14, 0x00, 0xf0, 0x27,
    0x00, 0x36, 0x0f, 0x0f, 0xc0, 0x00, 0x20, 0x00, 0x11, 0x31, 0xf0, 0x35, 0x00, 0x12, 0x4f, 0x10, 0x24,
    0x00, 0x50, 0xff, 0xff, 0xff, 0xf0, 0x38, 0x00, 0x2f, 0x00, 0x20, 0x00, 0x20, 0x00, 0x31, 0x00, 0x2c,
    0x00, 0x20, 0x00, 0x52, 0xff, 0xff, 0xff, 0xf3, 0x40, 0x39, 0x00, 0x38, 0x00, 0x2f, 0x00, 0x22, 0x00,
    0x2b, 0x00, 0x22, 0x00, 0x29, 0x00, 0x53, 0xff, 0xff, 0xff, 0xff, 0x38, 0x00, 0x2f, 0x00, 0x20, 0x00,
    0x20, 0x00, 0x36, 0x00, 0x26, 0x00, 0x31, 0x00, 0x2c, 0x00, 0x33};

static const uint32_t kPool[27] = {0,  1,  2,  3,   4,   5,   8,   10,  12,  15,   17,    20,     24,      32,
                                   34, 40, 60, 80, 100, 160, 240, 256, 560, 1440, 16384, 153600, 60000000};

#define OPS(...) (const uint16_t[]){__VA_ARGS__}, sizeof((const uint16_t[]){__VA_ARGS__}) / 2

static void battery(sk_g1_program *p) {
  uint16_t c[27];
  for (int i = 0; i < 27; i++) c[i] = sk_g1_program_const(p, kPool[i]);
  enum { C0, C1, C2, C3, C4, C5, C8, C10, C12, C15, C17, C20, C24, C32, C34, C40, C60, C80, C100, C160,
         C240, C256, C560, C1440, C16384, C153600 };
  int entry = sk_g1_program_label(p), next = sk_g1_program_label(p), done = sk_g1_program_label(p);
  sk_g1_program_bind(p, entry);
  sk_g1_program_op(p, 0x44, OPS(0, c[C17]), -1);
  sk_g1_program_op(p, 0x37, OPS(c[C100], 0), done);
  sk_g1_program_op(p, 0x10, OPS(1, c[C3]), -1);
  sk_g1_program_bind(p, next);
  sk_g1_program_op(p, 0x12, OPS(1, 1, c[C1]), -1);
  sk_g1_program_op(p, 0x15, OPS(2, 0, c[C10]), -1);
  sk_g1_program_op(p, 0x26, OPS(c[C256], 1, 2), -1);
  sk_g1_program_op(p, 0x14, OPS(0, 0, c[C10]), -1);
  sk_g1_program_op(p, 0x36, OPS(0, c[C0]), next);
  sk_g1_program_op(p, 0x11, OPS(3, 1, c[C256]), -1);
  sk_g1_program_op(p, 0x12, OPS(4, c[C4], 1), -1);
  sk_g1_program_op(p, 0x50, OPS(c[C16384], c[C40], c[C0], c[C0], c[C80], c[C24], c[C0]), -1);
  sk_g1_program_op(p, 0x52, OPS(c[C153600], c[C16384], c[C40], c[C2], c[C20], c[C2], c[C15], 3, 4), -1);
  sk_g1_program_op(p, 0x53, OPS(c[C16384], c[C40], c[C0], c[C0], c[C560], c[C8], c[C80], c[C24]), -1);
  sk_g1_program_bind(p, done);
  sk_g1_program_op(p, 0x33, NULL, 0, -1);
  sk_g1_program_on(p, SK_G1_TRIGGER_START, entry);
  sk_g1_program_on(p, SK_G1_TRIGGER_BATTERY, entry);
}

int main(void) {
  char error[200];
  sk_g1_program *spec = sk_g1_program_new(0x40, 0x80, 27, 0xC000);
  battery(spec);
  size_t n = 0;
  const uint8_t *code = sk_g1_program_code(spec, &n);
  CHECK(sk_g1_program_valid(spec, error, sizeof error));
  CHECK(n == sizeof kBattery && memcmp(code, kBattery, n) == 0);
  CHECK(sk_g1_program_table(spec)[0] == 0xC000 && sk_g1_program_table(spec)[5] == 0xC000);
  sk_g1_program_free(spec);

  /* VIDEO records: sizes outside 1...640 x 1...480 are refused, max_bytes keeps the encoder unchanged */
  CHECK(sk_g1_video_encoder_new(0, 10) == NULL && sk_g1_video_encoder_new(641, 10) == NULL &&
        sk_g1_video_encoder_new(10, 481) == NULL && sk_g1_video_state() == 1596);
  {
    sk_g1_video_encoder *enc = sk_g1_video_encoder_new(9, 3);
    uint8_t pix[27] = {0, 1, 1, 0, 0, 0, 0, 0, 1};
    size_t len = 0;
    CHECK(enc != NULL && sk_g1_video_encode(enc, pix, 0, 1, &len) == NULL && len == 0);
    const uint8_t *rec = sk_g1_video_encode(enc, pix, 1, 1000, &len);
    CHECK(rec != NULL && len > 1 && rec[0] == 3);
    sk_g1_video_encoder_free(enc);
  }

  CHECK(sk_g1_init(100, 9, NULL, NULL, "hci7", 10000, error, sizeof error) == SK_G1_ERROR_BLUETOOTH);
  CHECK(sk_g1_init(100, 9, NULL, NULL, NULL, 10000, error, sizeof error) == SK_G1_ERROR_NONE);
  sk_view display = sk_display();
  CHECK(display.width == 740 && display.height == 471);

  sk_font *font = sk_load_font(kDepartureMonoBlob, sizeof kDepartureMonoBlob, "", 2, error,
                               sizeof error);
  CHECK(font != NULL);
  int ascent = 0, descent = 0, spacing = 0;
  sk_font_metrics(font, &ascent, &descent, &spacing);
  CHECK(ascent == 22 && descent == 6 && spacing == 28);
  CHECK(sk_load_font("bad", 3, "", 1, error, sizeof error) == NULL && strlen(error) > 0);
  uint32_t glyphs[4];
  CHECK(sk_g1_glyph_indices(font, "0Az", glyphs, 4) == 3 && glyphs[0] == 0 && glyphs[1] == 10 &&
        glyphs[2] == 61);
  CHECK(sk_g1_font_address(font) >= 64);

  sk_bitmap *line = sk_create_bitmap(600, 32);
  CHECK(line != NULL);
  int stride = 0;
  CHECK(sk_g1_bitmap_address(line, &stride) > sk_g1_font_address(font) && stride == 300);
  int width = sk_font_draw_line(font, line, "Hello from C", 300, 24, SK_ALIGN_CENTER, 255);
  CHECK(width == 12 * 14);
  sk_draw(line, 70, 200);
  sk_flush();
  sk_flush();

  sk_view contents = sk_bitmap_contents(line);
  int lit = 0;
  for (int y = 0; y < contents.height; y++)
    for (int x = 0; x < contents.width; x++) lit += contents.data[y * contents.stride + x] != 0;
  CHECK(lit > 100);

  uint32_t arm_crc = 0, expected = 1;
  CHECK(sk_g1_arena_crc(0, 0, 0, &arm_crc, &expected) && arm_crc == expected);
  CHECK(sk_g1_panel_crc(1, 0, 480, &arm_crc, &expected) && arm_crc == expected);

  sk_g1_state state;
  sk_g1_get_state(0, &state);
  CHECK(state.present && state.connected && state.firmware_minor == 5 && state.errors == 0);
  CHECK(state.panel_on && state.triggers == 0xF063 && !state.microphone);
  sk_g1_fill(0, sk_g1_bitmap_address(line, NULL), 300, 0, 0, 10, 10, 16);
  sk_g1_get_state(0, &state);
  CHECK(state.errors == 1 && strstr(state.last_error, "FILL") != NULL);

  /* A program: USER0 answers with NOTIFY [box] + 1. */
  int64_t base = sk_g1_alloc(512);
  CHECK(base > 0);
  uint32_t table = (uint32_t)base, box = (uint32_t)base + 508;
  sk_g1_program *p = sk_g1_program_new(table, table + 64, 8, table + 96);
  int echo = sk_g1_program_label(p);
  sk_g1_program_bind(p, echo);
  uint16_t one = sk_g1_program_const(p, 1), tag = sk_g1_program_const(p, 0xC0DE);
  sk_g1_program_op(p, 0x11, OPS(1, (uint16_t)(box / 4), one), -1);
  sk_g1_program_op(p, 0x43, OPS(1, tag), -1);
  sk_g1_program_op(p, 0x33, NULL, 0, -1);
  sk_g1_program_on(p, SK_G1_TRIGGER_USER0, echo);
  sk_g1_program_changes(p, box, 4);
  CHECK(sk_g1_program_valid(p, error, sizeof error));
  CHECK(sk_g1_load(0, p) && sk_g1_vm_start(0, table));
  uint8_t value[4] = {9, 0, 0, 0};
  sk_g1_write_arena(0, box, value, 4);
  sk_g1_raise(0, SK_G1_TRIGGER_USER0);
  sk_g1_event event;
  CHECK(sk_g1_next_event(0, &event, 2000) && event.type == SK_G1_EVENT_NOTIFY && event.a == 10 &&
        event.b == 0xC0DE && event.trigger == SK_G1_TRIGGER_USER0);
  sk_g1_vm_status vm;
  CHECK(sk_g1_get_vm_status(0, &vm) && vm.state == 1 && vm.table == table && vm.tasks == 1 &&
        vm.pending == 1); /* START has no handler: it stays pending */
  CHECK(!sk_g1_arena_crc(0, box, 4, &arm_crc, &expected)); /* the program changes it */
  CHECK(sk_g1_vm_stop(0) && sk_g1_get_vm_status(0, &vm) && vm.state == 0);
  sk_g1_program_free(p);
  sk_g1_free((uint32_t)base);
  CHECK(sk_g1_arena_crc(0, box, 4, &arm_crc, &expected) && arm_crc == expected);

  uint8_t ids[2] = {0x10, 0x11};
  uint32_t props[2] = {0, 0};
  CHECK(sk_g1_props(1, ids, 2, props) && props[0] == 3950 && props[1] == 72);
  uint16_t in_force = 0;
  CHECK(sk_g1_report_inputs(1, 0xFFFF, &in_force) && in_force == 0xF073); // the right arm: with LIGHT
  uint8_t back[3] = {0};
  CHECK(sk_g1_read_arena(1, sk_g1_font_address(font), back, 3) && memcmp(back, "SKF", 3) == 0);

  CHECK(sk_free_memory() < sk_total_memory());
  sk_unload_all();
  CHECK(sk_free_memory() == sk_total_memory());

  /* the asset store: a blob on both arms, listed, a kept font, deleted */
  uint8_t blob[3000], id[16];
  sk_g1_asset list[8];
  for (int i = 0; i < (int)sizeof blob; i++) blob[i] = (uint8_t)(i * 31);
  sk_g1_asset_id(blob, sizeof blob, id);
  CHECK(sk_g1_store(blob, sizeof blob, SK_G1_ASSET_RAW, error, sizeof error));
  CHECK(sk_g1_assets(1, list, 8) == 1 && memcmp(list[0].id, id, 16) == 0 && list[0].size == sizeof blob);
  sk_font *kept = sk_load_font_keep(kDepartureMonoBlob, sizeof kDepartureMonoBlob, "kept", 1, SK_KEEP_STORED,
                                    error, sizeof error);
  CHECK(kept != NULL);
  CHECK(sk_g1_forget(id, error, sizeof error));
  CHECK(sk_g1_assets(0, list, 8) <= 1);
  sk_unload_font(kept);
  sk_g1_deinit();

  sk_g1_config config = {.dx = 100, .dy = 9, .right = "none", .log = 0};
  CHECK(sk_g1_init_config(&config, error, sizeof error) == SK_G1_ERROR_NONE);
  sk_g1_get_state(0, &state);
  CHECK(state.present && state.connected);
  sk_g1_get_state(1, &state);
  CHECK(!state.present);
  sk_g1_deinit();
  /* Clones share one signal, retain it independently, and never target a later Init. */
  sk_g1_cancellation *cancel = sk_g1_cancellation_new();
  sk_g1_cancellation *copy = sk_g1_cancellation_clone(cancel);
  CHECK(cancel != NULL && copy != NULL && !sk_g1_cancellation_cancelled(copy));
  sk_g1_cancellation_free(cancel);
  sk_g1_cancellation_cancel(copy);
  CHECK(sk_g1_cancellation_cancelled(copy));
  CHECK(sk_g1_init_cancelable(&config, copy, error, sizeof error) == SK_G1_ERROR_CANCELLED);
  CHECK(sk_g1_init_config(&config, error, sizeof error) == SK_G1_ERROR_NONE);
  sk_g1_cancellation_cancel(copy);
  sk_g1_get_state(0, &state);
  CHECK(state.connected);
  sk_g1_deinit();
  sk_g1_cancellation_free(copy);
  printf("%s (%d failures)\n", failures ? "FAILED" : "C test passed", failures);
  return failures ? 1 : 0;
}
