#pragma once

#include <array>
#include <atomic>
#include <compare>
#include <cstdint>
#include <initializer_list>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "../base/scouter_kit.hpp"

// Even Realities G1 running the scouter firmware (protocol 5,
// g1_firmware/docs/VM.md). Linux + BlueZ (sd-bus); built with
// -DSCOUTER_G1_EMULATOR it talks to two emulated arms instead. Other
// platforms (Android) pass their own Transport in Connection and build with
// -DSCOUTER_G1_NO_BLUEZ.
namespace scouter_kit::g1 {

using Address = uint32_t;   // arena byte address, 0 ... 262143
using ArenaWord = uint16_t; // an operand: word i = bytes 4i ... 4i + 3
using U24 = uint32_t;
using U16 = uint16_t;
using U8 = uint8_t;

// Registers r0 ... r14 are the arena words 0 ... 14.
constexpr ArenaWord R(int n) { return ArenaWord(n); }

enum Mode : uint8_t {
  MODE_COPY,
  MODE_MAX,
  MODE_MASK,
  MODE_ERASE,
};

enum Trigger : uint8_t {
  TRIGGER_START,
  TRIGGER_TIMER,
  TRIGGER_MOTION,
  TRIGGER_TILT,
  TRIGGER_LIGHT,
  TRIGGER_BATTERY,
  TRIGGER_CASE,
  TRIGGER_WEAR,
  TRIGGER_TOUCH,
  TRIGGER_PEER,
  TRIGGER_USER0 = 12,
  TRIGGER_USER1,
  TRIGGER_USER2,
  TRIGGER_USER3,
};

enum Property : uint8_t {
  PROP_TIME_US = 0x00,
  PROP_TRIGGER = 0x01,
  PROP_PENDING = 0x02,
  PROP_ARM = 0x03,
  PROP_LINK = 0x04,
  PROP_LINK_INTERVAL = 0x05,
  PROP_BATT_MV = 0x10,
  PROP_BATT_PCT = 0x11,
  PROP_CHARGE = 0x12,
  PROP_IN_CASE = 0x13,
  PROP_WORN = 0x14,
  PROP_BATT_MA = 0x15,
  PROP_BATT_TEMP = 0x16,
  PROP_ACC_X = 0x18,
  PROP_ACC_Y = 0x19,
  PROP_ACC_Z = 0x1A,
  PROP_PITCH = 0x1B,
  PROP_LIGHT = 0x1C,
  PROP_ROLL = 0x1D,
  PROP_BRIGHTNESS = 0x20,
  PROP_PANEL = 0x21,
  PROP_TIMER = 0x22,
  PROP_MOTION_MG = 0x23,
  PROP_PITCH_LO = 0x24,
  PROP_PITCH_HI = 0x25,
  PROP_LIGHT_LO = 0x26,
  PROP_LIGHT_HI = 0x27,
  PROP_IMU_RATE = 0x28,
  PROP_BRIGHTNESS_MAX = 0x29,
  PROP_BRIGHTNESS_CAP = 0x2A,
  PROP_GYR_X = 0x30,
  PROP_GYR_Y = 0x31,
  PROP_GYR_Z = 0x32,
  PROP_GYRO_RATE = 0x33,
  PROP_TOUCH_GESTURE = 0x34,
  PROP_CASE_LID = 0x35,
  PROP_CASE_CHARGING = 0x36,
  PROP_CASE_BATT_PCT = 0x37,
  PROP_AUTO_BRIGHTNESS = 0x38,
  PROP_PEER_LINK = 0x39,
  PROP_PEER_SEND = 0x3A,
  PROP_PEER_MSG = 0x3B,
  PROP_PEER_T_US = 0x3C,
};

// The value of a property the arm does not have.
constexpr uint32_t UNKNOWN = 0x80000000;

// Byte code of protocol 5 (VM.md §2): a program for the arena, or host code
// for Arm::Run. Operands are arena words; constants live in the program's
// pool (Const). Jumps go to labels. The layout is fixed when it is built:
// the 16-word trigger table, the pool, the code.
class Program {
public:
  Program(Address table, Address pool, int pool_words, Address code);
  // The table at `address`, the pool after it, then the code.
  explicit Program(Address address, int pool_words = 64);

  struct Label {
    int id;
    explicit constexpr Label(int i = -1) : id(i) {}
  };
  Label NewLabel();
  void Bind(Label);           // the label is the next instruction
  Label At(Address);          // a label at a fixed address
  void On(Trigger, Label);    // the handler of a trigger
  ArenaWord Const(uint32_t);  // a pool word that holds the value
  // Arena bytes the program changes; the library leaves them out of its
  // mirror checks (ArenaCrc) once the program is loaded.
  void Changes(Address, uint32_t length);
  Address Here() const;       // address of the next instruction

  void Write(Address, std::span<const uint8_t> bytes);
  void WriteRLE(Address, uint16_t length, std::span<const uint8_t> packbits);
  void Copy(ArenaWord dst, ArenaWord src, ArenaWord n);
  void Unpack(ArenaWord dst, ArenaWord src, ArenaWord n, ArenaWord length);
  void Mov(ArenaWord d, ArenaWord a);
  void Add(ArenaWord d, ArenaWord a, ArenaWord b);
  void Sub(ArenaWord d, ArenaWord a, ArenaWord b);
  void Mul(ArenaWord d, ArenaWord a, ArenaWord b);
  void DivU(ArenaWord d, ArenaWord a, ArenaWord b);
  void ModU(ArenaWord d, ArenaWord a, ArenaWord b);
  void DivS(ArenaWord d, ArenaWord a, ArenaWord b);
  void ModS(ArenaWord d, ArenaWord a, ArenaWord b);
  void And(ArenaWord d, ArenaWord a, ArenaWord b);
  void Or(ArenaWord d, ArenaWord a, ArenaWord b);
  void Xor(ArenaWord d, ArenaWord a, ArenaWord b);
  void Shl(ArenaWord d, ArenaWord a, ArenaWord b);
  void Shr(ArenaWord d, ArenaWord a, ArenaWord b);
  void Sar(ArenaWord d, ArenaWord a, ArenaWord b);
  // Element i of width 32, 16, 8 or 4 bits from the base address (VM.md §2.4).
  void Load(int bits, ArenaWord d, ArenaWord base, ArenaWord i);
  void Store(int bits, ArenaWord base, ArenaWord i, ArenaWord a);
  void Jmp(Label);
  void Call(Label);
  void Ret();
  void End();
  void Halt();
  void Beq(ArenaWord a, ArenaWord b, Label);
  void Bne(ArenaWord a, ArenaWord b, Label);
  void BltU(ArenaWord a, ArenaWord b, Label);
  void BgeU(ArenaWord a, ArenaWord b, Label);
  void BltS(ArenaWord a, ArenaWord b, Label);
  void BgeS(ArenaWord a, ArenaWord b, Label);
  void SleepUntil(ArenaWord t);
  void WaitEvent(ArenaWord d, ArenaWord mask);
  void Raise(ArenaWord trigger);
  void Notify(ArenaWord a, ArenaWord b);
  void SysRead(ArenaWord d, ArenaWord property);
  void SysWrite(ArenaWord property, ArenaWord a);
  void Fill(ArenaWord dst, ArenaWord stride, ArenaWord x, ArenaWord y, ArenaWord w,
            ArenaWord h, ArenaWord level);
  // mode: Mode, plus 4 for a 1-bpp source.
  void Blit(ArenaWord src, ArenaWord src_stride, ArenaWord src_x, ArenaWord src_y,
            ArenaWord dst, ArenaWord dst_stride, ArenaWord dst_x, ArenaWord dst_y,
            ArenaWord w, ArenaWord h, ArenaWord mode, ArenaWord level);
  void Glyphs(ArenaWord font, ArenaWord dst, ArenaWord stride, ArenaWord x, ArenaWord y,
              ArenaWord mode, ArenaWord level, ArenaWord text, ArenaWord n);
  void Draw(ArenaWord src, ArenaWord src_stride, ArenaWord src_x, ArenaWord src_y,
            ArenaWord dst_x, ArenaWord dst_y, ArenaWord w, ArenaWord h);
  void Line(ArenaWord dst, ArenaWord stride, ArenaWord x0, ArenaWord y0, ArenaWord x1,
            ArenaWord y1, ArenaWord level);
  void Video(ArenaWord bitmap, ArenaWord stride, ArenaWord w, ArenaWord h, ArenaWord x, ArenaWord y,
             ArenaWord level, ArenaWord src, ArenaWord n, ArenaWord state);
  // Bytes off ... off + n - 1 of the stored asset whose 16-byte ID is at
  // address id, to dst (VM.md §2.4).
  void AssetLoad(ArenaWord id, ArenaWord dst, ArenaWord off, ArenaWord n);
  // The display policy of VM.md §6.6, in this program: the panel off while
  // the arm lies in the case (IN_CASE 1) or the glasses are folded (WORN 0,
  // which the firmware debounces); meanwhile this program's DRAW and VIDEO
  // run with their destination off the panel (VIDEO still decodes), so
  // nothing shows. It takes the CASE and WEAR triggers and runs before the
  // START handler. When both have ended, the next DRAW shows, and it jumps
  // to `redraw` if one is given (code that draws the image and ends). Call
  // it before the first DRAW or VIDEO. It uses two pool words and up to five
  // constants; PanelHidden() is 1 while it holds the panel off.
  void PanelPolicy(Label redraw = Label());
  ArenaWord PanelHidden() const { return hidden_; }
  // Any instruction of VM.md §2.2 but WRITE and WRITE_RLE; `target` for JMP,
  // CALL and the branches.
  void Op(uint8_t opcode, std::initializer_list<ArenaWord> operands, Label target = Label());

  // False (and why) if a label is used but not bound, the pool is full, an
  // operand count is wrong or the code ends past the arena.
  bool Valid(std::string *error = nullptr) const;

  Address table_address, pool_address, code_address;
  int pool_words;
  std::vector<uint8_t> code;          // the instructions, at code_address
  std::vector<uint32_t> pool;         // the constants, at pool_address
  std::array<Address, 16> table{};    // handler address per trigger, 0 = none
  std::vector<std::pair<Address, uint32_t>> changes;

private:
  struct Fixup {
    size_t at;
    int label;
  };
  std::vector<int64_t> labels_;
  std::vector<Fixup> fixups_;
  std::array<int, 16> handlers_;
  std::string error_;
  std::vector<size_t> vars_;         // pool words the program writes (PanelPolicy)
  ArenaWord hidden_ = 0, scratch_ = 0;
  int policy_start_ = -1;            // fixup of PanelPolicy's jump to the START handler
  void Fail(const std::string &);
  void Target(Label);
  ArenaWord Var();
  ArenaWord PolicyX(ArenaWord x);
  void PolicyStart(Label);
};

// Bytes of a VIDEO decoder's state in the arena (VM.md §3.7).
constexpr uint32_t kVideoState = 1596;

// Encodes frames as VIDEO records (VM.md §3.7) against what the arm's 1-bpp
// bitmap holds: every record goes to the arm, in order, starting from a
// zeroed bitmap; the first one resets the decoder's state.
class VideoEncoder {
 public:
  VideoEncoder(int width, int height); // 1...640 x 1...480, else Encode fails
  // pixels: width x height bytes, row by row, nonzero = ink (pixels past the
  // end of a shorter span are 0). redraw: the arm draws every row, not only
  // the changed ones (the panel lost its image). Empty, and the encoder
  // unchanged, for a size out of range or a record over max_bytes.
  std::vector<uint8_t> Encode(std::span<const uint8_t> pixels, bool redraw = false,
                              size_t max_bytes = SIZE_MAX);
  int Width() const { return w_; }
  int Height() const { return h_; }
  int Stride() const { return nb_; } // the least bytes per row of the bitmap

 private:
  int w_, h_, nb_;
  bool started_ = false;
  std::vector<uint8_t> prev_;
  std::vector<uint16_t> p_;
};

// Arena memory, the same on both arms and shared with the fonts and bitmaps
// of scouter_kit.hpp: an address (a multiple of 4), or -1 when full.
int64_t Alloc(uint32_t bytes);
void Free(Address);
// Where the library put a bitmap (4 bpp, its stride in bytes) or a font blob.
Address AddressOf(const gray::Bitmap &, int *stride = nullptr);
Address AddressOf(const Font &);
// The font's glyph indices for the text (U+FFFD or 0 for missing characters).
std::vector<uint32_t> GlyphIndices(const Font &, std::string_view text);

struct Arm;

// The asset store (VM.md §6.10): blobs that each arm keeps in its flash, so a
// later session loads them with ASSET_LOAD (a flash read) instead of sending
// them again. LoadFont and LoadBitmap use it with Keep::kStored. An asset is
// named by the first 16 bytes of its SHA-256.
struct AssetId {
  uint8_t b[16];
  auto operator<=>(const AssetId &) const = default;
};
enum AssetType : uint8_t {
  ASSET_FONT = 1,    // a font blob (VM.md §4.2)
  ASSET_BITMAP = 2,  // "SKB1", u16 width, height, stride, 0, then 4-bpp rows
  ASSET_BITMAP1 = 3, // the same header, then 1-bpp rows
  ASSET_RAW = 4,
};
struct AssetEntry {
  AssetId id;
  uint32_t offset; // in the arm's store
  uint32_t size;   // 1 ... 262144 bytes
  uint32_t crc;    // crc32 (zlib's) of the body
  uint8_t type;    // kept by the arm, not used by it
};
AssetId AssetIdOf(std::span<const uint8_t> blob);
// Puts the blob on each arm that lacks it: staged in free arena memory, sent
// as WRITEs, stored by the arm (about 1 s per 64 KB), the memory freed again.
// Blocks; call it from the app's thread. False and why when an arm has no
// store (a firmware before 0.5.56), no reply, or no room.
bool Store(std::span<const uint8_t> blob, uint8_t type, std::string *error = nullptr);
// The arm's assets as it lists them now (empty without a store or a reply).
std::vector<AssetEntry> Assets(const Arm &);
// Deletes the asset on both arms; blocks.
bool Forget(AssetId, std::string *error = nullptr);

enum EventType : uint8_t {
  EVENT_ERROR = 0x02,  // the arm refused host code
  EVENT_HALT = 0x03,   // a program stopped: HALT or an error
  EVENT_NOTIFY = 0x04, // NOTIFY by a program or host code
  EVENT_INPUT = 0x07,  // a trigger reported by ReportInputs
  EVENT_RESET = 0x80,  // the arm lost its session (rebooted): its program
                       // stopped; the library restored its memory and panel
};

struct Event {
  EventType type;
  uint32_t epoch;
  uint32_t device_us; // the arm's clock when it made the event
  int64_t host_us;    // arrival, on the NowUs() clock
  int trigger;        // HALT: the task's; NOTIFY: its source (0xFF host
                      // code); INPUT: which
  uint32_t count;     // NOTIFY, INPUT: the arm's counter; ERROR: packet seq
  uint32_t a, b;      // NOTIFY: its values; INPUT: a = the value
  Address address;    // HALT, ERROR: the failing instruction
  int op, code;       // HALT, ERROR: opcode and error code (HALT: 0)
  uint32_t detail;
  int offset;         // ERROR: of the instruction in its packet
};

struct VmStatus {
  int state;       // 0 stopped, 1 running, 2 halted
  int trigger;     // of the current task, 0xFF none
  uint16_t pending;
  Address table;
  Address next;    // the task's next instruction (halted: the failing one)
  int task;        // 0 none, 1 running, 2 SLEEP_UNTIL, 3 WAIT_EVENT
  int jumps_left, depth, halt_code;
  uint32_t timer, tasks;
  int coalesced, notify_dropped, cap_pauses, busy_ms;
  bool timer_armed;
};

enum MicRate : uint8_t {
  MIC_32KBPS, // 40-byte LC3 frames
  MIC_16KBPS, // 20-byte LC3 frames
};

constexpr int kMicSampleRate = 16000;
constexpr int kMicFrameSamples = 160; // 10 ms

// 10 ms of the microphone, decoded: 16 kHz mono.
struct MicFrame {
  uint32_t counter;   // frames since MicStart
  bool concealed;     // lost on the link or undecodable: the decoder's PLC
  uint32_t device_us; // the arm's clock when it sent the frame
  int64_t host_us;    // arrival, on the NowUs() clock
  std::array<int16_t, kMicFrameSamples> pcm;
};

enum MicStopReason : uint8_t {
  MIC_STOP_HOST,      // MicStop, or never started
  MIC_STOP_PANEL_OFF, // a panel fault (NOTICE 5 reason 1; a PANEL = 0 no longer stops it, VM.md §6.5)
  MIC_STOP_FAULT,     // a capture fault on the arm
  MIC_STOP_LINK,      // the link dropped
  MIC_STOP_LEASE,     // the arm's lease ran out: no renewal for 2 s (VM.md §6.5)
};

struct MicState {
  bool on;
  MicStopReason stopped;             // why it last stopped
  int frame_bytes, frames_per_event; // the arm's reply to MIC
  uint64_t events, frames;           // received since MicStart
  uint64_t gaps, missing;            // counter gaps and the frames they missed
  uint64_t bad;                      // frames the decoder could not decode
  uint64_t dropped;                  // frames MicRead did not take in time
};

// Panel is 4bit so 1bit is only used for packed transfers & data storage.
// All drawing functions that use the 1bit representation use the suffix
// "Compressed" and expand the 1bit representation to 4bits.
//
// Each call queues host code (VM.md §2.2, constants from the library's pool)
// and applies it to `arena`. Code that would fail on the arm is not sent; it
// counts in State::errors and State::last_error says why.
struct Arm {
  // The library's mirror of the arm's memory, little-endian, read-only.
  // Regions that a program changes hold what the host last wrote there.
  std::array<uint32_t, 256 / 4 * 1024> arena;

  using Address = g1::Address;
  using ArenaWord = g1::ArenaWord;
  using U24 = g1::U24;
  using U16 = g1::U16;
  using U8 = g1::U8;

  void WriteArena(Address, std::span<const uint8_t> bytes);
  void WriteArenaRLE(Address, uint16_t out_length,
                     std::span<const uint8_t> packbits);
  void CopyArena(Address src, Address dst, uint32_t n);
  void Unpack(Address dst, Address src, uint32_t n, uint32_t out_length);

  void Fill(Address dst, uint32_t stride, int32_t x, int32_t y, uint32_t w,
            uint32_t h, U8 level_0_15 = 15);

  void Blit(Address src, uint32_t src_stride, int32_t src_x, int32_t src_y,
            Address dst, uint32_t dst_stride, int32_t dst_x, int32_t dst_y,
            uint32_t w, uint32_t h, Mode, U8 level_0_15 = 15);

  void BlitCompressed(Address src, uint32_t src_stride, int32_t src_x,
                      int32_t src_y, Address dst, uint32_t dst_stride,
                      int32_t dst_x, int32_t dst_y, uint32_t w, uint32_t h,
                      Mode, U8 level_0_15 = 15);

  void GlyphRunCompressed(Address font, Address dst, uint32_t dst_stride,
                          int32_t x, int32_t y, Mode,
                          std::span<const U8> glyphs_varints,
                          U8 level_0_15 = 15);

  void Draw(Address src, uint32_t src_stride, int32_t src_x, int32_t src_y,
            int32_t dst_x, int32_t dst_y, uint32_t w, uint32_t h);
  void Line(Address dst, uint32_t stride, int32_t x0, int32_t y0, int32_t x1,
            int32_t y1, U8 level_0_15 = 15);
  // VIDEO: decodes the n-byte record at src into the 1-bpp bitmap and draws
  // the rows that changed at (x, y), ink as level (VM.md §3.7).
  void Video(Address bitmap, uint32_t stride, uint32_t w, uint32_t h, int32_t x, int32_t y,
             U8 level, Address src, uint32_t n, Address state);

  void WaitUntil(uint32_t device_time_us); // SLEEP_UNTIL, at most 2 s ahead
  void SetPanel(bool on);
  void SysWrite(Property, uint32_t value);
  void Fence(uint32_t token);             // NOTIFY token, 0
  void Notify(uint32_t a, uint32_t b);
  void Raise(Trigger);                    // hands work to the program
  void Call(Address code);                // runs stored code in a packet
  // Host code as is (instructions marked H in VM.md §2.2), in one packet.
  void Run(std::span<const uint8_t> host_code);

  // Programs (VM.md §5): Load queues the WRITEs of the table, pool and code
  // and marks what the program changes. Start waits until the arm ran
  // everything queued before (as PanelCrc does), then starts the program
  // whose trigger table is at `table` (false: no reply, or refused).
  bool Load(const Program &);
  bool Start(Address table);
  bool Stop();
  bool Status(VmStatus *);

  // Properties (VM.md §2.5), up to 12 per call; UNKNOWN for missing values.
  bool Props(std::span<const uint8_t> ids, std::span<uint32_t> values);
  bool Prop(Property, uint32_t *value);
  // The arm reports these triggers (bit i = trigger i) as EVENT_INPUT, also
  // after reconnecting; in_force = triggers & what this firmware can raise.
  bool ReportInputs(uint16_t triggers, uint16_t *in_force = nullptr);
  // Events from the arm, oldest first: false when none came within
  // timeout_ms. The library keeps the last 256.
  bool NextEvent(Event *, int timeout_ms = 0);

  // Arena memory, shared with the fonts and bitmaps (g1::Alloc): an
  // address, or -1 when full.
  int64_t Alloc(uint32_t bytes);
  void Free(Address);

  // The arm's clock (µs, wraps) at host time NowUs() + in_us.
  uint32_t DeviceTime(int64_t in_us = 0);

  // Drops the link; the library connects again after `ms`. The session, the
  // arm's memory and its program stay.
  void Disconnect(int ms);

  struct State {
    bool present;   // configured (not "none")
    bool connected; // link up and session ready
    int firmware_major, firmware_minor, firmware_patch;
    uint32_t epoch;
    int max_packet;
    int interval_us;    // connection interval
    int clock_error_us; // bound of the clock estimate
    bool idle;          // the idle link profile is asked for
    int queued;         // commands not sent yet
    uint32_t packets, resent, reconnects, epoch_changes;
    uint32_t errors; // commands refused here or by the arm
    std::string last_error;
    uint64_t shown_update; // the last Flush (1, 2, ...) this eye showed
    int64_t shown_us;      // when, on the NowUs() clock
    bool panel_on;         // as the library expects it
    bool safe_mode, encrypted, in_case, microphone;
    uint16_t triggers; // that this firmware can raise
    uint32_t events_lost;
    std::string address;
  };
  State GetState();

  // Control requests: they wait for the reply (false: no reply or refused).
  bool SetBrightness(int level_0_42);
  bool Battery(int *millivolts, int *percent, bool *charging);
  // CRC-32 of panel rows / arena bytes as the arm holds them, and of what
  // the library expects there. Both first wait until the arm ran every
  // command queued before, however long that takes while it makes progress
  // (false after 15 s without progress, or after 10 minutes). False also
  // while the panel is off, and for arena bytes that a program changes.
  bool PanelCrc(int y0, int rows, uint32_t *arm_crc, uint32_t *expected_crc);
  bool ArenaCrc(Address, uint32_t length, uint32_t *arm_crc,
                uint32_t *expected_crc); // length 0: to the end
  // The arm's arena bytes (after the commands queued before have run).
  bool ReadArena(Address, std::span<uint8_t> bytes);

  // Microphone (VM.md §6.5; on the G1 the right arm, State::microphone).
  // While it is on, the arm shows its indicator, a 16 x 16 block in the
  // panel's outer top corner (MicStart powers the panel on), and streams LC3
  // frames that the library decodes, concealing missing ones. The arm stops
  // by itself when the link drops or the panel goes off and never starts
  // again by itself. MicStop switches the panel off again if it was off
  // before MicStart and the library has not drawn since. A program that
  // writes PANEL = 0 while the microphone is on stops it (NOTICE 5). MicRead
  // gives the frames oldest first; the last 200 are kept. Needs a build with
  // SCOUTER_G1_MIC (liblc3).
  bool MicStart(MicRate = MIC_32KBPS);
  bool MicStop();
  bool MicRead(MicFrame *, int timeout_ms = 0);
  MicState GetMicState();

  void *pimpl;
};

extern Arm left, right;

// Configures the scouter_kit::Gray
struct GrayConfig {
  int dx = 100; // horizontal separation between eyes (makes the bitmap wider)
  int dy = 9;   // vertical balance (makes the bitmap shorter)
};

// Which glasses: empty addresses use the bonded G1 (or, with the case open,
// one in range). "none" leaves an arm out (a bench board with one arm).
// `adapter` is the BlueZ adapter, by name ("hci1") or address; empty: the
// first powered adapter. Bonds belong to an adapter: another adapter bonds
// with the arms anew.
class Transport;
struct Connection {
  std::string left;
  std::string right;
  int timeout_ms = 30000;
  std::string adapter;
  // Keep the arms' sessions as they are (no RESET_SESSION): a program on
  // them runs on and their memory stays. The library then owns none of the
  // arena (no bitmaps, fonts or Alloc) and draws nothing: for apps that use
  // control requests and events only, such as the microphone.
  bool keep_sessions = false;
  // Log to stderr.
  bool log = false;
  // The application's own link to the arms (Android), alive until Deinit
  // returns. Null: BlueZ, or the emulated arms of an emulator build.
  Transport *transport = nullptr;
};

enum InitError {
  ERROR_NONE,
  // Something bad happened that prevented us from finding the glasses.
  ERROR_BLUETOOTH_ERROR,
  // We couldn't find any glasses.
  ERROR_NO_G1_FOUND,
  // We couldn't bond to the glasses. The should be in the open case to bond for
  // the first time.
  ERROR_G1_BONDING_ERROR,
  // The arms answered but do not run the scouter firmware (protocol 5).
  ERROR_G1_FIRMWARE,
  ERROR_CANCELLED,
};

// The Bluetooth link to both arms (service f3da4d59-…4400). Everything runs
// on the library's Bluetooth thread except Wake(). Write channels: 0 CMD
// (…4401), 1 CTRL (…4402), written without response; EVT (…4403)
// notifications arrive through Events::Data. LinkUp(arm, mtu) comes after
// the MTU exchange and the EVT subscription; Found(arm, address) when an arm
// is connected or seen.
class Transport {
public:
  struct Events {
    virtual ~Events() = default;
    virtual void LinkUp(int arm, int mtu) = 0;
    virtual void LinkDown(int arm) = 0;
    virtual void Data(int arm, const uint8_t *data, size_t n) = 0;
    virtual void Found(int arm, const std::string &address) = 0;
    virtual void Problem(InitError error, const std::string &message) = 0;
  };
  virtual ~Transport() = default;
  virtual bool Start(Events *events, const bool present[2]) = 0;
  virtual bool Write(int arm, int channel, const uint8_t *data, size_t n) = 0;
  // Waits up to `timeout_ms` for link events and delivers them to Events.
  virtual void Poll(int timeout_ms) = 0;
  // Ends the current or next Poll early; any thread.
  virtual void Wake() = 0;
  virtual void Disconnect(int arm) = 0;
  // Drops the link and connects again after `ms`.
  virtual void Hold(int arm, int ms) = 0;
  // The arm was found automatically but is not usable (a board without a
  // panel): true if the transport will try another candidate.
  virtual bool Reject(int arm) {
    (void)arm;
    return false;
  }
  virtual void Stop() = 0;
};

// Connects with the bonded glasses (or any available glasses & bonds with them)
// & returns once ready. Error message helps with the retry.
InitError Init(GrayConfig * = nullptr, std::string *error_message = nullptr,
               const Connection * = nullptr);
// A one-shot signal bound to one Init/session. Copies share the signal; old
// tokens never refer to a later session. Cancel is safe from any thread.
class Cancellation {
public:
  Cancellation();
  void Cancel() const;
  bool Cancelled() const;
private:
  std::shared_ptr<std::atomic<bool>> cancelled_;
};
// Cancellation fails pending calls and stops the transport between callbacks.
// Transport callbacks must return promptly (Poll within its requested bound).
// Call Deinit after cancellation to join cleanup and release native handles.
InitError InitCancelable(const Cancellation &, GrayConfig * = nullptr,
                        std::string *error_message = nullptr, const Connection * = nullptr);
void Deinit();

// Host clock used with device times (steady clock, µs).
int64_t NowUs();

#ifdef SCOUTER_G1_EMULATOR
// The emulated arms of an emulator build. They run the arms' interpreter
// (src/vm), so programs behave as on the glasses.
namespace emulator {
// The arm's panel, 640 x 480 levels 0..15 (0 = left arm, 1 = right arm).
const uint8_t *Panel(int arm);
// Probability that a packet (host to arm) or a notification (arm to host) is
// lost.
void SetLoss(double write_loss, double notify_loss);
// The link drops and comes back after `ms`; the arm keeps its memory.
void Disconnect(int arm, int ms);
// The arm reboots: new epoch, arena cleared, panel off.
void Reboot(int arm);
// A sensor value of the arm (battery, case, ...); BATT_PCT follows it with
// the firmware's hysteresis.
void SetProperty(int arm, Property, uint32_t value);
// What the emulated microphone hears (right arm, SCOUTER_G1_MIC builds): a
// sine of `hz` with its peak at `peak` x full scale (440 Hz, 0.25 at first).
void SetMicSignal(int arm, double hz, double peak);
// The library stops renewing the microphone's lease (a host that stopped
// reading): the arm switches the microphone off within 2 s (MIC_STOP_LEASE).
void HoldMicRenewals(bool hold);
// The arms see a new host: the library forgets their sessions and connects
// again, as a new process with Connection::keep_sessions would.
void NewHost(bool keep_sessions);
// The arm answers like a firmware without the asset store (false) or with it
// again (true); what it stored stays.
void SetAssetStore(int arm, bool available);
} // namespace emulator
#endif

} // namespace scouter_kit::g1
