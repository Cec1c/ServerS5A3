#pragma once
// DirectInput distinguishes the keypad even with NumLock off. The game's
// general scanMap intentionally translates those scans to navigation keys.
namespace swap_hotkey {
inline bool modifier(unsigned vk) {
    return vk==0x10||vk==0x11||vk==0x12||(vk>=0xa0&&vk<=0xa5)||vk==0x5b||vk==0x5c;
}
inline unsigned key(unsigned scan,const unsigned char* map) {
    switch(scan) {
    case 71:return 0x67;case 72:return 0x68;case 73:return 0x69;
    case 75:return 0x64;case 76:return 0x65;case 77:return 0x66;
    case 79:return 0x61;case 80:return 0x62;case 81:return 0x63;
    case 82:return 0x60;case 83:return 0x6e;
    case 55:return 0x6a;case 74:return 0x6d;case 78:return 0x6b;case 181:return 0x6f;
    default:return scan<256?map[scan]:0;
    }
}
inline bool valid(unsigned bind) {
    const unsigned vk=bind&255;
    return vk&&!modifier(vk)&&vk!=0x1b&&vk!=0x23&&(bind&~0x700ffu)==0;
}
}
