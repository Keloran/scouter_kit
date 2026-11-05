/* Memory and drawing primitives of the ScouterKit G1 VM, protocol 5 (g1_firmware/docs/VM.md §2.4,
 * §3, §4.2). They work on a plain 262 144-byte arena, so the interpreter (vm.c), the firmware and a
 * host's arena mirror share one implementation. Freestanding C99, also valid C++. */
#ifndef SCOUTER_VM_GFX_H
#define SCOUTER_VM_GFX_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define VM_ARENA_SIZE 262144u
#define VM_PANEL_W 640
#define VM_PANEL_H 480

enum vm_error {
	VM_OK = 0,
	VM_E_MALFORMED = 1,
	VM_E_OP = 2,
	VM_E_ARENA = 3,
	VM_E_FORMAT = 4,
	VM_E_ARG = 5,
	VM_E_WINDOW = 6,
	VM_E_SAFE = 7,
	VM_E_FONT = 8,
	VM_E_STACK = 9,
	VM_E_BUDGET = 10,
	VM_E_PANEL = 11,
	VM_E_MIC = 12,
	VM_E_ASSET = 13
};

/* PackBits (§2.4): vm_packbits_ok is 1 if the n bytes at in are whole runs that produce exactly len
 * bytes; vm_packbits_decode writes them (call it only on a stream that passed the check). */
int vm_packbits_ok(const uint8_t *in, uint32_t n, uint32_t len);
void vm_packbits_decode(const uint8_t *in, uint32_t n, uint8_t *out);

/* The drawing instructions. v holds the operand values in instruction order:
 *   FILL   dst, stride, x, y, w, h, level
 *   BLIT   src, src_stride, src_x, src_y, dst, dst_stride, dst_x, dst_y, w, h, mode, level
 *   GLYPHS font, dst, stride, x, y, mode, level, text, n
 *   LINE   dst, stride, x0, y0, x1, y1, level
 * Each returns 0 or an error code and changes nothing on an error; for codes 3 and 5 *detail is the
 * failing address or value. */
int vm_fill(uint8_t *arena, const uint32_t *v, uint32_t *detail);
int vm_blit(uint8_t *arena, const uint32_t *v, uint32_t *detail);
int vm_glyphs(uint8_t *arena, const uint32_t *v, uint32_t *detail);
int vm_line(uint8_t *arena, const uint32_t *v, uint32_t *detail);

/* DRAW src, src_stride, src_x, src_y, dst_x, dst_y, w, h in two steps. vm_draw_clip checks that the
 * source exists (error 3) and clips the destination to the panel: d->w and d->h are 0 when nothing
 * is visible. vm_draw_row packs visible row r (0 <= r < d->h, panel row d->y + r) into (d->w + 1) / 2
 * bytes, the left pixel in the high nibble, a 0 nibble after an odd last pixel. */
struct vm_draw {
	int32_t x, y, w, h;
	uint32_t stride;
	uint64_t nibble; /* nibble address of the source pixel shown at panel (x, y) */
};
int vm_draw_clip(const uint32_t *v, struct vm_draw *d, uint32_t *detail);
void vm_draw_row(const uint8_t *arena, const struct vm_draw *d, int32_t r, uint8_t *out);

/* VIDEO bitmap, stride, w, h, x, y, level, src, n, state (§3.7). vm_video_check checks the operands and
 * the record's flags (errors 3, 4 and 5, nothing changes); vm_video_decode then decodes the record into
 * the 1-bpp bitmap and calls span for each row to draw: pixels x0..x1-1 of row y, row being the bitmap's;
 * and at every 16th row with x0 = x1 = 0, to show progress (VM.md §3.7: the platform's liveness).
 * vm_video_expand turns pixels x0..x1-1 of such a row into (x1 - x0 + 1) / 2 bytes of 4 bpp: level for ink,
 * else 0, a 0 nibble after an odd last pixel; it reads only the bytes of those pixels. */
#define VM_VIDEO_STATE 1596u
struct vm_video {
	uint16_t t[2][VM_PANEL_W + 3], o[VM_PANEL_W + 4];
	uint8_t cls[2][VM_PANEL_W + 3];
};
typedef void vm_span_fn(void *ctx, uint32_t y, uint32_t x0, uint32_t x1, const uint8_t *row);
int vm_video_check(const uint8_t *arena, const uint32_t *v, uint32_t *detail);
void vm_video_decode(uint8_t *arena, const uint32_t *v, struct vm_video *s, vm_span_fn *span, void *ctx);
void vm_video_expand(const uint8_t *row, uint32_t x0, uint32_t x1, uint32_t level, uint8_t *out);

#ifdef __cplusplus
}
#endif

#endif
