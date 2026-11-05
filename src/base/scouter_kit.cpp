#include "scouter_kit.hpp"

#include <algorithm>
#include <cstring>
#include <memory>
#include <vector>

namespace scouter_kit {

namespace gray {
BitmapView display;
uint8_t response[256];
} // namespace gray

int total_memory = 0;
int free_memory = 0;

namespace {

constexpr int kHeaderSize = 16;

// Glyph order of every font blob (VM.md §4.1): glyph i < 62 is kSlots[i], so
// a digit's value is its glyph index; a missing one is an empty glyph with
// codepoint 0. The other glyphs follow from index 62 in ascending codepoint
// order; a font of kSlots characters only ends after the last one.
constexpr char kSlots[] = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
constexpr int kSlotCount = 62;

int SlotOf(uint32_t cp) {
  if (cp >= '0' && cp <= '9') return int(cp - '0');
  if (cp >= 'A' && cp <= 'Z') return int(cp - 'A') + 10;
  if (cp >= 'a' && cp <= 'z') return int(cp - 'a') + 36;
  return -1;
}

uint16_t Get16(const uint8_t *p) { return uint16_t(p[0] | p[1] << 8); }
uint32_t Get32(const uint8_t *p) {
  return uint32_t(p[0]) | uint32_t(p[1]) << 8 | uint32_t(p[2]) << 16 |
         uint32_t(p[3]) << 24;
}
void Put16(uint8_t *p, uint32_t v) {
  p[0] = uint8_t(v);
  p[1] = uint8_t(v >> 8);
}
void Put32(uint8_t *p, uint32_t v) {
  for (int i = 0; i < 4; i++) p[i] = uint8_t(v >> (8 * i));
}

struct Glyph {
  uint32_t bits;
  int w, h, advance, x_off, y_off;
};

// Read access to a font blob (g1_firmware/docs/PROTOCOL.md §5).
struct FontBlobView {
  const uint8_t *p = nullptr;
  uint32_t size = 0;
  int count = 0;
  int kerns = 0;
  int slots = 0; // leading glyphs in kSlots order

  Glyph glyph(int i) const {
    const uint8_t *e = p + kHeaderSize + 12 * i;
    return {Get32(e), e[4], e[5], e[6], int16_t(Get16(e + 8)),
            int16_t(Get16(e + 10))};
  }
  uint32_t codepoint(int i) const {
    return Get32(p + kHeaderSize + 12 * count + 4 * i);
  }
  uint32_t kern_key(int i) const {
    return Get32(p + kHeaderSize + 16 * count + 4 * i);
  }
  int kern_adjust(int i) const {
    return int8_t(p[kHeaderSize + 16 * count + 4 * kerns + i]);
  }
  bool bit(uint32_t j) const { return (p[j >> 3] >> (7 - (j & 7))) & 1; }

  // An older blob in plain codepoint order has slots = 0 (or a few) and is
  // found by the binary search.
  int find(uint32_t cp) const {
    int slot = SlotOf(cp);
    if (slot >= 0 && slot < slots) return codepoint(slot) == cp ? slot : -1;
    int lo = slots, hi = count - 1;
    while (lo <= hi) {
      int mid = (lo + hi) / 2;
      uint32_t v = codepoint(mid);
      if (v == cp) return mid;
      if (v < cp)
        lo = mid + 1;
      else
        hi = mid - 1;
    }
    return -1;
  }

  int kern(int left, int right) const {
    uint32_t key = uint32_t(left) << 16 | uint32_t(right);
    int lo = 0, hi = kerns - 1;
    while (lo <= hi) {
      int mid = (lo + hi) / 2;
      uint32_t v = kern_key(mid);
      if (v == key) return kern_adjust(mid);
      if (v < key)
        lo = mid + 1;
      else
        hi = mid - 1;
    }
    return 0;
  }
};

bool Fail(std::string *error, const char *message) {
  if (error) *error = message;
  return false;
}

bool ParseFontBlob(const uint8_t *p, size_t len, FontBlobView *out,
                   std::string *error) {
  if (len < kHeaderSize) return Fail(error, "font blob too short");
  if (std::memcmp(p, "SKF", 3) != 0 || p[3] != 1)
    return Fail(error, "not a font blob (magic or version)");
  FontBlobView v;
  v.p = p;
  v.size = Get32(p + 4);
  v.count = Get16(p + 8);
  v.kerns = Get16(p + 10);
  if (v.size < kHeaderSize || v.size > len)
    return Fail(error, "font blob size field does not match");
  if (v.count < 1) return Fail(error, "font blob without glyphs");
  if (kHeaderSize + 16ull * v.count + 5ull * v.kerns > v.size)
    return Fail(error, "font blob tables do not fit");
  while (v.slots < std::min(v.count, kSlotCount) &&
         (v.codepoint(v.slots) == uint8_t(kSlots[v.slots]) || v.codepoint(v.slots) == 0))
    v.slots++;
  for (int i = 0; i < v.count; i++) {
    if (i > v.slots && v.codepoint(i) <= v.codepoint(i - 1))
      return Fail(error, "font blob codepoints not in order");
    Glyph g = v.glyph(i);
    if (g.w && g.h && g.bits + uint64_t(g.w) * g.h > 8ull * v.size)
      return Fail(error, "font blob glyph bitmap outside the blob");
  }
  for (int i = 0; i < v.kerns; i++) {
    uint32_t key = v.kern_key(i);
    if (i > 0 && key <= v.kern_key(i - 1))
      return Fail(error, "font blob kerning keys not ascending");
    if (int(key >> 16) >= v.count || int(key & 0xFFFF) >= v.count)
      return Fail(error, "font blob kerning key out of range");
  }
  *out = v;
  return true;
}

struct FontImpl {
  std::vector<uint8_t> blob; // the subset that is on the device
  FontBlobView view;
  int replacement = -1; // glyph of U+FFFD
  void *device = nullptr;
  Keep keep = Keep::kSession;
};

struct BitmapImpl {
  std::vector<uint8_t> pixels;
  void *device = nullptr;
  Keep keep = Keep::kSession;
};

struct Placement {
  const gray::Bitmap *bitmap; // nullptr: black over everything (Clear)
  int x, y;
};

struct State {
  std::mutex mutex;
  device::Device *device = nullptr;
  int width = 0;
  int height = 0;
  int levels = 16;
  std::vector<uint8_t> shown;
  std::vector<uint8_t> composed;
  std::vector<int> owner;
  std::vector<Placement> placements;
  std::vector<std::unique_ptr<gray::Bitmap>> bitmaps;
  std::vector<std::unique_ptr<Font>> fonts;
  uint64_t last_present = 0;
};

State &S() {
  static State state;
  return state;
}

gray::Bitmap null_bitmap{{}, nullptr};
Font null_font{0, 0, 0, nullptr};

struct InitResponse {
  InitResponse() {
    for (int i = 0; i < 256; i++) gray::response[i] = uint8_t(i);
  }
} init_response;

void UpdateMemory() {
  State &s = S();
  total_memory = s.device ? s.device->TotalMemory() : 0;
  free_memory = s.device ? s.device->FreeMemory() : 0;
}

// The glyphs `keep` of `src` as a blob in the glyph order of kSlots; -1 in
// `order` is an empty placeholder.
std::vector<uint8_t> BuildSubset(const FontBlobView &src,
                                 const std::vector<int> &keep, int scale) {
  std::vector<int> order, others;
  for (int i : keep) {
    int slot = SlotOf(src.codepoint(i));
    if (slot < 0) {
      others.push_back(i);
      continue;
    }
    if (int(order.size()) <= slot) order.resize(size_t(slot) + 1, -1);
    order[size_t(slot)] = i;
  }
  std::sort(others.begin(), others.end(),
            [&](int a, int b) { return src.codepoint(a) < src.codepoint(b); });
  if (!others.empty()) order.resize(kSlotCount, -1);
  order.insert(order.end(), others.begin(), others.end());
  int n = int(order.size());
  std::vector<int> new_index(src.count, -1);
  for (int i = 0; i < n; i++)
    if (order[i] >= 0) new_index[order[i]] = i;
  std::vector<std::pair<uint32_t, int>> kerns;
  for (int i = 0; i < src.kerns; i++) {
    uint32_t key = src.kern_key(i);
    int l = new_index[key >> 16], r = new_index[key & 0xFFFF];
    if (l >= 0 && r >= 0)
      kerns.push_back({uint32_t(l) << 16 | uint32_t(r),
                       std::clamp(src.kern_adjust(i) * scale, -128, 127)});
  }
  std::sort(kerns.begin(), kerns.end());
  int k = int(kerns.size());
  uint32_t bitmap_start = kHeaderSize + 16 * n + 5 * k;

  std::vector<uint8_t> table(12 * n), codepoints(4 * n), bits;
  uint64_t bitpos = 0;
  for (int i = 0; i < n; i++) {
    if (order[i] < 0) continue;
    Glyph g = src.glyph(order[i]);
    int w = g.w * scale, h = g.h * scale;
    uint8_t *e = &table[12 * i];
    Put32(e, w && h ? uint32_t(8 * bitmap_start + bitpos) : 0);
    e[4] = uint8_t(w && h ? w : 0);
    e[5] = uint8_t(w && h ? h : 0);
    e[6] = uint8_t(g.advance * scale);
    e[7] = 0;
    Put16(e + 8, uint16_t(int16_t(g.x_off * scale)));
    Put16(e + 10, uint16_t(int16_t(g.y_off * scale)));
    Put32(&codepoints[4 * i], src.codepoint(order[i]));
    if (!w || !h) continue;
    for (int r = 0; r < h; r++) {
      for (int c = 0; c < w; c++) {
        if (bitpos % 8 == 0) bits.push_back(0);
        if (src.bit(g.bits + uint32_t(r / scale) * g.w + uint32_t(c / scale)))
          bits.back() |= uint8_t(0x80 >> (bitpos % 8));
        bitpos++;
      }
    }
  }

  uint32_t size = bitmap_start + uint32_t(bits.size());
  std::vector<uint8_t> out(size, 0);
  std::memcpy(out.data(), "SKF", 3);
  out[3] = 1;
  Put32(&out[4], size);
  Put16(&out[8], uint32_t(n));
  Put16(&out[10], uint32_t(k));
  out[12] = uint8_t(std::min(255, src.p[12] * scale));
  out[13] = uint8_t(std::min(255, src.p[13] * scale));
  out[14] = uint8_t(std::min(255, src.p[14] * scale));
  out[15] = 0;
  std::memcpy(&out[kHeaderSize], table.data(), table.size());
  std::memcpy(&out[kHeaderSize + 12 * n], codepoints.data(), codepoints.size());
  for (int i = 0; i < k; i++) {
    Put32(&out[kHeaderSize + 16 * n + 4 * i], kerns[i].first);
    out[kHeaderSize + 16 * n + 4 * k + i] = uint8_t(int8_t(kerns[i].second));
  }
  if (!bits.empty()) std::memcpy(&out[bitmap_start], bits.data(), bits.size());
  return out;
}

Font &LoadFontLocked(const uint8_t *data, size_t len,
                     std::string_view glyphs_to_load,
                     std::string *error_message, int scale, Keep kept) {
  State &s = S();
  if (!s.device) {
    Fail(error_message, "not initialized");
    return null_font;
  }
  if (scale < 1 || scale > 16) {
    Fail(error_message, "scale must be 1...16");
    return null_font;
  }
  FontBlobView src;
  if (!ParseFontBlob(data, len, &src, error_message)) return null_font;

  std::vector<bool> wanted(src.count, glyphs_to_load.empty());
  for (size_t i = 0; i < glyphs_to_load.size();) {
    int g = src.find(device::NextCodepoint(glyphs_to_load, i));
    if (g >= 0) wanted[g] = true;
  }
  for (uint32_t cp : {0x20u, 0xFFFDu}) {
    int g = src.find(cp);
    if (g >= 0) wanted[g] = true;
  }
  std::vector<int> keep;
  for (int i = 0; i < src.count; i++) {
    if (!wanted[i] || src.codepoint(i) == 0) continue;
    Glyph g = src.glyph(i);
    if (g.w * scale > 255 || g.h * scale > 255 || g.advance * scale > 255) {
      Fail(error_message, "scale too large for this font");
      return null_font;
    }
    keep.push_back(i);
  }
  if (keep.empty()) {
    Fail(error_message, "none of the glyphs are in the font");
    return null_font;
  }

  auto impl = std::make_unique<FontImpl>();
  impl->blob = BuildSubset(src, keep, scale);
  ParseFontBlob(impl->blob.data(), impl->blob.size(), &impl->view, nullptr);
  impl->replacement = impl->view.find(0xFFFD);
  impl->keep = kept;
  auto font = std::make_unique<Font>();
  font->ascent = impl->blob[12];
  font->descent = impl->blob[13];
  font->line_spacing = impl->blob[14];
  font->pimpl = impl.get();
  if (!s.device->AddFont(*font, impl->blob, error_message)) return null_font;
  impl.release();
  s.fonts.push_back(std::move(font));
  UpdateMemory();
  if (error_message) error_message->clear();
  return *s.fonts.back();
}

void Layout(const FontImpl &f, std::string_view text,
            std::vector<device::GlyphPosition> *out, int *width) {
  int pen = 0, prev = -1;
  for (size_t i = 0; i < text.size();) {
    int g = f.view.find(device::NextCodepoint(text, i));
    if (g < 0) g = f.replacement;
    if (g < 0) continue;
    if (prev >= 0) pen += f.view.kern(prev, g);
    out->push_back({uint32_t(g), pen});
    pen += f.view.glyph(g).advance;
    prev = g;
  }
  *width = pen;
}

void FreeBitmap(gray::Bitmap *bitmap) {
  State &s = S();
  auto &p = s.placements;
  p.erase(std::remove_if(p.begin(), p.end(),
                         [&](const Placement &q) { return q.bitmap == bitmap; }),
          p.end());
  if (s.device) s.device->RemoveBitmap(*bitmap);
  delete static_cast<BitmapImpl *>(bitmap->pimpl);
  bitmap->pimpl = nullptr;
  bitmap->contents = {};
}

void FreeFont(Font *font) {
  State &s = S();
  if (s.device) s.device->RemoveFont(*font);
  delete static_cast<FontImpl *>(font->pimpl);
  font->pimpl = nullptr;
}

void UnloadAllLocked() {
  State &s = S();
  s.placements.clear();
  for (auto &b : s.bitmaps) FreeBitmap(b.get());
  for (auto &f : s.fonts) FreeFont(f.get());
  s.bitmaps.clear();
  s.fonts.clear();
  UpdateMemory();
}

// Response curve, then rounding or Floyd-Steinberg (serpentine) to the levels.
void Convert(gray::ConstBitmapView src, gray::BitmapView dst,
             gray::Dither dither) {
  int w = src.width, h = src.height;
  if (dither == gray::DITHER_NONE) {
    for (int y = 0; y < h; y++)
      for (int x = 0; x < w; x++)
        dst(x, y) = device::Gray(device::Level(gray::response[src(x, y)]));
    return;
  }
  std::vector<int> err(w + 2, 0), next(w + 2, 0); // sixteenths, index x + 1
  for (int y = 0; y < h; y++) {
    bool forward = y % 2 == 0;
    int dir = forward ? 1 : -1;
    std::fill(next.begin(), next.end(), 0);
    for (int i = 0; i < w; i++) {
      int x = forward ? i : w - 1 - i;
      int want = std::clamp(gray::response[src(x, y)] * 16 + err[x + 1], 0,
                            255 * 16);
      uint8_t nearest = uint8_t(std::min(255, (want + 8) / 16));
      uint8_t out = device::Gray(device::Level(nearest));
      dst(x, y) = out;
      int e = want - out * 16;
      int e7 = e * 7 / 16, e3 = e * 3 / 16, e5 = e * 5 / 16;
      err[x + 1 + dir] += e7;
      next[x + 1 - dir] += e3;
      next[x + 1] += e5;
      next[x + 1 + dir] += e - e7 - e3 - e5;
    }
    std::swap(err, next);
  }
}

bool Clip(const gray::BitmapView &v, int &x, int &y, int &w, int &h) {
  int x0 = std::max(x, 0), y0 = std::max(y, 0);
  int x1 = std::min(x + w, v.width), y1 = std::min(y + h, v.height);
  if (x0 >= x1 || y0 >= y1) return false;
  x = x0;
  y = y0;
  w = x1 - x0;
  h = y1 - y0;
  return true;
}

struct Rect {
  int x0, x1, y0, y1, owner;
};

// Splits each band of changed rows into rectangles with one owner each.
void Pieces(int bx0, int bx1, int by0, int by1,
            std::vector<device::Piece> *pieces) {
  State &s = S();
  int w = s.width;
  std::vector<Rect> open, next;
  auto emit = [&](const Rect &r) {
    const Placement &p = s.placements[r.owner];
    device::Piece piece{r.x0, r.y0, r.x1 - r.x0, r.y1 - r.y0, p.bitmap, 0, 0};
    if (p.bitmap) {
      piece.source_x = r.x0 - p.x;
      piece.source_y = r.y0 - p.y;
    }
    pieces->push_back(piece);
  };
  for (int y = by0; y < by1; y++) {
    next.clear();
    const int *row = &s.owner[size_t(y) * w];
    for (int x = bx0; x < bx1;) {
      int o = row[x];
      int e = x + 1;
      while (e < bx1 && row[e] == o) e++;
      if (o >= 0) {
        auto it = std::find_if(open.begin(), open.end(), [&](const Rect &r) {
          return r.x0 == x && r.x1 == e && r.owner == o;
        });
        if (it != open.end()) {
          it->y1 = y + 1;
          next.push_back(*it);
          open.erase(it);
        } else {
          next.push_back({x, e, y, y + 1, o});
        }
      }
      x = e;
    }
    for (const Rect &r : open) emit(r);
    std::swap(open, next);
  }
  for (const Rect &r : open) emit(r);
}

uint64_t FlushLocked() {
  State &s = S();
  if (!s.device) return 0;
  for (auto &b : s.bitmaps) {
    auto &c = b->contents;
    for (int y = 0; y < c.height; y++) {
      uint8_t *row = c.row(y);
      for (int x = 0; x < c.width; x++)
        row[x] = device::Gray(device::Level(row[x]));
    }
    s.device->Sync(*b);
  }
  if (s.placements.empty()) return 0;

  int w = s.width, h = s.height;
  s.composed = s.shown;
  s.owner.assign(size_t(w) * h, -1);
  for (int i = 0; i < int(s.placements.size()); i++) {
    const Placement &p = s.placements[i];
    if (!p.bitmap) {
      std::fill(s.composed.begin(), s.composed.end(), 0);
      std::fill(s.owner.begin(), s.owner.end(), i);
      continue;
    }
    const auto &c = p.bitmap->contents;
    int x0 = std::max(p.x, 0), y0 = std::max(p.y, 0);
    int x1 = std::min(p.x + c.width, w), y1 = std::min(p.y + c.height, h);
    for (int y = y0; y < y1; y++) {
      std::memcpy(&s.composed[size_t(y) * w + x0], c.row(y - p.y) + (x0 - p.x),
                  size_t(x1 - x0));
      std::fill_n(&s.owner[size_t(y) * w + x0], x1 - x0, i);
    }
  }

  std::vector<device::Piece> pieces;
  const int gap = 8;
  int band0 = -1, band1 = -1, bx0 = w, bx1 = 0;
  for (int y = 0; y <= h; y++) {
    int first = -1, last = -1;
    if (y < h) {
      const uint8_t *a = &s.shown[size_t(y) * w], *b = &s.composed[size_t(y) * w];
      if (std::memcmp(a, b, size_t(w)) != 0) {
        first = 0;
        while (a[first] == b[first]) first++;
        last = w - 1;
        while (a[last] == b[last]) last--;
      }
    }
    if (band0 >= 0 && (y == h || (first >= 0 && y - band1 > gap))) {
      Pieces(bx0, bx1, band0, band1, &pieces);
      band0 = -1;
      bx0 = w;
      bx1 = 0;
    }
    if (first >= 0) {
      if (band0 < 0) band0 = y;
      band1 = y + 1;
      bx0 = std::min(bx0, first);
      bx1 = std::max(bx1, last + 1);
    }
  }

  uint64_t id = 0;
  if (!pieces.empty()) id = s.device->Present(pieces);
  s.shown.swap(s.composed);
  gray::display.data = s.shown.data();
  s.placements.clear();
  return id;
}

} // namespace

void Font::Measure(std::string_view text, int *width, int *height,
                   int *ascent_out, int *descent_out) {
  std::lock_guard<std::mutex> lock(S().mutex);
  auto *f = static_cast<FontImpl *>(pimpl);
  std::vector<device::GlyphPosition> glyphs;
  int advance = 0, top = 0, bottom = 0;
  bool ink = false;
  if (f) {
    Layout(*f, text, &glyphs, &advance);
    for (const auto &g : glyphs) {
      Glyph gl = f->view.glyph(int(g.index));
      if (!gl.w || !gl.h) continue;
      top = ink ? std::max(top, -gl.y_off) : -gl.y_off;
      bottom = ink ? std::max(bottom, gl.y_off + gl.h) : gl.y_off + gl.h;
      ink = true;
    }
  }
  if (width) *width = advance;
  if (height) *height = top + bottom;
  if (ascent_out) *ascent_out = top;
  if (descent_out) *descent_out = bottom;
}

void Font::DrawLine(gray::Bitmap &target, std::string_view text, int x,
                    int baseline_y, TextAlign align, int *out_width,
                    uint8_t gray) {
  State &s = S();
  std::lock_guard<std::mutex> lock(s.mutex);
  auto *f = static_cast<FontImpl *>(pimpl);
  if (out_width) *out_width = 0;
  if (!f || !target.pimpl || !s.device) return;
  std::vector<device::GlyphPosition> glyphs;
  int width = 0;
  Layout(*f, text, &glyphs, &width);
  if (out_width) *out_width = width;
  int start = align == ALIGN_LEFT     ? x
              : align == ALIGN_CENTER ? x - width / 2
                                      : x - width;
  int level = device::Level(gray);
  uint8_t value = device::Gray(level);
  gray::BitmapView c = target.contents;

  std::vector<device::GlyphPosition> run;
  bool run_has_ink = false;
  auto send = [&]() {
    while (!run.empty()) {
      Glyph gl = f->view.glyph(int(run.back().index));
      if (gl.w && gl.h) break;
      run.pop_back();
    }
    if (run_has_ink && !run.empty())
      s.device->GlyphRun(*this, target, baseline_y, run, level);
    run.clear();
    run_has_ink = false;
  };
  for (const auto &g : glyphs) {
    int pen = start + g.pen_x;
    Glyph gl = f->view.glyph(int(g.index));
    bool empty = !gl.w || !gl.h;
    int gx = pen + gl.x_off, gy = baseline_y + gl.y_off;
    if (!empty) {
      for (int r = 0; r < gl.h; r++) {
        int py = gy + r;
        if (py < 0 || py >= c.height) continue;
        for (int col = 0; col < gl.w; col++) {
          int px = gx + col;
          if (px < 0 || px >= c.width) continue;
          if (f->view.bit(gl.bits + uint32_t(r) * gl.w + uint32_t(col)))
            c(px, py) = value;
        }
      }
    }
    bool outside = !empty && (gx >= c.width || gx + gl.w <= 0 || gy + gl.h <= 0);
    bool below = !empty && gy + gl.h > c.height;
    bool in_range = pen >= -32768 && pen <= 32767 && baseline_y >= -32768 &&
                    baseline_y <= 32767;
    if (outside || below || !in_range) {
      send();
      continue;
    }
    if (empty && run.empty()) continue;
    run.push_back({g.index, pen});
    if (!empty) run_has_ink = true;
  }
  send();
}

namespace gray {

static Bitmap &CreateBitmapKept(int width, int height, Keep keep) {
  State &s = S();
  std::lock_guard<std::mutex> lock(s.mutex);
  if (!s.device || width <= 0 || height <= 0) return null_bitmap;
  auto impl = std::make_unique<BitmapImpl>();
  impl->pixels.assign(size_t(width) * height, 0);
  impl->keep = keep;
  auto bitmap = std::make_unique<Bitmap>();
  bitmap->contents = {impl->pixels.data(), width, height, width};
  bitmap->pimpl = impl.get();
  std::string error;
  if (!s.device->AddBitmap(*bitmap, &error)) return null_bitmap;
  impl.release();
  s.bitmaps.push_back(std::move(bitmap));
  UpdateMemory();
  return *s.bitmaps.back();
}

void Write(Bitmap &bitmap, ConstBitmapView pixels, int x, int y,
           Dither dither) {
  std::lock_guard<std::mutex> lock(S().mutex);
  if (!bitmap.pimpl || pixels.empty()) return;
  int w = pixels.width, h = pixels.height, x0 = x, y0 = y;
  if (!Clip(bitmap.contents, x0, y0, w, h)) return;
  ConstBitmapView src = pixels.sub(x0 - x, y0 - y, w, h);
  Convert(src, bitmap.contents.sub(x0, y0, w, h), dither);
}

Bitmap &CreateBitmap(int width, int height) {
  return CreateBitmapKept(width, height, Keep::kSession);
}

Bitmap &LoadBitmap(ConstBitmapView pixels, Dither dither, Keep keep) {
  if (pixels.empty()) return null_bitmap;
  Bitmap &bitmap = CreateBitmapKept(pixels.width, pixels.height, keep);
  Write(bitmap, pixels, 0, 0, dither);
  return bitmap;
}

void Fill(Bitmap &bitmap, int x, int y, int w, int h, uint8_t gray) {
  State &s = S();
  std::lock_guard<std::mutex> lock(s.mutex);
  if (!bitmap.pimpl || !s.device || !Clip(bitmap.contents, x, y, w, h)) return;
  int level = device::Level(gray);
  uint8_t value = device::Gray(level);
  for (int r = 0; r < h; r++) std::memset(bitmap.contents.row(y + r) + x, value, size_t(w));
  s.device->Fill(bitmap, x, y, w, h, level);
}

void Draw(const Bitmap &bitmap, int x, int y) {
  std::lock_guard<std::mutex> lock(S().mutex);
  if (bitmap.pimpl) S().placements.push_back({&bitmap, x, y});
}

void Clear() {
  std::lock_guard<std::mutex> lock(S().mutex);
  if (S().device) S().placements.push_back({nullptr, 0, 0});
}

void Unload(Bitmap &bitmap) {
  State &s = S();
  std::lock_guard<std::mutex> lock(s.mutex);
  auto it = std::find_if(s.bitmaps.begin(), s.bitmaps.end(),
                         [&](const auto &b) { return b.get() == &bitmap; });
  if (it == s.bitmaps.end()) return;
  FreeBitmap(it->get());
  s.bitmaps.erase(it);
  UpdateMemory();
}

} // namespace gray

Font &LoadFont(std::span<const char> scouter_font,
               std::string_view glyphs_to_load, std::string *error_message,
               int scale, Keep keep) {
  std::lock_guard<std::mutex> lock(S().mutex);
  return LoadFontLocked(reinterpret_cast<const uint8_t *>(scouter_font.data()),
                        scouter_font.size(), glyphs_to_load, error_message,
                        scale, keep);
}

Font &LoadFont(std::span<const uint8_t> scouter_font,
               std::string_view glyphs_to_load, std::string *error_message,
               int scale, Keep keep) {
  std::lock_guard<std::mutex> lock(S().mutex);
  return LoadFontLocked(scouter_font.data(), scouter_font.size(),
                        glyphs_to_load, error_message, scale, keep);
}

void Unload(Font &font) {
  State &s = S();
  std::lock_guard<std::mutex> lock(s.mutex);
  auto it = std::find_if(s.fonts.begin(), s.fonts.end(),
                         [&](const auto &f) { return f.get() == &font; });
  if (it == s.fonts.end()) return;
  FreeFont(it->get());
  s.fonts.erase(it);
  UpdateMemory();
}

void UnloadAll() {
  std::lock_guard<std::mutex> lock(S().mutex);
  UnloadAllLocked();
}

void Flush() {
  State &s = S();
  uint64_t wait_for = 0;
  device::Device *dev = nullptr;
  {
    std::lock_guard<std::mutex> lock(s.mutex);
    uint64_t id = FlushLocked();
    if (id) {
      wait_for = s.last_present;
      s.last_present = id;
    }
    dev = s.device;
  }
  if (dev && wait_for) dev->WaitPresented(wait_for);
}

namespace device {

uint32_t NextCodepoint(std::string_view s, size_t &i) {
  uint8_t c = uint8_t(s[i++]);
  if (c < 0x80) return c;
  int more;
  uint32_t cp;
  if ((c & 0xE0) == 0xC0) {
    more = 1;
    cp = c & 0x1F;
  } else if ((c & 0xF0) == 0xE0) {
    more = 2;
    cp = c & 0x0F;
  } else if ((c & 0xF8) == 0xF0) {
    more = 3;
    cp = c & 0x07;
  } else {
    return 0xFFFD;
  }
  for (int k = 0; k < more; k++) {
    if (i >= s.size() || (uint8_t(s[i]) & 0xC0) != 0x80) return 0xFFFD;
    cp = cp << 6 | (uint8_t(s[i++]) & 0x3F);
  }
  return cp;
}

void Attach(Device *dev, int width, int height, int levels) {
  State &s = S();
  s.device = dev;
  s.width = width;
  s.height = height;
  s.levels = levels;
  s.shown.assign(size_t(width) * height, 0);
  s.placements.clear();
  s.last_present = 0;
  gray::display = {s.shown.data(), width, height, width};
  UpdateMemory();
}

void Detach() {
  State &s = S();
  UnloadAllLocked();
  s.device = nullptr;
  s.shown.clear();
  s.composed.clear();
  s.owner.clear();
  gray::display = {};
  UpdateMemory();
}

std::mutex &Lock() { return S().mutex; }

void *&DeviceData(gray::Bitmap &bitmap) {
  return static_cast<BitmapImpl *>(bitmap.pimpl)->device;
}

void *&DeviceData(Font &font) {
  return static_cast<FontImpl *>(font.pimpl)->device;
}

std::span<const uint8_t> FontBlob(const Font &font) {
  return static_cast<const FontImpl *>(font.pimpl)->blob;
}

Keep KeptAs(const Font &font) {
  return static_cast<const FontImpl *>(font.pimpl)->keep;
}

Keep KeptAs(const gray::Bitmap &bitmap) {
  return static_cast<const BitmapImpl *>(bitmap.pimpl)->keep;
}

int Level(uint8_t gray) {
  int top = S().levels - 1;
  return (gray * top + 127) / 255;
}

uint8_t Gray(int level) {
  int top = S().levels - 1;
  return uint8_t((level * 255 + top / 2) / top);
}

} // namespace device

} // namespace scouter_kit
