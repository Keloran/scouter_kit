import hashlib

from PIL import Image

import scouterkit as sk


def test_store_list_forget(glasses):
    blob = bytes(range(256)) * 20
    id = sk.store(blob, sk.AssetType.RAW)
    assert id == hashlib.sha256(blob).digest()[:16] == sk.asset_id(blob)
    for arm in (sk.left, sk.right):
        listed = sk.assets(arm)
        assert [(a.id, a.size, a.type) for a in listed] == [(id, len(blob), sk.AssetType.RAW)]
    assert sk.store(blob) == id
    font = sk.load_font(sk.departure_mono(), "stored", stored=True)
    bitmap = sk.load_bitmap(Image.frombytes("L", (32, 8), bytes(range(256))), stored=True)
    sk.draw(bitmap, 10, 10)
    sk.flush()
    font.unload()
    sk.forget(id)
    assert all(a.id != id for a in sk.assets(sk.right))


def test_without_store(glasses):
    sk.emulate_asset_store(sk.left, False)
    try:
        try:
            sk.store(b"x" * 100)
            assert False, "stored on an arm without the store"
        except sk.ScouterKitError as e:
            assert "not stored" in str(e)
    finally:
        sk.emulate_asset_store(sk.left, True)
