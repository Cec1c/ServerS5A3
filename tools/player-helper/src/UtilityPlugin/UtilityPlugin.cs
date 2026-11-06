using System.Text.Json;
using Arad.Helper.Contracts;
namespace Arad.Helper.Utility;
public sealed class UtilityPlugin : IHelperPlugin,IHelperCommandPlugin
{
    sealed record Settings(byte SellMask=3,byte DisjointMask=3);
    IHelperUtilityContext? context;Settings settings=new();UtilityReply? state,pending;string commitId="",message="等待进入角色";CancellationTokenSource? stop;Task? poll;IDisposable? subscription;InstanceIdentity? displayed;readonly SemaphoreSlim serial=new(1,1);bool statusFailed=true;string lastStatusMessage="";
    static string Id()=>Guid.NewGuid().ToString("N");
    public ValueTask StartAsync(IHelperContext context,string settingsJson)
    {
        this.context=context as IHelperUtilityContext??throw new InvalidOperationException("宿主不支持便捷功能");
        try{settings=JsonSerializer.Deserialize<Settings>(settingsJson)??new();}catch(JsonException){}
        if(settings.SellMask>127 || settings.DisjointMask>127)throw new InvalidDataException("品级配置无效");
        stop=CancellationTokenSource.CreateLinkedTokenSource(context.Stopping);subscription=context.Subscribe(()=>{if(context.SelectedInstance!=displayed){state=pending=null;message="等待关联当前角色";Publish();}});poll=Task.Run(Poll);Publish();return ValueTask.CompletedTask;
    }
    async Task Poll(){while(!stop!.IsCancellationRequested)try{await Task.Delay(3000,stop.Token);await serial.WaitAsync(stop.Token);try{if(context!.SelectedInstance is {} id){var reply=await context.UtilityAsync(new("status"));if(context.SelectedInstance==id){if(state?.Binding!=reply.Binding){pending=null;statusFailed=true;}if(statusFailed||lastStatusMessage!=reply.Message){lastStatusMessage=reply.Message;message=string.IsNullOrWhiteSpace(reply.Message)?"便捷功能已就绪":reply.Message;statusFailed=false;}state=reply;displayed=id;}}Publish();}catch(Exception e){statusFailed=true;message=e.Message;Publish();}finally{serial.Release();}}catch(OperationCanceledException){break;}}
    static string GradeText(byte mask)=>string.Join("、",new[]{(0,"普通"),(1,"高级"),(2,"稀有"),(3,"神器"),(6,"传说"),(4,"史诗"),(5,"勇者")}.Where(x=>(mask&(1<<x.Item1))!=0).Select(x=>x.Item2));
    void Publish(){if(context is null||context.Stopping.IsCancellationRequested)return;try{
        var fields=new List<PanelField>{new("出售品级",GradeText(settings.SellMask)),new("分解品级",GradeText(settings.DisjointMask))};
        List<HelperAction> actions;IReadOnlyList<HelperRow> rows=[];var toggles=new List<HelperToggle>();
        if(pending is not null){fields.Add(new("待确认",$"{pending.Count}件；金币变化 {pending.Gold:+0;-0;0}"));actions=[new("confirm","确认执行"),new("cancel","取消")];rows=(pending.Items??[]).Select(x=>new HelperRow($"row.{x.ListType}.{x.Slot}",x.Name,$"×{x.Count}")).ToArray();}

        else {
            foreach(var (bit,label) in new[]{(0,"普通"),(1,"高级"),(2,"稀有"),(3,"神器"),(6,"传说"),(4,"史诗"),(5,"勇者")}){
                toggles.Add(new("sell."+bit,"出售 · "+label,(settings.SellMask&(1<<bit))!=0));toggles.Add(new("disjoint."+bit,"分解 · "+label,(settings.DisjointMask&(1<<bit))!=0));
            }
            actions=[new("settings","保存品级"),new("sell","一键出售"),new("disjoint","一键分解"),new("repair","装备维修"),new("cargo","远程仓库")];
        }
        context.Publish(new("便捷功能",message,fields){EditorKey=$"{displayed?.Pid}:{state?.Binding}:{pending?.Token}",Actions=actions,Rows=rows,Toggles=toggles});
    }catch(InvalidOperationException){}}
    public async ValueTask ExecuteAsync(string action,IReadOnlyDictionary<string,bool> values)
    {
        await serial.WaitAsync(stop!.Token);try{
            if(action=="settings"){
                byte Mask(string prefix){int m=0;for(int bit=0;bit<7;bit++)if(values.GetValueOrDefault(prefix+bit))m|=1<<bit;return (byte)m;}
                settings=new(Mask("sell."),Mask("disjoint."));message="品级已保存；Alt+S 出售，Alt+D 分解，按当前条件直接执行";Publish();return;
            }
            var id=context!.SelectedInstance??throw new InvalidOperationException("请先进入角色");
            var current=await context.UtilityAsync(new("status"));if(context.SelectedInstance!=id)throw new InvalidOperationException("游戏窗口已变化");
            if(state?.Binding!=current.Binding){pending=null;}displayed=id;
            string binding=current.Binding;state=current;
            if(action.StartsWith("room.")){state=await context.UtilityAsync(new("room",Binding:binding,RequestId:Id(),Room:current.Room,Direction:action[5..]));message=state.Message;Publish();return;}
            if(action=="cancel"){pending=null;message="已取消";Publish();return;}
            if(action=="confirm"){
                if(pending?.Token is not {Length:32})throw new InvalidOperationException("请先预览");
                var result=await context.UtilityAsync(new("commit",Binding:binding,RequestId:commitId,Token:pending.Token??throw new InvalidDataException("缺少操作令牌")));pending=null;message=result.Message;
                Publish();return;
            }
            if(action=="cargo"){
                state=await context.UtilityAsync(new("warehouse-open",Binding:binding,RequestId:Id()));pending=null;message=state.Message;Publish();return;
            }
            if(action is not ("sell" or "disjoint" or "repair"))throw new InvalidOperationException("未知便捷功能");
            pending=await context.UtilityAsync(new("preview",Kind:action,Binding:binding,RequestId:Id(),RarityMask:action=="sell"?settings.SellMask:settings.DisjointMask,Slot:0));commitId=Id();message=pending.Message;
            if(action is "sell" or "disjoint") {var done=await context.UtilityAsync(new("commit",Binding:binding,RequestId:commitId,Token:pending.Token??throw new InvalidDataException("缺少操作令牌")));pending=null;message=done.Message;}
            Publish();
        }catch(Exception e){message=e.Message;Publish();throw;}finally{serial.Release();}
    }
    public async ValueTask StopAsync(){subscription?.Dispose();stop?.Cancel();if(poll is not null)try{await poll;}catch(OperationCanceledException){}context=null;}
    public string ExportSettings()=>JsonSerializer.Serialize(settings);
    public async ValueTask DisposeAsync(){await StopAsync();stop?.Dispose();}
}
