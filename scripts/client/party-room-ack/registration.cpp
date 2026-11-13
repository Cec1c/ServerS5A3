#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <stdint.h>
#include <intrin.h>
#include "binding.h"
#pragma intrinsic(_InterlockedCompareExchange, _InterlockedExchange, _InterlockedIncrement)
#define API(name,addr) (*reinterpret_cast<decltype(&name)*>(addr))
#define Query API(VirtualQuery,0x6e85b18)
#define Thread API(GetCurrentThreadId,0x6e85a90)
#define Process API(GetCurrentProcessId,0x6e85a68)
#define WindowThread API(GetWindowThreadProcessId,0x6e8672c)
#define WindowOwner API(GetWindow,0x6e86728)
#define Visible API(IsWindowVisible,0x6e866f0)
#define ClientRect API(GetClientRect,0x6e866d8)
#define Enumerate API(EnumWindows,0x6f2106c)
#define Clock API(GetTickCount,0x6e85a24)
extern "C" void* __cdecl memset(void*p,int v,size_t n){auto b=(unsigned char*)p;while(n--)*b++=(unsigned char)v;return p;}
extern "C" void* __cdecl memcpy(void*d,const void*s,size_t n){auto a=(unsigned char*)d;auto b=(const unsigned char*)s;while(n--)*a++=*b++;return d;}
static bool equal(const void*a,const void*b,unsigned n){auto x=(const unsigned char*)a;auto y=(const unsigned char*)b;while(n--)if(*x++!=*y++)return false;return true;}
static unsigned word(const void*p,unsigned n=0){return *(unsigned*)((const char*)p+n);}
static unsigned get16(const unsigned char*p,unsigned n){return p[n]|unsigned(p[n+1])<<8;}
static unsigned get32(const unsigned char*p,unsigned n){return get16(p,n)|get16(p,n+2)<<16;}
static void put32(unsigned char*p,unsigned n,unsigned v){for(unsigned i=0;i<4;i++)p[n+i]=(unsigned char)(v>>(8*i));}
static bool readable(const void*p,unsigned n){MEMORY_BASIC_INFORMATION m{};if(!p||!Query(p,&m,sizeof(m))||m.State!=MEM_COMMIT||(m.Protect&(PAGE_GUARD|PAGE_NOACCESS)))return false;unsigned a=(unsigned)p,e=(unsigned)m.BaseAddress+m.RegionSize;return a<=e&&n<=e-a;}
enum { QuerySize=64, ReplySize=80, Opcode=0x7f14 };
enum Status { Waiting=1, MissingActor=2, WrongIdentity=3, InsertFailed=4, Registered=5, Stale=6, Expired=7 };
struct Mail { unsigned char query[QuerySize]; unsigned received,serial; };
struct Pending { unsigned char query[QuerySize]; unsigned serial,started,budget,next,epoch,attempts; void*room; void*registry; void*party; void*self; unsigned status; bool active,reply; };
struct State { volatile long lock,epoch; unsigned serial,uiThread; bool busy; Mail inbox[8]; Pending pending[8]; unsigned sweepEpoch,sweepStarted,sweepNext,sweepAttempts; };
static State& state(){return *reinterpret_cast<State*>(STATE_ADDRESS);}
static BOOL CALLBACK findWindow(HWND window,LPARAM out){DWORD pid=0;DWORD tid=WindowThread(window,&pid);RECT r{};if(pid==Process()&&!WindowOwner(window,GW_OWNER)&&Visible(window)&&ClientRect(window,&r)&&r.right>=400&&r.bottom>=300){*(unsigned*)out=tid;return FALSE;}return TRUE;}
static bool send(Pending&p){
 void*net=*(void**)0x57ed8d4;
 if(!readable(net,0x2bd069)||!*((unsigned char*)net+8)||*((unsigned char*)net+0x2bd068)||word(net,0x2bcc34)||word(net,0x2bcc2c))return false;
 unsigned char b[ReplySize]{};memcpy(b,p.query,QuerySize);b[2]='R';b[64]=(unsigned char)p.status;
 put32(b,68,p.epoch);put32(b,72,(unsigned)p.registry);put32(b,76,p.attempts);
 if(((int(__thiscall*)(void*,unsigned))0x374f280)(net,Opcode)<0)return false;
 ((void(__thiscall*)(void*,const void*,unsigned))0x374eeb0)(net,b,ReplySize);
 ((void(__cdecl*)())0x3750ac0)();return true;
}

// The audited NOTI120 helper preserves EBX and pops its two pinned arguments.
static __declspec(noinline) unsigned reconcile(unsigned slot,void*registry,void*party){
 unsigned result;
 __asm { push ebx
 mov ebx,slot
 push party
 push registry
 mov eax,PROCESS_SLOT
 call eax
 mov result,eax
 pop ebx }
 return result;
}
static bool member(void*registry,void*actor){return ((bool(__thiscall*)(void*,void*))0x2879c90)(registry,actor);}

// Local registration starts an independent bounded game-thread observer. It
// remains active after an ACK: a later room becoming ready does not need a new
// network packet to reconcile the already bound player objects.
static void sweep(){
 void*manager=*(void**)0x56a3768;void*local=*(void**)0x56a3758;
 if(!readable(manager,0x6c)||!readable(local,0x74))return;
 void*self=(void*)word(local,0x70);if(!readable(self,0x4fc))return;
 unsigned slot=8;for(unsigned i=0;i<8;i++)if(word(manager,0x48+i*4)==(unsigned)self){slot=i;break;}
 if(slot==8)return;
 unsigned result;void*registry;void*party;
 __asm { push ebx
 mov ebx,slot
 push 0
 push 0
 mov eax,PROCESS_SLOT
 call eax
 mov result,eax
 mov registry,ecx
 mov party,edx
 pop ebx }
 if(result!=2)return;
 for(unsigned i=0;i<8;i++)reconcile(i,registry,party);
}

static unsigned observe(Pending&p){
 auto &s=state();
 if(word((void*)0x57b9080))return WrongIdentity;
 void*ctrl=((void*(__cdecl*)())0x1fa2790)();
 if(!readable(ctrl,0xc0)||((unsigned(__thiscall*)(void*))0x2526b50)(ctrl)!=3)return p.room?Stale:Waiting;
 void*room=((void*(__thiscall*)(void*))0x2524fb0)(ctrl);
 if(!readable(room,0xc8))return p.room?Stale:Waiting;
 void*reg=(void*)word(room,0xc4);
 void*mgr=*(void**)0x56a3768;void*local=*(void**)0x56a3758;
 if(!readable(reg,0x14)||!readable(mgr,0x6c)||!readable(local,0x74))return Waiting;
 void*self=(void*)word(local,0x70);
 if(!readable(self,0x4fc)||word(mgr,0x48+p.query[5]*4)!=(unsigned)self)return WrongIdentity;
 void*party=(void*)word(self,0x4c8);void*own=(void*)word(self,0x4f8);
 if(!readable(party,0x78)||word(party,0x70)!=1)return WrongIdentity;
 if(!readable(own,0xcc)||own!=*(void**)0x57b6fb0)return Waiting;
 unsigned ns=word(party,0x74);
 if(ns&&ns!=(((unsigned(__thiscall*)(void*))0x2964680)(self)&255))return WrongIdentity;
 for(unsigned i=0;i<8;i++){
  unsigned uid=word(party,0x2c+i*4);if(uid==0xffff)uid=0;
  if(uid!=get16(p.query,24+i*2))return WrongIdentity;
 }
 if(get16((unsigned char*)self,0x20)!=get16(p.query,24+p.query[5]*2))return WrongIdentity;
 if(!member(reg,own)||word(own,0xa8)!=0x111||word(own,0xc8)!=(unsigned)reg)return Waiting;
 if(p.room&&(p.room!=room||p.registry!=reg||p.party!=party||p.self!=own||p.epoch!=(unsigned)s.epoch))return Stale;
 p.room=room;p.registry=reg;p.party=party;p.self=own;p.epoch=s.epoch;
 unsigned slot=p.query[6];void*wrapper=(void*)word(mgr,0x48+slot*4);
 if(!readable(wrapper,0x4fc))return MissingActor;
 if(get16((unsigned char*)wrapper,0x20)!=get16(p.query,24+slot*2)||word(wrapper,0x4c)!=1||word(wrapper,0x4c8)!=(unsigned)party)return WrongIdentity;
 void*actor=(void*)word(wrapper,0x4f8);
 if(!readable(actor,0xcc))return MissingActor;
 if(!member(reg,actor)){
  if(word(actor,0xa8)||word(actor,0xc8))return WrongIdentity;
  reconcile(slot,reg,party);
 }
 // Insertion may invoke callbacks. Recheck every pinned owner before an ACK;
 // the helper's generic 'handled' return is deliberately not success proof.
 if(p.epoch!=(unsigned)s.epoch||!readable(wrapper,0x4fc)||!readable(actor,0xcc)||!readable(room,0xc8)||!readable(party,0x78)||
    word(wrapper,0x4f8)!=(unsigned)actor||word(wrapper,0x4c8)!=(unsigned)party||own!=*(void**)0x57b6fb0||
    ((unsigned(__thiscall*)(void*))0x2526b50)(ctrl)!=3||((void*(__thiscall*)(void*))0x2524fb0)(ctrl)!=room||word(room,0xc4)!=(unsigned)reg||!member(reg,own))return Stale;
 for(unsigned i=0;i<8;i++){unsigned uid=word(party,0x2c+i*4);if(uid==0xffff)uid=0;if(uid!=get16(p.query,24+i*2))return Stale;}
 return member(reg,actor)&&word(actor,0xa8)==0x111&&word(actor,0xc8)==(unsigned)reg?Registered:InsertFailed;
}

extern "C" __declspec(dllexport) void __cdecl Tick(){
 auto&s=state();if(!s.uiThread)Enumerate(findWindow,(LPARAM)&s.uiThread);
 if(!s.uiThread||Thread()!=s.uiThread||s.busy)return;
 s.busy=true;unsigned now=Clock();
 if(s.epoch&&s.sweepEpoch!=(unsigned)s.epoch){s.sweepEpoch=s.epoch;s.sweepStarted=now;s.sweepNext=now;s.sweepAttempts=0;}
 if(s.sweepEpoch&&unsigned(now-s.sweepStarted)<15000&&s.sweepAttempts<60&&int(now-s.sweepNext)>=0){
  ++s.sweepAttempts;s.sweepNext=now+250;sweep();
 }
 if(_InterlockedCompareExchange(&s.lock,1,0)==0){
  for(unsigned i=0;i<8;i++){
   Mail&m=s.inbox[i];Pending&p=s.pending[i];
   if(m.serial&&m.serial!=p.serial){
    // Duplicate packets carry the same sequence and never extend a deadline.
    memset(&p,0,sizeof(p));memcpy(p.query,m.query,QuerySize);p.serial=m.serial;p.started=m.received;
    p.budget=get32(p.query,60);p.active=true;
   }
  }
  _InterlockedExchange(&s.lock,0);
 }
 for(unsigned i=0;i<8;i++){
  Pending&p=s.pending[i];if(!p.active)continue;
  if(unsigned(now-p.started)>=p.budget||p.attempts>=60){p.status=Expired;p.reply=true;}
  else if(!p.reply&&(!p.next||int(now-p.next)>=0)){
   ++p.attempts;p.status=observe(p);p.next=now+250;
   p.reply=p.status==Registered||p.status==Stale||p.status==WrongIdentity;
  }
  if(p.reply&&send(p))p.active=false;
  // A busy native transport must not keep an expired task alive forever.
  if(unsigned(now-p.started)>=p.budget+1000)p.active=false;
 }
 s.busy=false;
}

extern "C" __declspec(dllexport) int __cdecl Dispatch(unsigned char*frame){
 if(!readable(frame,16)||frame[0]!=1||get16(frame,1)!=Opcode)return ((int(__cdecl*)(unsigned char*))OLD_DISPATCH)(frame);
 if(frame[15]||get32(frame,3)!=16+QuerySize||word((void*)0x57f9ad4)!=QuerySize)return 1;
 auto*q=*(unsigned char**)0x57f9ad0;
 if(!readable(q,QuerySize)||!equal(q,"ARQ1",4)||q[4]!=1||q[5]>7||q[6]>7||q[5]==q[6]||q[7]||!get16(q,24+q[5]*2)||!get16(q,24+q[6]*2)||!get32(q,60)||get32(q,60)>15000)return 1;
 auto&s=state();if(_InterlockedCompareExchange(&s.lock,1,0)!=0)return 1;
 Mail&m=s.inbox[q[6]];
 if(!m.serial||!equal(m.query,q,QuerySize)){memcpy(m.query,q,QuerySize);m.received=Clock();m.serial=++s.serial;if(!m.serial)m.serial=++s.serial;}
 _InterlockedExchange(&s.lock,0);return 1;
}
extern "C" void __cdecl LocalRegistered(){_InterlockedIncrement(&state().epoch);}
static void* const PreviousRegistration=(void*)OLD_REG_HOOK;
static void* const PreviousGetter=(void*)OLD_GETTER;
static void* const PreviousGate=(void*)OLD_GATE;
extern "C" __declspec(dllexport) __declspec(naked) void RegisteredHook(){__asm{
 pushfd
 pushad
 test al,al
 jz done
 cmp dword ptr[ebp+0ch],111h
 jne done
 mov eax,[ebp+14h]
 cmp eax,dword ptr ds:[57b6fb0h]
 jne done
 call LocalRegistered
 done:
 popad
 popfd
 jmp dword ptr [PreviousRegistration]
}}
extern "C" __declspec(dllexport) __declspec(naked) void Getter(){__asm{
 pushfd
 pushad
 cld
 call Tick
 popad
 popfd
 jmp dword ptr [PreviousGetter]
}}
extern "C" __declspec(dllexport) __declspec(naked) void Gate(){__asm{
 cmp byte ptr[edi],1
 jne previous
 cmp word ptr[esp+4],7f14h
 jne previous
 mov eax,1
 ret 4
 previous:
 jmp dword ptr [PreviousGate]
}}
