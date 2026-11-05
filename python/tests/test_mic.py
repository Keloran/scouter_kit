"""The microphone on the emulated right arm: a sine encoded with liblc3, decoded by the library.
Only levels and counts are checked."""

import itertools
import math
import signal
import subprocess
import sys

import numpy as np
import pytest

import scouterkit as sk


def samples(frames):
    return np.concatenate([np.frombuffer(f.pcm, np.int16) for f in frames]).astype(float)


def test_frames(glasses):
    assert sk.right.state().microphone and not sk.right.mic_state().on
    sk.right.set_emulated_mic(1000, 0.25)
    with sk.right.mic() as mic:
        frames = list(itertools.islice(mic, 60))
        assert mic.state.on and mic.state.frame_bytes == 40
    assert [f.counter for f in frames] == list(range(60))
    assert not any(f.concealed for f in frames)
    pcm = samples(frames[10:])
    rms_db = 20 * math.log10(math.sqrt(np.mean(pcm**2)) / 32768)
    assert abs(rms_db - 20 * math.log10(0.25 / math.sqrt(2))) < 1.5
    crossings = np.count_nonzero(np.diff(np.signbit(pcm)))
    assert abs(crossings / 2 / (len(pcm) / sk.SAMPLE_RATE) - 1000) < 20
    state = sk.right.mic_state()
    assert not state.on and state.stopped == sk.MicStopReason.HOST and state.gaps == 0
    assert mic.read(0.2) is None


def test_16_kbps(glasses):
    with sk.right.mic(sk.MicRate.KBPS_16) as mic:
        assert len(list(itertools.islice(mic, 10))) == 10
        assert mic.state.frame_bytes == 20


def test_left_arm_refused(glasses):
    with pytest.raises(sk.ScouterKitError, match="microphone"):
        with sk.left.mic():
            pass
    assert not sk.left.mic_state().on


def test_off_after_an_exception_and_restored_signals(glasses):
    before = signal.getsignal(signal.SIGTERM)
    with pytest.raises(KeyError):
        with sk.right.mic():
            assert signal.getsignal(signal.SIGTERM) is not before
            raise KeyError
    assert not sk.right.mic_state().on
    assert signal.getsignal(signal.SIGTERM) is before


def test_link_drop_ends_the_frames(glasses):
    with sk.right.mic() as mic:
        frames = iter(mic)
        next(frames)
        sk.right.disconnect(0.5)
        list(frames)
        assert mic.state.stopped == sk.MicStopReason.LINK


def test_deinit_switches_it_off():
    with sk.init():
        sk.right.mic().start()
        assert sk.right.mic_state().on
    assert not sk.right.mic_state().on


def test_kept_session():
    with sk.init(left="none", keep_sessions=True):
        with sk.right.mic() as mic:
            assert len(list(itertools.islice(mic, 20))) == 20


CHILD = """
import sys, scouterkit as sk
with sk.init(emulator=True):
    try:
        with sk.right.mic() as mic:
            print("on", flush=True)
            for frame in mic:
                pass
    finally:
        print("off" if not sk.right.mic_state().on else "still on", flush=True)
"""


def test_sigterm_switches_it_off():
    child = subprocess.Popen([sys.executable, "-c", CHILD], stdout=subprocess.PIPE, text=True)
    assert child.stdout.readline().strip() == "on"
    child.send_signal(signal.SIGTERM)
    out, _ = child.communicate(timeout=30)
    assert out.strip() == "off" and child.returncode == 128 + signal.SIGTERM
