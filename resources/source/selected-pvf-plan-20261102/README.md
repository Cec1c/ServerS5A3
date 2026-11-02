# PKG001 十三项 PVF 变更计划（公开提取）

本目录是 2026-11-02 经用户授权公开的 07-42 提取：保留 PKG001 的 6 项新增和 7 项修改，共 13 个 token 变更，以及重建所需的脚本、计划和固定摘要。内容是构建资料，不是完整 PVF，也不是部署包或游戏验收结果。

## 文件

- **build_pvf.py**：读取本目录 manifest.json 和 pvf-plans.zip，核对输入摘要后生成候选 PVF。
- **manifest.json**：公开构建字段。只保留基础 PVF、预期输出、计划归档和两项外部工具的 SHA256，以及 4 个计划成员的字节数和摘要。
- **pvf-plans.zip**：与源归档逐字节一致，SHA256 为 021fb3e2074ade6fc0d270f227e8a063e48bed222749fd299533b68a09aae86c。
- **SHA256SUMS**：本目录公开文件的摘要。

## 13 项 token

新增 6 项：

| 路径 | 内容 |
| --- | --- |
| equipment/character/common/title/private_akern_priest_400339004.equ | 艾肯守护者称号 |
| equipment/character/common/title/private_akern_priest_400339005.equ | 艾肯守护者称号（带地城条件） |
| stackable/private_aiken_exchange/990031101.stk | 一次性增幅器 (Lv1~90) |
| stackable/private_aiken_exchange/990031102.stk | 装备增幅保护券 |
| stackable/private_aiken_exchange/990031103.stk | 装备强化保护券 |
| stackable/private_aiken_exchange/990031104.stk | 高级装扮兑换券 (无期限) |

修改 7 项：

| 路径 | 用途 |
| --- | --- |
| etc/iteminfo.dat | 物品目录 |
| etc/(r)iteminfo.dat | 物品目录 |
| etc/(f)iteminfo.dat | 物品目录 |
| dungeon/metrocenter/special/special.dgn | 地城引用 |
| equipment/equipment.lst | 装备目录 |
| stackable/stackable.lst | 消耗品目录 |
| itemshop/legend_yoshua.shp | 艾肯兑换货架 |

## 构建

需要外部的“新增工具”和“补丁工具”，其 SHA256 必须分别匹配 manifest.json 的 additionToolSHA256 和 patchToolSHA256。这两个可执行文件没有随本提取公开，其公开来源也未核实。缺少匹配工具时不要替换成其他版本，也不要把本目录当作可直接重建当前线上 PVF 的完整工具包。

    python build_pvf.py --base <已核验的基础PVF> --addition-tool <哈希匹配的新增工具> --patch-tool <哈希匹配的补丁工具> --output <新的空目录>

脚本需要 Python 3.11 或更高版本。它在输出目录写入候选文件、工具回执和 result.json，不上传、不部署、不启动游戏。

expectedPVFSHA256 是写入该计划时的 PKG001 累计候选摘要，不是 07-40 另行公开的完整 PVF 摘要。两者用途不同，不能互相替代。

## 公开边界

本提取不含客户端 EXE 或费用源码、寄售或钱包代码、七分之一费用、生产和部署配置、GM 索引、发布脚本、私有 Git 历史、玩家数据、日志、凭据、绝对机器路径或工具二进制。计划内容只含 PVF token 数据和文件路径。

是否与当前服务端或客户端规则兼容，需要另行按完整输入核对；本目录不声称已构建、已部署或已在游戏中验收。
