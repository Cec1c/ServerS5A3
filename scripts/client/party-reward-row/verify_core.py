"""Portable fixed-v2 row-gate fixtures; imports do not read client images.

Native windows, synthetic objects, assertions and case order are preserved
from verify_row_probe.py. Only explicit CLI setup loads the two input images.
"""
from pathlib import Path
import argparse
import contextlib
import hashlib
import json
import os
import struct
import sys
import traceback

sys.dont_write_bytecode = True
if not __debug__:
    raise RuntimeError("Assertions are required; do not use -O or PYTHONOPTIMIZE.")

# An optional package-root override for an already available Unicorn install.
# Otherwise use the selected interpreter's normal environment / PYTHONPATH.
_unicorn_root = os.environ.get("ROW_GATE_UNICORN_PATH")
if _unicorn_root:
    _unicorn_root = Path(_unicorn_root).expanduser().resolve(strict=True)
    if not (_unicorn_root / "unicorn" / "__init__.py").is_file():
        raise RuntimeError("ROW_GATE_UNICORN_PATH must contain the unicorn package.")
    sys.path.insert(0, str(_unicorn_root))
try:
    import unicorn
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
    from unicorn.x86_const import *
except ImportError as exc:
    raise RuntimeError(
        "Unicorn is unavailable. Use an environment with unicorn==2.1.4, "
        "or set ROW_GATE_UNICORN_PATH to its package parent directory."
    ) from exc

ROOT = Path(__file__).resolve().parent
BASE_SHA = "5a13cd951aa246682f21466542d582901fc136bbf5578cc0ab88bb742c3b0dcc"
CANDIDATE_SHA = "04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad"
BASE_SIZE, CANDIDATE_SIZE = 111890432, 111894528
VA = 0x6EC2000
# Copied metadata from the existing v2 receipt, not an on-disk input receipt.
# Full-image SHA checks below bind these helper windows to the exact candidate.
RECEIPT = dict(
    baseSha256=BASE_SHA, candidateSha256=CANDIDATE_SHA,
    helpers=[
        dict(address="0x6ec2000", size=339),
        dict(address="0x6ec2300", size=26),
        dict(address="0x6ec2340", size=26),
        dict(address="0x6ec2380", size=26),
    ],
    limitations=[
        "full GUI, external consumers and resource lifecycle remain unverified",
        "physical row compaction is not implemented; original slot coordinates remain",
        "no-party policy is self-only when cached self is available",
        "unknown/mismatching context preserves original behavior",
        "not yet suitable for release",
    ],
)
BASE = NEW = OLD_PE = NEW_PE = None


def require(condition, message):
    if not condition:
        raise ValueError(message)


def u16(data, at):
    return struct.unpack_from("<H", data, at)[0]


def u32(data, at):
    return struct.unpack_from("<I", data, at)[0]


class PE:
    """Minimal PE32 reader used for validation and VA-to-file mapping."""

    def __init__(self, data):
        self.data = data
        require(data[:2] == b"MZ", "input is not an MZ image")
        self.nt = u32(data, 0x3C)
        require(data[self.nt:self.nt + 4] == b"PE\0\0", "missing PE signature")
        require(u16(data, self.nt + 4) == 0x14C, "input is not PE32")
        self.count = u16(data, self.nt + 6)
        self.opt = self.nt + 24
        require(u16(data, self.opt) == 0x10B,
                "input is not a PE32 optional header")
        self.base = u32(data, self.opt + 28)
        self.sa, self.fa = struct.unpack_from("<II", data, self.opt + 32)
        self.st = self.opt + u16(data, self.nt + 20)
        self.sections = []
        for index in range(self.count):
            at = self.st + index * 40
            virtual_size, rva, raw_size, raw = struct.unpack_from(
                "<IIII", data, at + 8)
            self.sections.append(dict(
                index=index,
                name=data[at:at + 8].rstrip(b"\0").decode("ascii"),
                virtual_size=virtual_size,
                rva=rva,
                raw_size=raw_size,
                raw=raw,
                characteristics=u32(data, at + 36),
            ))

    def offset(self, va, size=1):
        rva = va - self.base
        for section in self.sections:
            if (section["rva"] <= rva and
                    rva + size <= section["rva"] + section["raw_size"]):
                return section["raw"] + rva - section["rva"]
        raise ValueError("VA has no raw backing: 0x{:x} / 0x{:x}".format(va, size))

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at + size]


ROW, ROOT_UI, CONTROL, SCENE, SELF, PARTY, PARENT, LEAF, NODE_PAGE = range(0x100000, 0x109000, 0x1000)
STACK, STOP, EVENT = 0x208000, 0x7000000, 0x7000010
NONVOL = {UC_X86_REG_EBX: 0x11110001, UC_X86_REG_ESI: 0x22220002,
          UC_X86_REG_EDI: 0x33330003, UC_X86_REG_EBP: STACK + 0x200}
WINDOWS = {0x1F1AAE0: 0x6B, 0x1E93430: 0x56, 0x20D09C0: 0x6A,
           0x2973110: 0x24, 0x2964560: 0x120, 0x2964680: 0x120,
           0x3829190: 0x244, 0x3826930: 0xC, 0x8CFE80: 4,
           0x38293E0: 0x16B}


class Machine:
    def __init__(self, variant='member', slot=1, patched=True):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pages = set()
        self.trace = []
        self.variant = variant
        self.slot = slot
        self.patched = patched
        self.owner, self.route = 5, 2
        self.iterator_parent = {}
        for page in range(ROW, NODE_PAGE + 0x1000, 0x1000):
            self.put(page, bytes(4096))
        self.put(0, bytes(4096))
        self.put(STACK - 0x6000, bytes(0xC000))
        self.put(STOP, b'\xcc')
        self.put(EVENT, b'\xc3')
        self.w(0x55B3BDC, 0x12345678)
        self.w(0x55ADAE4, 0xFFFFFFFF)
        self.w(0x55ADAE0, 0xFFFFFFFF)
        self.w(0x56A3758, SCENE)
        for address, size in WINDOWS.items():
            self.put(address, NEW_PE.read(address, size))
        for helper in RECEIPT['helpers']:
            at = int(helper['address'], 16)
            self.put(at, NEW_PE.read(at, helper['size']))
        self.put(0x491A05C, (NEW_PE if patched else OLD_PE).read(0x491A05C, 0xDC))
        # Leaf vtable uses the original input state gate and an event sink.
        self.w(LEAF, NODE_PAGE + 0x800)
        self.w(NODE_PAGE + 0x868, 0x3826930)
        self.w(NODE_PAGE + 0x86C, EVENT)
        self.w(ROW, 0x491A05C)
        self.w(ROW + 0x3E4, 20)
        self.w(ROW + 0x3E0, 3)  # reward slot need not equal party key slot
        self.w(ROW + 0x484, ROOT_UI)
        self.w(ROW + 0x488, CONTROL)
        self.w(CONTROL + 4, 2)
        self.w(CONTROL + 8, 1)
        self.w(ROOT_UI + 0x3BC, 0)
        self.w(SCENE + 0x70, SELF)
        self.w(SELF + 0x20, 19)
        self.w(SELF + 0x4C8, PARTY)
        self.w(PARTY + 0x70, 2)
        self.w(PARTY + 0x74, 0)
        for i in range(8):
            self.w(PARTY + 0x2C + 4 * i, 0xFFFF)
        self.w(PARTY + 0x2C + 4 * slot, 20)
        self.w(PARTY + 0x2C + 4 * ((slot + 1) % 8), 19)
        for obj in (PARENT, ROW, LEAF):
            self.put(obj + 0x60, b'\x01\x01')
            self.w(obj + 0x10C, obj)
        self.w(PARENT + 0x94, 1)
        self.w(ROW + 0x94, 1)
        self.install_iterators(PARENT, ROW, 0)
        self.install_iterators(ROW, LEAF, 1)
        fixtures = [0x18C2800, 0x37E3940, 0x37E3970, 0x37DF550,
            0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0,
            0x3828F50, 0x1F209D0, 0x1331A90, 0x10CE570,
            0x37EA920, 0x769AD0, 0x3A01CF0, 0x3A01CE0,
            0xBD09D0, 0x146FB30, 0x3A8FEA0, 0x1922B70, 0x37EADC0, 0x382A890]
        for at in fixtures:
            self.put(at, b'\xc3')
        self.apply_variant(variant)
        self.u.hook_add(UC_HOOK_CODE, self.hook)

    def put(self, address, data):
        for page in range(address & ~4095, (address + len(data) + 4095) & ~4095, 4096):
            if page not in self.pages:
                self.u.mem_map(page, 4096)
                self.pages.add(page)
        self.u.mem_write(address, data)

    def w(self, address, value):
        self.put(address, struct.pack('<I', value & 0xFFFFFFFF))

    def r(self, address):
        return int.from_bytes(self.u.mem_read(address, 4), 'little')

    def ret(self, value=0, pop=0):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        target = self.r(sp)
        self.u.reg_write(UC_X86_REG_EAX, value)
        self.u.reg_write(UC_X86_REG_ECX, 0xCAFE0001)
        self.u.reg_write(UC_X86_REG_EDX, 0xFACE0002)
        self.u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
        self.u.reg_write(UC_X86_REG_EIP, target)

    def install_iterators(self, obj, child, index):
        at = NODE_PAGE + index * 0x100
        proxy, container, node, end, entry = (at + n for n in (0, 0x10, 0x20, 0x30, 0x40))
        self.w(proxy, container)
        # The container's invalid/decrement sentinel is distinct from the
        # iterator's logical end; native increment checks it before comparing
        # against the logical end at the start of the following iteration.
        self.w(container + 4, at + 0x50)
        self.w(node + 4, end)
        self.w(entry, child)
        self.iterator_parent[obj + 0x8C] = (proxy, node, end, entry)

    def apply_variant(self, v):
        field = PARTY + 0x2C + 4 * self.slot
        if v == 'departed': self.w(field, 0xFFFF)
        elif v == 'no_party': self.w(SELF + 0x4C8, 0)
        elif v == 'self_no_party': self.w(SELF + 0x4C8, 0); self.w(ROW + 0x3E4, 19)
        elif v == 'self_missing': self.w(PARTY + 0x2C + 4 * ((self.slot + 1) % 8), 0xFFFF); self.w(field, 0xFFFF)
        elif v == 'no_scene': self.w(0x56A3758, 0)
        elif v == 'no_self': self.w(SCENE + 0x70, 0)
        elif v == 'expired_parent': self.w(CONTROL + 4, 0)
        elif v == 'no_parent': self.w(ROW + 0x484, 0); self.w(ROW + 0x488, 0)
        elif v == 'raw_namespace': self.w(SELF + 0x604, 0x205)
        elif v == 'party_owner': self.w(PARTY + 0x74, 5)
        elif v == 'bad_owner': self.w(PARTY + 0x74, 6); self.w(field, 0xFFFF)
        elif v == 'wide_owner': self.w(PARTY + 0x74, 0x105); self.w(field, 0xFFFF)
        elif v == 'bad_route': self.w(PARTY + 0x70, 3); self.w(field, 0xFFFF)
        elif v == 'wide_route': self.w(PARTY + 0x70, 0x102); self.w(field, 0xFFFF)
        elif v == 'zero_namespace': self.owner = 0; self.w(field, 0xFFFF)
        elif v == 'zero_route': self.route = 0; self.w(PARTY + 0x70, 0)
        elif v == 'wide_member': self.w(field, 0x10014)
        elif v == 'zero_member': self.w(field, 0)
        elif v == 'empty_member': self.w(field, 0xFFFF)
        elif v == 'zero_key': self.w(ROW + 0x3E4, 0)
        elif v == 'unbound': self.w(ROW + 0x3E4, 0xFFFFFFFF)
        elif v == 'wide_key': self.w(ROW + 0x3E4, 0x10014)
        elif v == 'zero_self': self.w(SELF + 0x20, 0); self.w(field, 0xFFFF)
        elif v == 'invalid_self': self.w(SELF + 0x20, 0xFFFF); self.w(field, 0xFFFF)
        elif v.startswith('category'):
            self.w(ROOT_UI + 0x3BC, int(v[-1])); self.w(field, 0xFFFF)
        elif v == 'arena': self.w(field, 0xFFFF)
        elif v == 'mouse_disabled': self.put(ROW + 0x138, b'\x01')
        elif v == 'invisible': self.put(ROW + 0x60, b'\x00')
        elif v == 'input_disabled': self.put(ROW + 0x61, b'\x00')
        elif v == 'all_eight':
            for i in range(8): self.w(PARTY + 0x2C + 4 * i, 19 + i)
        elif v == 'stale_clicked':
            self.w(field, 0xFFFF); self.w(0x55ADAE0, ROW); self.w(0x55ADAD4, LEAF)

    def hook(self, _u, address, _size, _data):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        this = self.u.reg_read(UC_X86_REG_ECX)
        if address == 0x18C2800: self.ret(int(self.variant == 'arena'))
        elif address in (0x37E3940, 0x37E3970): self.ret()
        elif address in (0x37DF550, 0x37DF5C0, 0x37DF630, 0x37DF6A0, 0x37DF7F0):
            assert this in (0x56A1E80, 0x56A1E74)
            self.w(self.r(sp + 8), self.owner if this == 0x56A1E80 else self.route)
            self.ret(pop=8)
        elif address in (0x3828F50, 0x1F209D0, 0x37EA920):
            assert this == ROW
            self.trace.append(('render_leaf', hex(address)))
            self.ret()
        elif address in (0x1331A90, 0x10CE570, 0x769AD0): self.ret()
        elif address == 0x3A01CF0: self.ret(0)
        elif address == 0x3A01CE0: self.ret(1)
        elif address in (0xBD09D0, 0x146FB30):
            proxy, node, end, entry = self.iterator_parent[this]
            out = self.r(sp + 4)
            self.w(out, proxy); self.w(out + 4, 0)
            self.w(out + 8, node if address == 0xBD09D0 else end)
            self.ret(out, 4)
        elif address == 0x3A8FEA0:
            out = self.r(sp + 4)
            self.put(out, bytes(self.u.mem_read(this, 12)))
            self.ret(out, 4)
        elif address == 0x1922B70:
            proxy = self.r(this)
            matches = [v for v in self.iterator_parent.values() if v[0] == proxy]
            assert len(matches) == 1
            self.trace.append(('iterator_child', hex(self.r(matches[0][3]))))
            self.ret(matches[0][3])
        elif address == 0x37EADC0:
            out = self.r(sp + 4)
            self.w(out, this); self.w(out + 4, 0)
            self.ret(out, 4)
        elif address == EVENT:
            assert this == LEAF
            self.trace.append(('hit_leaf', hex(this)))
            self.ret(77, 8)
        elif address == 0x382A890:
            raise AssertionError('unexpected outer hit fallback')

    def call(self, mode, expected_allow, label=None):
        self.trace.clear()
        tracked = [(at, bytes(self.u.mem_read(at, 4096))) for at in range(ROW, NODE_PAGE + 0x1000, 4096)]
        for register, value in NONVOL.items(): self.u.reg_write(register, value)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.u.reg_write(UC_X86_REG_ECX, PARENT if mode == 'tree' else ROW)
        self.w(STACK, STOP)
        if mode == 'predicate': start, args = VA, 0
        elif mode == 'draw': start, args = self.r(0x491A078), 0
        elif mode == 'state': start, args = self.r(0x491A0C4), 0
        elif mode == 'tree':
            start, args = 0x38293E0, 8
            self.w(STACK + 4, 7); self.w(STACK + 8, 11)
        else: raise ValueError(mode)
        try:
            self.u.emu_start(start, STOP, count=30000)
        except Exception:
            print(json.dumps(dict(variant=self.variant, mode=mode,
                eip=hex(self.u.reg_read(UC_X86_REG_EIP)), trace=self.trace)))
            raise
        assert self.u.reg_read(UC_X86_REG_EIP) == STOP
        assert self.u.reg_read(UC_X86_REG_ESP) == STACK + 4 + args
        for register, value in NONVOL.items(): assert self.u.reg_read(register) == value
        for at, data in tracked: assert bytes(self.u.mem_read(at, 4096)) == data, ('mutation', hex(at))
        result = self.u.reg_read(UC_X86_REG_EAX)
        allowed = expected_allow if self.patched or mode == 'predicate' else True
        if mode == 'predicate': assert result == int(allowed), (self.variant, result)
        elif mode == 'state':
            assert result == int(allowed and self.variant != 'mouse_disabled')
        elif mode == 'draw':
            assert result == 1
            expected_trace = ([('render_leaf', '0x3828f50')] if self.variant == 'invisible' else
                [('render_leaf', '0x3828f50'), ('render_leaf', '0x1f209d0'), ('render_leaf', '0x37ea920')])
            assert self.trace == (expected_trace if allowed else []), (self.variant, self.trace)
        elif mode == 'tree':
            hit = allowed and self.variant not in ('mouse_disabled', 'invisible', 'input_disabled')
            assert result == (77 if hit else 0xFFFFFFFF), (self.variant, result, self.trace)
            assert ('hit_leaf', hex(LEAF)) in self.trace if hit else ('hit_leaf', hex(LEAF)) not in self.trace
        return dict(variant=self.variant, mode=mode, label=label, patched=self.patched,
            slot=self.slot, allow=allowed, result=hex(result), trace=list(self.trace),
            objectsUnchanged=True, refcountUnchanged=True, registersPreserved=True)


def run_suite(target):
    assert not target.exists()
    denied = {'departed', 'no_party', 'wide_member', 'zero_member', 'empty_member', 'stale_clicked'}
    variants = ['member', 'departed', 'no_party', 'self_no_party', 'self_missing',
        'no_scene', 'no_self', 'expired_parent', 'no_parent', 'raw_namespace',
        'party_owner', 'bad_owner', 'wide_owner', 'bad_route', 'wide_route',
        'zero_namespace', 'zero_route', 'wide_member', 'zero_member', 'empty_member',
        'zero_key', 'unbound', 'wide_key', 'zero_self', 'invalid_self',
        'category1', 'category2', 'category3', 'arena', 'mouse_disabled',
        'invisible', 'all_eight', 'stale_clicked']
    cases = []
    for slot in (0, 1, 7):
        for variant in variants:
            m = Machine(variant, slot)
            for mode in ('predicate', 'state', 'draw', 'tree'):
                cases.append(m.call(mode, variant not in denied))
    for variant in ('departed', 'no_party', 'stale_clicked'):
        m = Machine(variant, patched=False)
        for mode in ('state', 'draw', 'tree'):
            cases.append(m.call(mode, False, 'old-client control'))
    # Same live synthetic objects: remove a member, reinsert at another slot,
    # detach party and attach a new one. No hidden marker can become sticky.
    m = Machine()
    sequences = [('initial', True), ('leave', False), ('rejoin_new_slot', True),
                 ('detach', False), ('reattach', True), ('second_leave', False)]
    for label, allow in sequences:
        if label in ('leave', 'second_leave'):
            for i in range(8):
                if m.r(PARTY + 0x2C + 4*i) == 20: m.w(PARTY + 0x2C + 4*i, 0xFFFF)
        elif label == 'rejoin_new_slot': m.w(PARTY + 0x2C + 4*6, 20)
        elif label == 'detach': m.w(SELF + 0x4C8, 0)
        elif label == 'reattach': m.w(SELF + 0x4C8, PARTY)
        for mode in ('predicate', 'state', 'draw', 'tree'):
            cases.append(m.call(mode, allow, label))
    result = dict(passed=True, cases=cases, caseCount=len(cases),
        baseSha256=BASE_SHA, candidateSha256=RECEIPT['candidateSha256'],
        nativeWindows={hex(k):v for k,v in WINDOWS.items()},
        realExecution=['whole new predicate and both vtable wrappers',
            'parent weak-to-shared promotion and native shared assignment',
            'party DWORD key lookup and raw/fallback namespace getter branches',
            'generic row render and recursive parent/row/leaf input traversal'],
        fixtures=['synthetic objects and iterator leaves', 'arena and namespace decoder/locks',
            'render prepare/tick/children leaves', 'final leaf input event'],
        limits=RECEIPT['limitations'], deployed=False, guiAccepted=False)
    write_result(target, result)
    print(json.dumps(dict(passed=True, caseCount=len(cases), candidateSha256=result['candidateSha256'])))
    return result


def dependency_info():
    version = getattr(unicorn, "__version__", None)
    require(version == "2.1.4", "Expected unicorn==2.1.4; found {!r}".format(version))
    return dict(python=sys.version, executable=sys.executable,
                unicornVersion=version, unicornModule=str(Path(unicorn.__file__).resolve()))


def load_inputs(base_client_exe, candidate_exe):
    """Load fixed inputs explicitly; never invoke or import a builder."""
    global BASE, NEW, OLD_PE, NEW_PE
    dependency_info()
    paths = [Path(p).expanduser().resolve(strict=True)
             for p in (base_client_exe, candidate_exe)]
    for path in paths:
        require(path.is_file(), "Input is not a file: {}".format(path))
    base, candidate = (path.read_bytes() for path in paths)
    metadata = {}
    for label, path, data, expected_sha, expected_size in (
        ("base", paths[0], base, BASE_SHA, BASE_SIZE),
        ("candidate", paths[1], candidate, CANDIDATE_SHA, CANDIDATE_SIZE),
    ):
        actual_sha = hashlib.sha256(data).hexdigest()
        require(len(data) == expected_size and actual_sha == expected_sha,
                "{} must be the fixed reviewed image: expected {} / {} bytes; "
                "got {} / {} bytes".format(label, expected_sha, expected_size,
                                          actual_sha, len(data)))
        metadata[label] = dict(path=str(path), sha256=actual_sha, size=len(data))
    old_pe, new_pe = PE(base), PE(candidate)
    BASE, NEW, OLD_PE, NEW_PE = base, candidate, old_pe, new_pe
    return metadata


def parse_args(description, argv=None):
    parser = argparse.ArgumentParser(description=description)
    parser.add_argument("--base-client-exe", required=True, type=Path)
    parser.add_argument("--candidate-exe", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    return parser.parse_args(argv)


def create_output_dir(path):
    path = path.expanduser().absolute()
    # mkdir also refuses a pre-existing empty directory, file or symlink.
    # Its parent must already exist; partial failure outputs are never removed.
    path.mkdir(exist_ok=False)
    return path


def write_result(target, result):
    with Path(target).open("x", encoding="utf-8", newline="\n") as stream:
        stream.write(json.dumps(result, indent=2) + "\n")


def run_logged(name, run, receipt_name, expected_count, output_dir):
    with (output_dir / (name + ".stdout.log")).open("x", encoding="utf-8") as stdout:
        with (output_dir / (name + ".stderr.log")).open("x", encoding="utf-8") as stderr:
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                try:
                    result = run(output_dir / receipt_name)
                    require(result.get("passed") is True, name + " did not pass")
                    require(result.get("caseCount") == expected_count
                            and len(result.get("cases", ())) == expected_count,
                            name + " case count changed")
                    return result
                except BaseException:
                    traceback.print_exc()
                    raise


def standalone_cli(run, name, receipt_name, expected_count, argv=None):
    args = parse_args("Run the fixed-v2 {} fixtures.".format(name), argv)
    try:
        load_inputs(args.base_client_exe, args.candidate_exe)
        output = create_output_dir(args.output_dir)
        result = run_logged(name, run, receipt_name, expected_count, output)
    except Exception as exc:
        print("{} failed: {}: {}".format(name, type(exc).__name__, exc), file=sys.stderr)
        return 1
    print(json.dumps(dict(passed=True, caseCount=result["caseCount"],
                          candidateSha256=CANDIDATE_SHA, outputDir=str(output))))
    return 0


if __name__ == "__main__":
    raise SystemExit(standalone_cli(run_suite, "core", "native-verification-v2.json", 429))
