using Arad.Helper.Contracts;
namespace Arad.Helper.TestPlugin;

public sealed class StartFailure : IHelperPlugin
{
    public ValueTask StartAsync(IHelperContext context, string settingsJson) => throw new InvalidOperationException("explicit test failure");
    public ValueTask StopAsync() => ValueTask.CompletedTask;
    public string ExportSettings() => "{}";
    public ValueTask DisposeAsync() => ValueTask.CompletedTask;
}
public sealed class StopTimeout : IHelperPlugin
{
    public ValueTask StartAsync(IHelperContext context, string settingsJson) { context.Subscribe(() => { }); return ValueTask.CompletedTask; }
    public ValueTask StopAsync() => new(Task.Delay(8000));
    public string ExportSettings() => "{}";
    public ValueTask DisposeAsync() => ValueTask.CompletedTask;
}
