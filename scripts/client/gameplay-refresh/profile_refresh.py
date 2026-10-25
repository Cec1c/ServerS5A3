"""Refresh only the already-open, exactly bound peer profile.

Mode8 still builds its appearance in the auxiliary cache. Its detail phase
may update an active actor only when that exact actor is the current personal
information window's binding. Background priming retains its auxiliary path.
The details consumer keeps parsing/cleanup; its UI branch is permitted only
for the same key and both namespace bytes in the active C8 window list.
"""
import struct

UI_GET = 0x32B5C90                 # active list only; never creates a window
UI_VTABLE = 0x4CFD41C
DETAIL_SITE = 0x22C42E6
QUIET_SITE = 0x22C2539
DETAIL_OLD = bytes.fromhex('e955c0bf0490')
QUIET_OLD = bytes.fromhex('e9c20ac004')
PANEL_LOOKUP_SITES = (0x3377706, 0x337837B)

class Code:
    def __init__(self, address):
        self.address, self.b, self.labels, self.fix = address, bytearray(), {}, []
    def raw(self, s): self.b.extend(bytes.fromhex(s))
    def rel(self, op, target):
        self.raw(op); self.b.extend(struct.pack('<i', target-self.address-len(self.b)-4))
    def branch(self, op, label):
        self.raw(op); self.fix.append((len(self.b),label)); self.b.extend(bytes(4))
    def label(self, name): self.labels[name]=len(self.b)
    def finish(self):
        for at,label in self.fix: struct.pack_into('<i',self.b,at,self.labels[label]-at-4)
        return bytes(self.b)

def active_window(c, reject):
    c.raw('8b0d98376a05 85c9'); c.branch('0f84',reject)
    c.raw('6a00 68c8000000'); c.rel('e8',UI_GET)
    c.raw('85c0'); c.branch('0f84',reject)
    c.raw('8138'); c.b.extend(struct.pack('<I',UI_VTABLE)); c.branch('0f85',reject)
    # A closing window can remain in the active vector until deferred removal.
    c.raw('8b5004 85d2'); c.branch('0f84',reject)
    c.raw('807a3c00'); c.branch('0f85',reject)

def detail_helper(address):
    c=Code(address)
    c.raw('9c60 817d04cd8a2c02'); c.branch('0f85','ordinary')
    active_window(c,'aux')
    c.raw('89c3 8b8538fbffff 6639838c030000'); c.branch('0f85','aux')
    c.raw('0fb60d19a56805 388bb8030000'); c.branch('0f85','aux')
    c.raw('0fb60d1aa56805 388bb9030000'); c.branch('0f85','aux')
    c.raw('50'); c.rel('e8',0x296D730); c.raw('83c404 85c0'); c.branch('0f84','aux')
    c.raw('3983b8010000'); c.branch('0f85','aux')
    c.label('ordinary'); c.raw('619d 8b8538fbffff'); c.rel('e9',0x22C42EC)
    c.label('aux'); c.raw('619d'); c.rel('e9',0x22C42F7)
    return c.finish()

def quiet_helper(address):
    c=Code(address)
    c.raw('9c60 817d041e462c02'); c.branch('0f85','ordinary')
    c.raw('8b4500 817804cd8a2c02'); c.branch('0f85','ordinary')
    active_window(c,'quiet')
    c.raw('89c3 8b750c 85f6'); c.branch('0f84','quiet')
    c.raw('8bce'); c.rel('e8',0x29614E0)
    c.raw('6639838c030000'); c.branch('0f85','quiet')
    c.raw('8bce'); c.rel('e8',0x2964680)
    c.raw('3883b8030000'); c.branch('0f85','quiet')
    c.raw('8bce'); c.rel('e8',0x2964560)
    c.raw('3883b9030000'); c.branch('0f85','quiet')
    c.label('ordinary'); c.raw('619d 8b750c 8bce'); c.rel('e9',0x22C253E)
    c.label('quiet'); c.raw('619d'); c.rel('e9',0x22C25B8)
    return c.finish()

def redirect(site, helper, size):
    return b'\xe9'+struct.pack('<i',helper-site-5)+b'\x90'*(size-5)

def lookup_helper(address):
    c=Code(address)
    c.raw('ff742404'); c.rel('e8',0x296D730); c.raw('83c404 85c0'); c.branch('0f85','done')
    # ESI is the C8 window at both reviewed call sites. The ordinary native
    # lookup remains first; only a missing scene actor uses the exact cached
    # identity that mode8 initialized before explicit mode3 inspection.
    c.raw('813e'); c.b.extend(struct.pack('<I',UI_VTABLE)); c.branch('0f85','done')
    c.raw('8b442404 85c0'); c.branch('0f84','none')
    c.raw('3dffff0000'); c.branch('0f83','none')
    c.raw('6639868c030000'); c.branch('0f85','none')
    c.raw('0fb68eb9030000 51 0fb68eb8030000 51 50'); c.rel('e8',0x1A5A5E0); c.raw('83c40c')
    c.label('done'); c.raw('c3')
    c.label('none'); c.raw('31c0c3')
    return c.finish()
