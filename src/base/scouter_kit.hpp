#pragma once

#include <cstddef>
#include <cstdint>
#include <mutex>
#include <span>
#include <string>
#include <string_view>
#include <type_traits>

// Provides a display abstraction for some specific set of smart glasses.
namespace scouter_kit {

// A 2D view of pixels owned by someone else: pixel (x, y) is data[y * stride + x].
template <typename T> struct View2D {
  T *data = nullptr;
  int width = 0;
  int height = 0;
  int stride = 0; // in elements

  T &operator()(int x, int y) const {
    return data[static_cast<std::ptrdiff_t>(y) * stride + x];
  }
  T *row(int y) const { return data + static_cast<std::ptrdiff_t>(y) * stride; }
  bool empty() const { return data == nullptr || width <= 0 || height <= 0; }
  // The rectangle must lie inside the view.
  View2D sub(int x, int y, int w, int h) const { return {&(*this)(x, y), w, h, stride}; }
  operator View2D<const T>() const
    requires(!std::is_const_v<T>)
  {
    return {data, width, height, stride};
  }
};

enum TextAlign {
  ALIGN_LEFT,
  ALIGN_CENTER,
  ALIGN_RIGHT,
};

// Where the device keeps a font or bitmap: kSession for this session only
// (sent again after a reboot), kStored also in the device's own storage (the
// G1: each arm's flash), so a reboot or a later session brings it back
// without sending it again.
enum class Keep { kSession, kStored };

namespace gray {
using BitmapView = View2D<uint8_t>;
using ConstBitmapView = View2D<const uint8_t>;
struct Bitmap;
} // namespace gray

struct Font {
  int ascent;       // pixels above the baseline
  int descent;      // pixels below the baseline
  int line_spacing; // baseline-to-baseline distance

  // width = advance width of the text; height, ascent and descent of its ink.
  void Measure(std::string_view text, int *width = nullptr,
               int *height = nullptr, int *ascent = nullptr,
               int *descent = nullptr);

  // Draws one line of UTF-8 text into the bitmap (glyph runs on the device).
  // x is the left edge, the centre or the right edge of the text.
  void DrawLine(gray::Bitmap &target, std::string_view text, int x,
                int baseline_y, TextAlign = ALIGN_LEFT,
                int *out_width = nullptr, uint8_t gray = 255);

  void *pimpl;
};

namespace gray {

// Pixels are 0 (off, see-through) ... 255 (brightest). The device rounds them
// to its own levels; contents and display always hold the rounded values.
struct Bitmap {
  // What the bitmap holds. Writing pixels here is allowed: Flush() uploads
  // the changes (rounded, without the response curve or dithering).
  BitmapView contents;

  void *pimpl;
};

// Allows read-back of the current display contents (as of the last Flush).
// Read-only.
extern BitmapView display;

// Level-response curve applied before dithering by LoadBitmap and Write
// (identity by default). Set it to calibrate the panel's levels.
extern uint8_t response[256];

enum Dither {
  DITHER_NONE,
  DITHER_FLOYD_STEINBERG,
};

// Bitmaps live in the device's memory. On failure (out of memory, bad size)
// the returned bitmap is empty: contents.data == nullptr.
Bitmap &LoadBitmap(ConstBitmapView pixels, Dither = DITHER_FLOYD_STEINBERG,
                   Keep = Keep::kSession);
Bitmap &CreateBitmap(int width, int height); // all pixels 0

// Puts pixels into the bitmap at (x, y): response curve, then dithering.
void Write(Bitmap &bitmap, ConstBitmapView pixels, int x = 0, int y = 0,
           Dither = DITHER_FLOYD_STEINBERG);
void Fill(Bitmap &bitmap, int x, int y, int w, int h, uint8_t gray);

// Places the bitmap on the virtual display (later calls draw on top).
// Flush() shows it on both eyes in sync, with the bitmap's contents at that
// time.
void Draw(const Bitmap &bitmap, int x, int y);
// Places black over the whole display.
void Clear();

void Unload(Bitmap &bitmap);

} // namespace gray

// scouter_font is a font blob (font_converter; the optional
// base/departure_mono.inc defines kDepartureMonoBlob). glyphs_to_load is the
// UTF-8 text of the characters to load; empty loads all of them. The space
// and U+FFFD are added when the blob has them. scale > 1 enlarges the glyphs.
// On failure the returned font has pimpl == nullptr.
Font &LoadFont(std::span<const char> scouter_font,
               std::string_view glyphs_to_load,
               std::string *error_message = nullptr, int scale = 1,
               Keep = Keep::kSession);
Font &LoadFont(std::span<const uint8_t> scouter_font,
               std::string_view glyphs_to_load,
               std::string *error_message = nullptr, int scale = 1,
               Keep = Keep::kSession);
void Unload(Font &font);

// Unloads fonts & bitmaps. After this call free_memory == total_memory
void UnloadAll();

// Device memory for fonts and bitmaps, in bytes.
extern int total_memory;
extern int free_memory;

// Uploads changed bitmap pixels and shows what Draw() placed since the last
// Flush. Returns when the update is queued; it waits first while the
// previous update is not on the display yet.
void Flush();

// The interface between the device-neutral part and a device backend
// (scouter_kit_g1). Applications do not use it.
namespace device {

// A rectangle of the display shown from a bitmap, or black (source nullptr).
struct Piece {
  int x, y, w, h;
  const gray::Bitmap *source;
  int source_x, source_y;
};

struct GlyphPosition {
  uint32_t index; // glyph index in the font blob
  int pen_x;      // pen position (kerning included)
};

class Device {
public:
  virtual ~Device() = default;
  // Reserves device memory for the bitmap (all pixels 0).
  virtual bool AddBitmap(gray::Bitmap &bitmap, std::string *error) = 0;
  virtual void RemoveBitmap(gray::Bitmap &bitmap) = 0;
  virtual bool AddFont(Font &font, std::span<const uint8_t> blob,
                       std::string *error) = 0;
  virtual void RemoveFont(Font &font) = 0;
  virtual void Fill(gray::Bitmap &bitmap, int x, int y, int w, int h,
                    int level) = 0;
  // Consecutive glyphs of one line; every glyph ends above the bitmap's
  // bottom edge.
  virtual void GlyphRun(Font &font, gray::Bitmap &bitmap, int baseline_y,
                        std::span<const GlyphPosition> glyphs, int level) = 0;
  // Uploads the bitmap's contents where the device holds other pixels.
  virtual void Sync(gray::Bitmap &bitmap) = 0;
  // Shows the pieces in order. Returns an id for WaitPresented.
  virtual uint64_t Present(std::span<const Piece> pieces) = 0;
  // Called without the lock: waits until present `id` is shown (or failed).
  virtual void WaitPresented(uint64_t id) = 0;
  virtual int TotalMemory() = 0;
  virtual int FreeMemory() = 0;
};

// The backend attaches itself in its Init. `levels` is the number of gray
// levels of the panel (16 for the G1).
void Attach(Device *device, int width, int height, int levels);
void Detach();
// Every API call runs under this lock; a backend thread takes it too.
std::mutex &Lock();
// A slot for the backend's own data per bitmap and font.
void *&DeviceData(gray::Bitmap &bitmap);
void *&DeviceData(Font &font);
// The font blob as uploaded (the loaded subset).
std::span<const uint8_t> FontBlob(const Font &font);
// What LoadFont and LoadBitmap were asked to keep (CreateBitmap: kSession).
Keep KeptAs(const Font &font);
Keep KeptAs(const gray::Bitmap &bitmap);
int Level(uint8_t gray);       // 0..255 -> device level
uint8_t Gray(int level);       // device level -> 0..255
// One UTF-8 character of `text` from byte i, which moves past it; U+FFFD for
// an invalid or cut-off sequence.
uint32_t NextCodepoint(std::string_view text, size_t &i);

} // namespace device

} // namespace scouter_kit
