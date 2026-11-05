"""The asset store (VM.md §6.10): blobs each arm keeps in its flash, named by the first 16
bytes of their SHA-256, so a later session loads them with ASSET_LOAD instead of sending
them again. load_font(..., stored=True) and load_bitmap(..., stored=True) use it too."""

from __future__ import annotations

import ctypes
from dataclasses import dataclass
from enum import IntEnum

from . import _c
from ._arm import Arm
from .errors import ScouterKitError


class AssetType(IntEnum):
    FONT = 1      # a font blob (VM.md §4.2)
    BITMAP = 2    # "SKB1", u16 width, height, stride, 0, then 4-bpp rows
    BITMAP1 = 3   # the same header, then 1-bpp rows
    RAW = 4


@dataclass(frozen=True)
class Asset:
    id: bytes
    offset: int   # in the arm's store
    size: int
    crc: int      # crc32 (zlib's) of the body
    type: int


def asset_id(blob: bytes) -> bytes:
    """The 16-byte ID of a blob."""
    out = (ctypes.c_uint8 * 16)()
    data = bytes(blob)
    _c.lib().sk_g1_asset_id(data, len(data), out)
    return bytes(out)


def store(blob: bytes, type: int = AssetType.RAW) -> bytes:
    """Puts the blob (1 ... 262144 bytes) on each arm that lacks it and returns its ID once
    both hold it (about 1 s per 64 KB on an arm that needs it)."""
    data = bytes(blob)
    if not data:
        raise ValueError("empty asset")
    error = ctypes.create_string_buffer(256)
    with _c.lock.shared():
        _c.require_init()
        if not _c.lib().sk_g1_store(data, len(data), int(type), error, len(error)):
            raise ScouterKitError(error.value.decode(errors="replace"))
    return asset_id(data)


def assets(arm: Arm) -> list[Asset]:
    """The arm's store as it lists it now (empty without a store)."""
    entries = (_c.Asset * 120)()
    with _c.lock.shared():
        _c.require_init()
        n = _c.lib().sk_g1_assets(arm.index, entries, len(entries))
    return [Asset(bytes(e.id), e.offset, e.size, e.crc, e.type) for e in entries[:min(n, 120)]]


def forget(id: bytes) -> None:
    """Deletes the asset on both arms."""
    if len(id) != 16:
        raise ValueError("an asset ID has 16 bytes")
    error = ctypes.create_string_buffer(256)
    with _c.lock.shared():
        _c.require_init()
        if not _c.lib().sk_g1_forget(bytes(id), error, len(error)):
            raise ScouterKitError(error.value.decode(errors="replace"))


def emulate_asset_store(arm: Arm, available: bool) -> None:
    """Emulator builds: the arm answers like a firmware without the store (False), or with it."""
    _c.lib().sk_g1_emulator_set_asset_store(arm.index, int(available))
