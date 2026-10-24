"""Native default-face dispatch/ownership verification with explicit renderer fixtures.

Executes bytes read from the supplied candidate. Resource acquisition, clipping
and the final GPU leaf are simulated; this is not a full game-render acceptance.
"""
import hashlib
import struct
U = X = None
DISPATCH,LEAF,MOVE,DRAW=0x29674e0,0x2966c60,0xc3b0a0,0x37f3f60
ACTOR,RESOURCE,CONTROL,FRAME,FCTRL,CTX,RM,RVT,NAME=range(0x30000000,0x30009000,0x1000)
STACK,STOP,FACTORY=0x31008000,0x33000000,0x33001000

stubs={STOP:b'\x90',FACTORY:bytes.fromhex('c20800'),0x37de240:b'\xc3',
       0x38080c0:bytes.fromhex('c20800'),0x288e930:bytes.fromhex('c20400'),
       0x25158c0:b'\xc3',0x2515920:b'\xc3',0x387f170:b'\xc3',
       0x12ea510:b'\xc3',0x37f3580:bytes.fromhex('c23000')}

class Case:
    def __init__(self,career,cold,blocked):
        self.uc=U.Uc(U.UC_ARCH_X86,U.UC_MODE_32);self.pages=set();self.events=[];self.trace=[]
        self.career,self.cold,self.blocked=career,cold,blocked
        for va,b in {**native,**stubs}.items():self.put(va,b)
        for va in [0,ACTOR,RESOURCE,CONTROL,FRAME,FCTRL,CTX,RM,RVT,NAME,0x581a000,0x57b7000,0x55b3000,0x566b000,0x45c2000]:self.put(va,bytes(4096))
        self.put(STACK-0x4000,bytes(0x5000))
        self.w(ACTOR+0x34,career);self.w(ACTOR+0x4f4,0)
        self.w(0x581a298,CTX);self.w(0x581a3e4,RM);self.w(RM,RVT);self.w(RVT+8,FACTORY)
        self.w(CONTROL+4,1);self.w(CONTROL+8,1);self.w(FCTRL+4,5);self.w(FCTRL+8,1)
        self.w(0x566b114,CTX);self.w(0x55b3bdc,0x12345678);self.w(0x45c2f00,0x3f800000)
        self.put(NAME,'character/defaultfaces.img\0'.encode('utf-16-le'))
        if not cold:self.w(0x57b7208,RESOURCE);self.w(0x57b720c,CONTROL)
        self.uc.hook_add(U.UC_HOOK_CODE,self.hook)
    def put(self,va,b):
        for page in range(va&~4095,(va+len(b)+4095)&~4095,4096):
            if page not in self.pages:self.uc.mem_map(page,4096);self.pages.add(page)
        self.uc.mem_write(va,b)
    def w(self,va,n):self.uc.mem_write(va,struct.pack('<I',n))
    def r(self,va):return struct.unpack('<I',self.uc.mem_read(va,4))[0]
    def reg(self,n):return self.uc.reg_read(getattr(X,'UC_X86_REG_'+n))
    def eax(self,n):self.uc.reg_write(X.UC_X86_REG_EAX,n)
    def hook(self,uc,address,size,_):
        self.trace.append(address)
        if address==STOP:uc.emu_stop();return
        assert any(a<=address<a+len(b) for a,b in {**native,**stubs}.items()),hex(address)
        sp=self.reg('ESP')
        if address==0x37de240:
            assert self.r(sp+4)==0x48e92c4;self.eax(NAME);self.events.append('decode-resource-name')
        elif address==FACTORY:
            assert self.reg('ECX')==RM and self.r(sp+8)==NAME
            dest=self.r(sp+4);self.w(dest,RESOURCE);self.w(dest+4,CONTROL);self.eax(dest);self.events.append('resource-provider')
        elif address==0x38080c0:
            assert self.reg('ECX')==RESOURCE and self.r(sp+8)==self.career
            dest=self.r(sp+4);self.put(dest,struct.pack('<IIfff',FRAME,FCTRL,0.,0.,0.))
            self.w(FCTRL+4,self.r(FCTRL+4)+1);self.eax(dest);self.events.append('frame-acquired')
        elif address==0x288e930:self.events.append('clip-'+str(self.r(sp+4)))
        elif address==0x25158c0:assert [self.r(sp+4*i) for i in range(1,5)]==[120,220,134,234]
        elif address==0x387f170:self.eax(CTX)
        elif address==0x12ea510:self.eax(int(self.blocked))
        elif address==0x37f3580:
            assert self.reg('ECX')==CTX and [self.r(sp+4*i) for i in range(1,4)]==[115,215,FRAME]
            self.events.append('gpu-boundary')
    def run(self,repeats=2):
        actor_before=bytes(self.uc.mem_read(ACTOR,4096));initial_regs={'EBX':0x12345678,'ESI':0x23456789,'EDI':0x3456789a,'EBP':0x456789ab}
        for _ in range(repeats):
            self.put(STACK,struct.pack('<8I',STOP,120,220,14,14,0,0,255))
            for name,value in {**initial_regs,'ECX':ACTOR,'ESP':STACK}.items():self.uc.reg_write(getattr(X,'UC_X86_REG_'+name),value)
            seh=self.r(0)
            self.uc.emu_start(DISPATCH,0,timeout=1000000,count=8192)
            assert self.reg('EIP')==STOP and self.reg('ESP')==STACK+32
            assert all(self.reg(k)==v for k,v in initial_regs.items())
            assert self.r(0)==seh and self.r(FCTRL+4)==5 and self.r(CONTROL+4)==1
            assert bytes(self.uc.mem_read(ACTOR,4096))==actor_before
            assert not self.uc.query(U.UC_QUERY_TIMEOUT)
        assert self.events.count('resource-provider')==int(self.cold)
        assert self.events.count('frame-acquired')==repeats
        assert self.events.count('gpu-boundary')==(0 if self.blocked else repeats)
        assert self.events.count('clip-1')==self.events.count('clip-0')==repeats
        assert self.r(0x57b7208)==RESOURCE and self.r(0x57b720c)==CONTROL
        return dict(career=self.career,cold=self.cold,renderBlocked=self.blocked,repeats=repeats,passed=True,instructions=len(self.trace))

WINDOWS={43414752: (195, '6d49c68a8bcadba7830653401ed26bb682c2a37851c39d35eedf05f590a08de4'), 43412576: (400, 'f0b16729c30c34af11b9a30638d848175821f82e09cdb07e3547d2b025b7c247'), 12824736: (176, '4b3bf49161c02dee4a8c12184080da7ab5b44c48dc4f81fd97ccdc56eaee1932'), 58670944: (336, '72c7bf97ebbaee25dafc97a27319342a303a70721a8d0ae75a2c6657b6b1d26a')}

def verify_native_render(read):
    global U, X, native
    import unicorn as U
    from unicorn import x86_const as X
    native={address:read(address,size) for address,(size,_) in WINDOWS.items()}
    for address,(_,digest) in WINDOWS.items():
        if hashlib.sha256(native[address]).hexdigest()!=digest:
            raise ValueError("native renderer window changed: "+hex(address))
    cases=[Case(career,cold,blocked).run() for career in range(16)
           for cold in (False,True) for blocked in (False,True)]
    return dict(passed=len(cases),failed=0,cases=cases,guiAcceptance=False,
                stubs="resource name decoder, resource provider, frame acquisition, clip state, renderer availability and final GPU draw",
                executed="portrait dispatcher, default-face leaf, shared ownership move and draw dispatch cleanup")
