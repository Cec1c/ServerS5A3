"""Build the V177 title-claim candidate from an exact published V177 image.

This is not the old v170 builder. It:

  * requires the exact V177 image (SHA256 and size),
  * requires the exact published 250-byte .op19ui extension (digest plus the
    five legacy return comparisons) and a zeroed extension tail,
  * reuses verify_boundary.inject for the in-memory patch, so the build and the
    boundary test share one implementation,
  * verifies the diff is confined to three segments before writing anything:
      extension (old 250 -> new 278 bytes)
      .op19ui virtual size
      PE checksum
  * never overwrites an existing output or receipt, and writes through temp
    files that are removed if any check fails.

This script does not deploy, publish, or run a client.

    python -B build_claim_v177.py --input <V177.exe> --output <candidate.exe> \
        --receipt <receipt.json> [--dry-run]
"""
import argparse
import json
import os
import struct
from pathlib import Path

from pe_support import Image, sha256
from title_extension import OLD_VIRTUAL_SIZE, OP19UI, title_extension

V177_SHA256 = '027eca60196f8e22b7d05a9cad4ae37fe0098ab48e3048714b8ceb9270f5e752'
V177_SIZE = 111919104
OLD_EXTENSION_SIZE = 250
OLD_EXTENSION_SHA256 = 'da62daefbd08ccc2808360df017a65e2ffa91e6c4ef9a9ad446bc74324b1c877'
LEGACY_RETURNS = (0x12A3479, 0x12AB199, 0x12ACB30, 0x12ACB7D, 0x12AF91D)
CLAIM_RETURN = 0x24A288E
EXTENSION_VA = OP19UI + OLD_VIRTUAL_SIZE
FALLBACK = 0x6E95050


def require(condition, message):
    if not condition:
        raise SystemExit(message)


def verify_baseline(data):
    image = Image.from_bytes(data)
    require(image.sha256 == V177_SHA256 and len(data) == V177_SIZE,
            'exact V177 image required')
    section = image.section('.op19ui')
    require(section['va'] == OP19UI and section['raw_size'] == 0x1000,
            'unexpected .op19ui section')
    require(section['virtual_size'] == OLD_VIRTUAL_SIZE + OLD_EXTENSION_SIZE,
            'unexpected baseline .op19ui virtual size')
    require(image.bytes_at(FALLBACK, 5) ==
            b'\xe9' + struct.pack('<i', EXTENSION_VA - FALLBACK - 5),
            'fallback does not target the baseline extension')
    extension = image.bytes_at(EXTENSION_VA, OLD_EXTENSION_SIZE)
    require(sha256(extension) == OLD_EXTENSION_SHA256,
            'baseline extension digest differs')
    for ret in LEGACY_RETURNS:
        pattern = b'\x81\x7d\x04' + struct.pack('<I', ret)
        require(extension.count(pattern) == 1,
                'baseline extension is missing return %#x' % ret)
    require(b'\x81\x7d\x04' + struct.pack('<I', CLAIM_RETURN) not in extension,
            'baseline already contains the claim return')
    tail = image.bytes_at(EXTENSION_VA + OLD_EXTENSION_SIZE,
                          0x1000 - OLD_VIRTUAL_SIZE - OLD_EXTENSION_SIZE)
    require(tail == bytes(len(tail)), 'extension tail is not zero')
    return image, section


def classify(offset, ranges):
    for name, start, end in ranges:
        if start <= offset < end:
            return name
    return None


def build(data):
    image, section = verify_baseline(data)
    from verify_boundary import inject  # shared patch implementation
    candidate = inject(data)
    patched = Image.from_bytes(candidate)
    extension = title_extension(EXTENSION_VA)
    require(len(extension) == OLD_EXTENSION_SIZE + 28,
            'extension length changed unexpectedly')
    require(patched.bytes_at(EXTENSION_VA, len(extension)) == extension,
            'candidate extension differs from title_extension output')
    require(patched.section('.op19ui')['virtual_size'] ==
            OLD_VIRTUAL_SIZE + len(extension),
            'candidate virtual size differs')
    tail = patched.bytes_at(EXTENSION_VA + len(extension),
                            0x1000 - OLD_VIRTUAL_SIZE - len(extension))
    require(tail == bytes(len(tail)), 'candidate tail is not zero')

    extension_off = image.file_offset(EXTENSION_VA, len(extension))
    checksum_off = image.optional + 64
    ranges = [
        ('extension', extension_off, extension_off + len(extension)),
        ('section-virtual-size', section['header'] + 8, section['header'] + 12),
        ('pe-checksum', checksum_off, checksum_off + 4),
    ]
    changed = [i for i, (a, b) in enumerate(zip(data, candidate)) if a != b]
    outside = [i for i in changed if classify(i, ranges) is None]
    prefix_outside = [i for i in changed
                      if i < extension_off and classify(i, ranges) is None]
    tail_outside = [i for i in changed
                    if i >= extension_off + len(extension)
                    and classify(i, ranges) is None]
    require(not outside, 'unreviewed byte changes at %r' % outside[:8])
    per_segment = {}
    for name, start, end in ranges:
        per_segment[name] = sum(1 for i in changed if start <= i < end)

    before_checksum = struct.unpack_from('<I', data, checksum_off)[0]
    after_checksum = struct.unpack_from('<I', candidate, checksum_off)[0]
    receipt = {
        'schema': 'title-claim-build.v1',
        'inputSha256': image.sha256,
        'inputSize': len(data),
        'outputSha256': sha256(candidate),
        'outputSize': len(candidate),
        'extension': {
            'va': hex(EXTENSION_VA),
            'oldBytes': OLD_EXTENSION_SIZE,
            'newBytes': len(extension),
            'oldSha256': OLD_EXTENSION_SHA256,
            'newSha256': sha256(extension),
            'fileOffset': extension_off,
        },
        'section': {
            'name': '.op19ui',
            'va': hex(OP19UI),
            'virtualSizeBefore': hex(section['virtual_size']),
            'virtualSizeAfter': hex(OLD_VIRTUAL_SIZE + len(extension)),
            'headerOffset': section['header'],
        },
        'peChecksum': {
            'offset': checksum_off,
            'before': hex(before_checksum),
            'after': hex(after_checksum),
        },
        'diff': {
            'changedBytes': len(changed),
            'perSegment': per_segment,
            'allowedRanges': [[name, start, end] for name, start, end in ranges],
            'outsideAllowedRanges': outside,
            'prefixUnchanged': not prefix_outside,
            'tailUnchanged': not tail_outside,
            'allOtherBytesUnchanged': not outside,
        },
        'gameAcceptance': False,
        'deployed': False,
        'builtBy': 'build_claim_v177.py',
    }
    return candidate, receipt


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()

    source_path = args.input.resolve()
    output_path = args.output.resolve()
    receipt_path = args.receipt.resolve()
    require(output_path != source_path, 'output must differ from input')
    require(receipt_path != source_path, 'receipt must differ from input')
    require(output_path != receipt_path, 'output and receipt must differ')
    require(not output_path.exists(), 'output already exists; refusing to overwrite')
    require(not receipt_path.exists(), 'receipt already exists; refusing to overwrite')

    data = source_path.read_bytes()
    candidate, receipt = build(data)
    if args.dry_run:
        print(json.dumps({'dryRun': True,
                          'outputSha256': receipt['outputSha256'],
                          'changedBytes': receipt['diff']['changedBytes'],
                          'perSegment': receipt['diff']['perSegment'],
                          'outsideAllowedRanges': receipt['diff']['outsideAllowedRanges'],
                          'prefixUnchanged': receipt['diff']['prefixUnchanged'],
                          'tailUnchanged': receipt['diff']['tailUnchanged'],
                          'virtualSizeBefore': receipt['section']['virtualSizeBefore'],
                          'virtualSizeAfter': receipt['section']['virtualSizeAfter'],
                          'extensionBytes': [receipt['extension']['oldBytes'],
                                             receipt['extension']['newBytes']]}))
        return

    output_path.parent.mkdir(parents=True, exist_ok=True)
    receipt_path.parent.mkdir(parents=True, exist_ok=True)
    output_tmp = output_path.with_name(output_path.name + '.tmp')
    receipt_tmp = receipt_path.with_name(receipt_path.name + '.tmp')
    for path in (output_tmp, receipt_tmp):
        require(not path.exists(), 'temporary file exists: %s' % path)
    try:
        output_tmp.write_bytes(candidate)
        receipt_tmp.write_text(json.dumps(receipt, indent=2) + '\n', encoding='utf8')
        os.replace(output_tmp, output_path)
        os.replace(receipt_tmp, receipt_path)
    except BaseException:
        for path in (output_tmp, receipt_tmp):
            try:
                path.unlink()
            except FileNotFoundError:
                pass
        raise
    print(json.dumps({'outputSha256': receipt['outputSha256'],
                      'changedBytes': receipt['diff']['changedBytes']}))


if __name__ == '__main__':
    main()
