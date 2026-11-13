#include "repeat_core.h"
#include <cassert>
#include <cstdio>
int main(){
 using namespace fullrepeat;Byte map[256]{};map[30]=65;map[28]=13;map[156]=13;map[219]=91;
 Legacy old{};old.magic=0x31524b44;old.version=1;old.slot[0]={65,1};old.slot[1]={13,1};Config c{};
 assert(migrate(old,c,map));assert(valid(c,map)&&c.enabled[30]&&c.enabled[28]&&c.enabled[156]);assert(!c.enabled[219]);
 for(unsigned bad=0;bad<10;bad++){auto v=old;v.slot[bad]={255,1};assert(!migrate(v,c,map));for(auto x:c.enabled)assert(!x);}
 old.slot[2]={65,2};assert(!migrate(old,c,map));for(auto x:c.enabled)assert(!x);
 defaults(c);c.enabled[30]=1;c.enabled[156]=1;Run run[256]{};Byte raw[256]{},state[256]{};raw[30]=raw[156]=128;
 unsigned now=0xfffffff0;step(c,run,now,true,raw,state);assert(state[30]==128&&state[156]==128);
 step(c,run,now+1,true,raw,state);assert(!state[30]);step(c,run,now+80,true,raw,state);assert(state[30]==128);
 step(c,run,now+81,false,raw,state);step(c,run,now+200,true,raw,state);assert(!state[30]);
 raw[30]=0;step(c,run,now+201,true,raw,state);raw[30]=128;step(c,run,now+202,true,raw,state);assert(state[30]==128&&state[156]==0);
 std::puts("repeat migration atomicity, aliases, 32-bit tick wrap and resume gates passed");
}
