using System.Text.Json;
using Arad.Helper.Contracts;

namespace Arad.Helper.Loot;
public sealed class LootPlugin : IHelperPlugin,IHelperCommandPlugin
{
    IHelperLootContext? context;CancellationTokenSource? stopping;Task? loop;IDisposable? subscription;
    readonly SemaphoreSlim serial=new(1,1); readonly SemaphoreSlim wake=new(0,1);LootPolicy settings=new(true,(1<<3)|(1<<4)|(1<<6),(1<<0)|(1<<1));
    InstanceIdentity? displayed;LootState? state;string message="等待关联角色会话…";
    static readonly (int Bit,string Label)[] Grades=[(0,"普通"),(1,"高级"),(2,"稀有"),(3,"神器"),(6,"传说"),(4,"史诗"),(5,"勇者")];
    public ValueTask StartAsync(IHelperContext context,string settingsJson)
    {
        this.context=context as IHelperLootContext??throw new InvalidOperationException("宿主不支持拾取能力。");
        try {var saved=JsonSerializer.Deserialize<LootPolicy>(settingsJson);if(saved is not null && saved.EquipmentMask<=127 && saved.StackableMask<=127)settings=saved;}catch(JsonException){ }
        stopping=CancellationTokenSource.CreateLinkedTokenSource(context.Stopping);subscription=context.Subscribe(()=>{if(this.context.SelectedInstance!=displayed){state=null;message="等待关联角色会话…";if(wake.CurrentCount==0)wake.Release();}Publish();});
        Publish();loop=Task.Run(Poll);return ValueTask.CompletedTask;
    }
    async Task Poll()
    {
        while(stopping?.IsCancellationRequested==false)try {
            await serial.WaitAsync(stopping.Token);
            try {var id=context!.SelectedInstance;if(id is not null){displayed=id;var next=await context.ReadLootAsync();if(context.SelectedInstance==id){displayed=id;state=next;message=next.Message;}}else{displayed=null;state=null;message="请先选择游戏窗口。";}Publish();}
            catch(Exception e)when(!stopping.IsCancellationRequested){message=Readable(e);Publish();}
            finally{serial.Release();}
            await wake.WaitAsync(TimeSpan.FromSeconds(5),stopping.Token);
        }catch(OperationCanceledException)when(stopping.IsCancellationRequested){break;}
    }
    static string Readable(Exception e)=>e is HttpRequestException or OperationCanceledException?"连接暂时中断，正在重试；拾取仍受服务端租约保护。":e.Message;
    void Publish() { try { PublishCurrent(); } catch(InvalidOperationException) { } }
    void PublishCurrent()
    {
        if(context is null || context.Stopping.IsCancellationRequested)return;
        var selected=context.SelectedInstance;var s=selected==displayed?state:null;
        if(s is null){context.Publish(new("自动拾取",message,[new("会话","等待进入角色；从原登录器登录关联。")]){EditorKey="waiting"});return;}
        var toggles=new List<HelperToggle>{new("gold","金币",settings.Gold){Editable=false}};
        foreach(var (bit,label) in Grades)toggles.Add(new("equip."+bit,"装备 · "+label,(settings.EquipmentMask&(1<<bit))!=0){Editable=false});
        foreach(var (bit,label) in Grades)toggles.Add(new("stack."+bit,"材料/消耗品 · "+label,(settings.StackableMask&(1<<bit))!=0){Editable=false});
        context.Publish(new("自动拾取",message!=""?message:"品级配置在游戏启动时读取；Alt+Q开关拾取。",[
            new("当前角色",string.IsNullOrEmpty(s.CharacterName)?s.CharacterId:s.CharacterName),
            new("拾取状态",!s.Available?"服务端未开放":s.Paused?"已暂停，整理背包后重新应用":s.Enabled?"已开启":"未开启"),
            new("范围","当前房间、归属自己的合法怪物掉落")]){
            EditorKey=$"{selected!.Pid}:{selected.CreatedFileTime}:{s.Binding}",Toggles=toggles,
            Actions=s.Available?[new("apply","开启拾取"),new("stop","暂停拾取"),new("refresh","刷新")]:[new("refresh","刷新")]
        });
    }
    public async ValueTask ExecuteAsync(string action,IReadOnlyDictionary<string,bool> values)
    {
        await serial.WaitAsync(stopping!.Token);
        try {
            var id=context!.SelectedInstance;if(id is null || id!=displayed || state is null)throw new InvalidOperationException("游戏窗口或角色已变化，请等待刷新。");
            if(action=="toggle")action=state.Enabled?"stop":"apply";
 if(action=="apply"){
                var policy=settings;
                if(!policy.Gold && policy.EquipmentMask==0 && policy.StackableMask==0)throw new InvalidOperationException("请至少选择一项拾取内容。");
                state=await context.ApplyLootAsync(state.Binding,policy);settings=policy;message="已应用当前品级并开启自动拾取。";
            }else if(action=="stop"){state=await context.StopLootAsync(state.Binding);message="自动拾取已暂停。";}
            else if(action=="refresh"){state=await context.ReadLootAsync();message="";}
            else throw new InvalidOperationException("不支持此操作。");
            Publish();
        }catch(Exception e){message=Readable(e);Publish();throw;}
        finally{serial.Release();}
    }
    public async ValueTask StopAsync(){subscription?.Dispose();subscription=null;stopping?.Cancel();if(loop is not null)try{await loop;}catch(Exception)when(stopping?.IsCancellationRequested==true){ }context=null;}
    public string ExportSettings()=>JsonSerializer.Serialize(settings);
    public async ValueTask DisposeAsync(){await StopAsync();stopping?.Dispose();}
}
