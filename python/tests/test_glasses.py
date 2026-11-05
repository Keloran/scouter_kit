"""On the glasses, with SCOUTERKIT_GLASSES=1: the image example, both panels read back. It
resets the arms' sessions, so a program on them (the g1 notification program) stops."""

import os
import subprocess
import sys

import pytest
from PIL import Image, ImageDraw

from conftest import EXAMPLES

pytestmark = pytest.mark.skipif(os.environ.get("SCOUTERKIT_GLASSES") != "1",
                                reason="needs the glasses: SCOUTERKIT_GLASSES=1")


def test_image_example(tmp_path):
    picture = Image.new("L", (300, 160))
    draw = ImageDraw.Draw(picture)
    for level in range(16):
        draw.rectangle((level * 18, 0, level * 18 + 17, 110), fill=level * 17)
    draw.text((10, 130), "ScouterKit for Python", fill=255)
    picture.save(tmp_path / "in.png")
    r = subprocess.run([sys.executable, str(EXAMPLES / "image.py"), str(tmp_path / "in.png")],
                       capture_output=True, text=True, timeout=300)
    print(r.stdout)
    assert r.returncode == 0, r.stdout + r.stderr
    assert r.stdout.count("as expected") == 2
