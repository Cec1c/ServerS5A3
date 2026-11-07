# 原生金库桥接参考

本组件仅打开游戏原生金库窗口；金库资产移动仍由服务端原有协议和仓储事务处理。

当前适配的 DNF.exe SHA256：

```text
bb7022670b546ab200bc6f9718a47e51ff1e8cc78b4049231229db72b6aa3552
```

客户端是 x86，ImageBase 为 `0x00400000`。已取证的窗口号为 198；入口 VA `0x032bea50` 使用 `__thiscall(manager, 198, 0, 0)`，管理器地址 VA `0x056a3798`，工厂分支 VA `0x03366faa`，构造函数 VA `0x03504b50`。这些地址只适用于上述完整文件身份，不能仅凭 V211 名称复用。

Go broker 校验进程创建时间、路径、摘要、同用户/桌面及前台窗口，并以构建时固定的 DLL 摘要校验组件。DLL 使用 `WH_GETMESSAGE` 在游戏窗口线程执行一次固定操作，随机 nonce 防串台，完成后卸载 hook。接口不接受调用方指定的任意 DLL 或 RVA，不修改磁盘上的游戏文件。

```text
python tools/player-helper/native-ui/build.py --out /absolute/path/to/new-native-build --zig /path/to/zig
```

需要 Zig 0.13.0 和 Go。产物为 Windows x86 DLL、broker 及独立假游戏进程。假游戏 broker 只接受对应夹具摘要，不接受正式 DNF.exe。构建记录区分正式组件与夹具；构建成功不等于在 Windows 或游戏中执行成功。

完整接入约束见 [设计说明](../../../Docs/PLAYER_HELPER_REFERENCE.md)。
