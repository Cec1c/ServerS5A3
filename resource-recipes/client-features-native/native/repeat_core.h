#pragma once
// Raw DirectInput scans preserve keypad Enter, numpad and left/right keys.
// The host calls this only after a real physical-device read.
namespace fullrepeat {
typedef unsigned char Byte;
typedef unsigned Tick;
enum { KeyCount=256, IntervalMs=80 };
struct Config { unsigned magic,version; Byte enabled[KeyCount]; };
struct Legacy { unsigned magic,version; struct Slot {unsigned vk,enabled;} slot[10]; };
struct Run {Tick last; unsigned held,pulse,blocked;};
inline bool permitted(unsigned scan,const Byte* map) {
    // Windows / context / power keys remain system controls.
    return scan>0&&scan<256&&map[scan]&&scan!=219&&scan!=220&&scan!=221&&scan!=222&&scan!=223;
}
inline void defaults(Config& c){c.magic=0x32524b44;c.version=2;for(auto& b:c.enabled)b=0;}
inline bool valid(const Config& c,const Byte* map){
    if(c.magic!=0x32524b44||c.version!=2)return false;
    for(unsigned i=0;i<KeyCount;i++)if(c.enabled[i]>1||(c.enabled[i]&&!permitted(i,map)))return false;
    return true;
}
inline bool migrate(const Legacy& old,Config& c,const Byte* map){
    defaults(c);Config next;defaults(next);if(old.magic!=0x31524b44||old.version!=1)return false;
    for(auto& s:old.slot){
        if(s.enabled>1||s.vk>254||(!s.vk&&s.enabled))return false;
        if(s.enabled)for(unsigned scan=1;scan<KeyCount;scan++)if(map[scan]==s.vk&&permitted(scan,map))next.enabled[scan]=1;
    }
    c=next;return true;
}
inline void step(const Config& c,Run* runs,Tick now,bool active,const Byte* physical,Byte* current){
    for(unsigned scan=0;scan<KeyCount;scan++){
        Run& r=runs[scan];if(!c.enabled[scan]){r={};continue;}
        bool down=(physical[scan]&0x80)!=0;
        if(!active){r={now,0,0,down?1u:0u};continue;}
        bool emit=false;
        if(!down)r={now,0,0,0};
        else if(r.blocked){r.held=0;r.pulse=0;}
        else if(!r.held){r={now,1,1,0};emit=true;}
        else if(r.pulse)r.pulse=0;
        else if(Tick(now-r.last)>=IntervalMs){r.last=now;r.pulse=1;emit=true;}
        if(current)current[scan]=emit?0x80:0;
    }
}
}
