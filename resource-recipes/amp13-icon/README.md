# 六种 +13 增幅券独立图标

六种增幅券 990027307–990027312 原来共用 consumption.img 第 844 帧的 +12 图标。此素材保留紫色卷轴底图，绘制金色 +13，输出透明 28×28 图标及独立 NPK，保留其他物品使用的共用帧。

安装 Pillow 后运行：

    python build_icon.py --output <不存在的图标输出目录>
    python make_plan.py --base <当前累计Script.pvf> --base-sha256 <核验后的完整摘要> --tool <匹配manifest的PVF-CLI> --output <不存在的计划输出目录>

第一条命令构建图标并执行 NPK 解包、像素逐字节回读；第二条只读取六个商品，生成仅修改图标路径及帧号的 schema 2 计划，不写 PVF。原图标条件不符时停止，不能覆盖其他后续改动。

资源路径保持 sprite/item/stackable/private_amp13/amp13.img，PVF 引用 Item/stackable/private_amp13/amp13.img 第 0 帧。生成 NPK 应为 1,902 字节，SHA256 为 c1add31403537464ffec73c6756ab25adb0a41553a2455660647db65855ae85a。原绘图输入与配方一并公开；完整累计 PVF 不随目录分发。

NPK 与修改后的 PVF 必须配套交付。manifest 的 validation 是原冻结配方的验证记录，deployed 不是当前服务器状态；构建回执与实际发布、游戏内显示验收分别记录。
