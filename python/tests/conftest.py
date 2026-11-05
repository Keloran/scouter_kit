import sys
from pathlib import Path

EXAMPLES = Path(__file__).resolve().parents[1] / "examples"
sys.path.insert(0, str(EXAMPLES))

import pytest  # noqa: E402

import scouterkit as sk  # noqa: E402
from scouterkit import _c  # noqa: E402

_c.load(emulator=True)


@pytest.fixture
def glasses():
    with sk.init() as session:
        yield session
