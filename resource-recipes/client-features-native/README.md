# 原生连发、换装、伤害统计与战力客户端

本目录公开完整键盘连发、六套换装、伤害统计、自己和他人战力侧栏、普通自动拾取、八件稀有装扮拾取范围及原生优惠券确认适配的源码、素材与精确输入构建器。功能与所选服务端变更见 [本批范围](../../Docs/SELECTED_PUBLICATION_20261113.md)。

参考插件来自 `https://gitgud.io/rewio/S4A21ClientPatch`，固定提交 `1402707de1a4d52a8d42a2a5fe330f0227a6d015`；保留原作者来源注释、公式版本及美术。两插件使用静态 CRT，只依赖 Windows 系统 DLL。当前 EXE 直接加载 `CombatPower.dll`、`DpsMeter.dll`，不需要新增 GameNative 或 Runner DLL。插件子目录中的可选 API 声明不是安装依赖。

## 精确输入与输出

`manifest.json` 记录四份客户端文件的完整 SHA-256 与长度。EXE 输入为 `a28601e349a65a1ec0356995b693a326620d81887031c17b76b5b73daaeb5216`，目标为 `6402835223a23f9e4300c57c175d1b990b78ab0eecc46bd03fd373d0893e73f4`。输入不是任意同名游戏文件。

前序公开复建链为 V212 `69ea3a8b…` → 已公开实验成长券 `250d78d2…` → `scripts/client/party-room-recovery` 的 `7970c5dd…` → `scripts/client/party-room-ack` 的 `71571e22…` → `scripts/client/raid-shared-rewards` 的 `a28601e3…`。每个构建器核对自身输入、原指令和范围外映射字节，输出到独立目录。

## 构建

在 MSVC x86 环境执行 `native/build.bat` 与 `build_plugins.bat`；脚本保留实际验证使用的工具链入口，可将入口调整到本机等价 MSVC x86 工具链。Python 构建器需要 `pefile`，原生验证另需 `unicorn` 和 Pillow。

```text
python native/build_candidate.py --base <a286-DNF.exe> --output <new-output-directory>
```

两插件输出分别为 `plugins/CombatPower/CombatPower.dll`、`plugins/DpsMeter/DpsMeter.dll`。安装四份配套文件时保留原客户端依赖和玩家自有 INI；源码中的默认 INI 是配置示例，不用于覆盖已保存的位置与偏好。

新界面素材包为 `assets/sprite_interface_private_features20261113.NPK`，安装位置为同名 `ImagePacks2/` 文件。其摘要在 manifest 中；原生 XUI、嵌入布局及插件源素材均随目录保留。

## 服务端及资源

服务端需要本批换装/战力查询、任务券、优惠券、组队与团本对应实现。V212 与后续保留双组协议的客户端需要 [Buff 双组配置](../../Docs/BUFF_GROUP_DEPLOYMENT.md)。

本次公开的资源修改配方在 [selected-features-20261113](../selected-features-20261113/README.md)。`originalCumulativePVFSHA256` 仅记录已验证累计客户端原配套资源的身份；所选资源配方有自己的输出摘要。运营者应按实际使用的资源与服务器规则验证组合。

## 验证边界

构建器检查原映射字节、导入、TLS、入口及列明钩子；验证脚本执行固定原生入口与显式外部叶子夹具。编译、摘要和离线原生验证分别记录，完整渲染、GPU与外网多人实机验收沿用各自范围。组队死亡显示仍有残余等待，首次冷进图模型和帝国返回偶发崩溃仍在观察。
