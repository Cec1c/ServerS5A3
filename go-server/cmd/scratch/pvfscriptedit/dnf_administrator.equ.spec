# pvfscriptedit spec：由现有 type-1 脚本文档导出
# 列：type\tkind\tvalue[\texpected_magic][\t# 注释]
# kind=key(type3)/str(type6) 的文本必须存在于 ANSI 字符串池，否则写入时报错
3	key	[name]	3028
6	raw	8983435	# text="DNF运营商"
3	key	[grade]	3068
0	int	1
3	key	[rarity]	142608
0	int	2
3	key	[usable job]	3118
6	str	[all]	3499716
3	key	[/usable job]	3188
3	key	[attach type]	3216
6	str	[trade]	3244
3	key	[minimum level]	3260
0	int	1
3	key	[icon]	3292
6	str	Item/Title/title.img	55167280
0	int	46
3	key	[equipment type]	3366
6	str	[title name]	23869826
0	int	1
3	key	[move wav]	4068
6	str	NAME_TOUCH	56231926
3	key	[weight]	3497460
0	int	1
3	key	[dictionary text]	3511880
6	raw	2820	# text=""
3	key	[custom animation]	92875256
6	str	TitleAnimation/dnf_administrator.ani	93469922
3	key	[/custom animation]	92875374
3	key	[room list move speed rate]	993436
2	raw	1065353216	# float32 1
# ===== 追加：六个战斗属性段（父任务指定 key，数值 999999999）=====
# 说明：[independent attack] 不存在于 ANSI 字符串池，改用 equipmentStatNumber
# 同样接受的 [separate attack]（equipment_stats.go:169）。
3	key	[physical attack]	142670
0	int	999999999
3	key	[magical attack]	142744
0	int	999999999
3	key	[separate attack]	3423614
0	int	999999999
3	key	[physical defense]	142706
0	int	999999999
3	key	[magical defense]	142778
0	int	999999999
3	key	[HP MAX]	1463368
0	int	999999999
