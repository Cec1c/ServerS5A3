"""Execute compiled x86 paths in Unicorn; OS/window endpoints are adapters.

This is offline executable verification, not a claim of game rendering acceptance.
"""
import argparse, hashlib, json, re, struct
from pathlib import Path
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *

class Machine:
    STACK=0x10010000;S=0x20000000;STOP=0x30000000
    def __init__(self,payload,mapfile,base):
        self.u=Uc(UC_ARCH_X86,UC_MODE_32);self.pages=set();self.mocks={};self.trace=[]
        self.pe=pefile.PE(data=base.read_bytes());self.data=base.read_bytes()
        lib=pefile.PE(data=payload.read_bytes());self.put(lib.OPTIONAL_HEADER.ImageBase,lib.get_memory_mapped_image())
        self.exports={s.name.decode():lib.OPTIONAL_HEADER.ImageBase+s.address for s in lib.DIRECTORY_ENTRY_EXPORT.symbols}
        for k,v in list(self.exports.items()):
            if k.startswith('@'):self.exports[k.split('@')[1]]=v
        self.g={}
        for line in mapfile.read_text().splitlines():
            m=re.search(r'\?([^@]+)@@\S+\s+([0-9a-fA-F]{8})\s',line)
            if m:self.g[m[1]]=int(m[2],16)
        self.put(self.STACK-0x10000,b'\0'*0x20000);self.put(self.S,b'\0'*0x400000);self.put(self.STOP,b'\x90')
        self.put(0x6e8c000,self.native(0x6e8c000,256));self.put(0x5846700,bytes(256));self.write(0x57e2698,0)
        self.put(0x6575e78,bytes(4));self.write(0x56a3798,0)
        self.u.hook_add(UC_HOOK_CODE,self.hook)
        self.now=100;self.keys=set();self.iat(0x6e85a24,lambda:self.now);self.iat(0x6e866a0,lambda:0x8000 if self.arg() in self.keys else 0,4)
        self.iat(0x6e85a90,7);self.mock(self.g['readable'],1)
    def native(self,va,n):return self.pe.get_data(va-self.pe.OPTIONAL_HEADER.ImageBase,n)
    def put(self,addr,data):
        for p in range(addr&~4095,(addr+len(data)+4095)&~4095,4096):
            if p not in self.pages:self.u.mem_map(p,4096);self.pages.add(p)
        self.u.mem_write(addr,bytes(data))
    def write(self,a,v):self.put(a,struct.pack('<I',v&0xffffffff))
    def read(self,a):return struct.unpack('<I',self.u.mem_read(a,4))[0]
    def byte(self,a):return bytes(self.u.mem_read(a,1))[0]
    def arg(self,i=0):return self.read(self.u.reg_read(UC_X86_REG_ESP)+4+i*4)
    def wide(self,a):
        out=bytearray()
        for i in range(1024):
            c=bytes(self.u.mem_read(a+i*2,2))
            if c==b'\0\0':return out.decode('utf-16le')
            out.extend(c)
        raise AssertionError('unterminated string')
    def mock(self,a,f=0,pop=0):
        # End the translated block even before the hook substitutes the call;
        # an endless zero-filled page can otherwise trigger a decoder fetch.
        self.put(a,b'\xc3');self.mocks[a]=(f,pop)
    def iat(self,a,f=0,pop=0):
        target=self.S+0x3f0000+len(self.mocks)*16;self.write(a,target);self.mock(target,f,pop);return target
    def hook(self,u,a,size,_):
        self.trace=(self.trace+[hex(a)])[-12:]
        if a==self.STOP:u.emu_stop();return
        if a not in self.mocks:return
        f,pop=self.mocks[a];v=f() if callable(f) else f;sp=u.reg_read(UC_X86_REG_ESP);ret=self.read(sp)
        u.reg_write(UC_X86_REG_EAX,(v or 0)&0xffffffff);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,ret)
    def call(self,a,args=(),pop=0,ecx=0,eax=0):
        self.put(self.STACK,struct.pack('<'+'I'*(len(args)+1),self.STOP,*[v&0xffffffff for v in args]))
        self.u.reg_write(UC_X86_REG_ESP,self.STACK);self.u.reg_write(UC_X86_REG_ECX,ecx);self.u.reg_write(UC_X86_REG_EDX,0);self.u.reg_write(UC_X86_REG_EAX,eax)
        for r,v in [(UC_X86_REG_EBX,0x11111111),(UC_X86_REG_ESI,0x22222222),(UC_X86_REG_EDI,0x33333333),(UC_X86_REG_EBP,0x44444444)]:self.u.reg_write(r,v)
        try:self.u.emu_start(a,self.STOP+1,count=1000000)
        except Exception as e:raise RuntimeError(f'x86 error at {self.u.reg_read(UC_X86_REG_EIP):x}; trace {self.trace}') from e
        assert self.u.reg_read(UC_X86_REG_EIP)==self.STOP,'instruction budget exhausted'
        assert self.u.reg_read(UC_X86_REG_ESP)==self.STACK+4+pop,'stack imbalance'
        for r,v in [(UC_X86_REG_EBX,0x11111111),(UC_X86_REG_ESI,0x22222222),(UC_X86_REG_EDI,0x33333333),(UC_X86_REG_EBP,0x44444444)]:assert self.u.reg_read(r)==v,'callee saved register corrupted'
        return self.u.reg_read(UC_X86_REG_EAX)
    def config(self,slots):
        b=struct.pack('<II',0x31524b44,1)+b''.join(struct.pack('<II',*s) for s in slots+[(0,0)]*(10-len(slots)))
        self.put(self.g['saved'],b);self.put(self.g['draft'],b);self.put(self.g['loaded'],b'\1');return b
    def setup_ui(self):
        S=self.S;self.manager=S+0x10000;self.parent=S+0x30000;self.win=S+0x32000;self.root=S+0x34000
        for n,p in [('manager',self.manager),('win',self.win),('root',self.root)]:self.write(self.g[n],p)
        self.write(0x56a3798,self.manager);self.write(self.g['windowId'],1214);self.write(self.g['uiThread'],7);self.write(self.manager+0xc5b4,1)
        self.active=True;self.allocations=0;self.captions=[]
        def vector(id,active,values):
            h=self.manager+(id*5+(0x41 if active else 0x1879))*4;b=S+0x50000+id*32+(0 if active else 16)
            for i,v in enumerate(values):self.write(b+i*4,v)
            self.write(h+4,b);self.write(h+8,b+4*len(values))
        self.vector=vector
        vector(1214,True,[self.win]);vector(1214,False,[]);vector(0x187,True,[self.parent])
        def close():
            assert self.arg()==1214;vector(1214,True,[]);vector(1214,False,[self.win]);self.active=False
        def opened():
            assert self.arg()==1214;vector(1214,True,[self.win]);vector(1214,False,[]);self.active=True;return self.win
        self.mock(0x32b94a0,close,12);self.mock(0x32bea50,opened,12)
        self.mock(self.g['render']);self.mock(self.g['placePanel'])
        def format_wide():
            fmt=self.wide(self.arg(1));text=fmt%self.arg(2) if '%' in fmt else fmt
            self.put(self.arg(),(text+'\0').encode('utf-16le'));return len(text)
        self.iat(0x6e866b8,format_wide)
        self.controls={};
        for i,name in enumerate(['KR_SAVE','KR_CANCEL']+[f'KR_CHECK_{i}' for i in range(10)]+[f'KR_BIND_{i}' for i in range(10)]):self.controls[name]=S+0x80000+i*0x600
        for i in range(10):self.write(self.g['checks']+i*4,self.controls[f'KR_CHECK_{i}']);self.write(self.g['buttons']+i*4,self.controls[f'KR_BIND_{i}'])
        self.mock(self.g['control'],lambda:self.controls.get(self.wide(self.arg()),0))
        self.mock(self.g['status'],lambda:self.captions.append(self.wide(self.arg())))
        self.write(0x584678c,9);self.write(0x5846770,9)
        self.put(self.g['shown'],b'\1');return self

def verify(payload,mapfile,base):
    def make():return Machine(payload,mapfile,base)
    m=make();g=m.g;obj=m.S+0x10000;m.put(obj,bytes(0x1400));m.put(obj+4,b'\1');m.put(obj+12,b'\1');m.write(0x584678c,9);m.write(0x5846770,9)
    m.mock(0x29610e0,m.S+0x8000);m.put(0x3840a30,m.native(0x3840a30,0x24));m.config([(65,1),(66,1),(0x25,1)])
    table=m.native(0x6e8c000,256);aliases=lambda vk:[i for i,k in enumerate(table) if k==vk]
    assert all(aliases(k) for k in (65,66,0x25));m.keys={65,66,0x25}
    def sample(time,result=0):
        m.now=time;m.put(obj+0x10d,b'\x35'*256);before=bytes(m.u.mem_read(obj+0x20d,256))
        m.call(m.exports['InputGate'],ecx=obj,eax=result)
        assert bytes(m.u.mem_read(obj+0x20d,256))==before
        return bytes(m.u.mem_read(obj+0x10d,256))
    for t,on in [(100,1),(101,0),(179,0),(180,1),(181,0),(260,1)]:
        b=sample(t)
        for k in (65,66,0x25):assert all(b[s]==(128 if on else 0) for s in aliases(k))
        assert all(b[s]==0x35 for s,k in enumerate(table) if k not in (65,66,0x25))
    m.keys.remove(65);b=sample(261);assert all(b[s]==0 for s in aliases(65))
    gates=[('HRESULT',lambda:m.put(obj+4,b'\1'),0x80000001),('device-disabled',lambda:m.put(obj+4,b'\0'),0),
           ('native-input-disabled',lambda:m.put(obj+12,b'\0'),0),('exclusive-text-input',lambda:m.write(obj+0x1310,1),0),
           ('background',lambda:m.write(0x5846770,8),0),('settings-open',lambda:m.put(g['shown'],b'\1'),0)]
    for name,change,result in gates:
        m.put(obj+4,b'\1');m.put(obj+12,b'\1');m.write(obj+0x1310,0);m.write(0x5846770,9);m.put(g['shown'],b'\0');change()
        b=sample(300,result);assert b==b'\x35'*256,name+' must pass through inactive input'
    m.put(g['shown'],b'\0');m.write(0x5846770,9);m.write(obj+0x1310,0);m.put(obj+12,b'\1')
    b=sample(400);assert all(b[s]==0 for k in m.keys for s in aliases(k))
    m.keys=set();sample(401);m.keys={66};b=sample(402);assert b[aliases(66)[0]]==128
    # Native postprocessor suppression counters still win over a repeat pulse.
    sample(403);s=aliases(66)[0];m.write(obj+0xf10+4*s,2);b=sample(482);assert b[s]==0 and m.read(obj+0xf10+4*s)==1
    # Run the existing IME/direction-release loop and the new hook together.
    m.put(0x6e8c200,m.native(0x6e8c200,0x7d));m.put(0x6eb2000,m.native(0x6eb2000,0x6b));m.put(0x65d1d70,bytes(256));m.write(0x6e8c100,0)
    target=m.exports['InputGate'];m.put(0x6e8c273,b'\xe8'+struct.pack('<i',target-0x6e8c278));m.put(0x6e8c278,b'\xc3')
    async_target=m.read(0x6e866a0);m.write(0x4588548,async_target)
    m.config([(66,1)]);m.put(0x65d1d70+0x25,b'\1');m.keys={66};m.put(obj+0x10d,b'\x35'*256)
    m.call(0x6e8c200,ecx=obj);assert not m.byte(0x65d1d70+0x25)
    assert all(m.byte(obj+0x10d+s)==0 for s in aliases(0x25))
    # Actual ownership traversal, panel click/record/cancel paths; render and
    # window endpoints are adapters, not assertions about graphics output.
    m=make().setup_ui();g=m.g;initial=m.config([(65,1)])
    def event(name,kind=13,v=0):return m.call(g['PanelEvent'],(m.controls[name],kind,v,0),16)
    event('KR_BIND_1');assert m.read(g['capture'])==1
    m.call(g['captureTick']);m.keys={66};m.call(g['captureTick']);assert m.read(g['capture'])==0xffffffff and m.read(g['draft']+16)==66
    event('KR_CHECK_1',23,1);assert m.read(g['draft']+20)==1
    event('KR_BIND_2');m.keys=set();m.call(g['captureTick']);m.keys={66};m.call(g['captureTick']);assert m.read(g['capture'])==2
    assert any('槽位 02' in s for s in m.captions)
    m.keys=set();m.call(g['captureTick']);m.keys={27};m.call(g['captureTick']);assert m.read(g['capture'])==0xffffffff and m.active
    event('KR_BIND_1');m.keys=set();m.call(g['captureTick']);m.keys={46};m.call(g['captureTick']);assert m.read(g['draft']+16)==0 and m.read(g['draft']+20)==0
    event('KR_BIND_2');m.write(0x5846770,8);m.call(g['captureTick']);assert m.read(g['capture'])==0xffffffff
    m.write(0x5846770,9);m.keys=set();event('KR_CANCEL');assert not m.active and bytes(m.u.mem_read(g['saved'],88))==initial
    # Close/reopen reuses the existing native cached object, never another slot.
    for i in range(40):
        m.call(g['openPanel']);assert m.active and m.read(g['win'])==m.win
        event('KR_CANCEL');assert not m.active
    # Commit is applied only after write AND readback success. Failed storage
    # leaves the draft open and the active repeat configuration untouched.
    m.call(g['openPanel']);m.put(g['configPath'],'C:\\fixture.ini\0'.encode('utf-16le'));m.write(g['draft']+8,67)
    store={};ok={'write':False,'read':True}
    def write_config():
        if ok['write']:store['data']=bytes(m.u.mem_read(m.arg(2),m.arg(3)))
        return ok['write']
    def read_config():
        if ok['read'] and 'data' in store:m.put(m.arg(2),store['data']);return 1
        return 0
    m.iat(0x6e85b58,write_config,20);m.iat(0x6e85b60,read_config,20)
    event('KR_SAVE');assert m.active and bytes(m.u.mem_read(g['saved'],88))==initial
    ok['write']=True;ok['read']=False;event('KR_SAVE');assert m.active and bytes(m.u.mem_read(g['saved'],88))==initial
    ok['read']=True;event('KR_SAVE');assert not m.active and m.read(g['saved']+8)==67
    # Unrelated ESC menu callbacks preserve exact native thiscall ABI.
    calls=[];m.mock(0x1228760,lambda:calls.append(('event',m.arg(),m.arg(1))) or 123,16)
    m.mock(0x1229c20,lambda:calls.append(('update',)))
    m.mock(0x1226c30,lambda:calls.append(('show',m.arg(),m.arg(1))) or 1,8)
    assert m.call(m.exports['MenuEvent'],(15,23,1,0),16,ecx=m.parent+0x1b0)==123
    m.call(m.exports['MenuUpdate'],ecx=m.parent)
    assert m.call(m.exports['MenuShow'],(11,22),8,ecx=m.parent)&255==1
    assert calls==[('event',15,23),('update',),('show',11,22)]
    # Bind the actual compiled window adapter and check native tile sizing.
    # Execute the original native width/height virtuals, not a pixel guess.
    m=make().setup_ui();g=m.g;S=m.S;props=S+0xb0000;title=S+0xb1000;close=S+0xb2000;parentroot=S+0xb3000;vt=S+0xb4000
    def format_wide():
        text=m.wide(m.arg(1)).replace('%u',str(m.arg(2)));m.put(m.arg(),(text+'\0').encode('utf-16le'));return len(text)
    m.iat(0x6e866b8,format_wide)
    m.put(0x49f03f0,m.native(0x49f03f0,516));m.write(m.win,0x49f03f4);m.write(m.win+0x1b0,0x49f03ec);m.write(m.win+0x30,m.root)
    m.write(m.win+4,props);m.write(m.win+8,S+0xb5000);m.write(m.win+0x48,title);m.write(m.win+0x50,close)
    m.write(title,vt);m.write(close,vt);m.write(vt+8,S+0xb6000);m.mock(S+0xb6000,0,4);m.mock(0x3826f40,0,4)
    for i,name in enumerate(['KR_STATUS']+[f'KR_KEY_{i}' for i in range(10)]):
        p=S+0xc0000+i*0x500;m.controls[name]=p;m.write(p,0x4d4fa4c)
    styles=[]
    def size_adapter():
        styles.append(m.arg());m.write(props+0x40,m.arg());m.write(m.root+0x6c,m.read(props+0x24)*m.read(props+0x44));m.write(m.root+0x70,m.read(props+0x28)*m.read(props+0x48))
    m.mock(0x335f0d0,size_adapter,4)
    assert m.call(g['bind'])&0xff==1 and styles==[0]  # MSVC bool return is AL.
    m.put(0x335a7b0,m.native(0x335a7b0,0x60))
    assert m.call(0x335a7b0,ecx=m.win)==456 and m.call(0x335a7e0,ecx=m.win)==338
    lib=pefile.PE(data=payload.read_bytes());m.mocks.pop(g['placePanel']);m.put(g['placePanel'],lib.get_data(g['placePanel']-lib.OPTIONAL_HEADER.ImageBase,1))
    m.write(m.parent+0x30,parentroot);m.write(parentroot+0x6c,635);m.write(parentroot+0x70,467)
    def xy():
        p=m.u.reg_read(UC_X86_REG_ECX);m.write(m.arg(),m.read(p+0x7c));m.write(m.arg()+4,m.read(p+0x80));return m.arg()
    def position():
        p=m.u.reg_read(UC_X86_REG_ECX);m.write(p+0x7c,m.arg());m.write(p+0x80,m.arg(1))
    m.mock(0x37eb710,xy,4);m.mock(0x37ecc40,position,8)
    dockcases=0
    for width,height in [(800,600),(1024,768),(1280,720),(1600,900)]:
        m.write(0x55b0318,width);m.write(0x55b031c,height);m.put(g['placed'],b'\0')
        m.call(g['placePanel'])
        assert m.read(m.root+0x7c)==(width-456)//2
        assert m.read(m.root+0x80)==(height-374)//2+25
        # Native drag coordinates remain unchanged at fixed resolution.
        m.write(m.root+0x7c,120);m.write(m.root+0x80,105);m.call(g['placePanel'])
        assert m.read(m.root+0x7c)==120 and m.read(m.root+0x80)==105
        # Resolution change clamps the actual dragged position, not re-centers.
        m.write(m.root+0x7c,width-100);m.write(m.root+0x80,height-30)
        m.write(0x55b0318,800);m.write(0x55b031c,600);m.write(g['screenWidth'],0)
        m.call(g['placePanel']);x=m.read(m.root+0x7c);y=m.read(m.root+0x80)
        assert x>=12 and x+456+12<=800 and y>=25 and y+338+11<=600
        dockcases+=3
    assert m.read(m.win)==g['panelVtable']+4
    assert m.read(m.read(m.win)+16)==g['PanelUpdate']
    assert bytes(m.u.mem_read(g['panelVtable'],4))==m.native(0x49f03f0,4)
    # Real load() path, with temporary data supplied only by an in-memory OS
    # adapter. Check Unicode path assembly and malformed persisted config.
    m=make();g=m.g;api=m.S+0x3e0000;folders=[];persist=initial
    def env():
        assert m.wide(m.arg())=='LOCALAPPDATA';s='C:\\Users\\测试\\AppData\\Local'
        m.put(m.arg(1),(s+'\0').encode('utf-16le'));return len(s)
    m.mock(api,env,12);m.iat(0x6e85ab0,1,4);m.iat(0x6e85a9c,api,8)
    m.iat(0x6e85b78,lambda:folders.append(m.wide(m.arg())) or 1,8)
    def read_persist():m.put(m.arg(2),persist);return 1
    m.iat(0x6e85b60,read_persist,20);m.call(g['load'])
    assert folders==['C:\\Users\\测试\\AppData\\Local\\DNF90']
    assert m.wide(g['configPath'])==folders[0]+'\\KeyRepeat.ini'
    assert bytes(m.u.mem_read(g['saved'],88))==initial
    persist=b'BAD!'+initial[4:];m.put(g['loaded'],b'\0');m.call(g['load'])
    assert m.read(g['saved'])==0x31524b44 and bytes(m.u.mem_read(g['saved']+8,80))==bytes(80)
    return dict(passed=True,payloadSha256=hashlib.sha256(payload.read_bytes()).hexdigest(),
       compiledInputGate=True,nativePostprocessor=True,existingDirectionReleaseAndIMEChain=True,
       gatingCases=[x[0] for x in gates],allScanAliases=True,nonConfiguredKeysAndPreviousBufferPreserved=True,
       popupReopens=40,cachedWindowReused=True,recordDuplicateEscapeDeleteAndFocusLoss=True,
       failedWritesAndReadbackRejected=True,nativeTileSizeVirtuals=[456,338],independentPositionCases=dockcases,
       unicodePerUserPersistenceAndCorruptConfigDefaults=True,originalCallbacksAndX86ABI=True,
       adapters=['Win32 physical key/time/storage endpoints','native window open/close and controls','render/position propagation; compiled independent positioning and native size virtuals executed'],
       gameExecuted=False,renderingAccepted=False)
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('payload',type=Path);p.add_argument('map',type=Path);p.add_argument('base',type=Path);p.add_argument('receipt',type=Path)
    a=p.parse_args();r=verify(a.payload,a.map,a.base);a.receipt.write_text(json.dumps(r,ensure_ascii=False,indent=2),'utf8');print(json.dumps(r,ensure_ascii=False,indent=2))
