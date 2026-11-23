import struct
import pefile

def build(source):
    p = pefile.PE(data=source, fast_load=True)
    p.parse_data_directories(directories=[1])
    last = p.sections[-1]
    assert last.Characteristics & 0x20000000
    assert last.PointerToRawData + last.SizeOfRawData == len(source)
    assert not p.OPTIONAL_HEADER.DllCharacteristics & 0x40
    base = p.OPTIONAL_HEADER.ImageBase
    def aligned(n, align): return ((n + align - 1)//align)*align
    # Append import metadata beyond all previous code. Keep original IAT slots
    # exactly where their callers and the cumulative launcher expect them.
    start = aligned(max(last.Misc_VirtualSize, last.SizeOfRawData), 16)
    rva = last.VirtualAddress + start
    imp = p.OPTIONAL_HEADER.DATA_DIRECTORY[1]
    old = p.get_data(imp.VirtualAddress, len(p.DIRECTORY_ENTRY_IMPORT)*20)
    assert p.get_data(imp.VirtualAddress+len(old),20) == bytes(20)
    meta = bytearray(old + bytes(40))
    def add(blob, alignment=1):
        meta.extend(bytes(aligned(len(meta),alignment)-len(meta)))
        addr = rva+len(meta)
        meta.extend(blob)
        return addr
    dll = add(b'AtlasWorkers101.dll\0')
    name = add(b'\0\0TryInflateAtlas\0',2)
    ilt = add(struct.pack('<II', name,0),4)
    iat = add(struct.pack('<II', name,0),4)
    struct.pack_into('<IIIII', meta,len(old),ilt,0,0,dll,iat)
    thunk = add(bytes.fromhex('ff25')+struct.pack('<I',base+iat), 4)
    assert last.Characteristics & 0x20000000
    out = bytearray(source)
    used = start+len(meta)
    rawsize = aligned(used,p.OPTIONAL_HEADER.FileAlignment)
    out.extend(bytes(rawsize-last.SizeOfRawData))
    off = last.PointerToRawData+start
    out[off:off+len(meta)] = meta
    def field(header, key, value):
        struct.pack_into('<I',out,header.get_field_absolute_offset(key),value)
    field(last,'Misc_VirtualSize',used)
    field(last,'SizeOfRawData',rawsize)
    field(p.OPTIONAL_HEADER,'SizeOfCode',p.OPTIONAL_HEADER.SizeOfCode+rawsize-last.SizeOfRawData)
    field(p.OPTIONAL_HEADER,'SizeOfImage',aligned(last.VirtualAddress+used,p.OPTIONAL_HEADER.SectionAlignment))
    field(imp,'VirtualAddress',rva); field(imp,'Size',len(old)+40)

    sites = (0x37f9912,0x37f9bfe,0x3801271)
    for site in sites:
        offset = p.get_offset_from_rva(site-base)
        def call(dest): return b'\xe8'+struct.pack('<i',dest-site-5)
        assert source[offset:offset+5]==call(0x3e448b0)
        out[offset:offset+5]=call(base+thunk)
    field(p.OPTIONAL_HEADER,'CheckSum',0)
    v = pefile.PE(data=out,fast_load=True)
    field(p.OPTIONAL_HEADER,'CheckSum',v.generate_checksum())
    return bytes(out)
