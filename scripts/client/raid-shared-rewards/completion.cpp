// Read-only completion predicate. Reward bindings and shared_ptr lifetimes
// remain owned by the native loop; other UI categories use its original path.
using U = unsigned int;
static U word(U p) { return *reinterpret_cast<const U*>(p); }
extern "C" __declspec(dllexport) int __cdecl OwnRootComplete(U root) {
    U category = word(root + 0x3bc);
    if (category > 2) return 2;
    U manager = word(0x056a3778), scene = word(0x056a3758);
    if (!manager || !scene) return 2;
    U self = word(scene + 0x70);
    if (!self) return 2;
    U id = *reinterpret_cast<const unsigned short*>(self + 0x20);
    if (!id || id == 0xffff) return 2;
    U raid = reinterpret_cast<U(__thiscall*)(void*)>(0x01a553e0)(reinterpret_cast<void*>(manager));
    if (!raid || word(raid + 0x24) > 2) return 2;
    U begin = word(raid + 0xa4), end = word(raid + 0xa8);
    if (!begin || end <= begin || (end - begin) % 0x64 || (end - begin) / 0x64 > 20) return 2;
    bool member = false;
    for (U p = begin; p < end; p += 0x64)
        if (*reinterpret_cast<const unsigned short*>(p) == id) member = true;
    if (!member) return 2;
    begin = word(root + 0x3c8); end = word(root + 0x3cc);
    if (end < begin || (end - begin) % 8 || (end - begin) / 8 > 40) return 2;
    if (begin == end) return category != 0 ? 1 : 0; // Optional gold/team pool.
    if (!begin) return 2;
    for (U p = begin; p < end; p += 8) {
        U row = word(p);
        if (row && word(row + 0x3e4) == id) return 1;
    }
    return 0; // Bound peers cannot substitute for this owner's pending row.
}
