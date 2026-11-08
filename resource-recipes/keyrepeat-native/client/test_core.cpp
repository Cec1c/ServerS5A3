#include "repeat_core.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
using namespace keyrepeat;
int main(){
 Byte map[256]={},physical[256]={},current[256]={};Run run[Slots]={};Config c;
 for(unsigned i=0;i<256;i++)map[i]=(Byte)i;
 defaults(c);assert(valid(c,map));
 c.slot[0]={65,1};c.slot[1]={66,1};physical[65]=physical[66]=1;current[70]=0x80;
 step(c,run,100,true,physical,current,map);assert(current[65]==128&&current[66]==128&&current[70]==128);
 step(c,run,101,true,physical,current,map);assert(!current[65]&&!current[66]);
 step(c,run,179,true,physical,current,map);assert(!current[65]);
 step(c,run,180,true,physical,current,map);assert(current[65]==128);
 physical[65]=0;step(c,run,181,true,physical,current,map);assert(!current[65]&&!current[66]);
 step(c,run,5000,true,physical,current,map);assert(!current[65]&&current[66]==128);
 step(c,run,5000,true,physical,current,map);assert(!current[66]); // no catch-up burst
 step(c,run,5001,false,physical,current,map);current[66]=128;
 step(c,run,5100,true,physical,current,map);assert(!current[66]); // held across focus change
 physical[66]=0;step(c,run,5110,true,physical,current,map);physical[66]=1;
 step(c,run,5120,true,physical,current,map);assert(current[66]==128);
 memset(run,0,sizeof(run));physical[65]=1;step(c,run,0xfffffff0u,true,physical,current,map);
 step(c,run,0xfffffff1u,true,physical,current,map);step(c,run,0x3fu,true,physical,current,map);assert(!current[65]);
 step(c,run,0x40u,true,physical,current,map);assert(current[65]==128); // tick wrap
 c.slot[1].vk=65;assert(!valid(c,map));c.slot[1].vk=17;assert(!valid(c,map));
 c.slot[1]={0,1};assert(!valid(c,map));c.slot[1]={0,0};assert(valid(c,map));
 c.slot[0].enabled=0;current[65]=128;step(c,run,6000,true,physical,current,map);assert(current[65]==128);
 defaults(c);memset(run,0,sizeof(run));for(unsigned i=0;i<Slots;i++){c.slot[i]={65+i,1};physical[65+i]=1;}
 assert(valid(c,map));step(c,run,1,true,physical,current,map);for(unsigned i=0;i<Slots;i++)assert(current[65+i]==128);
 for(unsigned tick=2;tick<100000;tick++) {step(c,run,tick,true,physical,current,map);for(unsigned i=0;i<Slots;i++)assert(current[65+i]==current[65]);}
 puts("PASS: immediate press, 80ms repeat, release frames, release stop, ten keys, untouched keys, focus rearm, no backlog, timer wrap, validation");return 0;
}
