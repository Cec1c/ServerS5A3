#pragma once
// Pure state machine shared by the native adapter and executable tests.
// The caller supplies physical key state and the game's existing scan->VK map.
namespace keyrepeat {
typedef unsigned char Byte;
typedef unsigned int Tick;
enum { Slots=10, IntervalMs=80 };
struct Slot { unsigned vk, enabled; };
struct Config { unsigned magic, version; Slot slot[Slots]; };
struct Run { Tick last; unsigned held, pulse, blocked; };
inline bool permitted(unsigned k) {
    if(k<8 || k>254) return false;
    switch(k) { case 9: case 13: case 17: case 18: case 27: case 91: case 92:
    case 93: case 162: case 163: case 164: case 165: return false; }
    return true;
}
inline bool mapped(unsigned k,const Byte* map) {
    for(unsigned s=0;s<256;s++) if(map[s]==k) return true;
    return false;
}
inline bool valid(const Config& c,const Byte* map) {
    if(c.magic!=0x31524b44 || c.version!=1) return false;
    for(unsigned i=0;i<Slots;i++) {
        const Slot& s=c.slot[i];
        if(s.enabled>1 || (!s.vk&&s.enabled)) return false;
        if(s.vk && (!permitted(s.vk)||!mapped(s.vk,map))) return false;
        for(unsigned j=0;j<i;j++) if(s.vk&&s.vk==c.slot[j].vk) return false;
    }
    return true;
}
inline void defaults(Config& c) {
    c.magic=0x31524b44; c.version=1;
    for(unsigned i=0;i<Slots;i++) c.slot[i]={0,0};
}
inline void step(const Config& c,Run* run,Tick now,bool active,
                 const Byte* physical,Byte* current,const Byte* map) {
    for(unsigned i=0;i<Slots;i++) {
        const Slot& slot=c.slot[i]; Run& r=run[i];
        if(!slot.enabled || !permitted(slot.vk)) { r={0,0,0,0}; continue; }
        bool down=physical[slot.vk]!=0;
        if(!active) { r={now,0,0,down?1u:0u}; continue; }
        bool emit=false;
        if(!down) r={now,0,0,0};
        else if(r.blocked) { r.held=0; r.pulse=0; }
        else if(!r.held) { r.held=1; r.pulse=1; r.last=now; emit=true; }
        else if(r.pulse) r.pulse=0; // always a real release sample between presses
        else if(Tick(now-r.last)>=IntervalMs) { r.last=now; r.pulse=1; emit=true; }
        for(unsigned s=0;s<256;s++) if(map[s]==slot.vk)
            current[s]=emit?0x80:0;
    }
}
}
