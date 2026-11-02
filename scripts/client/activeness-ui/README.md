# 每日活跃度原生窗口组件

该源码提供8项任务、30/50/100三档领取、持续可领奖提醒、窗口拖动与关闭、0x7f01请求/184字节状态消费，并保留已经公开的礼包窗口入口。服务端目录为 `internal/modules/dnf/activeness` 与 `internal/services/dnfbridge/private_activity*`。任务、奖励和图块映射必须与匹配PVF/NPK保持一致；未知配置不会伪装为可领奖。

在Windows x86 MSVC开发者命令提示符执行 `build.cmd`；也可把 `VCVARS32` 设置为安装的vcvars32.bat。输出是供静态集成的 `activity.payload`，不作为运行时DLL加载，编译过程不启动游戏。

`skin_mapping.h` 仅含已授权NPK的帧号、尺寸、颜色和控件名；不包含内嵌位图。`layout.h` 是 `Activeness.xui` 的UTF-8嵌入数组。经后续授权公开的历史活跃度帧映射及原始布局见 `../activeness-assets/`，无关图块不包含。两份NPK来源见仓库资源发布清单。

`native_contract.h` 固定真实原生入口与HUD标识；`native-contract.json`记录匹配输入。宿主必须核验完整客户端身份、挂接调用约定、现有通知和累计代码。这里仅构建所选组件，不提供将整个私有混合payload覆盖到新EXE的脚本。钱包合计预检和七分之一费用实现未包含；禁止用此payload直接覆盖仍引用它们的旧节。

`Verify-ClientImage.py <EXE>` 使用Windows `SEC_IMAGE_NO_EXECUTE`读取映像，不运行游戏，不代替功能或游戏实测。来源通知及其他累计修改由宿主保留；无需日常重复运行通知工具。

自动验证核对XUI/嵌入数组一致、协议目录和编译结果。游戏窗口及交互验收独立记录，源码公开不修改玩家更新通道。
