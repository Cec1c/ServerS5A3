"""Isolated native title fixture; explicit UI/resource adapters, no game process."""
import struct
import hashlib
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_UNMAPPED
from unicorn.x86_const import *
STACK=0x10000000;FRAME=0x11000000;STOP=0x12000000
WRITER=0x13000000;TEB=0x15000000;GDT=0x15100000;SETUP=0x16000000
PRODUCER=0x25F9A70;BOUNDARY=0x25FA98D;CONTINUE=0x25FA994;AFTER_WRITER=0x25FA99B
REGS={'ebx':UC_X86_REG_EBX,'ecx':UC_X86_REG_ECX,'edx':UC_X86_REG_EDX,'esi':UC_X86_REG_ESI,'edi':UC_X86_REG_EDI,'flags':UC_X86_REG_EFLAGS}

class Image:
 def __init__(self,data):
  self.data=data;self.sha=hashlib.sha256(self.data).hexdigest()
  p=pefile.PE(data=self.data,fast_load=True);self.base=p.OPTIONAL_HEADER.ImageBase;self.image_size=p.OPTIONAL_HEADER.SizeOfImage
  self.sections=[dict(va=self.base+s.VirtualAddress,rs=s.SizeOfRawData,offset=s.PointerToRawData) for s in p.sections]
 def at(self,a,l):
  s=next(s for s in self.sections if s['va']<=a and a+l<=s['va']+s['rs']);off=s['offset']+a-s['va'];return self.data[off:off+l]
 def page(self,a):
  b=bytearray(4096)
  for s in self.sections:
   lo=max(a,s['va']);hi=min(a+4096,s['va']+s['rs'])
   if lo<hi:b[lo-a:hi-a]=self.at(lo,hi-lo)
  return bytes(b)

class Machine:
    def __init__(self,image):
        self.image=image;self.u=Uc(UC_ARCH_X86,UC_MODE_32);self.pages=set()
        self.trace=[];self.adapters=[];self.producers=[];self.tape=[];self.stopped=None;self.mode='setup'
        self.handler=None;self.return_producer=False;self.path=[];self.boundary=None
        for at,size in [(STACK,0x20000),(FRAME,0x40000),(STOP,0x1000),(WRITER,0x2be000),(TEB,0x1000),(GDT,0x1000),(SETUP,0x1000)]:
            self.u.mem_map(at,size);self.pages.update(range(at,at+size,4096))
        def missing(cpu,access,at,size,value,ctx):
            if self.image.base <= at and at+size <= self.image.base+self.image.image_size:
                self.page(at);self.page(at+size-1);return True
            self.fault=dict(pc=hex(self.u.reg_read(UC_X86_REG_EIP)),address=hex(at),size=size,access=access)
            return False
        self.u.hook_add(UC_HOOK_MEM_UNMAPPED,missing)
        self.fault=None
        self.put(TEB,0xffffffff,STACK+0x20000,STACK)
        descriptor=struct.pack('<HHBBBB',0xffff,TEB&0xffff,(TEB>>16)&255,0x92,0,(TEB>>24)&255)
        self.u.mem_write(GDT+8,descriptor)
        # Loading a GDT/FS alone leaves Unicorn's default SS with a 16-bit
        # stack. Use explicit flat 32-bit data/code descriptors as on Win32.
        self.u.mem_write(GDT+16,struct.pack('<HHBBBB',0xffff,0,0,0x92,0xcf,0))
        self.u.mem_write(GDT+24,struct.pack('<HHBBBB',0xffff,0,0,0x9a,0xcf,0))
        self.u.reg_write(UC_X86_REG_GDTR,(0,GDT,31,0))
        self.u.reg_write(UC_X86_REG_CS,24)
        setup=bytes.fromhex('66b810008ed88ec08ed066b808008ee064a104000000648b1508000000')
        self.u.mem_write(SETUP,setup)
        self.u.reg_write(UC_X86_REG_ESP,STACK+0x10000)
        self.u.emu_start(SETUP,SETUP+len(setup),count=16,timeout=1_000_000)
        assert self.u.reg_read(UC_X86_REG_EIP)==SETUP+len(setup)
        assert self.u.reg_read(UC_X86_REG_FS)==8
        assert self.u.reg_read(UC_X86_REG_SS)==16 and self.u.reg_read(UC_X86_REG_CS)==24
        assert self.u.reg_read(UC_X86_REG_EAX)==STACK+0x20000 and self.u.reg_read(UC_X86_REG_EDX)==STACK
        self.fs=dict(selector=8,ss=16,cs=24,stackBase=hex(STACK+0x20000),stackLimit=hex(STACK),testedByActualInstructions=True)
        self.put(0x57ED8D4,WRITER);self.u.mem_write(WRITER+8,b'\1');self.put(WRITER+0x2BCC2C,0)
        self.u.hook_add(UC_HOOK_CODE,self.code)

    def page(self,at):
        base=at&~4095
        if base not in self.pages:
            data=self.image.page(base);self.u.mem_map(base,4096);self.pages.add(base);self.u.mem_write(base,data)
    def put(self,at,*values):
        for i,v in enumerate(values):
            self.page(at+i*4);self.page(at+i*4+3);self.u.mem_write(at+i*4,struct.pack('<I',v&0xffffffff))
    def read(self,at):
        self.page(at);self.page(at+3);return struct.unpack('<I',self.u.mem_read(at,4))[0]
    def args(self,at):return [struct.unpack('<i',self.u.mem_read(at+4*i,4))[0] for i in range(5)]
    def ret(self,extra=0,value=0):
        sp=self.u.reg_read(UC_X86_REG_ESP);dest=self.read(sp)
        self.u.reg_write(UC_X86_REG_EAX,value&0xffffffff);self.u.reg_write(UC_X86_REG_ESP,sp+4+extra);self.u.reg_write(UC_X86_REG_EIP,dest)
    def stop(self,reason):self.stopped=reason;self.u.emu_stop()
    def registers(self):return {k:self.u.reg_read(v) for k,v in REGS.items()}
    def code(self,cpu,at,size,ctx):
        self.trace.append(hex(at));self.trace=self.trace[-32:]
        if at==STOP:self.stop('function-return');return
        if self.handler and self.handler(at):return
        if at==PRODUCER:
            sp=cpu.reg_read(UC_X86_REG_ESP);parent=cpu.reg_read(UC_X86_REG_EBP)
            grand=self.read(parent)
            self.producers.append(dict(returnAddress=hex(self.read(sp)),args=self.args(sp+4),parentReturn=hex(self.read(parent+4)),grandReturn=hex(self.read(grand+4)) if STACK<=grand<STACK+0x20000 else None))
        elif at==PRODUCER+3:
            frame=cpu.reg_read(UC_X86_REG_EBP)
            assert self.read(frame+4)==int(self.producers[-1]['returnAddress'],16)
            self.adapters.append('producer-after-prologue-to-marker: full op19 serialization not exercised')
            cpu.reg_write(UC_X86_REG_EIP,BOUNDARY)
        elif at==BOUNDARY:
            self.boundary=dict(esp=cpu.reg_read(UC_X86_REG_ESP),ebp=cpu.reg_read(UC_X86_REG_EBP),registers=self.registers())
            self.path.append('marker-boundary')
        elif at in (0x6E952E8,0x6E95000):self.path.append(hex(at))
        elif at==CONTINUE:
            assert self.boundary is not None
            assert cpu.reg_read(UC_X86_REG_ESP)==self.boundary['esp']-4
            assert self.registers()==self.boundary['registers'],(self.registers(),self.boundary)
            self.path.append('marker-continuation')
        elif at==0x38C7F70:
            sp=cpu.reg_read(UC_X86_REG_ESP);ptr=self.read(sp+4);size=self.read(sp+8)
            assert size==1;self.tape.extend(self.u.mem_read(ptr,size));self.adapters.append('terminal append captured');self.ret(8)
        elif at==AFTER_WRITER:
            assert cpu.reg_read(UC_X86_REG_ESP)==self.boundary['esp']
            self.path.append('writer-return')
            if self.return_producer:
                frame=cpu.reg_read(UC_X86_REG_EBP);saved=self.read(frame);dest=self.read(frame+4)
                cpu.reg_write(UC_X86_REG_EBP,saved);cpu.reg_write(UC_X86_REG_ESP,frame+28);cpu.reg_write(UC_X86_REG_EIP,dest)
                self.adapters.append('producer-after-marker-to-real-caller: remaining serialization/epilogue adapted')
            else:self.stop('marker-captured')
    def run(self,start,ecx=0,args=(),setup=True):
        self.stopped=None;self.fault=None;self.trace=[]
        if setup:
            self.put(STACK+0x18000,STOP,*args)
            self.u.reg_write(UC_X86_REG_ESP,STACK+0x18000);self.u.reg_write(UC_X86_REG_EBP,STACK+0x1f000)
            self.put(STACK+0x1f000,0,STOP)
            self.u.reg_write(UC_X86_REG_ECX,ecx);self.u.reg_write(UC_X86_REG_EFLAGS,0x246)
        try:self.u.emu_start(start,STOP+1,count=100000,timeout=3_000_000)
        except BaseException as error:raise AssertionError(dict(error=repr(error),lastInstructions=self.trace,fault=self.fault,adapters=self.adapters,producers=self.producers)) from error
        assert self.stopped is not None,dict(error='instruction/time budget',lastInstructions=self.trace)
    def evidence(self):return dict(fs=self.fs,tape=self.tape,producers=self.producers,path=self.path,adapters=self.adapters,stopped=self.stopped,virtualMappedPages=len(self.pages))

Base=Machine
def boundary_case(image,ret=0x249C880,parent=0x24A6B0F,grand=0x32C76FD,slot=13,qty=1,arg4=6,source=40,parent_outside_stack=False,parent_unaligned=False):
    m=Base(image);p=STACK+0x1c000;g=STACK+0x1d000;sp=STACK+0x18000
    if parent_outside_stack:p=FRAME+0x2000
    if parent_unaligned:p+=2
    m.put(p,g,parent);m.put(g,0,grand);m.put(sp,source,slot,qty,arg4,0)
    m.u.reg_write(UC_X86_REG_EBP,p);m.u.reg_write(UC_X86_REG_ESP,sp)
    m.u.reg_write(UC_X86_REG_EFLAGS,0x246)
    assert image.at(ret-5,5)==b'\xe8'+struct.pack('<i',PRODUCER-ret)
    m.run(ret-5,setup=False)
    assert m.producers[0]['args']==[source,slot,qty,arg4,0]
    return m.evidence()

class Full(Base):
 mutation=None
 def __init__(self,image):
  super().__init__(image);self.serial=[];self.opcode=None;self.resolver=[]
  self.title=FRAME+0x1000;self.info=FRAME+0x1800;self.group=FRAME+0x38000
  self.put(self.title,FRAME+0x1400);self.put(FRAME+0x1428,SETUP+0x300);self.put(FRAME+0x1554,SETUP+0x310)
  self.put(FRAME+0x1a00,13);self.put(self.info+0x18,400330094)
  self.put(self.group,0,FRAME+0x39000,FRAME+0x39000)
 def code(self,cpu,at,size,ctx):
  sp=cpu.reg_read(UC_X86_REG_ESP);bp=cpu.reg_read(UC_X86_REG_EBP)
  if at==BOUNDARY and self.mutation:
   self.mutation(self,bp)
  if at==PRODUCER+3:return # execute actual SEH/local-frame prologue
  if at==0x25f9aa4:
   self.put(bp-0x14,FRAME+0x20000)
   cpu.reg_write(UC_X86_REG_ESI,0);cpu.reg_write(UC_X86_REG_EDI,self.title)
   self.adapters.append('inventory validation/resource leaves adapted before title-specific branch; exact arguments/ancestry retained')
   cpu.reg_write(UC_X86_REG_EIP,0x25fa236);return
  if at==SETUP+0x300:self.ret(value=self.info);return
  if at==SETUP+0x310:self.ret(value=FRAME+0x1a00);return
  if at==0x3c618a0:
   self.resolver.append(self.read(sp+8));self.ret(value=self.group);return
  if at==0x2531a80:self.ret(value=FRAME+0x32000);return
  if at in (0x11f6aa0,0x116acf0):
   ptr=cpu.reg_read(UC_X86_REG_ECX);src=self.read(sp+4)
   self.put(ptr+4,self.read(src if at==0x11f6aa0 else src+4));self.ret(4,ptr);return
  if at==0x994e30:self.ret(value=self.read(cpu.reg_read(UC_X86_REG_ECX)+4));return
  if at==0x3e4b1c0:self.ret();return
  if at==0x25e9b70:self.ret(8,0);return
  if at==0x25ea1e0:self.ret(8,1);return
  if at==0x25efc00:
   ptr=self.read(sp+4);self.put(ptr+4,0);self.ret(8,ptr);return
  if at in (0x37df550,0x37df5c0,0x37df630,0x37df6a0,0x37df7f0):
   self.put(self.read(sp+8),0);self.ret(8);return
  if at==0x37df540:self.ret();return
  if at==0x277c630:self.ret(8,self.title);return
  if at==0x255ea00:self.ret(value=0);return
  if at==0x296dd80:self.ret(value=0);return
  if at==0x374f280:self.opcode=self.read(sp+4);self.ret(4);return
  if at==0x38c7f70:
   ptr,length=self.read(sp+4),self.read(sp+8);assert length in (1,2,4)
   self.serial.extend(self.u.mem_read(ptr,length));self.ret(8);return
  if at==AFTER_WRITER:
   self.marker=self.serial[-1];self.markerArgs=self.args(bp+8);return
  if at==0x3750ac0:self.stop('full-op19-serialized-before-transport');return
  return super().code(cpu,at,size,ctx)

Machine=Full
def generic_context(image,index=6,selected_group=0,event_present=True,event_code=2,hit_setup=None,repeat_cleared=False):
    """Real registry +0x40 call -> actual selected-slot action -> full builder.

    Window registry, inventory and input-map leaves are fixture data. No parent
    or grandparent return address is manually written for this path.
    """
    m=Machine(image);window=FRAME;builder=FRAME+0x2000;vt=FRAME+0x4000
    item=FRAME+0x6000;manager=FRAME+0x10000;vector=FRAME+0x30000;bag=FRAME+0x32000
    m.put(window,0x49BD76C);m.put(window+0x1F8,selected_group)
    m.put(window+0x1DC,builder)
    m.put(manager+0x108,vector,vector+4);m.put(vector,window)
    m.put(builder,vt);m.put(vt+0x18,SETUP+0x200);m.put(vt+0x30,SETUP+0x210);m.put(vt+0x1c,SETUP+0x220)
    m.put(builder+4,FRAME+0x8000);m.put(builder+12,FRAME+0x9000)
    m.put(builder+0xE8,index);m.put(builder+0x94,item);m.put(builder+0x24,bag)
    m.put(builder+0x2C,0);m.put(builder+0x70,0);m.put(builder+0x74,4)
    m.put(0x58467B0,event_code)
    m.return_producer=True;scanned=[];dispatch=[]
    def handler(at):
        sp=m.u.reg_read(UC_X86_REG_ESP)
        if at in (0x32C76FB,0x24A6AB0,0x249C1E0,0x249C871):
            dispatch.append(hex(at));return False
        if at==0x32B5C90:
            assert [m.read(sp+4),m.read(sp+8)]==[0x23b,0]
            m.adapters.append('nonblocking dialog registry lookup');m.ret(8,0)
        elif at==0x3E73CB6:m.adapters.append('RTTI cast identity adapter');m.ret(value=m.read(sp+4))
        elif at==0x32B54B0:
            assert m.read(sp+4) in (0x2ea,0x2eb)
            m.adapters.append('modal window visibility lookup');m.ret(4,0)
        elif at==SETUP+0x200:m.adapters.append('slot blocking-state predicate');m.ret(value=0)
        elif at==0x38853B0:
            assert m.read(sp+4)==6
            m.adapters.append('input map key6 lookup');m.ret(4,index if event_present else -1)
        elif at==0x2498E00:
            assert m.read(sp+4)==item
            assert m.read(m.read(sp+8))==4 and m.read(m.read(sp+12))==0xffffffff
            m.adapters.append('item tab/classification query left at unresolved4/-1');m.ret()
        elif at==SETUP+0x210:m.adapters.append('bag page start getter');m.ret(4,40)
        elif at==SETUP+0x220:m.adapters.append('bag page size getter');m.ret(4,4)
        elif at==0x25EB680:
            slot=m.read(sp+4);scanned.append(slot)
            assert m.u.reg_read(UC_X86_REG_ECX)==bag
            m.adapters.append('bag slot lookup');m.ret(4,item if slot==40 else 0)
        elif at==0x24A6B0F:m.stop('real-builder-return-before-window-presentation')
        elif at==0x32C771E:m.stop('registry-exhausted-before-unrelated-global-actions')
        else:return False
        return True
    m.handler=handler
    if hit_setup is not None:hit_setup(m,window,builder)
    m.run(0x32C7660,manager)
    if m.producers:
        assert m.producers[0]['returnAddress']=='0x249c880'
        assert m.producers[0]['parentReturn']=='0x24a6b0f'
        assert m.producers[0]['grandReturn']=='0x32c76fd'
        assert len(m.producers)==2
        assert m.producers[0]['args']==[41,13 if index==6 else 12,1,6,0]
        assert scanned[:2]==[40,41]
        assert m.stopped=='full-op19-serialized-before-transport'
    if repeat_cleared:
        before=(len(m.producers),len(m.tape),len(scanned))
        event_present=False
        m.adapters.append('external input key6 cleared between dispatches; same cached selected object retained')
        m.run(0x32C7660,manager)
        assert (len(m.producers),len(m.tape),len(scanned))==before
    result=m.evidence();result.update(opcode=m.opcode,body=bytes(m.serial).hex(),marker=m.marker,markerArgs=m.markerArgs,resolverGroups=m.resolver);result.update(dispatch=dispatch,scannedSlots=scanned,
                                      actualAncestorCalls=True,hitTestExecuted=hit_setup is not None,
                                      selectedGroupFixture=selected_group,inputIndexFixture=index)
    return result
