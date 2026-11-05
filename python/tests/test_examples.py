"""The examples on the emulated arms, each in its own process."""

import subprocess
import sys

from PIL import Image, ImageDraw

from conftest import EXAMPLES


def run(name, *args, timeout=120):
    r = subprocess.run([sys.executable, str(EXAMPLES / name), "--emulator", *map(str, args)],
                       capture_output=True, text=True, timeout=timeout)
    assert r.returncode == 0, r.stdout + r.stderr
    return r.stdout


def test_image(tmp_path):
    picture = Image.new("RGBA", (1000, 600), (0, 0, 0, 0))
    ImageDraw.Draw(picture).ellipse((100, 50, 900, 550), fill=(255, 160, 40, 255))
    picture.save(tmp_path / "in.png")
    out = run("image.py", "--gamma", 2.2, "--save", tmp_path / "out.png", tmp_path / "in.png")
    assert out.count("as expected") == 2
    shown = Image.open(tmp_path / "out.png")
    assert shown.size == (740, 471) and shown.getbbox() is not None


def test_text(tmp_path):
    run("text.py", "--save", tmp_path / "out.png")
    assert Image.open(tmp_path / "out.png").getbbox() is not None


def test_vmclock():
    out = run("vmclock.py", "--away", 1, "--stop")
    assert out.count("corrected, redrawn") == 2 and "programs stopped" in out


def test_notify():
    out = run("notify.py", "--check", "--timeout", 1, "--app", "pytest", "Café € — done",
              "A body long enough to wrap over the second line and to be cut with dots at the end")
    assert "read back as drawn here" in out and "hidden by itself" in out


def test_status():
    out = run("status.py")
    assert out.count("program stopped") == 2
