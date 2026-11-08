# 十槽连发与原生 ESC 菜单（累计功能兼容恢复版）

在已累计的 UI-20261105-007 上恢复统计、排行榜与战令的共享入口兼容。007 改写了旧模块校验和临时挂接依赖的两处 CALL，导致模块拒绝初始化、统计入口退回小悦、战令入口失效。本版保留共享 CALL 原字节，改在其分发函数入口追加连发，并以 trampoline 重放经构建器校验的完整指令。按住已配置按键以 80ms 周期连发，松开、失焦、文本输入和设置窗口等状态保留原有输入保护。十个槽位与菜单显示选择按当前 Windows 用户保存；没有后台工作线程、全局键盘钩子或服务端协议扩展。

菜单入口使用原生条目表、网格排列、暖金色文字、悬停动画及编辑勾选框。原生 47 项、188 字节设置保存协议保持；新增类型 48 的显示开关单独保存，47 哨兵及其他未知类型继续拒绝。独立窗口保留原生拖动和关闭，修正实际换行宽度及三列表头。

## 精确来源与产物

- 输入是已经正式发布的群服 G211：`686eaa069ba3cc374520d249628efc9de89bf0892ad45ad1e4bd2d2206b08dc5`，112549888 字节，对应私有 `group-client-v211-town-gold-20261104`。
- 输出 EXE：`e142a7e6d530a0e369c722cb7fcc90c18f2d18884a343096d88336aed29f75e4`，112607232 字节。
- payload：`082ddd5c5839c5d42b7c785457a7a26b615cbc9d9f202ad540f956aafb1a1bdb`。MSVC x86 使用 `/Brepro`；正式合入后从最新权威主干重建并独立核对。
- 原映射字节只允许十一处明确挂接修改。原入口、导入、TLS 和累计显示代码保留；精确输出已登记城镇显示及分解显示能力，未放宽其他客户端。
- 交付声明保留 `ImagePacks2/sprite_interface_privatekeyrepeat002.NPK`，1328 字节，摘要 `3b22698ecb4cb1d066285654aefe447033eb784d33b042297b199fe9b3a1dc50`。007 菜单实际复用原生火铳素材；该历史小包仍按完整交付依赖保留，不宣称 007 渲染使用了它。

本目录仅含获授权公开的源码、配方和离线测试。独立测试服登录器、配置、玩家信息、原始客户端及运行日志不纳入仓库。

## 构建

使用 Windows MSVC x86、Python，安装 `pefile==2024.8.26`；离线执行检查另需 `unicorn==2.1.4`、`Pillow==13.0.0`，字体检查使用 Windows SimSun。先核对完整输入摘要，在新的输出目录运行：

```text
python client/generate_layout.py
client/build.cmd <输出目录>/build
python client/build_candidate.py <精确G211.exe> <输出目录>/build/key_repeat.payload <输出目录>/client/DNF.exe <输出目录>/client-build.json
```

素材配方只读取精确原生 `sprite_interface2_keyoption.NPK`（SHA256 `fc457ccb645c8372baa5d3cc512cc159ae556c4db76be6816c1475a26512468c`），保留原像素、补齐目录摘要，并拒绝覆盖已有输出：

```text
python assets/build_assets.py <原生keyoption.NPK> <新的输出NPK> <新的回执.json>
```

## 离线核验

`verify_native.py` 明确接收 payload、map、G211 EXE 和输出回执四个参数。其他核验通过环境变量 `KR_VERIFY_OUTPUT` 指向上述输出目录、`KR_BASE_EXE` 指向精确 G211；ABI 负例还要求 `KR_OLD002_BUILD` 指向原 002 的历史 payload/map，其摘要由 `verification_paths.py` 固定，不能用当前客户端冒充旧基线。

依次运行 `verify_native.py`、`verify_menu.py`、`verify_menu_integration.py`、`verify_menu_abi.py`、`verify_cumulative_hooks.py`。新增检查实际执行旧模块 CodeMatches、ChangeCall 和新旧分发代码，比较 trampoline 寄存器、标志、栈和参数，验证统计/排行/战令临时挂接安装恢复和重复安装拒绝。覆盖原生表和字符串构造、勾选框、隐藏保存和恢复、悬停绘制分支、94 项原设置状态、14 个类型门、002 原生登录栈错误负例、输入保护及窗口重开。图形与系统端点使用显式适配器，检查通过不等于真实游戏画面验收。

布局、核心状态机和素材源码输入未变，按摘要复用独立验证。变更后的完整EXE重新核对活跃度、日历、消耗品和Windows非执行映像。旧007实机验收不沿用为本恢复版本游戏验收。


本次公开授权及用户验收范围见 ../../Docs/SELECTED_PUBLICATION_20261108.md；历史恢复版待验收文字由该文档中的最新用户验收覆盖。
