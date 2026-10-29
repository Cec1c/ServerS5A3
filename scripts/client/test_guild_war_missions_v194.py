"""Native mission receiver, title ordering and UI getters.

STL allocation/map/vector operations, PVF config and UI are explicit adapters.
Packet readers, classification/order/title helpers and getters run original x86.
"""
from pathlib import Path
import collections,json,os,re,struct,unittest
from test_guild_war_refresh_v173 import WarNative
from test_guild_hongbao_requests_v188 import HongbaoSender
from unicorn.x86_const import *
import patch_guild_war_missions_v194 as patch

def catalog(path):
    text=Path(path).read_text('utf8')
    missions=[]
    for block in re.findall(r'\[mission\](.*?)\[/mission\]',text,re.S):
        ident=int(re.search(r'\[index\]\s*(\d+)',block)[1])
        category=re.search(r'\[category\]\s*`([^`]+)`',block)[1]
        cond=re.search(r'\[condition\]\s*`([^`]+)`\s*(\d+)',block)
        missions.append(dict(id=ident,category=category,target=int(cond[2])))
    assert len(missions)==30
    return missions

class MissionNative(WarNative):
    def __init__(self,data,missions):
        self.trace=collections.deque(maxlen=600)
        super().__init__(data)
        self.missions=missions
        self.u.mem_map(0x24000000,0x200000)
        self.heap=0x24000000;self.trees={};self.vectors=set();self.refreshes=0
        for x in [0x8df100,0xa8f6c0]:self.hooks.pop(x,None)
        self.hooks.update({0x8a9480:self.alloc_call,0x106d420:self.vec_clear,
            0xc8d2d0:self.tree_construct,0x106e7a0:self.tree_insert,
            0x8df100:lambda:self.iterator(False),0xa8f6c0:lambda:self.iterator(True),
            0x3b97f80:self.next_iter,0x3a20eb0:self.next_iter,
            0x168b960:self.tree_find,0x91ae30:self.tree_find,
            0x10723c0:self.catalog,0x106e5c0:self.vec_append,
            0x45f9b0:lambda:self.ret(12,self.reg(UC_X86_REG_ECX)),
            0x19ccfb0:lambda:self.ret(4,self.reg(UC_X86_REG_ECX)),
            0xedc3f0:lambda:self.ret(),0xedf210:lambda:self.ret(),
            0x106d2b0:self.vec_copy,0x224e5f0:lambda:self.iterator(False),
            0x22731e0:lambda:self.ret(4,0x2000b000),
            0x2270560:lambda:self.ret(),0x2270620:lambda:self.ret(4),
            0x226e1e0:lambda:self.ret(),0xede990:lambda:self.ret(),0xa102e0:lambda:self.ret(),
            0x32b55e0:lambda:self.ret(4,0),0xe93a90:self.refreshed,
        })
        self.init_vec(self.MANAGER+0x740);self.init_vec(self.MANAGER+0x754)
    def hook(self,u,at,size,ctx):
        self.trace.append(at)
        super().hook(u,at,size,ctx)
    def alloc(self,size):
        p=self.heap;self.heap+=(size+15)&~15;assert self.heap<0x24200000
        self.u.mem_write(p,bytes(size));return p
    def alloc_call(self):self.ret(value=self.alloc(self.arg()))
    def init_vec(self,v):
        self.vectors.add(v)
        proxy=self.alloc(8);buf=self.alloc(4096);self.put(proxy,v);self.put(v,proxy,buf,buf,buf+4096,0)
    def vec_copy(self):
        v=self.reg(UC_X86_REG_ECX);src=self.arg();self.init_vec(v)
        raw=bytes(self.u.mem_read(self.read(src+4),self.read(src+8)-self.read(src+4)))
        self.u.mem_write(self.read(v+4),raw);self.put(v+8,self.read(v+4)+len(raw));self.ret(4,v)
    def vec_clear(self):
        v=self.reg(UC_X86_REG_ECX)
        if self.read(v+4)==0:self.init_vec(v)
        self.put(v+8,self.read(v+4));self.ret()
    def vec_append(self):
        v=self.reg(UC_X86_REG_ECX)
        if self.read(v+4)==0:self.init_vec(v)
        p=self.read(v+8)
        self.u.mem_write(p,bytes(self.u.mem_read(self.arg(),9)));self.put(v+8,p+9);self.ret(4)
    def tree(self,v,rows,config=False):
        proxy=self.alloc(8);head=self.alloc(128);self.put(proxy,v)
        nodes=[]
        for key,raw in sorted(rows.items()):
            p=self.alloc(128);self.put(p+12,key);self.u.mem_write(p+16,raw);nodes.append(p)
        self.put(head,nodes[0] if nodes else head,head,nodes[-1] if nodes else head)
        self.put(v,proxy,0,head,len(nodes));self.trees[head]=nodes
        for p in nodes:self.put(p,head,head,head)
        return head
    def tree_construct(self):self.tree(self.reg(UC_X86_REG_ECX),{});self.ret(8,self.reg(UC_X86_REG_ECX))
    def tree_insert(self):
        v=self.reg(UC_X86_REG_ECX);out,pair=self.arg(),self.arg(1);head=self.read(v+8)
        key=self.read(pair);p=self.alloc(128);self.put(p+12,key);self.u.mem_write(p+16,bytes(self.u.mem_read(pair+4,9)))
        self.trees[head].append(p);self.trees[head].sort(key=lambda x:self.read(x+12))
        self.put(head,self.trees[head][0],head,self.trees[head][-1]);self.put(v+12,len(self.trees[head]))
        self.put(out,self.read(v),0,p);self.ret(8,out)
    def iterator(self,end):
        v=self.reg(UC_X86_REG_ECX);out=self.arg()
        if v in self.vectors:
            self.put(out,self.read(v),0,self.read(v+(8 if end else 4)));self.ret(4,out);return
        head=self.read(v+8)
        self.put(out,self.read(v),0,head if end else self.read(head));self.ret(4,out)
    def next_iter(self):
        it=self.reg(UC_X86_REG_ECX);out=self.arg();v=self.read(self.read(it));head=self.read(v+8)
        nodes=self.trees[head];i=nodes.index(self.read(it+8));self.put(it+8,nodes[i+1] if i+1<len(nodes) else head)
        self.ret(8,out)
    def tree_find(self):
        v=self.reg(UC_X86_REG_ECX);out,key=self.arg(),self.read(self.arg(1));head=self.read(v+8)
        found=next((p for p in self.trees[head] if self.read(p+12)==key),head)
        self.put(out,self.read(v),0,found);self.ret(8,out)
    def catalog(self):
        out=self.arg();self.u.mem_write(out,bytes(0x654));rows={}
        for m in self.missions:
            raw=bytearray(0x48);struct.pack_into('<I',raw,0,m['id']);struct.pack_into('<I',raw,24,7)
            struct.pack_into('<IIII',raw,36,m['target'],0,0 if m['category']=='daily' else 1,0)
            rows[m['id']]=bytes(raw)
        self.tree(out+0x3b0,rows,True);self.put(out+0x47c,1,10);self.ret(4,out)
    def refreshed(self):self.refreshes+=1;self.ret()
    def receive_missions(self,daily=(),season=()):
        wire=struct.pack('<I',len(daily))+b''.join(struct.pack('<IIB',*x) for x in daily)
        wire+=struct.pack('<I',len(season))+b''.join(struct.pack('<IIB',*x) for x in season)
        return self.receive_wire(wire)
    def receive_wire(self,wire):
        self.u.mem_write(self.PACKET,wire);self.put(0x57f9ad0,self.PACKET,len(wire),0,0)
        try:self.run(0x1074c40)
        except Exception as e:
            print([hex(x) for x in self.trace if 0x1060000<=x<0x1080000][-45:]);print({k:hex(self.reg(v)) for k,v in dict(eax=UC_X86_REG_EAX,ebx=UC_X86_REG_EBX,ecx=UC_X86_REG_ECX,edx=UC_X86_REG_EDX,esi=UC_X86_REG_ESI,edi=UC_X86_REG_EDI,ebp=UC_X86_REG_EBP,esp=UC_X86_REG_ESP).items()})
            raise RuntimeError(f'pc={self.reg(UC_X86_REG_EIP):08x}: {e}') from e
        assert self.read(0x57f9ad4)==0
        rows=[]
        for v in [self.MANAGER+0x740,self.MANAGER+0x754]:
            data=bytes(self.u.mem_read(self.read(v+4),self.read(v+8)-self.read(v+4)))
            rows.append([struct.unpack_from('<IIB',data,i) for i in range(0,len(data),9)])
        return rows

    def check_getters(self,rows):
        for season,group in enumerate(rows):
            self.run(0x1068b80 if season else 0x1068b60)
            assert self.reg(UC_X86_REG_EAX)==len(group)
            for index,row in enumerate(group):
                self.run(0x1068b10 if season else 0x1068ac0,(index,0x2000d000))
                assert self.reg(UC_X86_REG_EAX)&255==1
                assert struct.unpack('<IIB',self.u.mem_read(0x2000d000,9))==row
            self.run(0x1068b10 if season else 0x1068ac0,(len(group),0x2000d000))
            assert self.reg(UC_X86_REG_EAX)&255==0


class MissionNativeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        exe,etc=os.environ.get('DNF_GUILD_WAR_V194_CLIENT'),os.environ.get('DNF_GUILD_WAR_ETC')
        if not exe or not etc:
            raise unittest.SkipTest('exact V194 and approved PVF agitwar export required')
        cls.before=Path(exe).read_bytes()
        cls.after,_=patch.build(cls.before)
        cls.missions=catalog(etc)

    def test_real_tab_sender(self):
        for data in (self.before,self.after):
            for tab,want in ((0,3),(1,0),(2,2),(3,None)):
                lab=HongbaoSender(data)
                lab.hooks[0x1072870]=lambda:lab.ret(value=0)
                lab.hooks[0x10666c0]=lambda:lab.ret(value=1)
                lab.run(0xef8820,(tab,))
                self.assertEqual(lab.sent,[] if want is None else [dict(opcode=1345,body=struct.pack('<I',want).hex())])

    def test_native_catalog_titles_and_ui_getters(self):
        for data in (self.before,self.after):
            lab=MissionNative(data,self.missions)
            rows=lab.receive_missions()
            self.assertEqual(rows,[[(i,0,0) for i in range(1,10)],[(i,0,0) for i in range(10,31)]])
            lab.check_getters(rows)

    def test_native_progress_claim_order_and_refresh_reset(self):
        lab=MissionNative(self.after,self.missions)
        rows=lab.receive_missions([(9,3,0),(2,1,1)],[(30,7,0),(11,1,1)])
        self.assertEqual(rows[0],[(1,0,0),(9,3,0)]+[(i,0,0) for i in range(3,9)]+[(2,1,1)])
        self.assertEqual(rows[1],[(10,0,0),(30,7,0)]+[(i,0,0) for i in range(12,30)]+[(11,1,1)])
        lab.check_getters(rows)
        reset=lab.receive_missions()
        self.assertEqual(reset,[[(i,0,0) for i in range(1,10)],[(i,0,0) for i in range(10,31)]])
        lab.check_getters(reset)

    def test_production_reply_native_consumption(self):
        path=os.environ.get('DNF_GUILD_WAR_MISSION_WIRE')
        if not path:self.skipTest('bridge fixture output required')
        wire=json.loads(Path(path).read_text('utf8'))
        self.assertEqual((wire['opcode'],wire['class'],wire['bodyHex']),(1283,0,'0000000000000000'))
        lab=MissionNative(self.after,self.missions)
        rows=lab.receive_wire(bytes.fromhex(wire['bodyHex']))
        lab.check_getters(rows)
        self.assertEqual([len(group) for group in rows],[9,21])


if __name__=='__main__':unittest.main()
