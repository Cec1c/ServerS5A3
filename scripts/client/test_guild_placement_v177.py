"""Matched V177 placement sender, receivers, geometry and exact patch tests.

STL containers, UI/resources, object factory, footprint binding and scene effects
are adapters. Native sender, geometry, record/queue updates and scene registration
control flow execute original machine code. Production bodies can be supplied by
GUILD_PLACEMENT_NATIVE_DIR; no game process or network is started.
"""
import hashlib, json, os, struct, tempfile, unittest
from pathlib import Path
from unicorn.x86_const import *
from test_guild_war_refresh_v173 import WarNative
from test_patch_fatigue_count_v164 import native, windows_image
import patch_guild_placement_v177 as patch

class PlacementLab(WarNative):
    DATA=WarNative.GUILD+0x28
    GRID=0x20010008
    CONTROL=GRID-8
    RECTS=0x20012000
    MAP=0x20016000
    FOOT=0x20018000
    MASK=0x20018100
    TEMP=0x20019000
    def __init__(self, data):
        super().__init__(data)
        self.cells={}; self.heap=0x23010000; self.lookups=[]; self.sent=[];self.current=bytearray()
        self.cursor=(900,270)
        self.hooks.update({
            0x224e5f0:self.begin,0xA8F6C0:self.end,0x3c501e0:self.cell_insert,
            0x3754af0:lambda:self.ret(),0x3d14570:self.cell_lookup,
            0x25204a0:self.clear_vector,0x12f2fb0:self.push_point,
            0x26f9b30:self.init_vector,0x283c270:lambda:self.ret(),
            0x2046e20:lambda:self.ret(value=self.SCENE),
            0x2046ef0:lambda:self.ret(value=self.SCENE),
            0x14244c0:lambda:self.ret(value=self.MAP),
            0x1a68d40:lambda:self.ret(value=self.MAP),
            0x3819fb0:lambda:self.ret(value=self.cursor[0]),
            0x3819fd0:lambda:self.ret(value=self.cursor[1]),
            0x1338ca0:lambda:self.ret(4,self.SCENE),
            0xb28450:lambda:self.ret(12,self.arg()),
            0xb284d0:lambda:self.ret(16,self.arg()),
            0x1078950:self.absent,
            0x3c4fca0:lambda:self.ret(4,0),
            0x25eca10:lambda:self.ret(4,42),
            0x374f280:self.opcode,0x374eeb0:self.write_buffer,0x374f3f0:self.write_u32,
            0x3750ac0:self.send,
            0x1077ed0:lambda:self.ret(),0x1077f30:lambda:self.ret(4),
            0x32c0830:lambda:self.ret(4),
            0x32c2ef0:lambda:self.ret(),
            0x37e0840:self.small_format,
        })
        self.put(self.CONTROL+4,1)
        self.put(self.GRID,0)
        self.put(self.CONTROL+0x5c,2)
        self.put(self.CONTROL+0x9c,0,10156101)
        self.put(self.MAP+0x56c,0)
        self.put(self.DATA+0x3677,15)

    def alloc(self,size=64):
        at=self.heap;self.heap+=(size+15)&~15;return at
    def small_format(self):
        out=self.arg();self.put(out,0x2000b800,0);self.ret(value=out)
    def run(self,va,args=(),ecx=None,pop=None):
        self.put(self.STACK,self.STOP,*args)
        self.u.reg_write(UC_X86_REG_ESP,self.STACK);self.u.reg_write(UC_X86_REG_EFLAGS,2)
        keep={UC_X86_REG_EBX:0x12345670,UC_X86_REG_ESI:0x12345671,UC_X86_REG_EDI:0x12345672,UC_X86_REG_EBP:0x12345673}
        for r,v in keep.items():self.u.reg_write(r,v)
        self.u.reg_write(UC_X86_REG_ECX,self.MANAGER if ecx is None else ecx)
        seh=self.read(0)
        self.u.emu_start(va,self.STOP,count=2000000)
        assert self.reg(UC_X86_REG_EIP)==self.STOP,hex(self.reg(UC_X86_REG_EIP))
        assert self.reg(UC_X86_REG_ESP)==self.STACK+4+(len(args)*4 if pop is None else pop),'stack ABI'
        assert all(self.reg(r)==v for r,v in keep.items()),'callee-saved ABI'
        assert self.read(0)==seh,'SEH chain'
        return self.reg(UC_X86_REG_EAX)
    def vec(self,at,start,end):
        proxy=self.alloc();self.put(proxy,at);self.put(at,proxy,start,end,end)
    def iterator(self,out,at,ptr):self.put(out,self.read(at),0,ptr);self.ret(4,out)
    def begin(self):
        at=self.reg(UC_X86_REG_ECX);self.iterator(self.arg(),at,self.read(at+4))
    def end(self):
        at=self.reg(UC_X86_REG_ECX)
        if at==self.CONTROL+0x74:self.absent();return
        self.iterator(self.arg(),at,self.read(at+8))
    def cell_insert(self):
        p=self.arg();key=tuple(struct.unpack('<ii',self.u.mem_read(p,8)));out=self.alloc()
        self.cells[key]=out;self.ret(4,out)
    def cell_lookup(self):
        key=tuple(struct.unpack('<ii',self.u.mem_read(self.arg(1),8)))
        out=self.arg();at=self.reg(UC_X86_REG_ECX)
        node=self.cells.get(key)
        if node is None:node=self.read(at+8)
        else:node-=0x14
        self.put(out,self.read(at),0,node);self.ret(8,out)
    def absent(self):
        out=self.arg();proxy=self.TEMP+0x80;tree=self.TEMP+0xa0
        self.put(proxy,tree);self.put(tree,0,0,tree)
        self.put(out,proxy,0,tree);self.ret(4 if self.reg(UC_X86_REG_EIP)!=0x1078950 else 8,out)
    def init_vector(self):
        at=self.reg(UC_X86_REG_ECX);p=self.alloc(4096);self.vec(at,p,p);self.ret(value=at)
    def clear_vector(self):
        at=self.reg(UC_X86_REG_ECX);self.put(at+8,self.read(at+4));self.ret()
    def push_point(self):
        at=self.reg(UC_X86_REG_ECX);p=self.read(at+8)
        self.u.mem_write(p,bytes(self.u.mem_read(self.arg(),8)));self.put(at+8,p+8);self.ret(4)
    def opcode(self):self.op=self.arg();self.current=bytearray();self.ret(4)
    def write_buffer(self):self.current+=self.u.mem_read(self.arg(),self.arg(1));self.ret(8)
    def write_u32(self):self.current+=struct.pack('<I',self.arg());self.ret(4)
    def send(self):self.sent.append(dict(opcode=self.op,body=self.current.hex()));self.ret()
    def geometry(self,rects):
        for i,r in enumerate(rects):self.put(self.RECTS+i*40,*r)
        self.vec(self.GRID+4,self.RECTS,self.RECTS+40*len(rects))
        sentinel=self.alloc();proxy=self.alloc();self.put(proxy,self.GRID+0x40)
        self.put(self.GRID+0x40,proxy,0,sentinel)
        assert self.run(0x3c502d0,ecx=self.GRID)&255==1
        return tuple(struct.unpack('<4i',self.u.mem_read(self.GRID+0x18,16)))
    def footprint(self,w,h,mask):
        self.put(self.CONTROL+0xb4,0,w,h)
        self.vec(self.CONTROL+0xc0,self.MASK,self.MASK+4*len(mask));self.put(self.MASK,*mask)
    def send_book(self):
        self.run(0x1079720,(0,),ecx=self.CONTROL)
        return self.sent

class ProjectionLab(PlacementLab):
    def __init__(self, data):
        super().__init__(data);self.created=[];self.now=1800000000
        self.hooks.update({
            0x296dd80:lambda:self.ret(value=0x2000b000),
            0x3c1bea0:lambda:self.ret(value=0x2000b000),
            0x2986ab0:lambda:self.ret(4,0x2000d000),
            0x2985cc0:lambda:self.ret(4,self.GUILD),
            0x37df550:self.guild_id,0x37df5c0:self.guild_id,0x37df630:self.guild_id,
            0x37df6a0:self.guild_id,0x37df7f0:self.guild_id,
            0x1066200:lambda:self.ret(),0x1066670:lambda:self.ret(),0x10665f0:lambda:self.ret(12),
            0x1077e50:lambda:self.ret(value=self.CONTROL),
            0x107bc80:lambda:self.ret(4),
            0x2b79120:self.object_factory,0x3e77844:lambda:self.ret(value=self.now),
            0x107c8a0:self.bind_object,
            0x2978750:lambda:self.ret(value=0),
            0x287a190:lambda:self.ret(4,0),0x288a260:self.scene_add,
            self.STOP+0x100:self.position_object,
            0x1079d40:lambda:self.ret(16),0x106c720:lambda:self.ret(4,0),
            0x107b660:lambda:self.ret(8),
        })
        self.vec(self.GUILD+0x36c4,0x230b0000,0x230b0000)
        self.put(self.GUILD+0x36d0,0x230b8000)
    def guild_id(self):self.put(self.arg(1),123);self.ret(8)
    def object_factory(self):
        obj=self.alloc(0x100);vt=self.alloc(0x200);self.put(obj,vt);self.put(vt+0xbc,self.STOP+0x100)
        self.created.append({'object':self.arg(),'seconds':self.arg(1),'ptr':obj});self.ret(value=obj)
    def bind_object(self):
        self.created[-1].update(key=self.arg(1),x=self.arg(2),y=self.arg(3));self.ret(16,1)
    def scene_add(self):
        self.created[-1]['event']=self.arg(1);self.created[-1]['scene_object']=self.arg(3);self.ret(32,1)
    def position_object(self):self.created[-1]['position']=[self.arg(),self.arg(1),self.arg(2)];self.ret(12)
    def packet(self,va,body):
        self.u.mem_write(self.PACKET,body);self.put(0x57f9ad0,self.PACKET,len(body),0,0)
        self.run(va)
        assert self.read(0x57f9ad4)==0
    def snapshot(self,record):
        body=bytearray(1+0x369a);body[0]=1;body[1+5:1+5+23]=record
        self.packet(0x2038b00,bytes(body));assert bytes(self.u.mem_read(self.DATA+5,23))==record
        assert self.read(self.GUILD+8)==2
        self.run(0x106ecb0)
        assert self.read(self.GUILD+8)==1
    def notice(self,record,action=4):
        self.packet(0x1068760,struct.pack('<4I',action,15,0,0)+record)
        assert bytes(self.u.mem_read(self.DATA+5,23))==record
        self.run(0x106ecb0)

class PlacementTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path=os.environ.get('GUILD_PLACEMENT_V177_EXE')
        if not path: raise unittest.SkipTest('set GUILD_PLACEMENT_V177_EXE')
        cls.source=Path(path); cls.before=cls.source.read_bytes()
        cls.after,cls.receipt=patch.build(cls.before)

    def test_sender_prefix_and_fields(self):
        for data,fixed in ((self.before,False),(self.after,True)):
            lab=PlacementLab(data);lab.geometry([(750,210,500,160),(290,370,900,450)])
            lab.footprint(3,3,[1]*9)
            for poison in (0xa5,0x5a):
                lab.u.mem_write(lab.STACK-0x400,bytes([poison])*0x400)
                body=bytes.fromhex(lab.send_book()[-1]['body'])
                self.assertEqual(len(body),89)
                self.assertEqual(body[:13]==bytes(13),fixed)
                self.assertEqual(struct.unpack_from('<10I',body,13),(10156101,42,902,276,0,3,3,9,0,15))
                self.assertEqual(struct.unpack_from('<9I',body,53),(1,)*9)

    def test_native_geometry(self):
        lab=PlacementLab(self.after)
        self.assertEqual(lab.geometry([(750,210,500,160),(290,370,900,450)]),(290,210,960,610))
        self.assertEqual(len(lab.cells),1011)
        digest=hashlib.sha256(b''.join(struct.pack('<ii',*k) for k in sorted(lab.cells))).hexdigest()
        self.assertEqual(digest,'122819b35928904f32990f3e1c45ba9efdafbba026be7818e9b8576b5b304817')

    def test_snapshot_and_delta_scene_creation(self):
        for data in (self.before,self.after):
            lab=ProjectionLab(data)
            record=struct.pack('<IIIBHhhI',1,71021,29,0,0,902,276,lab.now+180)
            lab.snapshot(record)
            row=lab.created[-1]
            self.assertEqual((row['object'],row['key'],row['seconds'],row['event'],row['position']),(71021,1,180,33,[902,276,0]))
            delta=ProjectionLab(data)
            delta.notice(record,1);delta.notice(record,4)
            self.assertEqual(delta.read(delta.DATA+5+23+4),0)
            self.assertEqual(delta.read(delta.GUILD+0x36cc),delta.read(delta.GUILD+0x36c8))

    def test_exact_gate_delta(self):
        for cls in (0,1,2,255):
            old=native(self.before,(cls,),range(65536));new=native(self.after,(cls,),range(65536))
            changes=[i for i,(x,y) in enumerate(zip(old,new)) if x!=y]
            self.assertEqual(changes,[777] if cls==0 else [837] if cls==1 else [])
            for i in changes:self.assertEqual((old[i],new[i]),(0,1))

    def test_ack_dispatch(self):
        for data,fixed in ((self.before,False),(self.after,True)):
            for body in (b'\1',b'\0\3'):
                lab=PlacementLab(data);lab.put(0x2000c000,0x1066750,0)
                calls=[]
                def hook(_u,at,size,ctx):
                    if at==0x1066750:calls.append([lab.arg(),lab.arg(1)&255,lab.arg(2)&255])
                from unicorn import UC_HOOK_CODE
                lab.u.hook_add(UC_HOOK_CODE,hook)
                wire=bytearray(16)+body;wire[0]=1
                struct.pack_into('<H',wire,1,837);struct.pack_into('<I',wire,3,len(wire))
                lab.u.mem_write(lab.PACKET,bytes(wire))
                lab.run(0x25239a0,(0,lab.PACKET,0),ecx=lab.NETWORK)
                self.assertEqual(len(calls),int(fixed))
                if fixed:
                    self.assertEqual(calls[0],[837,body[0],body[1] if len(body)>1 else 0])
                    self.assertEqual(lab.read(0x57f9ad4),0)

    def test_production_packets(self):
        folder=os.environ.get('GUILD_PLACEMENT_NATIVE_DIR')
        if not folder:self.skipTest('set GUILD_PLACEMENT_NATIVE_DIR')
        folder=Path(folder);full=(folder/'reconnect-191.bin').read_bytes()
        notice=(folder/'notice-777.bin').read_bytes()
        for data in (self.before,self.after):
            lab=ProjectionLab(data);lab.now=struct.unpack_from('<I',full,25)[0]-180
            lab.receive(full[1+0x2e05:]);lab.packet(0x2038b00,full);lab.run(0x106ecb0)
            self.assertEqual(lab.created[-1]['object'],71021)
            delta=ProjectionLab(data);delta.now=lab.now
            delta.packet(0x1068760,notice);delta.run(0x106ecb0)
            self.assertEqual(delta.created[-1]['seconds'],180)
            self.assertEqual(delta.receive(full[1+0x2e05:]),[])
            lab.packet(0x2038b00,(folder/'cleared-191.bin').read_bytes())
            self.assertEqual(bytes(lab.u.mem_read(lab.DATA+5,8*64*23)),bytes(8*64*23))

    def test_patch_boundary_and_image(self):
        end=0
        for lo,hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[end:lo],self.after[end:lo]);end=hi
        self.assertEqual(self.before[end:],self.after[end:])
        opt,_=patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I',self.after,opt+64)[0],patch.pe.checksum(self.after,opt+64))
        for invalid in (self.after,self.before[:-1],b'X'+self.before[1:]):
            with self.assertRaises(ValueError):patch.build(invalid)
        with self.assertRaises(ValueError):patch.write_candidate(self.source,self.source)
        with tempfile.TemporaryDirectory() as tmp:
            dest=Path(tmp)/'DNF.exe';patch.write_candidate(self.source,dest)
            self.assertEqual(dest.read_bytes(),self.after)
            with self.assertRaises(ValueError):patch.write_candidate(self.source,dest)
            if os.name=='nt':windows_image(dest)

if __name__=='__main__':unittest.main()
