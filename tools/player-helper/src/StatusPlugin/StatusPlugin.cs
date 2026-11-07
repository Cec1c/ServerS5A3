using Arad.Helper.Contracts;
namespace Arad.Helper.Status;

public sealed class StatusPlugin : IHelperPlugin
{
    IHelperContext? context; IDisposable? subscription;
    public ValueTask StartAsync(IHelperContext value, string settingsJson)
    {
        context = value; subscription = value.Subscribe(Render); Render(); return ValueTask.CompletedTask;
    }
    void Render()
    {
        var all = context!.Instances; var selected = all.FirstOrDefault(x => x.Identity == context.SelectedInstance);
        context.Publish(selected is null
            ? new("冒险状态", all.Count == 0 ? "等待启动游戏" : "请选择一个游戏窗口", [new("运行实例", all.Count.ToString())])
            : new("冒险状态", selected.WindowTitle, [new("运行实例", all.Count.ToString()), new("当前进程", selected.Identity.Pid.ToString()),
                new("窗口状态", selected.Responsive switch { true => "正常响应", false => "暂未响应", _ => "尚未创建窗口" }),
                new("当前窗口", selected.Foreground ? "正在前台" : "位于后台"),
                new("客户端", selected.KnownClient ? "90 S5A3 · 已识别" : "未识别版本 · 仅显示进程状态") ]));
    }
    public string ExportSettings() => "{}";
    public ValueTask StopAsync() { subscription?.Dispose(); subscription = null; context = null; return ValueTask.CompletedTask; }
    public ValueTask DisposeAsync() => StopAsync();
}
