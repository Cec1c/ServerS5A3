"""Independent byte-scope and native table-row verification; no producer import."""
from pathlib import Path
import argparse,hashlib,json,struct,sys

BASE_SHA='8e92c29cbbbcc2e643a87cb73e199ec52e9b3beeb4146b3ec16d62404d36064c'

def parse(data):
    if data[:2]!=b'MZ':raise ValueError('MZ')
    head=struct.unpack_from('<I',data,60)[0]
    if data[head:head+4]!=b'PE\0\0':raise ValueError('PE')
    optional=head+24
    if struct.unpack_from('<H',data,optional)[0]!=267:raise ValueError('PE32')
    base=struct.unpack_from('<I',data,optional+28)[0]
    table=optional+struct.unpack_from('<H',data,head+20)[0]
    sections=[]
    for i in range(struct.unpack_from('<H',data,head+6)[0]):
        _,rva,size,offset=struct.unpack_from('<IIII',data,table+40*i+8)
        sections.append((base+rva,size,offset))
    def offset(va,size=1):
        for start,count,raw in sections:
            if start<=va and va+size<=start+count:return raw+va-start
        raise ValueError(hex(va))
    return optional,offset

def check_images(base,new):
    assert hashlib.sha256(base).hexdigest()==BASE_SHA and len(base)==len(new)==111861760
    optional,offset=parse(base)
    assert parse(new)[0]==optional
    areas=sorted([(optional+64,optional+68)]+[(offset(a,n),offset(a,n)+n) for a,n in ((0x110c254,21),(0x110c343,11),(0x1875359,42),(0x1875415,11))])
    cursor=0
    for lo,hi in areas:
        assert base[cursor:lo]==new[cursor:lo],('unexpected edit',hex(cursor),hex(lo))
        cursor=hi
    assert base[cursor:]==new[cursor:]
    check=bytearray(new);struct.pack_into('<I',check,optional+64,0)
    value=sum(memoryview(check).cast('H'))
    while value>>16:value=(value&65535)+(value>>16)
    assert value+len(new)==struct.unpack_from('<I',new,optional+64)[0]
    return areas

def execute(data,mode,ids,roster,actor_present,widget_present=True):
    from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
    from unicorn.x86_const import UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_ESP,UC_X86_REG_EBP,UC_X86_REG_EIP
    _,offset=parse(data)
    u=Uc(UC_ARCH_X86,UC_MODE_32);mapped=set()
    def put(a,b):
        for p in range(a&~4095,(a+len(b)+4095)&~4095,4096):
            if p not in mapped:u.mem_map(p,4096);mapped.add(p)
        u.mem_write(a,b)
    def w(a,v):put(a,struct.pack('<I',v))
    def r(a):return int.from_bytes(u.mem_read(a,4),'little')
    def ret(v=0,pop=0):
        sp=u.reg_read(UC_X86_REG_ESP);dest=r(sp)
        u.reg_write(UC_X86_REG_EAX,v);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,dest)
    record,participants,manager,owner,descriptor,rows,actor,control,widget=range(0x120000,0x1b0000,0x10000)
    for addr in (0,record,participants,manager,owner,descriptor,rows,actor,control,widget):put(addr,bytes(4096))
    for start,size in ((0x110c23f,0x410),(0x187533e,0x6d0),(0x187acd0,0xd5)):
        put(start,data[offset(start,size):offset(start,size)+size])
    w(record+4,participants);w(record+8,participants+len(ids)*4)
    for i,value in enumerate(ids):w(participants+4*i,value)
    w(owner,descriptor);w(descriptor+4,rows);w(descriptor+8,rows+len(roster)*0x38)
    for i,(char,group) in enumerate(roster):
        put(rows+i*0x38,struct.pack('<H',char));put(rows+i*0x38+0x2d,bytes([group]))
    w(control+4,10);w(control+8,10)
    w(0x56a3778,manager);w(0x55b3bdc,0)
    stack,frame=0x402000,0x403000;put(stack-0x1000,bytes(0x3000))
    is_luke=mode=='luke'
    begin_offset=-0xfc if is_luke else -0x144
    w(frame+(-0xa4 if is_luke else -0xd0),widget if widget_present else 0)
    w(frame+(-0xa0 if is_luke else -0xcc),control)
    w(frame-0xa0,control if is_luke else 0)
    before_rows=bytes(u.mem_read(rows,4096));before_participants=bytes(u.mem_read(participants,4096))
    frames=[];lookups=[];stopped=[]
    leaves=(0x296d730,0x29614e0,0x1a553e0,0x224e5f0,0xa8f6c0,0xed9b20,0x20d09c0,0x8c78c0,0x37ed900)
    for a in leaves:put(a,b'\xc3')
    stop=0x110c447 if is_luke else 0x18754e0
    empty=0x110c634 if is_luke else 0x1875a07
    def hook(_u,a,size,_):
        sp=u.reg_read(UC_X86_REG_ESP)
        if a in (stop,empty):stopped.append(hex(a));u.emu_stop()
        elif a==0x296d730:
            lookups.append(r(sp+4));ret(actor if actor_present and ids and ids[0]!=0 else 0)
        elif a==0x29614e0:assert u.reg_read(UC_X86_REG_ECX)==actor;ret(ids[0])
        elif a==0x1a553e0:ret(manager)
        elif a in (0x224e5f0,0xa8f6c0):
            out=r(sp+4);w(out,owner);w(out+4,0);w(out+8,rows if a==0x224e5f0 else rows+len(roster)*0x38);ret(out,4)
        elif a==0xed9b20:
            iterator=u.reg_read(UC_X86_REG_ECX);assert iterator==frame+begin_offset
            w(iterator+8,r(iterator+8)+0x38);ret(r(sp+4),8)
        elif a==0x20d09c0:
            out=u.reg_read(UC_X86_REG_ECX);w(out,r(sp+4));w(out+4,r(sp+8));ret(out,8)
        elif a==0x8c78c0:
            out=r(sp+4);incoming=r(sp+8);assert r(incoming)==widget
            w(out,widget);w(out+4,0);ret(out)
        elif a==0x37ed900:
            assert u.reg_read(UC_X86_REG_ECX)==widget;frames.append(r(sp+4));ret(0,4)
    u.hook_add(UC_HOOK_CODE,hook)
    u.reg_write(UC_X86_REG_ESP,stack);u.reg_write(UC_X86_REG_EBP,frame)
    u.reg_write(UC_X86_REG_EBX,record);u.reg_write(UC_X86_REG_ECX,0 if is_luke else record)
    u.emu_start(0x110c23f if is_luke else 0x187533e,0x300000,count=5000)
    assert stopped and u.reg_read(UC_X86_REG_ESP)==stack,('stack or stop',stopped,hex(u.reg_read(UC_X86_REG_ESP)))
    assert bytes(u.mem_read(rows,4096))==before_rows and bytes(u.mem_read(participants,4096))==before_participants
    return dict(frames=frames,sceneLookups=lookups,stop=stopped[0],stackBalanced=True,sourceRecordsPreserved=True)

def main():
    p=argparse.ArgumentParser()
    for name in ('baseline','candidate','output'):p.add_argument('--'+name,type=Path,required=True)
    p.add_argument('--python-deps',type=Path,help='optional external dependency directory; otherwise use the active Python environment')
    args=p.parse_args()
    if args.python_deps:sys.path.insert(0,str(args.python_deps))
    base=args.baseline.read_bytes();candidate=args.candidate.read_bytes();areas=check_images(base,candidate)
    results=[]
    scenarios=[('local',[1047],[(1018,2),(1047,1)],True,True,1),
               ('remote',[1047],[(1018,2),(1047,1)],False,True,1),
               ('changed_group',[1047],[(1047,4),(1018,2)],False,True,4),
               ('first_participant',[1018,1047],[(1047,1),(1018,2)],False,True,2),
               ('missing_roster',[1047],[(1018,2)],False,True,None),
               ('empty_roster',[1047],[],False,True,None),
               ('empty_participants',[],[(1047,1)],False,True,None),
               ('zero_key',[0],[(0,5),(1047,1)],False,True,None),
               ('missing_widget',[1047],[(1047,1)],False,False,None)]
    for mode in ('luke','anton'):
        for name,ids,roster,present,widget,expected in scenarios:
            old=execute(base,mode,ids,roster,present,widget)
            new=execute(candidate,mode,ids,roster,present,widget)
            assert old['frames']==([expected] if present and expected is not None else []),(mode,name,'baseline',old)
            assert new['frames']==([] if expected is None else [expected]),(mode,name,'candidate',new)
            assert not new['sceneLookups'],(mode,name,'unexpected local actor dependency')
            results.append(dict(mode=mode,case=name,baseline=old,candidate=new))
    receipt=dict(passed=True,input_sha256=BASE_SHA,candidate_sha256=hashlib.sha256(candidate).hexdigest(),edit_ranges=areas,cases=results,
                 actualCode=['participant nonempty/first-key branch','native roster comparison loop','group byte selection','187ACD0 badge wrapper'],
                 fixtures=['scene actor lookup/identity for baseline control','raid manager and STL iterator leaves','shared pointer construction','UI cast/frame sink'],
                 limits='Starts inside each native table row with valid surrounding state. Full packet ingestion, window construction, phase UI and GUI rendering remain untested.')
    with args.output.open('x',encoding='utf8') as f:json.dump(receipt,f,indent=2)
    print(json.dumps({k:v for k,v in receipt.items() if k!='cases'}));print('native_cases',len(results),'per_image')

if __name__=='__main__':main()
