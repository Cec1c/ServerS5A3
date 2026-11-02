# selected-item-text-20261102

本归档对应本轮授权公开的 N02 资源：一份累计 PVF 与同代生成的六项 GM 索引。资源保持原始文件名和原始字节，未重新生成索引，记录为 reused=true。

## 资源身份

- PVF：Script.pvf
- PVF 字节数：126018619
- PVF SHA256：2e416c0b5464b7aa9ddbd3949f7294a9f971d2546a1ed3df354f2815dc43b29d
- GM 索引：六项，全部绑定同一 PVF 摘要。
- 资源标识：selected-item-text-20261102-2e416c0b

## 本轮变化

上一版累计 PVF SHA256 95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c（126018482 字节）已在公开的 resources-selected-20261101 发布。本轮 PVF 为 126018619 字节，仅包含 3 个条目、4 段文本变化：

| 条目 | 名称 | PVF 路径 | 变化字段 |
|---|---|---|---|
| 490701733 | 黄金汪新春礼盒 | stackable/cash/chn_490701733.stk | name、explain |
| 10151892 | 一次性增幅器 (Lv1~90) | stackable/10151001/10151892.stk | explain |
| 990031101 | 一次性增幅器 (Lv1~90) | stackable/private_aiken_exchange/990031101.stk | explain |

其余 PVF 条目与载荷位保持。任务、职业和副职业索引的有效载荷经独立比较语义未变；这些文件因绑定新 PVF 摘要或目录地址而具有不同文件摘要。六项索引必须与本 PVF 配对使用。

## 下载包

- selected-item-text-20261102-pvf.zip：仅含 Script.pvf。
- selected-item-text-20261102-gm-indexes.zip：仅含六个 GM 索引文件。

两个 ZIP 的文件集合和每个条目的 SHA256 已在打包后重新读取核对。ZIP 自身的 SHA256 见 manifest.json。SHA256SUMS 记录七个数据文件的摘要，文件路径均相对 ZIP 根目录。

## 校验

Linux/macOS：

sha256sum -c SHA256SUMS

Windows PowerShell 可对每个文件执行：

Get-FileHash -Algorithm SHA256 .\Script.pvf

items.json 的 pvfChecksum、expert-jobs 载荷中的 pvfChecksum，以及四个二进制索引头部均绑定 2e416c0b5464b7aa9ddbd3949f7294a9f971d2546a1ed3df354f2815dc43b29d。pvf-directory.bin 的目录条目数为 1126438，内嵌 PVF 字节数为 126018619，目录表校验通过。

## 边界

本归档不包含源码、EXE、登录器、NPK、频道表、部署配置或地址、原始日志、玩家数据、账号、凭据、签名资料或私有历史。索引生成工具及其私有资料未随包提供，本归档只包含已核验产物。本次文本生成配方由主控另行公开。

本包不代表新增 EXE、频道或 NPK，也不代表下游全部机制均已公开；本任务未执行部署、玩家通道更新或游戏验收。

## 发行下载

GitGud 提供 32MiB 分卷，`downloads.json` 列出每个分卷及合并后归档摘要。下载全部分卷和本目录 `join_resources.py`、`downloads.json` 后运行校验合并工具；工具只输出 ZIP，不安装游戏。GitHub 同标签发行提供两份完整 ZIP。

- [GitGud 发行](https://gitgud.io/16ww/servers5a3/-/releases/resources-selected-20261102-item-text)
- [GitHub 完整 ZIP](https://github.com/Cec1c/ServerS5A3/releases/tag/resources-selected-20261102-item-text)
