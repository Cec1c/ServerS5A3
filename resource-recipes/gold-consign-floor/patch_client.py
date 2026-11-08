"""Apply the private proportional consignment floor to the exact V210 image."""
import argparse
import json
from pathlib import Path
import struct
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'scripts/client'))
import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA256 = '24372bf2df57cdaecc696752299dbe8c61e222806a697e3d1c6f57fbcaffbdae'
INPUT_SIZE = 112549888
OUTPUT_SHA256 = '69c4c962ece1de5f9fb8e79a8b245a00b6020f00bcbd8020d21f91fbea85dd62'
REVIEWED_IMAGES = {
    INPUT_SHA256: OUTPUT_SHA256,
    # PR17 town display-only helper ends at .ctnull+5ae; gold uses +800..+9e4.
    'd1930d5bb07ee8fae348e0c23551b0f12bcfaea67ed7bf53ab0c9099e195c309':
        '686eaa069ba3cc374520d249628efc9de89bf0892ad45ad1e4bd2d2206b08dc5',
}
TIERS = ((2681725, 1000), (2681726, 2000), (2681727, 3000),
         (2681728, 4000), (2681729, 5000), (2681730, 6000),
         (2681731, 7000), (2681732, 8000), (2681733, 9000),
         (2681734, 10000), (2681735, 20000), (2681736, 30000),
         (2683069, 40000), (2683070, 50000), (2683071, 100000),
         (2683072, 150000), (2683073, 200000))
FEE_SITE = 0x233DD70
SEND_SITE = 0x233F692
HINT_SITE = 0x233ED4B
HINT = '最低价：每100万金币1000点券'


def helpers(base):
    c = Code(base)
    c.label('fee')
    c.emit('56 57 8bf1 8b8eb0010000 85c9')  # selected tier control
    c.branch('0f84', 'rejected')
    c.branch('e8', 0x3858FA0)  # native combo selected item ID; no args
    c.branch('e8', 'minimum')
    c.emit('85c0')
    c.branch('0f84', 'rejected')
    c.emit('8bf8 8b8eb8010000 85c9')  # price text control
    c.branch('0f84', 'rejected')
    c.emit('8b01 ff90e4000000 50')
    c.branch('e8', 0x3E76CF7)  # existing native integer text parser; cdecl
    c.emit('83c404 3bc7')
    c.branch('0f8c', 'rejected')
    c.emit('8bce 5f 5e 83b9d801000000 f30f108114020000')
    c.branch('0f85', 0x233DDAF)  # retain high-price fee ladder, skip old 40% floor
    c.branch('e9', 0x233DD81)  # no history: native coupon/base fee
    c.label('rejected')
    c.emit('83c8ff 5f 5e c3')

    c.label('send')
    c.emit('9c 60 8bc3')  # EBX=item ID, EDI=parsed price, preserve native frame
    c.branch('e8', 'minimum')
    c.emit('85c0')
    c.branch('0f84', 'no_send')
    c.emit('3bf8')
    c.branch('0f8c', 'no_send')
    c.emit('61 9d 68b7000000')
    c.branch('e9', SEND_SITE + 5)
    c.label('no_send')
    c.emit('61 9d 5f 5b 5e c3')  # unwind exact native submit saves; emit no packet

    c.label('minimum')
    for index, (item, minimum) in enumerate(TIERS):
        c.emit('3d' + struct.pack('<I', item).hex())
        c.branch('0f85', 'tier' + str(index))
        c.emit('b8' + struct.pack('<I', minimum).hex() + 'c3')
        c.label('tier' + str(index))
    c.emit('31c0 c3')
    c.label('hint')
    c.data.extend((HINT + '\0').encode('utf-16le'))
    return c.finish(), c.labels


def build(data):
    expected = REVIEWED_IMAGES.get(pe.sha(data))
    if len(data) != INPUT_SIZE or expected is None:
        raise ValueError('exact reviewed cumulative V210/V211 SHA256/size required')
    opt, sections = pe.headers(data)
    coff = opt - 24
    table = opt + struct.unpack_from('<H', data, coff + 20)[0]
    headers = [table + n*40 for n in range(struct.unpack_from('<H', data, coff + 6)[0])
               if data[table+n*40:table+n*40+8] == b'.ctnull\0']
    if len(headers) != 1:
        raise ValueError('audited RX section missing')
    header = headers[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if (rva, rawsize) != (0x6b8b000, 0x1000) or vsize > 0x800 or any(data[raw+vsize:raw+rawsize]):
        raise ValueError('audited unused tail changed')
    if struct.unpack_from('<I', data, header + 36)[0] != 0x60000020:
        raise ValueError('section permissions changed')
    code, labels = helpers(pe.IMAGE_BASE + rva + 0x800)
    if 0x800 + len(code) > rawsize:
        raise ValueError('helper exceeds reserved tail')
    out = bytearray(data)
    ranges = []

    def patch(va, before, after):
        at = pe.offset(sections, va, len(before))
        if data[at:at+len(before)] != before or len(before) != len(after):
            raise ValueError('native fingerprint changed: ' + hex(va))
        out[at:at+len(after)] = after
        ranges.append([at, at+len(after)])

    for site, before, label in [(FEE_SITE, bytes.fromhex('83b9d801000000'), 'fee'),
                                (SEND_SITE, bytes.fromhex('68b7000000'), 'send')]:
        patch(site, before, b'\xe9' + struct.pack('<i', labels[label]-site-5) + b'\x90'*(len(before)-5))
    # The old native lookup used one cdecl argument. The replacement has none;
    # preserve that stack slot so the original formatter cleanup remains exact.
    patch(HINT_SITE, bytes.fromhex('6879c40000e8cb786601'),
          b'\x6a\x00\xb8' + struct.pack('<I',labels['hint']) + b'\x90'*3)
    out[raw+0x800:raw+0x800+len(code)] = code
    struct.pack_into('<I', out, header+8, 0x800+len(code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    ranges.extend([[raw+0x800,raw+0x800+len(code)], [header+8,header+12], [opt+64,opt+68]])
    restored = bytearray(out)
    for lo, hi in ranges:
        restored[lo:hi] = data[lo:hi]
    if restored != data:
        raise ValueError('unexpected changes outside reviewed locations')
    if pe.sha(out) != expected:
        raise ValueError('deterministic output identity changed')
    return bytes(out), dict(inputSha256=pe.sha(data), outputSha256=pe.sha(out), bytes=len(out),
                           labels=labels, changedRanges=sorted(ranges), tierMinimums=dict(TIERS),
                           playerAcceptance='pending', pvfChanged=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    a = parser.parse_args()
    if a.output.exists() or a.input.resolve() == a.output.resolve():
        raise ValueError('output must be separate and new')
    output, receipt = build(a.input.read_bytes())
    with a.output.open('xb') as stream:
        stream.write(output)
    if pe.sha(a.input.read_bytes()) != receipt['inputSha256'] or a.output.read_bytes() != output:
        raise ValueError('input/output readback mismatch')
    print(json.dumps(receipt, ensure_ascii=False, indent=2))
