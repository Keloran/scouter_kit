"""The builder against the byte code of VM.md."""

import re
from pathlib import Path

import pytest

import scouterkit as sk
from scouterkit import R

VM_MD = Path(__file__).resolve().parents[2] / "g1_firmware" / "docs" / "VM.md"


def section(text, title, end):
    return text.split(title, 1)[1].split(end, 1)[0]


def spec_clock():
    """VM.md §9.1's pool and §9.2's code, table and size, as the spec writes them."""
    text = VM_MD.read_text()
    layout = section(text, "### 9.1", "### 9.2")
    pool = [int(v) for v in re.search(r"j-th value of ([\d,\s]+)\.", layout).group(1).split(",")]
    clock = section(text, "### 9.2", "### 9.3")
    size, address = re.search(r"Code \((\d+) bytes at 0x([0-9A-Fa-f]+)", clock).groups()
    code = bytearray()
    for line in clock.split("```")[1].splitlines():
        tokens = line.split()
        if tokens and re.fullmatch(r"[0-9a-f]{5}", tokens[0]):
            for t in tokens[1:]:
                if not re.fullmatch(r"[0-9a-f]{2}", t):
                    break
                code.append(int(t, 16))
    table = [0] * 16
    for word, value in re.findall(r"word (\d+)\s+\(`\w+`\) = 0x([0-9A-Fa-f]+)", clock):
        table[int(word)] = int(value, 16)
    assert len(code) == int(size)
    return pool, bytes(code), table, int(address, 16)


def clock_program(pool, code_address):
    """§9.2 with the builder: HH:MM in a 160 x 32 band at panel (240, 8)."""
    p = sk.Program(0x40, 32, pool=0x80, code=code_address)
    for v in pool:
        p.const(v)
    c = p.const
    m, n, m_new, n_new = 68, 69, 66, 67
    sync, tick, show = p.label(), p.label(), p.label()
    p.bind(sync)
    p.mov(m, m_new)
    p.mov(n, n_new)
    p.jmp(show)
    p.bind(tick)
    p.add(m, m, c(1))
    p.modu(m, m, c(1440))
    p.add(n, n, c(60_000_000))
    p.bind(show)
    p.divu(R(4), m, c(60))
    p.modu(R(5), m, c(60))
    for op, value, index in ((p.divu, R(4), 0), (p.modu, R(4), 1), (p.divu, R(5), 3), (p.modu, R(5), 4)):
        op(R(6), value, c(10))
        p.store(8, c(256), c(index), R(6))
    p.fill(c(16384), c(80), c(0), c(0), c(160), c(32), c(0))
    p.glyphs(c(153600), c(16384), c(80), c(4), c(24), c(2), c(15), c(256), c(5))
    p.draw(c(16384), c(80), c(0), c(0), c(240), c(8), c(160), c(32))
    p.sys_write(c(sk.Prop.TIMER), n)
    p.end()
    p.on(sk.Trigger.START, sync)
    p.on(sk.Trigger.TIMER, tick)
    p.on(sk.Trigger.USER0, sync)
    return p


def test_clock_is_byte_equal_to_the_spec():
    pool, code, table, address = spec_clock()
    p = clock_program(pool, address)
    p.validate()
    assert p.code == code
    assert p.table == table
    assert p.pool == pool
    assert p.here == address + len(code)
    raise_user0 = sk.Program(0x40, 32, pool=0x80, code=0xC000)
    for v in pool:
        raise_user0.const(v)
    raise_user0.raise_(raise_user0.const(12))
    assert raise_user0.code == bytes.fromhex("42 f0 28 00")


def test_encoding_and_labels():
    p = sk.Program(0x1000, 64)
    assert (p.table_address, p.pool_address, p.code_address) == (0x1000, 0x1040, 0x1140)
    p.add(R(3), R(1), 0x0120)
    assert p.code == bytes.fromhex("11 31 f0 20 01")
    back, ahead = p.label(), p.label()
    p.bind(back)
    p.bges(R(1), 0x40, ahead)
    p.jmp(back)
    p.call(p.at(0x12345))
    p.bind(ahead)
    p.ret()
    assert p.error is None
    assert p.code[5:] == bytes.fromhex("3a 1f 54 11 00 40 00 30 45 11 00 31 45 23 01 32")


def test_errors():
    unbound = sk.Program(0x2000, 64)
    unbound.jmp(unbound.label())
    assert "not bound" in unbound.error
    with pytest.raises(sk.ProgramError):
        unbound.validate()
    full = sk.Program(0x2000, 1)
    full.const(1)
    full.const(2)
    assert "pool is full" in full.error
    p = sk.Program(0x2000, 4)
    with pytest.raises(ValueError):
        p.load(12, R(0), R(1), R(2))
    with pytest.raises(ValueError):
        p.mov(R(0), 0x10000)
    with pytest.raises(ValueError):
        R(15)


def test_negative_constants_are_int32():
    p = sk.Program(0x2000, 4)
    p.const(-90)
    assert p.pool == [0xFFFFFFA6]
