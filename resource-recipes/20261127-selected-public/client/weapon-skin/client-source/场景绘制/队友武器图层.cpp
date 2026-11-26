// 现用 90 副本的 mode0 描述和画面里的队员角色可以位于不同记录。
using Byte = unsigned char;
using Object = Byte;
#include "场景入口.h"
#include "角色原生表.h"
constexpr unsigned roomVT = 0x49eca2c;
constexpr unsigned keyMask = 0xffff;
constexpr unsigned selectedBit = 1u << 16;

struct ParseState {
    const void* appearance;
    unsigned selected;
    bool hasAvatarRow;
};

struct Pending {
    Object* projection;
    unsigned selection;
    unsigned appearanceID;
};

struct Party {
    Object* local;
    Object* own;
    Object* record;
    Object* room;
    Object* roomManager;
};

struct Deferred {
    Party scope;
    Pending slots[8];
};

static_assert(sizeof(ParseState) == 12, "须匹配已安装的仓库解析区");
static_assert(sizeof(Deferred) == 0x74, "须放入已安装仓库状态节未使用的末尾");

template<class T>
T Native(unsigned address) {
    return reinterpret_cast<T>(address);
}

template<class T>
T Read(Object* object, unsigned offset = 0) {
    return *reinterpret_cast<const T*>(object+offset);
}

struct MemoryRegion {
    unsigned base, allocation, allocationProtect, size, state, protect, type;
};

bool Readable(const void* pointer, unsigned size) {
    unsigned address = reinterpret_cast<unsigned>(pointer);
    unsigned end = address+size;
    if (!address || end <= address) return false;
    auto query = *Native<unsigned (__stdcall**)(unsigned, MemoryRegion*, unsigned)>(0x7014008);
    while (address < end) {
        MemoryRegion region;
        if (!query(address,&region,sizeof(region)) || region.state != 0x1000 ||
                (region.protect & 0x101) || !(region.protect & 0xee)) return false;
        unsigned next = region.base+region.size;
        if (next <= address) return false;
        address = next;
    }
    return true;
}

Deferred& Queue() {
    return *reinterpret_cast<Deferred*>(0x7089f8c);
}

bool InDungeonContext() {
    return *Native<const Byte*>(0x568a519) == 0 && *Native<const Byte*>(0x568a51a) == 0;
}

bool ValidKey(unsigned key) {
    return key > 0 && key < keyMask;
}

int MemberSlot(const Party& party, unsigned key) {
    for (int index = 0; index < 8; ++index) {
        if (Read<unsigned>(party.record,0x2c+4*index) == key) return index;
    }
    return -1;
}

Object* ActiveEntry(unsigned key) {
    return Native<Object* (__cdecl*)(unsigned short)>(0x296d730)(static_cast<unsigned short>(key));
}

void RoomIdentity(Party& party) {
    party.room = nullptr;
    party.roomManager = nullptr;
    Object* context = *Native<Object**>(0x56a2250);
    if (!(*Native<const unsigned*>(0x56a2254) & 1) || !Readable(context,0x130)) return;
    Object* scene = Native<Object* (__thiscall*)(Object*)>(0x2524fb0)(context);
    if (!Readable(scene,0xc8) || Read<unsigned>(scene) != roomVT) return;
    Object* manager = Read<Object*>(scene,0xc4);
    if (!Readable(manager,0x114) || Read<Object*>(manager,0x110) != scene) return;
    party.room = scene;
    party.roomManager = manager;
}

bool CurrentParty(Party& party) {
    if (!InDungeonContext()) return false;
    Object* manager = *Native<Object**>(0x56a3758);
    if (!Readable(manager,0x74)) return false;
    party.own = Read<Object*>(manager,0x70);
    party.local = Native<Object* (__cdecl*)()>(0x29610e0)();
    if (!party.local || !Readable(party.own,0x500)) return false;
    const unsigned ownKey = Read<unsigned short>(party.own,0x20);
    party.record = Read<Object*>(party.own,0x4c8);
    if (!ValidKey(ownKey) || !Readable(party.record,0x4c) ||
            Read<Object*>(party.own,0x4f8) != party.local || MemberSlot(party,ownKey) < 0) return false;
    RoomIdentity(party);
    return true;
}

bool PendingExists() {
    for (const auto& item : Queue().slots) {
        if (item.selection) return true;
    }
    return false;
}

void Clear() {
    for (auto& item : Queue().slots) item.selection = 0;
    Queue().scope.own = nullptr;
}

bool SameParty(const Party& stored, const Party& current) {
    return stored.own == current.own && stored.local == current.local && stored.record == current.record &&
        (!stored.room || (stored.room == current.room && stored.roomManager == current.roomManager));
}

Object* PacketEntry(unsigned key) {
    return Native<Object* (__cdecl*)(unsigned short, Byte, Byte)>(0x1a5a5e0)(
        static_cast<unsigned short>(key),0,0);
}

Object* PeerEntry(const Party& party, unsigned key) {
    Object* entry = ActiveEntry(key);
    if (entry == party.own || !Readable(entry,0x500) ||
            Read<unsigned short>(entry,0x20) != key || Read<Object*>(entry,0x4c8) != party.record) return nullptr;
    return entry;
}

Object* LinkedPeer(const Party& party, Object* entry, unsigned key) {
    if (!entry) return nullptr;
    Object* link = Read<Object*>(entry,0x4fc);
    if (!Readable(link,0xac)) return nullptr;
    Object* role = Read<Object*>(link,0xa8);
    if (role == party.local || role != Read<Object*>(entry,0x4f8) || !Readable(role,0x6868) ||
            !OriginalRoleDestructor(Read<unsigned>(role)) || Read<unsigned>(role,0x6864) != key) return nullptr;
    return role;
}

void PriorApply(Object* role, Object* scene, const void* appearance) {
    Native<void (__cdecl*)(Object*,Object*,const void*)>(kApplySceneAppearance)(role,scene,appearance);
}

extern "C" __declspec(noinline) void __cdecl RouteAppearance(
        Object* role, Object* scene, const void* appearance) {
    const auto& parse = *Native<const ParseState*>(0x7089e00);
    if (!InDungeonContext()) {
        Clear();
        PriorApply(role,scene,appearance);
        return;
    }
    if (!parse.hasAvatarRow || parse.appearance != appearance || !Readable(scene,0xa4)) {
        PriorApply(role,scene,appearance);
        return;
    }
    const unsigned key = Read<unsigned short>(scene,0x20);
    if (role) {
        // 角色已经直接绑定时，新帧优先于同一角色早先留下的待应用值。
        for (auto& item : Queue().slots) {
            if ((item.selection & keyMask) == key) item.selection = 0;
        }
        PriorApply(role,scene,appearance);
        return;
    }
    Party party;
    if (!ValidKey(key) || !CurrentParty(party) || key == Read<unsigned short>(party.own,0x20) ||
            PacketEntry(key) != scene) {
        PriorApply(role,scene,appearance);
        return;
    }
    const int slot = MemberSlot(party,key);
    if (slot < 0) {
        PriorApply(role,scene,appearance);
        return;
    }
    auto& pending = Queue();
    if (!SameParty(pending.scope,party)) Clear();
    pending.scope = party;
    // 同一角色接到更新时先取消旧的待应用值，避免迟到的落位回调重放。
    pending.slots[slot].selection = 0;
    Object* entry = PeerEntry(party,key);
    if (Object* linked = LinkedPeer(party,entry,key)) {
        PriorApply(linked,entry,appearance);
        return;
    }
    const bool selected = parse.selected != 0;
    const unsigned appearanceID = Read<unsigned>(scene,0xa0);
    if (!selected || appearanceID == parse.selected) {
        auto& item = pending.slots[slot];
        item.projection = scene;
        item.appearanceID = appearanceID;
        item.selection = key | (selected ? selectedBit : 0);
    }
    PriorApply(role,scene,appearance);
}

void Replay(Object* arrivedEntry, Object* arrivedRole, Object* roster) {
    if (!PendingExists()) return;
    auto& pending = Queue();
    Party party;
    if (!CurrentParty(party) || !SameParty(pending.scope,party)) {
        Clear();
        return;
    }
    if (!party.room || (roster && roster != party.record)) return;
    pending.scope = party;
    for (int index = 0; index < 8; ++index) {
        auto& item = pending.slots[index];
        const unsigned selection = item.selection;
        if (!selection) continue;
        const unsigned key = selection & keyMask;
        if (Read<unsigned>(party.record,0x2c+4*index) != key ||
                PacketEntry(key) != item.projection ||
                !Readable(item.projection,0xa4) || Read<unsigned>(item.projection,0xa0) != item.appearanceID) {
            item.selection = 0;
            continue;
        }
        Object* entry = PeerEntry(party,key);
        if (arrivedEntry && entry != arrivedEntry) continue;
        Object* role = LinkedPeer(party,entry,key);
        if (!role || (arrivedRole && role != arrivedRole)) continue;
        item.selection = 0;
        Native<void (__cdecl*)(Object*,Object*,unsigned)>(kSelectAppearance)(role,entry,
            (selection & selectedBit) ? item.appearanceID : 0);
    }
}

extern "C" void __cdecl ReplayOnMode1(Object* entry,Object* role) {
    if (entry && role) Replay(entry,role,nullptr);
}

extern "C" void __cdecl ReplayOnRoster(Object* record) {
    if (record) Replay(nullptr,nullptr,record);
}

extern "C" __declspec(naked) void PartyAppearanceHook() {
    __asm {
        push [esp+8]
        push [esp+8]
        mov eax,022c0d50h
        call eax
        add esp,8
        pushfd
        pushad
        cld
        mov ebp,esp
        sub esp,210h
        and esp,-16
        fxsave [esp]
        push [ebp+44]
        push esi
        push [ebp+40]
        call RouteAppearance
        add esp,12
        fxrstor [esp]
        mov esp,ebp
        popad
        popfd
        ret
    }
}

extern "C" __declspec(naked) void PartyMode1Hook() {
    __asm {
        pushfd
        pushad
        cld
        mov ebp,esp
        sub esp,210h
        and esp,-16
        fxsave [esp]
        push edi
        push esi
        call ReplayOnMode1
        add esp,8
        fxrstor [esp]
        mov esp,ebp
        popad
        popfd
        push 07111720h
        ret
    }
}

extern "C" __declspec(naked) void PartyRosterHook() {
    __asm {
        pushfd
        cmp byte ptr [ebp-351h],0
        jne existing
        pushad
        cld
        mov ebp,esp
        sub esp,210h
        and esp,-16
        fxsave [esp]
        push ebx
        call ReplayOnRoster
        add esp,4
        fxrstor [esp]
        mov esp,ebp
        popad
    existing:
        popfd
        push 07111760h
        ret
    }
}

extern "C" void __cdecl PartyPayloadEntry() {}
