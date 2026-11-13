#pragma once
// Exact S5A3 CharacterInfoWindow: constructor 03350E20 pushes ID 0xF0.
// Controller +0x38/+0x3C stores two stat IDs and two value objects per row.
// Native 0334AD40 formats the final personal-panel values into value+0x14.
// This adapter reads those completed strings on the owning UI thread only.
static bool nativeStatText(const wchar_t*text,unsigned len,unsigned&out){
 if(!len||len>48||!readable(text,(len+1)*2)||text[len])return false;
 unsigned number=0;bool any=false;
 for(unsigned i=0;i<len;i++){
  wchar_t c=text[i];if(c==L'/'){number=0;any=false;continue;}
  if(c>=L'0'&&c<=L'9'){if(number>10000000)return false;number=number*10+(c-L'0');any=true;}
  else if(c!=L' '&&c!=L',')return false;
 }
 if(!any)return false;out=number;return true;
}
static bool nativeStatValue(void*valueObject,unsigned&out){
 if(!readable(valueObject,0x30))return false;Byte*s=(Byte*)valueObject+0x14;
 unsigned len=(unsigned)value(s,0x14),capacity=(unsigned)value(s,0x18);
 if(!len||len>48||capacity<len||capacity>4096)return false;
 const wchar_t*text=capacity>=8?(const wchar_t*)value(s,4):(const wchar_t*)(s+4);
 return nativeStatText(text,len,out);
}
static bool applyNativeStats(const unsigned*values,unsigned mask,combat_power::CombatSnapshot&c){
 if((mask&0x2ff)!=0x2ff||values[0]<100||values[0]>10000000||values[1]>10000000)return false;
 for(unsigned i=2;i<10;i++)if(values[i]>1000000)return false;
 c.hpMax=values[0];c.mpMax=values[1];c.strength=values[2];c.vitality=values[3];c.intelligence=values[4];c.spirit=values[5];c.physicalAttack=values[6];c.magicalAttack=values[7];c.independentAttack=values[9];return true;
}
struct PeerStats{void*window;void*role;unsigned target,epoch,mask,values[10];};
static PeerStats peerStats;
static volatile long peerStatsLock;
// Native 0349B6D0 owns the complete inspected actor, applies its equipment,
// passives and town multipliers, and renders a final string for each stat.
// Capture that exact result rather than reconstructing a partial PVF score.
extern "C" __declspec(dllexport) void __cdecl CapturePeerStat(void*w,unsigned id,const wchar_t*text){
 if(!roleActor||!roleUnique||id>9||id==8||!readable(w,0x390)||value(w)!=0x4cfd41c)return;
 unsigned target=*(unsigned short*)((Byte*)w+0x38c);void*role=(void*)value(w,0x1bc);
 if(!target||target==roleUnique||!role)return;
 if(_InterlockedCompareExchange(&peerStatsLock,1,0))return;
 if(peerStats.window!=w||peerStats.role!=role||peerStats.target!=target||peerStats.epoch!=epoch){memset(&peerStats,0,sizeof(peerStats));peerStats.window=w;peerStats.role=role;peerStats.target=target;peerStats.epoch=epoch;}
 if(!readable(text,98)){_InterlockedExchange(&peerStatsLock,0);return;}unsigned n=0;while(n<49&&text[n])n++;
 unsigned result;if(nativeStatText(text,n,result)){peerStats.values[id]=result;peerStats.mask|=1u<<id;}else peerStats.mask&=~(1u<<id);
 _InterlockedExchange(&peerStatsLock,0);
}
extern "C" __declspec(dllexport) __declspec(naked) void PeerStatGate(){__asm{
 pushfd
 pushad
 lea eax,[ebp-0810h]
 push eax
 push dword ptr[ebp+8]
 push esi
 call CapturePeerStat
 add esp,12
 popad
 popfd
 lea eax,[ebp-0810h]
 push 0349efb5h
 ret}}
// Independent attack (ID 9) bypasses 349EFAF: 349CA6A replaces ESI
// with its formatted text, then jumps directly to 349EFBC. Capture while
// ESI still owns the inspected window; replay the displaced LEA unchanged.
extern "C" __declspec(dllexport) __declspec(naked) void PeerIndependentStatGate(){__asm{
 pushfd
 pushad
 lea eax,[ebp-0810h]
 push eax
 push 9
 push esi
 call CapturePeerStat
 add esp,12
 popad
 popfd
 lea esi,[ebp-0810h]
 push 0349ca70h
 ret}}
static bool readPeerStats(void*w,unsigned target,combat_power::CombatSnapshot&c){
 if(_InterlockedCompareExchange(&peerStatsLock,1,0))return false;PeerStats seen=peerStats;_InterlockedExchange(&peerStatsLock,0);
 return w&&seen.window==w&&seen.target==target&&seen.epoch==epoch&&readable(w,0x1c0)&&seen.role==(void*)value(w,0x1bc)&&applyNativeStats(seen.values,seen.mask,c);
}
static bool readPersonalStats(void*w,void*role,combat_power::CombatSnapshot&c){
 if(!readable(w,0x1e0))return false;void*controller=(void*)value(w,0x1dc);
 if(!readable(controller,0x40)||value(controller,0xc)!=(uintptr_t)role)return false;
 uintptr_t b=value(controller,0x38),e=value(controller,0x3c);
 if(e<=b||(e-b)%4||e-b>256||!readable((void*)b,(unsigned)(e-b)))return false;
 unsigned values[10]{},mask=0;
 for(;b<e;b+=4){void*row=*(void**)b;if(!readable(row,16))return false;
  for(unsigned side=0;side<2;side++){unsigned id=(unsigned)value(row,side*4);if(id>9||id==8)continue;
   unsigned n;if(nativeStatValue((void*)value(row,8+side*4),n)){values[id]=n;mask|=1u<<id;}}
 }
 return applyNativeStats(values,mask,c);
}
