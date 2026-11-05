/* C interface to scouter_kit.hpp and scouter_kit_g1.hpp, for bindings (Go,
   Python, JavaScript). Build src/c/scouter_kit_c.cpp together with
   src/base/scouter_kit.cpp, src/g1/scouter_kit_g1.cpp, src/vm/vm.c and
   src/vm/vm_gfx.c.

   Fonts, bitmaps and programs are handles. Functions that load or create
   something return NULL on failure. Arms: 0 = left, 1 = right. Arena
   addresses are bytes; operands of programs are word indices. */
#ifndef SCOUTER_KIT_C_H
#define SCOUTER_KIT_C_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct sk_font sk_font;
typedef struct sk_bitmap sk_bitmap;

/* Pixel (x, y) is data[y * stride + x]; 0 (off) ... 255 (brightest). */
typedef struct {
  uint8_t *data;
  int width;
  int height;
  int stride;
} sk_view;

enum { SK_ALIGN_LEFT, SK_ALIGN_CENTER, SK_ALIGN_RIGHT };
enum { SK_DITHER_NONE, SK_DITHER_FLOYD_STEINBERG };
/* keep: SK_KEEP_STORED also keeps it in the device's own storage (the G1:
   each arm's flash), so a reboot or a later session loads it from there */
enum { SK_KEEP_SESSION, SK_KEEP_STORED };

/* ---------------------------------------------------------- scouter_kit.hpp */

sk_font *sk_load_font(const void *blob, size_t size, const char *glyphs_to_load,
                      int scale, char *error, size_t error_size);
sk_font *sk_load_font_keep(const void *blob, size_t size,
                           const char *glyphs_to_load, int scale, int keep,
                           char *error, size_t error_size);
void sk_font_metrics(const sk_font *font, int *ascent, int *descent,
                     int *line_spacing);
void sk_font_measure(sk_font *font, const char *text, int *width, int *height,
                     int *ascent, int *descent);
/* Returns the advance width of the text. */
int sk_font_draw_line(sk_font *font, sk_bitmap *target, const char *text, int x,
                      int baseline_y, int align, uint8_t gray);
void sk_unload_font(sk_font *font);

sk_bitmap *sk_load_bitmap(const uint8_t *pixels, int width, int height,
                          int stride, int dither);
sk_bitmap *sk_load_bitmap_keep(const uint8_t *pixels, int width, int height,
                               int stride, int dither, int keep);
sk_bitmap *sk_create_bitmap(int width, int height);
sk_view sk_bitmap_contents(const sk_bitmap *bitmap);
void sk_write(sk_bitmap *bitmap, const uint8_t *pixels, int width, int height,
              int stride, int x, int y, int dither);
void sk_fill(sk_bitmap *bitmap, int x, int y, int w, int h, uint8_t gray);
void sk_draw(const sk_bitmap *bitmap, int x, int y);
void sk_clear(void);
void sk_unload_bitmap(sk_bitmap *bitmap);

sk_view sk_display(void);    /* read-only */
uint8_t *sk_response(void);  /* 256 entries */
void sk_unload_all(void);
int sk_total_memory(void);
int sk_free_memory(void);
void sk_flush(void);

/* ------------------------------------------------------- scouter_kit_g1.hpp */

enum {
  SK_G1_ERROR_NONE,
  SK_G1_ERROR_BLUETOOTH,
  SK_G1_ERROR_NO_G1_FOUND,
  SK_G1_ERROR_BONDING,
  SK_G1_ERROR_FIRMWARE
};
enum { SK_G1_MODE_COPY, SK_G1_MODE_MAX, SK_G1_MODE_MASK, SK_G1_MODE_ERASE };
/* Triggers, properties and opcodes are the numbers of VM.md (§5.2, §2.5,
   §2.2). */
enum {
  SK_G1_TRIGGER_START = 0,
  SK_G1_TRIGGER_TIMER = 1,
  SK_G1_TRIGGER_BATTERY = 5,
  SK_G1_TRIGGER_USER0 = 12
};
enum {
  SK_G1_EVENT_ERROR = 0x02,
  SK_G1_EVENT_HALT = 0x03,
  SK_G1_EVENT_NOTIFY = 0x04,
  SK_G1_EVENT_INPUT = 0x07,
  SK_G1_EVENT_RESET = 0x80
};
#define SK_G1_UNKNOWN 0x80000000u

/* left / right: NULL or "" = the bonded glasses, "none" = no such arm.
   adapter: the BlueZ adapter ("hci1" or its address), NULL or "" = the first
   powered one. */
int sk_g1_init(int dx, int dy, const char *left, const char *right,
               const char *adapter, int timeout_ms, char *error,
               size_t error_size);
/* The same with every option. keep_sessions: keep the arms' sessions (no
   RESET_SESSION; a program on them runs on), the library then owns none of
   their memory and draws nothing (for the microphone, control requests and
   events). log: log to stderr. */
typedef struct {
  int dx, dy;
  const char *left, *right, *adapter;
  int timeout_ms;
  int keep_sessions;
  int log;
} sk_g1_config;
int sk_g1_init_config(const sk_g1_config *config, char *error,
                      size_t error_size);
void sk_g1_deinit(void);
int64_t sk_g1_now_us(void);

/* Host code; the values become constants in the library's pool. */
void sk_g1_write_arena(int arm, uint32_t address, const uint8_t *bytes, size_t n);
void sk_g1_write_arena_rle(int arm, uint32_t address, uint16_t out_length,
                           const uint8_t *packbits, size_t n);
void sk_g1_copy_arena(int arm, uint32_t src, uint32_t dst, uint32_t n);
void sk_g1_unpack(int arm, uint32_t dst, uint32_t src, uint32_t n,
                  uint32_t out_length);
void sk_g1_fill(int arm, uint32_t dst, uint32_t stride, int32_t x, int32_t y,
                uint32_t w, uint32_t h, uint8_t level);
void sk_g1_blit(int arm, uint32_t src, uint32_t src_stride, int32_t src_x,
                int32_t src_y, uint32_t dst, uint32_t dst_stride, int32_t dst_x,
                int32_t dst_y, uint32_t w, uint32_t h, int mode, uint8_t level);
void sk_g1_blit_compressed(int arm, uint32_t src, uint32_t src_stride,
                           int32_t src_x, int32_t src_y, uint32_t dst,
                           uint32_t dst_stride, int32_t dst_x, int32_t dst_y,
                           uint32_t w, uint32_t h, int mode, uint8_t level);
void sk_g1_glyph_run_compressed(int arm, uint32_t font, uint32_t dst,
                                uint32_t dst_stride, int32_t x, int32_t y,
                                int mode, const uint8_t *varints, size_t n,
                                uint8_t level);
void sk_g1_draw(int arm, uint32_t src, uint32_t src_stride, int32_t src_x,
                int32_t src_y, int32_t dst_x, int32_t dst_y, uint32_t w,
                uint32_t h);
void sk_g1_line(int arm, uint32_t dst, uint32_t stride, int32_t x0, int32_t y0,
                int32_t x1, int32_t y1, uint8_t level);
/* VIDEO (VM.md §3.7): decodes the n-byte record at src into the 1-bpp
   bitmap and draws the rows that changed at (x, y), ink as level. */
void sk_g1_video(int arm, uint32_t bitmap, uint32_t stride, uint32_t w, uint32_t h,
                 int32_t x, int32_t y, uint8_t level, uint32_t src, uint32_t n,
                 uint32_t state);
void sk_g1_wait_until(int arm, uint32_t device_time_us);
void sk_g1_set_panel(int arm, int on);
void sk_g1_sys_write(int arm, int property, uint32_t value);
void sk_g1_fence(int arm, uint32_t token);
void sk_g1_notify(int arm, uint32_t a, uint32_t b);
void sk_g1_raise(int arm, int trigger);
void sk_g1_call(int arm, uint32_t address);
void sk_g1_run(int arm, const uint8_t *host_code, size_t n);

/* VIDEO records for an arm whose 1-bpp bitmap starts zeroed; the first record
   resets the decoder state (sk_g1_video_state bytes). The encoder is NULL for a
   size outside 1...640 x 1...480. pixels: width x height bytes, nonzero = ink;
   redraw: draw every row. The record stays valid until the next call; NULL
   (and the encoder unchanged) if it is over max_bytes. */
typedef struct sk_g1_video_encoder sk_g1_video_encoder;
uint32_t sk_g1_video_state(void);
sk_g1_video_encoder *sk_g1_video_encoder_new(int width, int height);
void sk_g1_video_encoder_free(sk_g1_video_encoder *encoder);
const uint8_t *sk_g1_video_encode(sk_g1_video_encoder *encoder,
                                  const uint8_t *pixels, int redraw,
                                  size_t max_bytes, size_t *n);

/* Programs: the 16-word trigger table, the pool of constants, the code. */
typedef struct sk_g1_program sk_g1_program;
sk_g1_program *sk_g1_program_new(uint32_t table, uint32_t pool, int pool_words,
                                 uint32_t code);
void sk_g1_program_free(sk_g1_program *program);
int sk_g1_program_label(sk_g1_program *program); /* a new label */
int sk_g1_program_label_at(sk_g1_program *program, uint32_t address);
void sk_g1_program_bind(sk_g1_program *program, int label);
void sk_g1_program_on(sk_g1_program *program, int trigger, int label);
uint16_t sk_g1_program_const(sk_g1_program *program, uint32_t value);
void sk_g1_program_changes(sk_g1_program *program, uint32_t address,
                           uint32_t length);
uint32_t sk_g1_program_here(const sk_g1_program *program);
/* The display policy (VM.md §6.6): the panel off while the arm lies in the
   case or the glasses are folded (WORN 0); meanwhile the program's DRAW and
   VIDEO show nothing. It takes the CASE and WEAR triggers and runs before the
   START handler; when both have ended it jumps to label redraw (-1: none).
   Call it before the first DRAW or VIDEO. */
void sk_g1_program_panel_policy(sk_g1_program *program, int redraw);
/* One instruction: its operand words, and the label of JMP, CALL and the
   branches (else -1). */
void sk_g1_program_op(sk_g1_program *program, int opcode,
                      const uint16_t *operands, int n, int label);
void sk_g1_program_write(sk_g1_program *program, uint32_t address,
                         const uint8_t *bytes, size_t n);
void sk_g1_program_write_rle(sk_g1_program *program, uint32_t address,
                             uint16_t length, const uint8_t *packbits,
                             size_t n);
/* 1 if it can be loaded, else 0 and why. */
int sk_g1_program_valid(const sk_g1_program *program, char *error,
                        size_t error_size);
const uint8_t *sk_g1_program_code(const sk_g1_program *program, size_t *n);
const uint32_t *sk_g1_program_pool(const sk_g1_program *program, size_t *n);
const uint32_t *sk_g1_program_table(const sk_g1_program *program); /* 16 */

/* sk_g1_program_op's opcode for ASSET_LOAD id, dst, off, n (VM.md §2.4) */
enum { SK_G1_OP_ASSET_LOAD = 0x56 };

/* The asset store (VM.md §6.10): blobs each arm keeps in its flash, named by
   the first 16 bytes of their SHA-256. sk_g1_store puts a blob on each arm
   that lacks it and blocks until both hold it (1), else 0 and why;
   sk_g1_forget deletes it on both; sk_g1_assets fills up to max entries of an
   arm's listing and returns how many it has (0 also without a store). */
enum { SK_G1_ASSET_FONT = 1, SK_G1_ASSET_BITMAP = 2, SK_G1_ASSET_BITMAP1 = 3, SK_G1_ASSET_RAW = 4 };
typedef struct {
  uint8_t id[16];
  uint32_t offset, size, crc; /* crc32 (zlib's) of the body */
  uint8_t type;
} sk_g1_asset;
void sk_g1_asset_id(const void *blob, size_t n, uint8_t id[16]);
int sk_g1_store(const void *blob, size_t n, int type, char *error,
                size_t error_size);
int sk_g1_forget(const uint8_t id[16], char *error, size_t error_size);
int sk_g1_assets(int arm, sk_g1_asset *out, int max);

/* These return 1 on success, 0 without a reply (or refused). */
int sk_g1_load(int arm, const sk_g1_program *program);
int sk_g1_vm_start(int arm, uint32_t table);
int sk_g1_vm_stop(int arm);
typedef struct {
  int state; /* 0 stopped, 1 running, 2 halted */
  int trigger;
  uint16_t pending;
  uint32_t table, next;
  int task, jumps_left, depth, halt_code;
  uint32_t timer, tasks;
  int coalesced, notify_dropped, cap_pauses, busy_ms, timer_armed;
} sk_g1_vm_status;
int sk_g1_get_vm_status(int arm, sk_g1_vm_status *status);
int sk_g1_props(int arm, const uint8_t *ids, int n, uint32_t *values);
int sk_g1_report_inputs(int arm, uint16_t triggers, uint16_t *in_force);
int sk_g1_read_arena(int arm, uint32_t address, uint8_t *bytes, size_t n);
void sk_g1_disconnect(int arm, int ms);

typedef struct {
  int type; /* SK_G1_EVENT_ */
  uint32_t epoch, device_us;
  int64_t host_us;
  int trigger;
  uint32_t count, a, b, address;
  int op, code;
  uint32_t detail;
  int offset;
} sk_g1_event;
/* 1 and the oldest event, or 0 when none came within timeout_ms. */
int sk_g1_next_event(int arm, sk_g1_event *event, int timeout_ms);

/* Arena memory, the same on both arms. */
int64_t sk_g1_alloc(uint32_t bytes); /* address or -1 */
void sk_g1_free(uint32_t address);
uint32_t sk_g1_bitmap_address(const sk_bitmap *bitmap, int *stride);
uint32_t sk_g1_font_address(const sk_font *font);
/* Writes up to max glyph indices of the text; returns how many it has. */
size_t sk_g1_glyph_indices(const sk_font *font, const char *text,
                           uint32_t *indices, size_t max);
uint32_t sk_g1_device_time(int arm, int64_t in_us);
const uint32_t *sk_g1_arena(int arm); /* 65536 words, read-only */

typedef struct {
  int present;
  int connected;
  int firmware_major, firmware_minor, firmware_patch;
  uint32_t epoch;
  int max_packet;
  int interval_us;
  int clock_error_us;
  int idle;
  int queued;
  uint32_t packets, resent, reconnects, epoch_changes, errors;
  uint64_t shown_update;
  int64_t shown_us;
  int panel_on, safe_mode, encrypted, in_case, microphone;
  uint16_t triggers;
  uint32_t events_lost;
  char last_error[160];
  char address[40];
} sk_g1_state;
void sk_g1_get_state(int arm, sk_g1_state *state);

int sk_g1_set_brightness(int arm, int level_0_42);
int sk_g1_battery(int arm, int *millivolts, int *percent, int *charging);
int sk_g1_panel_crc(int arm, int y0, int rows, uint32_t *arm_crc,
                    uint32_t *expected_crc);
/* length 0: to the end of the arena */
int sk_g1_arena_crc(int arm, uint32_t address, uint32_t length,
                    uint32_t *arm_crc, uint32_t *expected_crc);

/* Microphone (VM.md §6.5; on the G1 the right arm, sk_g1_state.microphone);
   builds with SCOUTER_G1_MIC. While it is on, the arm shows its indicator.
   Frames are 10 ms of 16 kHz mono, decoded, missing ones concealed. The arm
   stops by itself when the link drops, at a fault, or when its 2 s lease is
   not renewed (the library renews it from the frames it receives). */
enum { SK_G1_MIC_32KBPS, SK_G1_MIC_16KBPS };
enum {
  SK_G1_MIC_STOP_HOST,
  SK_G1_MIC_STOP_PANEL_OFF,
  SK_G1_MIC_STOP_FAULT,
  SK_G1_MIC_STOP_LINK,
  SK_G1_MIC_STOP_LEASE /* no renewal reached the arm for 2 s (VM.md §6.5) */
};
typedef struct {
  uint32_t counter; /* frames since the start */
  int concealed;    /* lost or undecodable: the decoder's concealment */
  uint32_t device_us;
  int64_t host_us;
  int16_t pcm[160];
} sk_g1_mic_frame;
typedef struct {
  int on, stopped; /* SK_G1_MIC_STOP_: why it last stopped */
  int frame_bytes, frames_per_event;
  uint64_t events, frames, gaps, missing, bad, dropped;
} sk_g1_mic_state;
/* 1 when on; 0 refused (sk_g1_state.last_error says why) or no reply. */
int sk_g1_mic_start(int arm, int rate);
int sk_g1_mic_stop(int arm);
/* 1 and the oldest frame, or 0 when none came within timeout_ms. */
int sk_g1_mic_read(int arm, sk_g1_mic_frame *frame, int timeout_ms);
void sk_g1_mic_get_state(int arm, sk_g1_mic_state *state);

#ifdef SCOUTER_G1_EMULATOR
const uint8_t *sk_g1_emulator_panel(int arm); /* 640 x 480 levels 0..15 */
void sk_g1_emulator_set_loss(double write_loss, double notify_loss);
void sk_g1_emulator_disconnect(int arm, int ms);
void sk_g1_emulator_reboot(int arm);
void sk_g1_emulator_set_property(int arm, int property, uint32_t value);
void sk_g1_emulator_set_mic(int arm, double hz, double peak);
void sk_g1_emulator_new_host(int keep_sessions);
void sk_g1_emulator_set_asset_store(int arm, int available);
#endif

#ifdef __cplusplus
}
#endif

#endif
