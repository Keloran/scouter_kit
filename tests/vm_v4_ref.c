/* Reference for vm_test.c: the protocol 4 drawing code of g1v0, copied verbatim from
 * g1v0 0.4.x's src/gfx.c (lines 45-376, 2026-10-26) with a prologue for the Zephyr helpers. */
#include <stdbool.h>
#include <stdint.h>
#include <string.h>

#define ARENA_SIZE (256 * 1024)
#define MIN(a, b) (((a) < (b)) ? (a) : (b))
#define MAX(a, b) (((a) > (b)) ? (a) : (b))

enum err_code { E_MALFORMED = 1, E_UNKNOWN_OP = 2, E_ARENA = 3, E_FORMAT = 4, E_ARG = 5, E_FONT = 9 };
enum mode { MODE_COPY, MODE_MAX, MODE_MASK, MODE_ERASE };

static uint8_t arena[ARENA_SIZE];

static inline uint16_t get16(const uint8_t *p) { return p[0] | (p[1] << 8); }
static inline uint32_t get32(const uint8_t *p) { return p[0] | (p[1] << 8) | (p[2] << 16) | ((uint32_t)p[3] << 24); }

static inline uint8_t get4(uint32_t base, uint16_t stride, int x, int y)
{
	uint8_t b = arena[base + (uint32_t)y * stride + (x >> 1)];

	return (x & 1) ? (b & 0x0F) : (b >> 4);
}

static inline void set4(uint32_t base, uint16_t stride, int x, int y, uint8_t v)
{
	uint8_t *p = &arena[base + (uint32_t)y * stride + (x >> 1)];

	*p = (x & 1) ? ((*p & 0xF0) | v) : ((*p & 0x0F) | (v << 4));
}

static inline uint8_t get1(uint32_t base, uint16_t stride, int x, int y)
{
	return (arena[base + (uint32_t)y * stride + (x >> 3)] >> (7 - (x & 7))) & 1;
}

static inline uint8_t get_bit(uint32_t bit)
{
	return (arena[bit >> 3] >> (7 - (bit & 7))) & 1;
}

static inline uint8_t apply_mode(uint8_t d, uint8_t a, uint8_t mode, uint8_t level)
{
	switch (mode) {
	case MODE_COPY:
		return (a * level + 7) / 15;
	case MODE_MAX:
		return MAX(d, (uint8_t)((a * level + 7) / 15));
	case MODE_MASK:
		return (d * (15 - a) + level * a + 7) / 15;
	default:
		return (d * (15 - a) + 7) / 15;
	}
}

/* The rectangle rule (PROTOCOL.md §3.2): rows of ceil((x + w) * bpp / 8) bytes fit the stride and the
 * last row ends inside the arena. An empty rectangle is always inside. */
static bool inside(uint32_t base, uint16_t stride, uint32_t x, uint32_t y, uint32_t w, uint32_t h, uint8_t bpp)
{
	uint32_t row_bytes = ((x + w) * bpp + 7) / 8;

	if (w == 0 || h == 0) {
		return true;
	}
	return row_bytes <= stride && (uint64_t)base + (uint64_t)(y + h - 1) * stride + row_bytes <= ARENA_SIZE;
}

static int write_rle(const uint8_t *b)
{
	uint32_t dst = (uint32_t)get16(b) * 4, out_len = get16(b + 2), o = 0;
	const uint8_t *in = b + 5;
	int n = b[4], i = 0;

	if (dst + out_len > ARENA_SIZE) {
		return E_ARENA;
	}
	while (i < n) {
		int8_t c = (int8_t)in[i++];

		if (c >= 0) {
			if (i + c + 1 > n) {
				return E_FORMAT;
			}
			i += c + 1;
			o += c + 1;
		} else if (c != -128) {
			if (i >= n) {
				return E_FORMAT;
			}
			i++;
			o += 1 - c;
		}
	}
	if (o != out_len) {
		return E_FORMAT;
	}
	for (i = 0, o = 0; i < n;) {
		int8_t c = (int8_t)in[i++];

		if (c >= 0) {
			memcpy(&arena[dst + o], &in[i], c + 1);
			i += c + 1;
			o += c + 1;
		} else if (c != -128) {
			memset(&arena[dst + o], in[i++], 1 - c);
			o += 1 - c;
		}
	}
	return 0;
}

static int fill(const uint8_t *b)
{
	uint32_t dst = (uint32_t)get16(b) * 4;
	uint16_t stride = get16(b + 2);
	int x = get16(b + 4), y = get16(b + 6), w = get16(b + 8), h = get16(b + 10);
	uint8_t level = b[12];

	if (level > 15) {
		return E_ARG;
	}
	if (!inside(dst, stride, x, y, w, h, 4)) {
		return E_ARENA;
	}
	for (int r = y; r < y + h; r++) {
		uint8_t *row = &arena[dst + (uint32_t)r * stride];
		int c = x, end = x + w;

		if ((c & 1) && c < end) {
			row[c >> 1] = (row[c >> 1] & 0xF0) | level;
			c++;
		}
		if (end - c >= 2) {
			memset(&row[c >> 1], level * 0x11, (end - c) >> 1);
			c += (end - c) & ~1;
		}
		if (c < end) {
			row[c >> 1] = (row[c >> 1] & 0x0F) | (level << 4);
		}
	}
	return 0;
}

/* byte ranges of a 4 bpp BLIT's source and destination rectangles do not overlap */
static bool disjoint(const uint8_t *b)
{
	uint32_t src = (uint32_t)get16(b) * 4, dst = (uint32_t)get16(b + 8) * 4;
	uint32_t ss = get16(b + 2), ds = get16(b + 10), w = get16(b + 16), h = get16(b + 18);
	uint32_t s0 = src + get16(b + 6) * ss + get16(b + 4) / 2, d0 = dst + get16(b + 14) * ds + get16(b + 12) / 2;
	uint32_t s1 = src + (get16(b + 6) + h - 1) * ss + (get16(b + 4) + w + 1) / 2;
	uint32_t d1 = dst + (get16(b + 14) + h - 1) * ds + (get16(b + 12) + w + 1) / 2;

	return s1 <= d0 || d1 <= s0;
}

/* BLIT (bpp 4, level 15) and BLIT_COMPRESSED (bpp 1, level from the body). If the destination's first
 * pixel has a higher bit address than the source's, rows run last to first and pixels right to left
 * (PROTOCOL.md §4.7), so a scroll inside one bitmap works in any direction. */
static int blit(const uint8_t *b, uint8_t bpp)
{
	uint32_t src = (uint32_t)get16(b) * 4, dst = (uint32_t)get16(b + 8) * 4;
	uint16_t ss = get16(b + 2), ds = get16(b + 10);
	int sx = get16(b + 4), sy = get16(b + 6), dx = get16(b + 12), dy = get16(b + 14);
	int w = get16(b + 16), h = get16(b + 18);
	uint8_t mode = b[20], level = bpp == 1 ? b[21] : 15;
	bool back;

	if (mode > MODE_ERASE || level > 15) {
		return E_ARG;
	}
	if (!inside(src, ss, sx, sy, w, h, bpp) || !inside(dst, ds, dx, dy, w, h, 4)) {
		return E_ARENA;
	}
	if (w == 0 || h == 0) {
		return 0;
	}
	back = 8 * ((uint64_t)src + (uint64_t)sy * ss) + (uint64_t)sx * bpp <
	       8 * ((uint64_t)dst + (uint64_t)dy * ds) + (uint64_t)dx * 4;
	if (bpp == 4 && mode == MODE_COPY && !((sx | dx | w) & 1) && (ss == ds || disjoint(b))) {
		/* whole bytes: one memmove per row, in the same row order, gives the same pixels */
		for (int i = 0; i < h; i++) {
			int r = back ? h - 1 - i : i;

			memmove(&arena[dst + (uint32_t)(dy + r) * ds + dx / 2],
				&arena[src + (uint32_t)(sy + r) * ss + sx / 2], w / 2);
		}
		return 0;
	}
	for (int i = 0; i < h; i++) {
		int r = back ? h - 1 - i : i;

		for (int j = 0; j < w; j++) {
			int c = back ? w - 1 - j : j;
			uint8_t a = bpp == 1 ? get1(src, ss, sx + c, sy + r) * 15 : get4(src, ss, sx + c, sy + r);
			uint8_t d = get4(dst, ds, dx + c, dy + r);

			set4(dst, ds, dx + c, dy + r, apply_mode(d, a, mode, level));
		}
	}
	return 0;
}

/* Font blob (PROTOCOL.md §5): "SKF" 1, u32 size, u16 glyphs, u16 kerning pairs, metrics, then
 * 12-byte glyph entries {u32 bits, u8 w, u8 h, u8 advance, u8 0, i16 x_off, i16 y_off}, u32
 * codepoints (host only), u32 kerning keys (left << 16 | right, ascending), i8 adjustments, and
 * tightly packed 1 bpp bitmaps at bit offsets from the blob start. */
struct font {
	uint32_t base, size;
	uint16_t n, k;
};

static int font_open(uint32_t base, struct font *f)
{
	const uint8_t *h = &arena[base];

	if (base + 16 > ARENA_SIZE || h[0] != 'S' || h[1] != 'K' || h[2] != 'F' || h[3] != 1) {
		return E_FONT;
	}
	f->base = base;
	f->size = get32(h + 4);
	f->n = get16(h + 8);
	f->k = get16(h + 10);
	if (f->n == 0 || 16 + 16 * (uint32_t)f->n + 5 * (uint32_t)f->k > f->size ||
	    (uint64_t)base + f->size > ARENA_SIZE) {
		return E_FONT;
	}
	return 0;
}

static const uint8_t *glyph(const struct font *f, uint32_t g)
{
	return &arena[f->base + 16 + 12 * g];
}

static int kern(const struct font *f, uint16_t left, uint16_t right)
{
	const uint8_t *keys = &arena[f->base + 16 + 16 * (uint32_t)f->n];
	uint32_t key = ((uint32_t)left << 16) | right;
	int lo = 0, hi = f->k;

	while (lo < hi) {
		int mid = (lo + hi) / 2;

		if (get32(keys + 4 * mid) < key) {
			lo = mid + 1;
		} else {
			hi = mid;
		}
	}
	return lo < f->k && get32(keys + 4 * lo) == key ? (int8_t)keys[4 * f->k + lo] : 0;
}

/* next unsigned LEB128 value of v[*i..n), at most 3 bytes; -1 if cut off or longer */
static int32_t varint(const uint8_t *v, int n, int *i)
{
	uint32_t val = 0;

	for (int k = 0; k < 3; k++) {
		uint8_t b;

		if (*i >= n) {
			return -1;
		}
		b = v[(*i)++];
		val |= (uint32_t)(b & 0x7F) << (7 * k);
		if (!(b & 0x80)) {
			return val;
		}
	}
	return -1;
}

/* One glyph into the 4 bpp bitmap at dst (2 * ds pixels wide, down to the arena's end), top-left at
 * (gx, gy); pixels outside are clipped. */
static void draw_glyph(const struct font *f, const uint8_t *e, uint32_t dst, uint16_t ds, int gx, int gy,
		       uint8_t mode, uint8_t level)
{
	int w = e[4], h = e[5];
	uint32_t bit0 = f->base * 8 + get32(e);
	int c0 = MAX(0, -gx), c1 = MIN(w, 2 * ds - gx);

	for (int r = MAX(0, -gy); r < h; r++) {
		uint32_t row = dst + (uint32_t)(gy + r) * ds;
		int c_end;

		if (row >= ARENA_SIZE) {
			break;
		}
		c_end = MIN(c1, (int)(2 * (ARENA_SIZE - row)) - gx);
		for (int c = c0; c < c_end; c++) {
			int px = gx + c;
			uint8_t *p = &arena[row + (px >> 1)];
			uint8_t d = (px & 1) ? (*p & 0x0F) : (*p >> 4);
			uint8_t v = apply_mode(d, get_bit(bit0 + (uint32_t)r * w + c) * 15, mode, level);

			*p = (px & 1) ? ((*p & 0xF0) | v) : ((*p & 0x0F) | (v << 4));
		}
	}
}

/* GLYPH_RUN_COMPRESSED: u16 font, u16 dst, u16 ds, i16 x, i16 y, u8 mode, u8 level, u8 n, varints */
static int glyph_run(const uint8_t *b)
{
	uint32_t dst = (uint32_t)get16(b + 2) * 4;
	uint16_t ds = get16(b + 4);
	int pen = (int16_t)get16(b + 6), y = (int16_t)get16(b + 8);
	uint8_t mode = b[10], level = b[11], n = b[12];
	const uint8_t *v = b + 13;
	struct font f;
	int32_t g, prev = -1;
	int i, err;

	if (mode > MODE_ERASE || level > 15) {
		return E_ARG;
	}
	err = font_open((uint32_t)get16(b) * 4, &f);
	if (err) {
		return err;
	}
	for (i = 0; i < n;) {
		const uint8_t *e;

		g = varint(v, n, &i);
		if (g < 0) {
			return E_MALFORMED;
		}
		if (g >= f.n) {
			return E_FONT;
		}
		e = glyph(&f, g);
		if ((uint64_t)get32(e) + e[4] * e[5] > 8ull * f.size) {
			return E_FONT;
		}
	}
	for (i = 0; i < n; prev = g) {
		const uint8_t *e;

		g = varint(v, n, &i);
		e = glyph(&f, g);
		if (prev >= 0 && f.k) {
			pen += kern(&f, prev, g);
		}
		if (e[4] && e[5]) {
			draw_glyph(&f, e, dst, ds, pen + (int16_t)get16(e + 8), y + (int16_t)get16(e + 10), mode, level);
		}
		pen += e[6];
	}
	return 0;
}

uint8_t *v4_arena(void) { return arena; }
int v4_write_rle(const uint8_t *body) { return write_rle(body); }
int v4_fill(const uint8_t *body) { return fill(body); }
int v4_blit(const uint8_t *body, int compressed) { return blit(body, compressed ? 1 : 4); }
int v4_glyph_run(const uint8_t *body) { return glyph_run(body); }
