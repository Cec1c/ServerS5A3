"""Bounded current-member/current-row cases using actual packaged helper bytes."""
import verify_packaged as h
PROXY,MEMBERS,ROWS,GROUPS,KEYS=0x30010000,0x30011000,0x30012000,0x30013000,0x30014000
FRAME=h.STACK+0x800
blobs={}
class Harness(h.Harness):
    def __init__(self,case):
        super().__init__(blobs,case)
        for a in (PROXY,MEMBERS,ROWS,GROUPS,KEYS):self.put(a,bytes(4096))
        raid_id=0x31|(case['owner']<<16)|(case['route']<<24)
        self.w32(h.MANAGER+4,raid_id);self.w32(h.ROW,raid_id)
        self.w32(h.ROW+0xa0,PROXY);self.w32(PROXY,h.ROW+0xa0)
        self.w32(h.ROW+0xa4,MEMBERS);self.w32(h.ROW+0xa8,MEMBERS+100);self.w32(h.ROW+0xac,MEMBERS+100)
        self.w32(MEMBERS,case['key'])
        self.w32(h.MANAGER+0xcc,PROXY+16);self.w32(PROXY+16,h.MANAGER+0xcc)
        self.w32(h.MANAGER+0xd0,ROWS);self.w32(h.MANAGER+0xd4,ROWS+36);self.w32(h.MANAGER+0xd8,ROWS+36)
        self.w32(ROWS,32001)
        row_obj=ROWS+4
        self.w32(row_obj+8,PROXY+32);self.w32(PROXY+32,row_obj+8)
        self.w32(row_obj+12,GROUPS);self.w32(row_obj+16,GROUPS+20);self.w32(row_obj+20,GROUPS+20)
        self.w32(GROUPS,PROXY+48);self.w32(PROXY+48,GROUPS)
        self.w32(GROUPS+4,KEYS);self.w32(GROUPS+8,KEYS+4);self.w32(GROUPS+12,KEYS+4);self.w32(KEYS,case['key'])
        self.w32(FRAME-0x3c,row_obj);self.w32(FRAME-0x40,0);self.w32(FRAME+8,32001)
        fields={'stale_raid':h.MANAGER+4,'departed':MEMBERS,'wrong_dungeon':FRAME+8,
                'stale_row':FRAME-0x3c,'wrong_group_key':KEYS,'oversized_group':FRAME-0x40}
        for k,v in case.get('changes',{}).items():self.w32(fields[k],v)
    def watched(self):
        result=super().watched()
        for a in (PROXY,MEMBERS,ROWS,GROUPS,KEYS):result[hex(a)]=bytes(self.uc.mem_read(a,4096))
        return result
    def run_combined(self):
        c=self.case
        initial=dict(eax=0x11223344,ebx=c['ebx'],ecx=0x44556677,edx=0x55667788,
                     esi=0x66778899,edi=0xa5a50000|c['key'],ebp=FRAME,esp=h.STACK,eflags=c['flags'])
        for k,v in initial.items():self.uc.reg_write(self.reg_ids[k],v)
        before=self.watched();stack=bytes(self.uc.mem_read(h.STACK,4096))
        self.uc.emu_start(h.SITE-2,0,timeout=1000000,count=8192)
        end=self.regs();entry=self.branch_entry
        assert self.endpoint==(h.HIT if c['hit'] else h.MISS),c['name']
        assert end['ebx']==(c['ebx'] or (h.ACTOR if c['hit'] else 0)),c['name']
        for k in ('eax','ecx','edx','esi','edi','ebp','esp'):assert end[k]==initial[k],(c['name'],k)
        assert end['eflags']==entry['eflags'] and self.watched()==before
        assert bytes(self.uc.mem_read(h.STACK,4096))==stack
        assert not self.uc.query(h.UC.UC_QUERY_TIMEOUT)
        assert not any(e['kind']=='getter' for e in self.events)
        assert all(e['category']==1 for e in self.events if 'category' in e)
        return {'name':c['name'],'passed':True,'hit':c['hit'],'instructions':len(self.trace)}

def run_cases():
    cases=[]
    for career in range(16):cases.append((f'class_{career}',True,{'career':career}))
    cases += [('unknown_class',False,{'career':16}),('large_class',False,{'career':32}),
              ('negative_class',False,{'career':0xffffffff}),('cached_model_present',False,{'model':h.MODEL}),
              ('profile_missing',False,{'profile':0}),('absent_manager',False,{'manager':False}),
              ('absent_current_raid',False,{'context':False}),('stale_raid',False,{'changes':{'stale_raid':0x2020304}}),
              ('departed_member',False,{'changes':{'departed':0x2202}}),('wrong_dungeon',False,{'changes':{'wrong_dungeon':32002}}),
              ('stale_row',False,{'changes':{'stale_row':ROWS+40}}),('wrong_group_key',False,{'changes':{'wrong_group_key':0x2202}}),
              ('oversized_group',False,{'changes':{'oversized_group':20}}),('empty_map',False,{'map_mode':'empty'}),
              ('other_category_only',False,{'map_mode':'other_categories'}),('actor_wrong_key',False,{'actor_key':0x2202}),
              ('actor_wrong_owner',False,{'actor_owner':1}),('actor_wrong_route',False,{'actor_route':0}),
              ('wrong_vtable',False,{'actor_vptr':h.VTABLE+4}),('nonnull_original',True,{'ebx':h.ACTOR,'manager':False})]
    results=[]
    for name,hit,overrides in cases:
        for flags in (0x246,0xed7):
            c=dict(name=f'{name}_{flags:x}',hit=hit,ebx=0,key=0x2201,manager=True,context=True,
                   owner=0,route=1,actor_key=0x2201,actor_owner=0,actor_route=1,actor_vptr=h.VTABLE,
                   model=0,profile=h.PROFILE,career=0,map_mode='exact',flags=flags)
            c.update(overrides);results.append(Harness(c).run_combined())
    return results
