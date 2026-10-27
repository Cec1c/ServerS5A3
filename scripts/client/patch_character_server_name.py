"""Build a separate character-server-name dstr resource from the audited input.

The V179 character tooltip uses string 71700 for character server group zero.
This edits that string in FILE/102/1042, within its existing SC01 capacity.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import struct

import pefile

INPUT_SHA256 = 'ee8904c8540ef63807dc984604a3ea0f5cc4040228e5ac2e95d490c03d21ec08'
INITIAL_KEY = 0x5819af17


def sha(data):
    return hashlib.sha256(data).hexdigest()


def decode(raw):
    if len(raw) < 8 or raw[:4] != b'SC01' or (len(raw)-8) % 4:
        raise ValueError('SC01 resource layout mismatch')
    length = struct.unpack_from('<I', raw, 4)[0]
    if not 0 < length <= len(raw)-8:
        raise ValueError('SC01 text size mismatch')
    key, result = INITIAL_KEY, bytearray()
    for offset in range(8, len(raw), 4):
        value = struct.unpack_from('<I', raw, offset)[0]
        result.extend(struct.pack('<I', value ^ key))
        key = value
    return bytes(result[:length]).decode('utf-8')


def encode(text, capacity):
    raw = text.encode('utf-8')
    if len(raw) >= capacity or capacity % 4:
        raise ValueError('new name does not fit the existing resource capacity')
    plaintext = raw + bytes(capacity-len(raw))
    result = bytearray(b'SC01' + struct.pack('<I', len(raw)))
    key = INITIAL_KEY
    for offset in range(0, capacity, 4):
        key ^= struct.unpack_from('<I', plaintext, offset)[0]
        result.extend(struct.pack('<I', key))
    return bytes(result)


def resources(image):
    result = []
    for group in image.DIRECTORY_ENTRY_RESOURCE.entries:
        if str(group.name) != 'FILE':
            continue
        for entry in group.directory.entries:
            for language in entry.directory.entries:
                record = language.data.struct
                result.append((entry.id, language.id,
                               image.get_offset_from_rva(record.OffsetToData), record.Size))
    return result


def build(data, server_name):
    if sha(data) != INPUT_SHA256:
        raise ValueError('exact audited dstr input required')
    if not server_name or server_name.strip() != server_name or any(ord(c)<32 or c in '>\x7f' for c in server_name):
        raise ValueError('one nonempty server name required')
    image = pefile.PE(data=data)
    rows = [row for row in resources(image) if row[:2] == (102,1042)]
    if len(rows) != 1:
        raise ValueError('expected one FILE/102/1042 resource')
    _,_,offset,size = rows[0]
    text = decode(data[offset:offset+size])
    matches = list(re.finditer(r'^71700>([^\r\n]*)',text,re.M))
    if len(matches) != 1 or matches[0].group(1) != 'YKCN90':
        raise ValueError('expected original character server string')
    match = matches[0]
    replacement = text[:match.start(1)] + server_name + text[match.end(1):]
    encoded = encode(replacement,size-8)
    if decode(encoded) != replacement:
        raise ValueError('SC01 round trip mismatch')
    out = bytearray(data)
    out[offset:offset+size] = encoded
    checksum_at = image.OPTIONAL_HEADER.get_field_absolute_offset('CheckSum')
    updated = pefile.PE(data=bytes(out))
    struct.pack_into('<I',out,checksum_at,updated.generate_checksum())
    return bytes(out), dict(inputSha256=sha(data),outputSha256=sha(out),stringID=71700,
                           serverName=server_name,characterServerGroup=0,resource=[102,1042],
                           changedRanges=sorted([(offset,offset+size),(checksum_at,checksum_at+4)]))


def write_candidate(source,destination,server_name):
    source,destination = Path(source).resolve(),Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    data,receipt = build(source.read_bytes(),server_name)
    destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(data)
    if sha(destination.read_bytes()) != receipt['outputSha256'] or sha(source.read_bytes()) != receipt['inputSha256']:
        raise RuntimeError('readback mismatch')
    return receipt


if __name__ == '__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--server-name',required=True)
    args=parser.parse_args()
    print(json.dumps(write_candidate(args.input,args.output,args.server_name),ensure_ascii=False,indent=2))
