# DPS 伤害统计插件

`DpsMeter.dll` 通过 `GameGaurd.dll` 加载。独立窗口，统计地下城伤害。

## 功能

- 统计普通攻击和技能造成的伤害。
- 按总伤害从高到低显示最多 8 名角色。
- 显示角色名、总伤害和伤害占比。
- 根据本人及队伍成员的基础职业显示对应的官方样式排行条颜色；转职和觉醒沿用所属基础职业颜色。
- 切换角色或开始新一轮战斗时自动清空旧数据。
- 没有伤害数据时自动隐藏窗口。
- 支持拖动窗口并自动保存位置，不挡住游戏内鼠标。
- 支持 OBS 游戏捕获。
- 载入后聊天栏提示「DPS插件已载入」。`WindowHotkey` 开关提示「DPS已开启」或「DPS已关闭」。

## 安装

1. 将 `DpsMeter.dll` 和 `DpsMeter.ini` 放到 `GameGaurd.dll` 所在目录。
2. 在同目录的 `GameGaurd.ini` 中注册，写在未占用的下一项：

   ```ini
   [Plugins]
   Plugin4=DpsMeter.dll
   ```

3. 将 `DpsMeter.ini` 中的 `Enabled` 设置为 `1`。
4. 完整退出并重新启动客户端。

## 使用

进入地下城并造成伤害后，DPS 窗口会自动显示。光标在面板上时按住左键拖动，松开后保存相对于游戏客户区的位置。载入和开关会写聊天栏公告。

## 配置

```ini
[General]
Enabled=1
Debug=0
WindowHotkey=None
ClickThrough=0
AutoPosition=1
OffsetX=24
OffsetY=120
Scale=100
```

修改配置后需要重新启动客户端。拖动窗口产生的位置更新会自动写入配置文件。

## 开发说明

使用 Visual Studio 2022 构建 `ClientPatch.sln` 的 `Release|Win32` 配置。
