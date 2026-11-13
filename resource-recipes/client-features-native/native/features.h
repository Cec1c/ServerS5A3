#pragma once
#include "native_power_stats.h"
struct DamageRow{wchar_t name[64];uint64_t total;unsigned hits;int job;};
static uint64_t damageGeneration;
static DamageRow damageRows[8];
static unsigned firstDamage,lastDamage,lastDamageRender;static void*damageRole;
static volatile long damageLock;
static void clearDamage(){
if(_InterlockedCompareExchange(&damageLock,1,0))return;memset(damageRows,0,sizeof(damageRows));firstDamage=lastDamage=0;damageGeneration++;_InterlockedExchange(&damageLock,0);
}
static bool sameName(const wchar_t*a,const wchar_t*b){for(unsigned i=0;i<64;i++){if(a[i]!=b[i])return false;if(!a[i])return true;}return false;}
extern "C" __declspec(dllexport) void __cdecl RecordDamage(const unsigned*q){
 // Native cdecl(attacker, victim, damageKind, signed64 damage, skill, extra).
 // Observe the existing RDAR call before its optional training-window gate.
 int64_t amount=*(const int64_t*)(q+3);void*source=(void*)q[0];void*victim=(void*)q[1];
 if(amount<=0||!source||!victim||!readable(source,4)||!readable(victim,4))return;
 if(q[2]==((unsigned(__cdecl*)())0x78f6a0)())return;
 auto vt=(void*)value(source);if(!readable(vt,0x4fc))return;
 void*owner=((void*(__thiscall*)(void*))value(vt,0x4f8))(source);if(owner)source=owner;
 if(!readable(source,4)||!readable((void*)value(source),0x4fc))return;vt=(void*)value(source);
 if(!((bool(__thiscall*)(void*,unsigned))value(vt,0x3c))(source,0x111))return;
 auto name=((const wchar_t*(__thiscall*)(void*))value(vt,0x3ac))(source);
 if(!readable(name,128))return;wchar_t copy[64]{};unsigned n=0;
 for(;n<63&&name[n];n++){if(name[n]<32)return;copy[n]=name[n];}if(!n)return;
 if(_InterlockedCompareExchange(&damageLock,1,0))return;
 unsigned now=Time();if(!firstDamage)firstDamage=now;lastDamage=now;
 unsigned i=0;for(;i<8;i++)if(!damageRows[i].name[0]||sameName(copy,damageRows[i].name))break;
 if(i<8){if(!damageRows[i].name[0])memcpy(damageRows[i].name,copy,128);uint64_t v=damageRows[i].total;damageRows[i].total=UINT64_MAX-v<(uint64_t)amount?UINT64_MAX:v+(uint64_t)amount;damageRows[i].hits++;damageRows[i].job=(source==roleActor&&haveSnapshot)?(int)u32(snapshot,148):-1;damageGeneration++;}
 _InterlockedExchange(&damageLock,0);
}
extern "C" __declspec(dllexport) __declspec(naked) void DamageGate(){__asm{
 pushfd
 pushad
 lea eax,[esp+40]
 push eax
 call RecordDamage
 add esp,4
 popad
 popfd
 push ebp
 mov ebp,esp
 mov eax,0567d43ch
 mov eax,dword ptr[eax]
 push 01edc0a8h
 ret}}
static bool rareSky(){
 if(!haveSnapshot||!(u16(snapshot,142)&1)||!roleActor||actor()!=roleActor)return false;
 for(unsigned i=0;i<8;i++)if(!u32(snapshot,48+i*4)||itemId(itemAt(3,i))!=u32(snapshot,48+i*4))return false;
 return true;
}
static bool radius200(void*a,void*drop){
 if(!rareSky()||a!=roleActor||!readable(drop,4))return false;
 void*vt=(void*)value(drop);if(!readable(vt,0x40)||!((bool(__thiscall*)(void*,unsigned))value(vt,0x3c))(drop,0x121))return false;
 int x=((int(__thiscall*)(void*))0x2677960)(a)-((int(__thiscall*)(void*))0x2677960)(drop);
 int y=((int(__thiscall*)(void*))0x2677970)(a)-((int(__thiscall*)(void*))0x2677970)(drop);
 return x>=-200&&x<=200&&y>=-200&&y<=200&&x*x+y*y<=40000;
}
// Native position predicate is used only by native loot candidate scans.
// Replaying its first six bytes keeps all original range rules otherwise.
extern "C" __declspec(dllexport) __declspec(naked) void PickupDistanceOriginal(){__asm{
 push ebp
 mov ebp,esp
 push ecx
 push esi
 mov esi,dword ptr[ebp+8]
 push 0287cf58h
 ret}}
extern "C" __declspec(dllexport) bool __fastcall PickupDistance(void*self,void*,void*a,void*drop){
 if(radius200(a,drop))return true;
 return ((bool(__thiscall*)(void*,void*,void*))&PickupDistanceOriginal)(self,a,drop);
}
// Filled by the builder into verified original executable padding. Its call
// to the native pickup function has the same native return-address provenance
// as the existing X-key path, and preserves that function's own checks.
#ifndef PICKUP_NATIVE_BRIDGE
#define PICKUP_NATIVE_BRIDGE 0x1edc163
#endif
static void autoPickup(){
 static unsigned last;unsigned now=Time();if(now-last<120||!foreground()||modal()||!roleActor||!readable(roleActor,0x64c0))return;last=now;
 void*candidate=(void*)value(roleActor,0x61e8);if(!readable(candidate,4))return;
 void*container=(void*)value(roleActor,0x64bc);
 if(!container){container=((void*(__cdecl*)(int))0x2531a80)(0);if(container)((void(__thiscall*)(void*,void*))0x276bd20)(roleActor,container);container=(void*)value(roleActor,0x64bc);}
 if(!readable(container,0x74))return;
 if(!PickupDistance(nullptr,nullptr,roleActor,candidate))return;
 // Use the native selected target: falling delay, ownership and capacity
 // remain the same as X pickup. The extension never moves actor or loot.
 ((void(__thiscall*)(void*,void*,bool))PICKUP_NATIVE_BRIDGE)(container,candidate,true);
}
static uint64_t div64(uint64_t n,uint64_t d){uint64_t q=0,r=0;if(!d)return 0;for(unsigned i=0;i<64;i++){r=(r<<1)|(n>>63);n<<=1;q<<=1;if(r>=d){r-=d;q|=1;}}return q;}
static unsigned fraction1000(uint64_t v,uint64_t total){if(!total)return 0;if(v>=total)return 1000;uint64_t r=0;unsigned out=0;for(unsigned i=0;i<1000;i++){if(r>=total-v){r-=total-v;out++;}else r+=v;}return out;}
static void compactNumber(uint64_t n,wchar_t*out){
 if(n>=100000000){uint64_t whole=div64(n,100000000);Format(out,L"%u.%02u亿",(unsigned)whole,(unsigned)div64(n-whole*100000000,1000000));}
 else if(n>=10000){uint64_t whole=div64(n,10000);Format(out,L"%u.%01u万",(unsigned)whole,(unsigned)div64(n-whole*10000,1000));}
 else Format(out,L"%u",(unsigned)n);
}
static void renderDamage(){

 if(!dpsFeed)return;S5DpsFrame f{};f.size=sizeof(f);
 if(_InterlockedCompareExchange(&damageLock,1,0))return;
 f.generation=damageGeneration;f.elapsed=lastDamage-firstDamage;
 if(haveSnapshot&&(u16(snapshot,142)&2))f.reserved=0x80000000u|(u32(snapshot,236)&0x7fffffffu);
 for(auto&r:damageRows)if(r.name[0]){auto&d=f.rows[f.count++];memcpy(d.name,r.name,128);d.damage=r.total;d.hits=r.hits;d.job=r.job;}
 // An enabled meter is visible before the first hit, with the actual local
 // character and a zero total rather than an empty, invisible panel.
 if(!f.count&&f.reserved){f.count=1;memcpy(f.rows[0].name,snapshot+160,64);f.rows[0].job=(int)u32(snapshot,148);}
 _InterlockedExchange(&damageLock,0);dpsFeed(&f);

}
static void*activeWindow(unsigned id){
 void*m=*(void**)0x56a3798;if(!readable(m,0xc640)||id>=0x4d8)return nullptr;
 auto h=(Byte*)m+(id*5+0x41)*4;auto b=value(h,4),e=value(h,8);
 if(e<b||e-b>64||(e-b)%4||!readable((void*)b,(unsigned)(e-b)))return nullptr;
 for(;b<e;b+=4){void*w=*(void**)b;if(readable(w,0x40))return w;}return nullptr;
}
#include "native_power_ui.h"
static void powerSidebar(){

 if(!powerCompute)return;S5PowerFrame f{};f.size=sizeof(f);f.uiWidth=*(int*)0x55b0318;f.uiHeight=*(int*)0x55b031c;auto&c=f.stats;
 void*peer=activeWindow(200);unsigned target=readable(peer,0x3bc)&&value(peer)==0x4cfd41c?*(unsigned short*)((Byte*)peer+0x38c):0;
 if(target==roleUnique)target=0;
 if(inspectedTarget!=target){inspectedTarget=target;inspectedAt=0;memset(inspectedSnapshot,0,sizeof(inspectedSnapshot));}
 const Byte*ps=target?inspectedSnapshot:snapshot;
 bool ready=target?(u32(ps,16)==target):haveSnapshot;
 void*w=target?peer:activeWindow(240);
 if(w&&ready&&roleActor){void*root=(void*)value(w,0x30);if(readable(root,0x84)){
  f.x=(int)value(root,0x7c);f.y=(int)value(root,0x80);f.width=(int)value(root,0x6c);f.height=(int)value(root,0x70);
  // Native 0335ECF0 sizes this root to the frame's actual tiled width.
  // CharacterInfo disables its base title/close controls in XUI; their
  // cached coordinates do not describe the visible character frame.
  f.visible=f.width>=160&&f.width<=850&&f.height>=200;
 }}
 if(ready){c.identityValid=true;c.equipmentValid=true;c.affixesValid=true;
 c.characterId=u32(ps,16);c.level=u32(ps,144);c.job=u32(ps,148);c.firstGrow=u32(ps,152);c.awakening=u32(ps,156);
 c.equippedItems=u32(ps,228);c.combatSlots=u32(ps,232);
 c.hpMax=u32(ps,104);c.mpMax=u32(ps,108);c.strength=u32(ps,112);c.intelligence=u32(ps,116);c.vitality=u32(ps,120);c.spirit=u32(ps,124);
 c.physicalAttack=u32(ps,128);c.magicalAttack=u32(ps,132);c.independentAttack=u32(ps,136);
 c.whiteDamageTenths=u16(ps,92);c.yellowDamageTenths=u16(ps,94);c.criticalDamageTenths=u16(ps,96);c.yellowAdditionalTenths=u16(ps,98);c.criticalAdditionalTenths=u16(ps,100);c.allAttackTenths=u16(ps,102);
 c.baseScore=u32(ps,84);c.equipmentScore=u32(ps,88);c.totalScore=u32(ps,80);c.baseValid=c.hpMax>=100&&c.baseScore!=0;memcpy(c.name,ps+160,64);memcpy(c.profession,ps+416,64);
 }
 f.equipmentV2=ready?u32(ps,224):0;
 static combat_power::CombatSnapshot town;static unsigned townEquipment;
 if(target){c.baseValid=ready&&readPeerStats(w,target,c);if(!c.baseValid)c.baseScore=c.totalScore=c.equipmentScore=0;}
 else if(w&&roleActor&&haveSnapshot&&!(u16(ps,142)&2)&&readPersonalStats(w,roleActor,c)){c.baseValid=true;town=c;townEquipment=f.equipmentV2;}
 else if(town.characterId==c.characterId&&town.baseValid){c=town;f.equipmentV2=townEquipment;}
 else {c.baseValid=false;c.baseScore=c.totalScore=c.equipmentScore=0;}
 nativePowerFrame(f,w);

}
void featureTick(){

 loadPlugins();
 static unsigned battle;
 unsigned current=haveSnapshot?u32(snapshot,236):0;
 if(damageRole!=roleActor||battle!=current){damageRole=roleActor;battle=current;clearDamage();}
 if(roleActor)autoPickup();powerSidebar();if(Time()-lastDamageRender>=100){lastDamageRender=Time();renderDamage();}

}
