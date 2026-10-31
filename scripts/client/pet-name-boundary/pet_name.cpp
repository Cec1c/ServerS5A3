// Isolated peer creature-name reader. No CRT, heap, imports or global state.
typedef unsigned char u8;
typedef unsigned short u16;
typedef unsigned int u32;

extern "C" __declspec(dllexport) int __stdcall ReadPeerPetName(
    void* reader, u16* destination, const u8* packet, u32 packetBytes) {
    if (!reader || !destination || !packet) return -1;
    destination[0] = 0;
    u8** cursor = (u8**)((u8*)reader + 0x20a024);
    const u8* source = *cursor;
    u32 begin = (u32)packet, current = (u32)source;
    if (packetBytes > 0xffffffffU - begin || current < begin ||
        current > begin + packetBytes || begin + packetBytes - current < 4) return -1;
    u32 length = *(const u32*)source;
    source += 4;
    *cursor = (u8*)source;
    if (length > begin + packetBytes - (u32)source) return -1;
    // The caller immediately reads alive/u32 serial/flags after this DSTR.
    if (begin + packetBytes - (u32)source - length < 6) return -1;
    // Consume the complete DSTR even when its display is shortened. The next
    // alive/serial/flags fields must keep their original byte offsets.
    *cursor = (u8*)source + length;
    if (length > 255) return -1;
    char raw[256];
    for (u32 index = 0; index < length; ++index) raw[index] = (char)source[index];
    raw[length] = 0;
    typedef const u16* (__cdecl *ConvertName)(const char*);
    const u16* wide = ((ConvertName)0x037e0ae0)(raw);
    if (!wide) return -1;
    u32 used = 0;
    while (used < 12 && wide[used]) {
        u16 ch = wide[used];
        if (ch >= 0xd800 && ch <= 0xdbff) {
            if (used + 1 >= 12 || wide[used + 1] < 0xdc00 || wide[used + 1] > 0xdfff) break;
            destination[used] = ch;
            destination[used + 1] = wide[used + 1];
            used += 2;
        } else {
            if (ch >= 0xdc00 && ch <= 0xdfff) break;
            destination[used++] = ch;
        }
    }
    destination[used] = 0;
    return (int)used;
}

extern "C" __declspec(dllexport) __declspec(naked) void PeerPetNameGate() {
    __asm {
        push dword ptr [ebp-4274h]
        push dword ptr [ebp-4268h]
        lea eax, [ebp-2ch]
        push eax
        push esi
        call ReadPeerPetName
        mov edx, 0329e75dh
        jmp edx
    }
}
