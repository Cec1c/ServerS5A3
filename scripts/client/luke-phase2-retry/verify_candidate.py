"""Bounded actual x86 execution. UI, packet transport and STL primitives are fixtures."""
import argparse,json,struct,sys
from collections import deque
from pathlib import Path
sys.dont_write_bytecode=True
import build_candidate as b
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE,UC_HOOK_MEM_READ_UNMAPPED,UC_HOOK_MEM_FETCH_UNMAPPED,UC_HOOK_MEM_WRITE_UNMAPPED
from unicorn.x86_const import *
REGS=(UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP,UC_X86_REG_EFLAGS)
class Machine:
    W=0x20000000;P=0x20002000;M=0x20004000;R=0x20005000;D=0x20006000;C=0x20009000;V=0x2000a000
    FRAME=0x10010000;SP=0x10008000;STOP=0x30000100
    def __init__(self,data,**kw):
        self.kw=kw;self.data=data;self.sec=b.pe.headers(data)[1];self.u=Uc(UC_ARCH_X86,UC_MODE_32)
        self.events=[];self.packets=[];self.packet=[];self.calls=[];self.done=False;self.config_reads=[];self.trace=deque(maxlen=32)
        def missing(uc,access,addr,size,value,user):
            page=addr&~4095;uc.mem_map(page,4096)
            for start,count,raw in self.sec:
                lo=max(page,start);hi=min(page+4096,start+count)
                if lo<hi:uc.mem_write(lo,data[raw+lo-start:raw+hi-start])
            return True
        self.u.hook_add(UC_HOOK_MEM_READ_UNMAPPED|UC_HOOK_MEM_FETCH_UNMAPPED|UC_HOOK_MEM_WRITE_UNMAPPED,missing)
        for addr,size in ((0,4096),(0x10000000,0x20000),(0x20000000,0x40000),(0x30000000,4096),(0x56a3000,4096),(0x5655000,4096)):
            self.u.mem_map(addr,size)
        self.w(self.W+0xfc8,kw.get('channel',23));self.w(self.P+8,kw.get('panel_state',10))
        self.w(self.W+0xc36c,0 if kw.get('stale_panel') else self.P);self.w(0x56a3798,self.W)
        self.w(0x56a3778,0 if kw.get('no_manager') else self.M);self.w(self.M+4,0xffffffff if kw.get('retired') else 7)
        self.w(self.M+8,0 if kw.get('no_raid') else self.R);self.w(self.R+0x24,kw.get('mode',1))
        self.w(self.R+0x28,kw.get('state',2));self.w(self.R+0x30,kw.get('phase',1));self.byte(self.R+0xb4,kw.get('raid_flag',1))
        self.w(self.D+0x19c,kw.get('map',5060));self.w(self.D+0x140+0x5dc,0)
        self.w(self.W+0xc8,0 if kw.get('no_map') else self.D)
        self.w(self.FRAME-0x7c,0 if kw.get('no_map') else self.D)
        self.w(self.P+0x784,kw.get('ready',9));self.w(self.P+0x730,0xff);self.w(self.P+0x734,0)
        for i in range(4):
            self.w(self.P+0x70c+i*8,self.C+i*0x100);self.w(self.C+i*0x100,self.V)
            self.byte(self.C+i*0x100+0x60,1);self.byte(self.C+i*0x100+0x61,1)
        self.w(self.V+8,0x30000000);self.w(self.V+12,0x30000010)
        self.w(0x5655b04,0x2000b000)
        self.alloc=0x20010000;self.containers={}
        self.setup_config()
        self.u.hook_add(UC_HOOK_CODE,self.hook)
    def w(self,a,v):self.u.mem_write(a,struct.pack('<I',v&0xffffffff))
    def byte(self,a,v):self.u.mem_write(a,bytes([v]))
    def rd(self,a):return struct.unpack('<I',self.u.mem_read(a,4))[0]
    def malloc(self,size=0x100):a=self.alloc;self.alloc+=size;return a
    def container(self,at,begin,end,stride,keys=None):
        proxy=self.malloc();holder=self.malloc();self.w(proxy,holder);self.w(holder+4,begin);self.w(holder+8,end)
        self.containers[at]=(proxy,begin,end,stride,keys or {})
    def setup_config(self):
        # Checked-iterator structures follow actual consumer fields; STL lookup primitives remain fixtures.
        root=self.malloc();node=self.malloc();self.w(0x56a377c,root)
        self.container(root,node,node+0x40,0x40,{1:node})
        row=self.malloc();self.container(node+0x10,row,row+0x18,0x18)
        self.w(row,4);self.w(row+4,5)
        ids=self.malloc();self.w(row+0xc,ids);self.w(row+0x10,ids+4)
        self.w(ids,self.kw.get('map',5060) if self.kw.get('exception') else 0xdead)
        event=self.malloc();self.w(event,0x81);self.w(event+4,4)
        self.container(self.M+0xb8,event,event+8,8)
        route=self.malloc();self.w(route+0x10,5)
        self.container(self.M+0x1f8,route,route+0x20,0x20,{1:route})
    def ret(self,v=0,pop=0):
        sp=self.u.reg_read(UC_X86_REG_ESP);pc=self.rd(sp)
        self.u.reg_write(UC_X86_REG_EAX,v&0xffffffff);self.u.reg_write(UC_X86_REG_ESP,sp+4+pop);self.u.reg_write(UC_X86_REG_EIP,pc)
    def hook(self,uc,a,size,user):
        self.trace.append(hex(a))
        if a>=0x5000000 and a<0x5500000:raise AssertionError('unexpected protected leaf; recent='+str([x for x in self.trace if x.startswith(('0x359','0x261','0xc72'))]))
        sp=uc.reg_read(UC_X86_REG_ESP);ecx=uc.reg_read(UC_X86_REG_ECX)
        if a==self.STOP or a==0x35a91b5 and self.operation=='show':self.done=True;uc.emu_stop();return
        if a in (0x30000000,0x30000010):
            index=(ecx-self.C)//0x100;value=self.rd(sp+4);op='visible' if a==0x30000000 else 'enabled'
            assert index in range(4) and value in (0,1)
            self.events.append([index,op,value]);self.byte(ecx+(0x60 if op=='visible' else 0x61),value);self.ret(pop=4);return
        if a==0x1fa2790:self.ret(self.W);return
        # Singleton allocation only. Actual 2525F90 -> 2046CC0 -> 2904180
        # consumes the pushed world argument and current scene +C8.
        if a==0x2047360:self.ret(self.W);return
        if a==0x2524fb0:self.ret(self.W);return
        if a==0x2526b50:self.ret(3);return
        if a==0x261f620:self.ret();return
        if a==0x261f360:self.ret(pop=8);return
        if a==0x2974ae0:self.ret(int(self.kw.get('party_denied',False)));return
        # Special-mode leaves are controlled. Native default-action branching,
        # final EBX selection, packet construction and cleanup remain real.
        if a==0x91a120:self.ret(int(self.kw.get('special',False)));return
        if a==0x1f5d0e0:self.ret(7);return
        if a==0x91af20:self.ret(self.W);return
        if a==0x91a980:self.ret(7,4);return
        if a==0x91a510:self.ret(self.kw.get('special_lookup',-1));return
        if a==0x91b1f0:self.ret(self.kw.get('special_value',7));return
        if a in (0x359c037,0x359c042,0x359c0b4,0x23a9df0):
            self.calls.append(hex(a))
        if a==0x359bc84:
            self.calls.append('original_party_denial');self.done=True;uc.emu_stop();return
        if a in (0x261e130,0x359bd70,0x14de7b0,0x14dce60,0x1f65ab0,0x1f5d4a0,0xe7e010,0xe7d7b0,0x1331da0,0x261e9c0,0xe91820,0x2534e90,0x2906d10,0x359eb20,0x8cfdc0,0x23a9df0,0x23a9dc0,0x38160c0,0x22bb930):self.ret();return
        if a in (0x2440510,0xe90fb0,0x22bb310):self.ret(pop=4);return
        if a==0x3816810:self.ret(pop=8);return
        if a==0x3816310:self.ret(1000);return
        if a==0x374ea10:self.packet.append(self.rd(sp+4));self.ret(0x2000f000,4);return
        if a in (0x374f280,0x374f390):self.ret();return
        if a==0x3750ac0:self.packets.append(self.packet[:]);self.packet.clear();self.ret();return
        if a in (0x268f640,0xb46290,0x224e5f0,0xa8f6c0):
            proxy,start,end,stride,keys=self.containers[ecx];out=self.rd(sp+4)
            cursor=start if a==0x224e5f0 else end
            if a in (0x268f640,0xb46290):
                key=self.rd(self.rd(sp+8));cursor=keys.get(key,end);self.config_reads.append(['lookup',hex(ecx),key])
            for off,v in ((0,proxy),(4,0),(8,cursor)):self.w(out+off,v)
            self.ret(out,8 if a in (0x268f640,0xb46290) else 4);return
        if a in (0x37bafa0,0x3a2d530):
            out=self.rd(sp+4);self.u.mem_write(out,bytes(self.u.mem_read(ecx,12)))
            proxy=self.rd(ecx);stride=next(v[3] for v in self.containers.values() if v[0]==proxy)
            self.w(ecx+8,self.rd(ecx+8)+stride);self.ret(out,8);return
        if a==0x2628890:self.config_reads.append(['real_exception',self.rd(ecx+0x19c)])
        if a in (0x3e738b1,):raise AssertionError('native checked iterator failure')
    def invoke(self,op,entries,args=()):
        self.operation=op;self.done=False
        for reg,value in zip(REGS,(0x1234,0x2345,self.P,0x3456,self.P,0x4567,self.FRAME,self.SP,0x246)):self.u.reg_write(reg,value)
        self.w(self.SP,self.STOP)
        for i,arg in enumerate(args):self.w(self.SP+4+i*4,arg)
        pc={'predicate':entries['predicate'],'show':0x35a8eef,'refresh':0x359ee40,'input':0x359baa0,'default':0x359bee0,'exception':0x2628890}.get(op)
        if op=='exception':self.u.reg_write(UC_X86_REG_ECX,self.D)
        if op=='mouse':pc=0x35a1adf;self.u.reg_write(UC_X86_REG_EDI,args[0])
        if op=='hotkey':pc=0x35a1d59;self.w(self.FRAME-0x1c,args[0])
        if op in ('mouse','hotkey'):
            # Actual caller instructions and guarded callee, stop after original call returns.
            stop=0x35a1ae9 if op=='mouse' else 0x35a1d66
            def endpoint(uc,a,s,user):
                if a==stop:self.done=True;uc.emu_stop()
            hook=self.u.hook_add(UC_HOOK_CODE,endpoint)
        try:self.u.emu_start(pc,0,count=50000)
        except Exception as e:raise AssertionError(f'{op} pc={self.u.reg_read(UC_X86_REG_EIP):x} kw={self.kw}: {e}; trace={list(self.trace)}') from e
        assert self.done,(op,hex(self.u.reg_read(UC_X86_REG_EIP)))
        if op in ('mouse','hotkey'):self.u.hook_del(hook)
        return self.snapshot()
    def snapshot(self):
        return {'buttons':[list(self.u.mem_read(self.C+i*0x100+0x60,2)) for i in range(4)],'packets':self.packets[:],'selection':[self.rd(self.P+0x730),self.rd(self.P+0x734)],'events':self.events[:],'eax':self.u.reg_read(UC_X86_REG_EAX),'sp':self.u.reg_read(UC_X86_REG_ESP),'registers':[self.u.reg_read(r) for r in REGS],'calls':self.calls[:],'config_reads':self.config_reads[:]}
def verify(src,candidate,receipt):
    available=b.memory_guard();rebuilt,rebuild=b.build(src);assert bytes(rebuilt)==candidate
    assert receipt['output_sha256']==b.pe.sha(candidate)
    e=receipt['entries'];rows=[]
    def add(name,kw,op,args=(),target=False):
        m=Machine(candidate,**kw);got=m.invoke(op,e,args)
        old=Machine(src,**kw).invoke(op,e,args) if op!='predicate' else None
        if op=='predicate':assert bool(got['eax'])==target
        elif target and op in ('show','refresh'):
            assert got['buttons'][0]==[0,0],got
            assert got['buttons'][1:]==old['buttons'][1:]
        elif target:
            assert got['packets']==[] and got['selection']==[255,0],got
            assert old['packets']==[[72,1,0]] if op!='input' or args[1]==1 else old['packets']==[[72,2,0]],old
        else:
            for key in ('buttons','packets','selection','calls'):assert got[key]==old[key],(name,key,got,old)
        if op not in ('show',) and not kw.get('party_denied'):
            expected=m.SP+(4+4*len(args) if op in ('input',) else 0 if op in ('mouse','hotkey') else 4)
            assert got['sp']==expected,(name,got['sp'],expected)
            for i in (1,4,5,6):
                initial=(0x2345,m.P,args[0] if op=='mouse' else 0x4567,m.FRAME)[(1,4,5,6).index(i)]
                assert got['registers'][i]==initial,(name,'callee-saved register',i,got)
        rows.append({'name':name,'input':kw,'operation':op,'args':list(args),'target':target,'passed':True,'result':got,'baseline':old})
        return got,old
    for mapid in b.MAPS:add(f'map_{mapid}',{'map':mapid},'predicate',target=True)
    for name,kw in [('ordinary',{'channel':0}),('normal_luke',{'raid_flag':0}),('anton',{'mode':0}),('fiendwar',{'mode':2}),('phase0',{'map':5054,'phase':0}),('no_map',{'no_map':True}),('unknown_map',{'map':99999}),('retired',{'retired':True}),('stale_panel',{'stale_panel':True}),('no_manager',{'no_manager':True}),('no_raid',{'no_raid':True}),('other_panel_state',{'panel_state':9})]:
        add(name,kw,'predicate')
    for phase in (0,1,7):add(f'phase_arg_not_authority_{phase}',{'phase':phase},'show',target=True)
    for state in (2,4,5,6):
        for exception in (False,True):add(f'state{state}_exception{exception}',{'state':state,'exception':exception},'show',target=True)
    for ready in (0,6,9):add(f'refresh_ready{ready}',{'ready':ready},'refresh',target=True)
    for op in ('mouse','hotkey','input','default'):
        args=(0,) if op in ('mouse','hotkey') else (0,1) if op=='input' else ()
        add(op+'_target',{},op,args,target=True)
        add(op+'_ordinary',{'channel':0},op,args)
        add(op+'_phase0',{'map':5054,'phase':0},op,args)
    for index in (1,2):add(f'other_action_{index}',{},'input',(index,1))
    for label,params in [('missing',{'special_lookup':-1}),('different',{'special_lookup':3,'special_value':8})]:
        kw={'special':True,**params}
        # Explicitly establish target identity; do not let the control pass by
        # accidentally leaving Luke mode/map/channel outside the predicate.
        assert Machine(candidate,**kw).invoke('predicate',e)['eax']==1
        got,old=add('luke_special_default_town_'+label,kw,'default')
        assert got['packets']==old['packets']==[[72,1,2]],(got,old)
        assert got['selection']==old['selection']==[2,1]
        assert '0x359c037' in got['calls'] and '0x23a9df0' in got['calls']
        got,old=add('ordinary_special_default_town_'+label,{'channel':0,**kw},'default')
        assert got['packets']==old['packets']==[[72,1,2]]
    add('luke_special_default_retry',{'special':True,'special_lookup':3,'special_value':7},'default',target=True)
    add('focus_common_entry',{},'input',(0,2),target=True)
    for state in (2,6):
        for exception in (False,True):add(f'phase0_original_exception_{state}_{exception}',{'map':5054,'phase':0,'state':state,'exception':exception},'show')
    for op in ('show','refresh'):add(op+'_ordinary',{'channel':0},op)
    for allowed in (False,True):add('original_party_denial_'+str(allowed),{'channel':0,'party_denied':allowed},'input',(0,1))
    for match in (False,True):
        m=Machine(src,exception=match);r=m.invoke('exception',e);assert bool(r['eax']&255)==match,r
        rows.append({'name':'actual_config_membership_'+str(match),'passed':True,'result':r})
    # Refresh cannot re-enable hidden target; new normal result initialization restores it.
    m=Machine(candidate);a=m.invoke('show',e);a=m.invoke('refresh',e);assert a['buttons'][0]==[0,0]
    m.w(m.D+0x19c,25);m.w(m.W+0xfc8,0);a=m.invoke('show',e);a=m.invoke('refresh',e);assert a['buttons'][0]==[1,1]
    rows.append({'name':'same_panel_next_ordinary_restores','passed':True,'result':a})
    return {'passed':True,'count':len(rows),'input_sha256':b.SHA,'candidate_sha256':b.pe.sha(candidate),'available_bytes':available,'cases':rows,'fixture_boundary':'Actual predicate/current-raid eligibility, current-dungeon getter/adapter/scene+C8, show tail, refresh, common input/default native send paths, mouse/key caller instructions, real config membership function and its value getters. World/scene service lookup, singleton allocator, unrelated predicates, checked STL iterator primitives, UI setter/render leaves and packet writer transport are controlled fixtures. Original party denial stops at its existing dialog entry. No full GUI/live network/PVF load.'}
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--input',type=Path,required=True);p.add_argument('--candidate-dir',type=Path,required=True);a=p.parse_args()
    result=verify(a.input.read_bytes(),(a.candidate_dir/'candidate.exe').read_bytes(),json.loads((a.candidate_dir/'build.json').read_text()))
    (a.candidate_dir/'verify.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8');print(json.dumps({k:v for k,v in result.items() if k!='cases'}))
