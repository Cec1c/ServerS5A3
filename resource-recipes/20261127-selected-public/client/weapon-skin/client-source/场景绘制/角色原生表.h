// 从现装 90 客户端的共同角色 RTTI 层级生成。
#pragma once
struct NativeRoleTable { unsigned table, destructor; };
constexpr NativeRoleTable kNativeRoleTables[] = {
    {0x045d8404, 0x00993da0},
    {0x04683e3c, 0x00d92eb0},
    {0x046850e4, 0x00d9a120},
    {0x046ee6f4, 0x00febee0},
    {0x047408dc, 0x01341380},
    {0x0474996c, 0x01365680},
    {0x0474a8fc, 0x01366d40},
    {0x047ae45c, 0x0164e510},
    {0x047b67f4, 0x0166e510},
    {0x047b7734, 0x0166e670},
    {0x047babc4, 0x0167aaf0},
    {0x047bbb04, 0x0167b3e0},
    {0x047dbf0c, 0x0174c460},
    {0x047dce74, 0x0174c8b0},
    {0x0481741c, 0x01837db0},
    {0x0485c17c, 0x019b3290},
    {0x048b3d5c, 0x01bdf0b0},
    {0x04b44824, 0x02e05550},
    {0x04b462d4, 0x02e2dc70},
    {0x04b4b884, 0x02e88610},
    {0x04b54c6c, 0x02f15cb0},
    {0x04b5f58c, 0x02fa19d0},
    {0x04b66f24, 0x03001c90},
    {0x04b6cf04, 0x030b38d0},
    {0x04b6e68c, 0x030c5b70},
};

inline unsigned OriginalRoleDestructor(unsigned table) {
    for (const auto& role:kNativeRoleTables) if (role.table==table) return role.destructor;
    return 0;
}
