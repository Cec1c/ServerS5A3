// 从现装武器目录生成。
constexpr unsigned kWeaponCatalogCount = 13329;
constexpr unsigned kWeaponCatalogChecksum = 0xc8730e57;
constexpr unsigned kWeaponCatalogAddress = 0x071f1000;
constexpr unsigned kNewLookBase = 101690003;
constexpr unsigned kNewMagicBase = 10309003;
constexpr unsigned kNewSecretBase = 490013003;
constexpr unsigned kNewWeaponCount = kWeaponCatalogCount - 3;
using WeaponCatalogArray = const WeaponLook[kWeaponCatalogCount];
#define kWeaponCatalog (*reinterpret_cast<WeaponCatalogArray*>(kWeaponCatalogAddress))
unsigned WeaponLookPosition(unsigned id) {
    if (id - kNewLookBase < kNewWeaponCount) return id - kNewLookBase + 3;
    for (unsigned i=0;i<3;++i) if (kWeaponCatalog[i].id == id) return i;
    return kWeaponCatalogCount;
}
