# 战力插件

`CombatPower.dll` 通过 `GameGaurd.dll` 加载。进角色后在个人信息页右侧显示战力侧栏。

## 功能

- 个人信息页打开时贴在右侧，显示总分、段位、基础分和装备加成。
- 职业名使用角色已有的觉醒名。
- 基础分来自城镇已结算属性，只在城镇显示。
- 装备分按当前穿戴的战斗槽、强化和增幅计算。
- 白字跨件相加；黄字、爆伤取最高项并加上追伤。第二页的黄追、爆追、全攻当前为 0%。
- 悬停段位区查看区间；「战力提升」打开攻略；标题栏 X 收成展开条；装备行箭头翻页。
- 可拖动，不挡住游戏内鼠标。
- 载入后聊天栏提示「战力插件已载入」。没有快捷键。

## 安装

1. 将 `CombatPower.dll` 和 `CombatPower.ini` 放到 `GameGaurd.dll` 所在目录。
2. 在同目录的 `GameGaurd.ini` 中写在 `GameNative.dll` 后面：

   ```ini
   [Plugins]
   Plugin5=CombatPower.dll
   ```

3. 将 `CombatPower.ini` 中的 `Enabled` 设置为 `1`。
4. 完整退出并重新启动客户端。

## 配置

```ini
[General]
Enabled=1
Debug=0
```

修改后需要重新启动客户端。

## 开发说明

使用 Visual Studio 2022 构建 `ClientPatch.sln` 的 `Release|Win32` 配置。改分数、词缀合并或段位阈值只编辑 `CombatPowerFormula.cpp`。
