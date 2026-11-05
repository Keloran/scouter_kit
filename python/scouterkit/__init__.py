"""Draw on Even Realities G1 glasses that run the scouter firmware.

A ctypes wrapper of the ScouterKit C++ library (../src) through its C layer. Both eyes form
one gray virtual display; bitmaps live in the glasses' memory: create or load them, draw
text into them, place them on the display with draw(), and show the result on both eyes at
once with flush(). Pixels are 0 (off, see-through) to 255; the G1 shows 16 levels, so
bitmaps and the display hold multiples of 17. Every function is safe to call from several
threads.

    import scouterkit as sk

    with sk.init():
        font = sk.load_font(sk.departure_mono(), scale=2)
        line = sk.create_bitmap(700, 30)
        font.draw_line(line, "Hello, both eyes", 350, 24, sk.Align.CENTER)
        sk.draw(line, 20, 200)
        sk.flush()
"""

from ._arm import (Arm, ArmState, Battery, Event, EventType, VMStatus, emulate_new_host, left,
                   now_us, right)
from ._assets import Asset, AssetType, asset_id, assets, emulate_asset_store, forget, store
from ._c import is_emulator
from ._core import (ARENA_BYTES, PANEL_HEIGHT, PANEL_WIDTH, Bitmap, ImageLike, Session, alloc,
                    clear, create_bitmap, deinit, display, draw, flush, free, free_memory,
                    gamma_response, init, load_bitmap, response, set_response, size, to_gray,
                    total_memory, unload_all)
from ._font import Align, Extent, Font, departure_mono, load_font
from ._mic import (FRAME_SAMPLES, SAMPLE_RATE, MicFrame, Microphone, MicRate, MicState,
                   MicStopReason)
from ._program import ONE_BPP, UNKNOWN, Label, Mode, Program, Prop, R, Trigger, Word
from ._video import VIDEO_STATE, VideoEncoder
from .errors import (BluetoothError, BondingError, DeviceMemoryError, FirmwareError,
                     NoG1FoundError, NoReplyError, NotInitializedError, ProgramError,
                     ScouterKitError)

__version__ = "0.1.0"

__all__ = [
    "ARENA_BYTES", "FRAME_SAMPLES", "ONE_BPP", "PANEL_HEIGHT", "PANEL_WIDTH", "SAMPLE_RATE",
    "UNKNOWN", "Align", "Arm", "ArmState", "Asset", "AssetType", "Battery", "Bitmap", "BluetoothError", "BondingError",
    "DeviceMemoryError", "Event", "EventType", "Extent", "FirmwareError", "Font", "ImageLike",
    "Label", "MicFrame", "MicRate", "MicState", "MicStopReason", "Microphone", "Mode",
    "NoG1FoundError", "NoReplyError", "NotInitializedError", "Program", "ProgramError", "Prop",
    "R", "ScouterKitError", "Session", "Trigger", "VIDEO_STATE", "VMStatus", "VideoEncoder", "Word",
    "alloc", "asset_id", "assets", "clear",
    "create_bitmap", "deinit", "departure_mono", "display", "draw", "emulate_asset_store",
    "emulate_new_host", "flush", "forget", "free", "free_memory", "gamma_response", "init", "is_emulator", "left", "load_bitmap",
    "load_font", "now_us", "response", "right", "set_response", "size", "store", "to_gray",
    "total_memory", "unload_all",
]
