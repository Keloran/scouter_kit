#include "scouter_kit_c.h"

#include <algorithm>
#include <cstring>
#include <string>
#include <vector>

#include "../base/scouter_kit.hpp"
#include "../g1/scouter_kit_g1.hpp"

namespace sk = scouter_kit;
namespace g1 = scouter_kit::g1;

struct sk_g1_cancellation { g1::Cancellation value; };

namespace {

sk::Font *F(sk_font *font) { return reinterpret_cast<sk::Font *>(font); }
const sk::Font *F(const sk_font *font) { return reinterpret_cast<const sk::Font *>(font); }
sk::gray::Bitmap *B(sk_bitmap *bitmap) { return reinterpret_cast<sk::gray::Bitmap *>(bitmap); }
const sk::gray::Bitmap *B(const sk_bitmap *bitmap) {
  return reinterpret_cast<const sk::gray::Bitmap *>(bitmap);
}

g1::Arm *A(int arm) { return arm == 0 ? &g1::left : &g1::right; }

void CopyText(const std::string &text, char *out, size_t size) {
  if (!out || size == 0) return;
  size_t n = std::min(text.size(), size - 1);
  std::memcpy(out, text.data(), n);
  out[n] = 0;
}

sk_view View(const sk::gray::BitmapView &v) { return {v.data, v.width, v.height, v.stride}; }

sk::gray::ConstBitmapView Pixels(const uint8_t *pixels, int width, int height, int stride) {
  return {pixels, width, height, stride};
}

sk::gray::Dither DitherOf(int dither) {
  return dither == SK_DITHER_NONE ? sk::gray::DITHER_NONE : sk::gray::DITHER_FLOYD_STEINBERG;
}

g1::Mode ModeOf(int mode) { return g1::Mode(mode); }

} // namespace

struct sk_g1_program {
  g1::Program p;
};

struct sk_g1_video_encoder {
  g1::VideoEncoder e;
  std::vector<uint8_t> record;
};

namespace {

g1::Program::Label L(int label) { return g1::Program::Label(label); }

} // namespace

extern "C" {

sk_font *sk_load_font(const void *blob, size_t size, const char *glyphs_to_load, int scale,
                      char *error, size_t error_size) {
  return sk_load_font_keep(blob, size, glyphs_to_load, scale, SK_KEEP_SESSION, error, error_size);
}

sk_font *sk_load_font_keep(const void *blob, size_t size, const char *glyphs_to_load, int scale, int keep,
                           char *error, size_t error_size) {
  std::string text;
  std::span<const uint8_t> data(static_cast<const uint8_t *>(blob), size);
  sk::Font &font = sk::LoadFont(data, glyphs_to_load ? glyphs_to_load : "", &text, scale,
                                keep == SK_KEEP_STORED ? sk::Keep::kStored : sk::Keep::kSession);
  CopyText(text, error, error_size);
  return font.pimpl ? reinterpret_cast<sk_font *>(&font) : nullptr;
}

void sk_font_metrics(const sk_font *font, int *ascent, int *descent, int *line_spacing) {
  const sk::Font *f = F(font);
  if (ascent) *ascent = f ? f->ascent : 0;
  if (descent) *descent = f ? f->descent : 0;
  if (line_spacing) *line_spacing = f ? f->line_spacing : 0;
}

void sk_font_measure(sk_font *font, const char *text, int *width, int *height, int *ascent,
                     int *descent) {
  if (!font) return;
  F(font)->Measure(text ? text : "", width, height, ascent, descent);
}

int sk_font_draw_line(sk_font *font, sk_bitmap *target, const char *text, int x, int baseline_y,
                      int align, uint8_t gray) {
  if (!font || !target) return 0;
  int width = 0;
  F(font)->DrawLine(*B(target), text ? text : "", x, baseline_y, sk::TextAlign(align), &width, gray);
  return width;
}

void sk_unload_font(sk_font *font) {
  if (font) sk::Unload(*F(font));
}

sk_bitmap *sk_load_bitmap(const uint8_t *pixels, int width, int height, int stride, int dither) {
  return sk_load_bitmap_keep(pixels, width, height, stride, dither, SK_KEEP_SESSION);
}

sk_bitmap *sk_load_bitmap_keep(const uint8_t *pixels, int width, int height, int stride, int dither,
                               int keep) {
  sk::gray::Bitmap &b = sk::gray::LoadBitmap(Pixels(pixels, width, height, stride), DitherOf(dither),
                                             keep == SK_KEEP_STORED ? sk::Keep::kStored : sk::Keep::kSession);
  return b.pimpl ? reinterpret_cast<sk_bitmap *>(&b) : nullptr;
}

sk_bitmap *sk_create_bitmap(int width, int height) {
  sk::gray::Bitmap &b = sk::gray::CreateBitmap(width, height);
  return b.pimpl ? reinterpret_cast<sk_bitmap *>(&b) : nullptr;
}

sk_view sk_bitmap_contents(const sk_bitmap *bitmap) {
  return bitmap ? View(B(bitmap)->contents) : sk_view{nullptr, 0, 0, 0};
}

void sk_write(sk_bitmap *bitmap, const uint8_t *pixels, int width, int height, int stride, int x,
              int y, int dither) {
  if (bitmap)
    sk::gray::Write(*B(bitmap), Pixels(pixels, width, height, stride), x, y, DitherOf(dither));
}

void sk_fill(sk_bitmap *bitmap, int x, int y, int w, int h, uint8_t gray) {
  if (bitmap) sk::gray::Fill(*B(bitmap), x, y, w, h, gray);
}

void sk_draw(const sk_bitmap *bitmap, int x, int y) {
  if (bitmap) sk::gray::Draw(*B(bitmap), x, y);
}

void sk_clear(void) { sk::gray::Clear(); }

void sk_unload_bitmap(sk_bitmap *bitmap) {
  if (bitmap) sk::gray::Unload(*B(bitmap));
}

sk_view sk_display(void) { return View(sk::gray::display); }

uint8_t *sk_response(void) { return sk::gray::response; }

void sk_unload_all(void) { sk::UnloadAll(); }

int sk_total_memory(void) { return sk::total_memory; }

int sk_free_memory(void) { return sk::free_memory; }

void sk_flush(void) { sk::Flush(); }

int sk_g1_init(int dx, int dy, const char *left, const char *right, const char *adapter,
               int timeout_ms, char *error, size_t error_size) {
  sk_g1_config c{dx, dy, left, right, adapter, timeout_ms, 0, 0};
  return sk_g1_init_config(&c, error, error_size);
}

int sk_g1_init_config(const sk_g1_config *c, char *error, size_t error_size) {
  return sk_g1_init_cancelable(c, nullptr, error, error_size);
}

sk_g1_cancellation *sk_g1_cancellation_new(void) {
  try { return new sk_g1_cancellation; } catch (...) { return nullptr; }
}
sk_g1_cancellation *sk_g1_cancellation_clone(const sk_g1_cancellation *token) {
  if (!token) return nullptr;
  try { return new sk_g1_cancellation(*token); } catch (...) { return nullptr; }
}
void sk_g1_cancellation_cancel(sk_g1_cancellation *token) { if (token) token->value.Cancel(); }
int sk_g1_cancellation_cancelled(const sk_g1_cancellation *token) { return token && token->value.Cancelled(); }
void sk_g1_cancellation_free(sk_g1_cancellation *token) { delete token; }

int sk_g1_init_cancelable(const sk_g1_config *c, const sk_g1_cancellation *token,
                          char *error, size_t error_size) {
  g1::GrayConfig config;
  config.dx = c->dx;
  config.dy = c->dy;
  g1::Connection connection;
  connection.left = c->left ? c->left : "";
  connection.right = c->right ? c->right : "";
  connection.adapter = c->adapter ? c->adapter : "";
  if (c->timeout_ms > 0) connection.timeout_ms = c->timeout_ms;
  connection.keep_sessions = c->keep_sessions != 0;
  connection.log = c->log != 0;
  std::string text;
  g1::InitError e = token ? g1::InitCancelable(token->value, &config, &text, &connection)
                         : g1::Init(&config, &text, &connection);
  CopyText(text, error, error_size);
  switch (e) {
  case g1::ERROR_NONE: return SK_G1_ERROR_NONE;
  case g1::ERROR_NO_G1_FOUND: return SK_G1_ERROR_NO_G1_FOUND;
  case g1::ERROR_G1_BONDING_ERROR: return SK_G1_ERROR_BONDING;
  case g1::ERROR_G1_FIRMWARE: return SK_G1_ERROR_FIRMWARE;
  case g1::ERROR_CANCELLED: return SK_G1_ERROR_CANCELLED;
  default: return SK_G1_ERROR_BLUETOOTH;
  }
}

void sk_g1_deinit(void) { g1::Deinit(); }

int64_t sk_g1_now_us(void) { return g1::NowUs(); }

void sk_g1_write_arena(int arm, uint32_t address, const uint8_t *bytes, size_t n) {
  A(arm)->WriteArena(address, {bytes, n});
}

void sk_g1_write_arena_rle(int arm, uint32_t address, uint16_t out_length, const uint8_t *packbits,
                           size_t n) {
  A(arm)->WriteArenaRLE(address, out_length, {packbits, n});
}

void sk_g1_copy_arena(int arm, uint32_t src, uint32_t dst, uint32_t n) { A(arm)->CopyArena(src, dst, n); }

void sk_g1_unpack(int arm, uint32_t dst, uint32_t src, uint32_t n, uint32_t out_length) {
  A(arm)->Unpack(dst, src, n, out_length);
}

void sk_g1_fill(int arm, uint32_t dst, uint32_t stride, int32_t x, int32_t y, uint32_t w, uint32_t h,
                uint8_t level) {
  A(arm)->Fill(dst, stride, x, y, w, h, level);
}

void sk_g1_blit(int arm, uint32_t src, uint32_t src_stride, int32_t src_x, int32_t src_y, uint32_t dst,
                uint32_t dst_stride, int32_t dst_x, int32_t dst_y, uint32_t w, uint32_t h, int mode,
                uint8_t level) {
  A(arm)->Blit(src, src_stride, src_x, src_y, dst, dst_stride, dst_x, dst_y, w, h, ModeOf(mode), level);
}

void sk_g1_blit_compressed(int arm, uint32_t src, uint32_t src_stride, int32_t src_x, int32_t src_y,
                           uint32_t dst, uint32_t dst_stride, int32_t dst_x, int32_t dst_y, uint32_t w,
                           uint32_t h, int mode, uint8_t level) {
  A(arm)->BlitCompressed(src, src_stride, src_x, src_y, dst, dst_stride, dst_x, dst_y, w, h, ModeOf(mode),
                         level);
}

void sk_g1_glyph_run_compressed(int arm, uint32_t font, uint32_t dst, uint32_t dst_stride, int32_t x,
                                int32_t y, int mode, const uint8_t *varints, size_t n, uint8_t level) {
  A(arm)->GlyphRunCompressed(font, dst, dst_stride, x, y, ModeOf(mode), {varints, n}, level);
}

void sk_g1_draw(int arm, uint32_t src, uint32_t src_stride, int32_t src_x, int32_t src_y, int32_t dst_x,
                int32_t dst_y, uint32_t w, uint32_t h) {
  A(arm)->Draw(src, src_stride, src_x, src_y, dst_x, dst_y, w, h);
}

void sk_g1_line(int arm, uint32_t dst, uint32_t stride, int32_t x0, int32_t y0, int32_t x1, int32_t y1,
                uint8_t level) {
  A(arm)->Line(dst, stride, x0, y0, x1, y1, level);
}

void sk_g1_video(int arm, uint32_t bitmap, uint32_t stride, uint32_t w, uint32_t h, int32_t x, int32_t y,
                 uint8_t level, uint32_t src, uint32_t n, uint32_t state) {
  A(arm)->Video(bitmap, stride, w, h, x, y, level, src, n, state);
}

uint32_t sk_g1_video_state(void) { return g1::kVideoState; }

sk_g1_video_encoder *sk_g1_video_encoder_new(int width, int height) {
  if (width < 1 || width > 640 || height < 1 || height > 480) return nullptr;
  return new sk_g1_video_encoder{g1::VideoEncoder(width, height), {}};
}

void sk_g1_video_encoder_free(sk_g1_video_encoder *encoder) { delete encoder; }

const uint8_t *sk_g1_video_encode(sk_g1_video_encoder *encoder, const uint8_t *pixels, int redraw,
                                  size_t max_bytes, size_t *n) {
  const g1::VideoEncoder &e = encoder->e;
  encoder->record = encoder->e.Encode({pixels, size_t(e.Width()) * size_t(e.Height())}, redraw != 0, max_bytes);
  *n = encoder->record.size();
  return *n ? encoder->record.data() : nullptr;
}

void sk_g1_wait_until(int arm, uint32_t device_time_us) { A(arm)->WaitUntil(device_time_us); }

void sk_g1_set_panel(int arm, int on) { A(arm)->SetPanel(on != 0); }

void sk_g1_sys_write(int arm, int property, uint32_t value) {
  A(arm)->SysWrite(g1::Property(property), value);
}

void sk_g1_fence(int arm, uint32_t token) { A(arm)->Fence(token); }

void sk_g1_notify(int arm, uint32_t a, uint32_t b) { A(arm)->Notify(a, b); }

void sk_g1_raise(int arm, int trigger) { A(arm)->Raise(g1::Trigger(trigger)); }

void sk_g1_call(int arm, uint32_t address) { A(arm)->Call(address); }

void sk_g1_run(int arm, const uint8_t *host_code, size_t n) { A(arm)->Run({host_code, n}); }

sk_g1_program *sk_g1_program_new(uint32_t table, uint32_t pool, int pool_words, uint32_t code) {
  return new sk_g1_program{g1::Program(table, pool, pool_words, code)};
}

void sk_g1_program_free(sk_g1_program *program) { delete program; }

int sk_g1_program_label(sk_g1_program *program) { return program->p.NewLabel().id; }

int sk_g1_program_label_at(sk_g1_program *program, uint32_t address) { return program->p.At(address).id; }

void sk_g1_program_bind(sk_g1_program *program, int label) { program->p.Bind(L(label)); }

void sk_g1_program_panel_policy(sk_g1_program *program, int redraw) { program->p.PanelPolicy(L(redraw)); }

void sk_g1_program_on(sk_g1_program *program, int trigger, int label) {
  program->p.On(g1::Trigger(trigger), L(label));
}

uint16_t sk_g1_program_const(sk_g1_program *program, uint32_t value) { return program->p.Const(value); }

void sk_g1_program_changes(sk_g1_program *program, uint32_t address, uint32_t length) {
  program->p.Changes(address, length);
}

uint32_t sk_g1_program_here(const sk_g1_program *program) { return program->p.Here(); }

void sk_g1_program_op(sk_g1_program *program, int opcode, const uint16_t *operands, int n, int label) {
  switch (n) {
#define SK_OPS(...) program->p.Op(uint8_t(opcode), {__VA_ARGS__}, L(label)); break;
  case 0: SK_OPS()
  case 1: SK_OPS(operands[0])
  case 2: SK_OPS(operands[0], operands[1])
  case 3: SK_OPS(operands[0], operands[1], operands[2])
  case 4: SK_OPS(operands[0], operands[1], operands[2], operands[3])
  case 7: SK_OPS(operands[0], operands[1], operands[2], operands[3], operands[4], operands[5], operands[6])
  case 8:
    SK_OPS(operands[0], operands[1], operands[2], operands[3], operands[4], operands[5], operands[6],
           operands[7])
  case 9:
    SK_OPS(operands[0], operands[1], operands[2], operands[3], operands[4], operands[5], operands[6],
           operands[7], operands[8])
  case 10:
    SK_OPS(operands[0], operands[1], operands[2], operands[3], operands[4], operands[5], operands[6],
           operands[7], operands[8], operands[9])
  case 12:
    SK_OPS(operands[0], operands[1], operands[2], operands[3], operands[4], operands[5], operands[6],
           operands[7], operands[8], operands[9], operands[10], operands[11])
#undef SK_OPS
  default: program->p.Op(uint8_t(opcode), {0, 0, 0, 0, 0, 0}, L(label)); // not an operand count: fails
  }
}

void sk_g1_program_write(sk_g1_program *program, uint32_t address, const uint8_t *bytes, size_t n) {
  program->p.Write(address, {bytes, n});
}

void sk_g1_program_write_rle(sk_g1_program *program, uint32_t address, uint16_t length,
                             const uint8_t *packbits, size_t n) {
  program->p.WriteRLE(address, length, {packbits, n});
}

int sk_g1_program_valid(const sk_g1_program *program, char *error, size_t error_size) {
  std::string text;
  bool ok = program->p.Valid(&text);
  CopyText(text, error, error_size);
  return ok;
}

const uint8_t *sk_g1_program_code(const sk_g1_program *program, size_t *n) {
  if (n) *n = program->p.code.size();
  return program->p.code.data();
}

const uint32_t *sk_g1_program_pool(const sk_g1_program *program, size_t *n) {
  if (n) *n = program->p.pool.size();
  return program->p.pool.data();
}

const uint32_t *sk_g1_program_table(const sk_g1_program *program) { return program->p.table.data(); }

void sk_g1_asset_id(const void *blob, size_t n, uint8_t id[16]) {
  g1::AssetId a = g1::AssetIdOf({static_cast<const uint8_t *>(blob), n});
  std::memcpy(id, a.b, 16);
}

int sk_g1_store(const void *blob, size_t n, int type, char *error, size_t error_size) {
  std::string text;
  bool ok = g1::Store({static_cast<const uint8_t *>(blob), n}, uint8_t(type), &text);
  CopyText(text, error, error_size);
  return ok;
}

int sk_g1_forget(const uint8_t id[16], char *error, size_t error_size) {
  g1::AssetId a;
  std::memcpy(a.b, id, 16);
  std::string text;
  bool ok = g1::Forget(a, &text);
  CopyText(text, error, error_size);
  return ok;
}

int sk_g1_assets(int arm, sk_g1_asset *out, int max) {
  std::vector<g1::AssetEntry> list = g1::Assets(*A(arm));
  for (int i = 0; i < max && i < int(list.size()); i++) {
    std::memcpy(out[i].id, list[size_t(i)].id.b, 16);
    out[i].offset = list[size_t(i)].offset;
    out[i].size = list[size_t(i)].size;
    out[i].crc = list[size_t(i)].crc;
    out[i].type = list[size_t(i)].type;
  }
  return int(list.size());
}

int sk_g1_load(int arm, const sk_g1_program *program) { return program && A(arm)->Load(program->p); }

int sk_g1_vm_start(int arm, uint32_t table) { return A(arm)->Start(table); }

int sk_g1_vm_stop(int arm) { return A(arm)->Stop(); }

int sk_g1_get_vm_status(int arm, sk_g1_vm_status *out) {
  g1::VmStatus s{};
  if (!A(arm)->Status(&s)) return 0;
  if (out)
    *out = sk_g1_vm_status{s.state,   s.trigger,   s.pending,     s.table,          s.next,
                           s.task,    s.jumps_left, s.depth,      s.halt_code,      s.timer,
                           s.tasks,   s.coalesced, s.notify_dropped, s.cap_pauses, s.busy_ms,
                           s.timer_armed};
  return 1;
}

int sk_g1_props(int arm, const uint8_t *ids, int n, uint32_t *values) {
  if (n <= 0) return 0;
  return A(arm)->Props({ids, size_t(n)}, {values, size_t(n)});
}

int sk_g1_report_inputs(int arm, uint16_t triggers, uint16_t *in_force) {
  return A(arm)->ReportInputs(triggers, in_force);
}

int sk_g1_read_arena(int arm, uint32_t address, uint8_t *bytes, size_t n) {
  return A(arm)->ReadArena(address, {bytes, n});
}

void sk_g1_disconnect(int arm, int ms) { A(arm)->Disconnect(ms); }

int sk_g1_next_event(int arm, sk_g1_event *out, int timeout_ms) {
  g1::Event e{};
  if (!A(arm)->NextEvent(&e, timeout_ms)) return 0;
  if (out)
    *out = sk_g1_event{e.type,  e.epoch,   e.device_us, e.host_us, e.trigger, e.count,  e.a,
                       e.b,     e.address, e.op,        e.code,    e.detail,  e.offset};
  return 1;
}

int64_t sk_g1_alloc(uint32_t bytes) { return g1::Alloc(bytes); }

void sk_g1_free(uint32_t address) { g1::Free(address); }

uint32_t sk_g1_bitmap_address(const sk_bitmap *bitmap, int *stride) {
  if (!bitmap) {
    if (stride) *stride = 0;
    return 0;
  }
  return g1::AddressOf(*B(bitmap), stride);
}

uint32_t sk_g1_font_address(const sk_font *font) { return font ? g1::AddressOf(*F(font)) : 0; }

size_t sk_g1_glyph_indices(const sk_font *font, const char *text, uint32_t *indices, size_t max) {
  if (!font) return 0;
  std::vector<uint32_t> v = g1::GlyphIndices(*F(font), text ? text : "");
  if (indices) std::copy_n(v.begin(), std::min(max, v.size()), indices);
  return v.size();
}

uint32_t sk_g1_device_time(int arm, int64_t in_us) { return A(arm)->DeviceTime(in_us); }

const uint32_t *sk_g1_arena(int arm) { return A(arm)->arena.data(); }

void sk_g1_get_state(int arm, sk_g1_state *out) {
  if (!out) return;
  g1::Arm::State s = A(arm)->GetState();
  *out = sk_g1_state{};
  out->present = s.present;
  out->connected = s.connected;
  out->firmware_major = s.firmware_major;
  out->firmware_minor = s.firmware_minor;
  out->firmware_patch = s.firmware_patch;
  out->epoch = s.epoch;
  out->max_packet = s.max_packet;
  out->interval_us = s.interval_us;
  out->clock_error_us = s.clock_error_us;
  out->idle = s.idle;
  out->queued = s.queued;
  out->packets = s.packets;
  out->resent = s.resent;
  out->reconnects = s.reconnects;
  out->epoch_changes = s.epoch_changes;
  out->errors = s.errors;
  out->shown_update = s.shown_update;
  out->shown_us = s.shown_us;
  out->panel_on = s.panel_on;
  out->safe_mode = s.safe_mode;
  out->encrypted = s.encrypted;
  out->in_case = s.in_case;
  out->microphone = s.microphone;
  out->triggers = s.triggers;
  out->events_lost = s.events_lost;
  CopyText(s.last_error, out->last_error, sizeof(out->last_error));
  CopyText(s.address, out->address, sizeof(out->address));
}

int sk_g1_set_brightness(int arm, int level_0_42) { return A(arm)->SetBrightness(level_0_42); }

int sk_g1_battery(int arm, int *millivolts, int *percent, int *charging) {
  bool c = false;
  bool ok = A(arm)->Battery(millivolts, percent, &c);
  if (charging) *charging = c;
  return ok;
}

int sk_g1_panel_crc(int arm, int y0, int rows, uint32_t *arm_crc, uint32_t *expected_crc) {
  return A(arm)->PanelCrc(y0, rows, arm_crc, expected_crc);
}

int sk_g1_arena_crc(int arm, uint32_t address, uint32_t length, uint32_t *arm_crc, uint32_t *expected_crc) {
  return A(arm)->ArenaCrc(address, length, arm_crc, expected_crc);
}

int sk_g1_mic_start(int arm, int rate) {
  return A(arm)->MicStart(rate == SK_G1_MIC_16KBPS ? g1::MIC_16KBPS : g1::MIC_32KBPS);
}

int sk_g1_mic_stop(int arm) { return A(arm)->MicStop(); }

int sk_g1_mic_read(int arm, sk_g1_mic_frame *out, int timeout_ms) {
  g1::MicFrame f;
  if (!A(arm)->MicRead(&f, timeout_ms)) return 0;
  if (out) {
    out->counter = f.counter;
    out->concealed = f.concealed;
    out->device_us = f.device_us;
    out->host_us = f.host_us;
    std::copy(f.pcm.begin(), f.pcm.end(), out->pcm);
  }
  return 1;
}

void sk_g1_mic_get_state(int arm, sk_g1_mic_state *out) {
  g1::MicState s = A(arm)->GetMicState();
  if (out)
    *out = sk_g1_mic_state{s.on,    s.stopped, s.frame_bytes, s.frames_per_event, s.events, s.frames,
                           s.gaps,  s.missing, s.bad,         s.dropped};
}

#ifdef SCOUTER_G1_EMULATOR
const uint8_t *sk_g1_emulator_panel(int arm) { return g1::emulator::Panel(arm); }

void sk_g1_emulator_set_loss(double write_loss, double notify_loss) {
  g1::emulator::SetLoss(write_loss, notify_loss);
}

void sk_g1_emulator_disconnect(int arm, int ms) { g1::emulator::Disconnect(arm, ms); }

void sk_g1_emulator_reboot(int arm) { g1::emulator::Reboot(arm); }

void sk_g1_emulator_set_property(int arm, int property, uint32_t value) {
  g1::emulator::SetProperty(arm, g1::Property(property), value);
}

void sk_g1_emulator_set_mic(int arm, double hz, double peak) { g1::emulator::SetMicSignal(arm, hz, peak); }

void sk_g1_emulator_new_host(int keep_sessions) { g1::emulator::NewHost(keep_sessions != 0); }

void sk_g1_emulator_set_asset_store(int arm, int available) { g1::emulator::SetAssetStore(arm, available != 0); }
#endif

} // extern "C"
