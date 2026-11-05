"""The ctypes structures against the C compiler's layout of scouter_kit_c.h."""

import ctypes
import shutil
import subprocess
from pathlib import Path

import pytest

from scouterkit import _c

HEADERS = Path(__file__).resolve().parents[2] / "src" / "c"
STRUCTS = {
    "sk_view": _c.View,
    "sk_g1_config": _c.Config,
    "sk_g1_vm_status": _c.VmStatus,
    "sk_g1_event": _c.Event,
    "sk_g1_state": _c.State,
    "sk_g1_mic_frame": _c.MicFrame,
    "sk_g1_mic_state": _c.MicState,
}


def test_struct_layout(tmp_path):
    cc = shutil.which("gcc") or pytest.skip("no gcc")
    lines = ["#include <stdio.h>", "#include <stddef.h>", '#include "scouter_kit_c.h"', "int main(void) {"]
    for name, struct in STRUCTS.items():
        lines.append(f'printf("{name} %zu\\n", sizeof({name}));')
        for field, *_ in struct._fields_:
            lines.append(f'printf("{name}.{field} %zu\\n", offsetof({name}, {field}));')
    lines.append("return 0; }")
    (tmp_path / "layout.c").write_text("\n".join(lines))
    subprocess.run([cc, "-std=c11", f"-I{HEADERS}", "layout.c", "-o", "layout"], cwd=tmp_path, check=True)
    out = subprocess.run([tmp_path / "layout"], check=True, capture_output=True, text=True).stdout
    want = dict(line.split() for line in out.splitlines())
    for name, struct in STRUCTS.items():
        assert int(want[name]) == ctypes.sizeof(struct), name
        for field, *_ in struct._fields_:
            assert int(want[f"{name}.{field}"]) == getattr(struct, field).offset, f"{name}.{field}"


def test_every_function_is_in_the_header():
    header = (HEADERS / "scouter_kit_c.h").read_text()
    for name in {**_c.FUNCTIONS, **_c.EMULATOR_FUNCTIONS}:
        assert name.startswith("sk_py_") or f"{name}(" in header, name
