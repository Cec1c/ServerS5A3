using System.Reflection;
using System.Runtime.CompilerServices;
using System.Runtime.Loader;
using Arad.Helper.Contracts;
[assembly: InternalsVisibleTo("Arad.Helper.Tests")]

namespace Arad.Helper.Runtime;

sealed class PluginLoadContext(VerifiedPackage package) : AssemblyLoadContext(isCollectible: true)
{
    protected override Assembly? Load(AssemblyName name)
    {
        if (name.Name == typeof(IHelperPlugin).Assembly.GetName().Name) return typeof(IHelperPlugin).Assembly;
        if (package.Images.TryGetValue(name.Name + ".dll", out var bytes)) return LoadFromStream(new MemoryStream(bytes, false));
        // Runtime framework assemblies resolve in the default context. Private
        // dependencies may only come from the verified package dictionary.
        if (name.Name is not null && (name.Name.StartsWith("System.", StringComparison.Ordinal) || name.Name is "System" or "netstandard" or "mscorlib")) return null;
        throw new FileNotFoundException("插件依赖不在已签名清单中。", name.Name);
    }
    protected override nint LoadUnmanagedDll(string name) => throw new NotSupportedException("首版插件不包含原生依赖。");
}

public sealed class InstanceModel
{
    readonly object gate = new();
    GameInstance[] instances = []; InstanceIdentity? selected;
    readonly Dictionary<long, Action> observers = []; long serial;
    public IReadOnlyList<GameInstance> Instances { get { lock (gate) return instances.ToArray(); } }
    public InstanceIdentity? Selected { get { lock (gate) return selected; } }
    public int SubscriptionCount { get { lock (gate) return observers.Count; } }
    public void Update(IEnumerable<GameInstance> value, InstanceIdentity? selection)
    {
        Action[] callbacks;
        lock (gate) { instances = value.ToArray(); selected = instances.Any(x => x.Identity == selection) ? selection : null; callbacks = observers.Values.ToArray(); }
        foreach (var callback in callbacks) callback();
    }
    public IDisposable Subscribe(Action action)
    {
        long id; lock (gate) { id = ++serial; observers.Add(id, action); }
        return new Registration(() => { lock (gate) observers.Remove(id); });
    }
}

sealed class Registration(Action stop) : IDisposable
{
    Action? value = stop;
    public void Dispose() => Interlocked.Exchange(ref value, null)?.Invoke();
}

public interface ILootBackend
{
    ValueTask<LootState> ReadAsync(InstanceIdentity instance, CancellationToken cancel);
    ValueTask<LootState> ApplyAsync(InstanceIdentity instance, long generation, string binding, LootPolicy policy, CancellationToken cancel);
    ValueTask<LootState> StopAsync(InstanceIdentity instance, long generation, string binding, CancellationToken cancel);
    void Revoke(long generation);
}
public interface IUtilityBackend { ValueTask<UtilityReply> ExecuteUtilityAsync(InstanceIdentity instance,long generation,UtilityRequest request,CancellationToken cancel); }
sealed class PluginContext(InstanceModel model, long generation, string[] capabilities, Action<HelperPanel> publish, ILootBackend? loot,IUtilityBackend? utility) : IHelperLootContext,IHelperUtilityContext, IDisposable
{
    readonly object gate = new(); readonly CancellationTokenSource stopping = new(); readonly List<IDisposable> subscriptions = [];
    bool active = true; Task cancellation = Task.CompletedTask;
    public long Generation { get; } = generation;
    public CancellationToken Stopping => stopping.Token;
    void Demand(string capability) { if (!active || !capabilities.Contains(capability, StringComparer.Ordinal)) throw new InvalidOperationException("插件已停用或没有此能力。"); }
    public IReadOnlyList<GameInstance> Instances { get { lock (gate) { Demand("process.summary"); return model.Instances; } } }
    public InstanceIdentity? SelectedInstance { get { lock (gate) { Demand("process.summary"); return model.Selected; } } }
    public IDisposable Subscribe(Action changed)
    {
        lock (gate) {
            Demand("process.summary");
            var subscription = model.Subscribe(() => { lock (gate) { if (!active) return; } changed(); });
            subscriptions.Add(subscription); return subscription;
        }
    }
    public void Publish(HelperPanel panel)
    {
        lock (gate) {
            Demand("ui.panel");
            if (panel.Title is null || panel.Summary is null || panel.Fields is null || panel.Title.Length > 100 || panel.Summary.Length > 500 || panel.Fields.Count > 20) throw new InvalidDataException("面板内容超限。");
            // Never retain a plugin-defined collection in the host UI.
            var fields = panel.Fields.Select(f => new PanelField(f.Label, f.Value)).ToArray();
            if (fields.Any(f => f.Label is null || f.Value is null || f.Label.Length > 100 || f.Value.Length > 500)) throw new InvalidDataException("面板内容超限。");
            var toggles = panel.Toggles.Select(t => new HelperToggle(t.Key,t.Label,t.Value){Editable=t.Editable}).ToArray();
            var actions = panel.Actions.Select(a => new HelperAction(a.Key,a.Label)).ToArray();
            static bool Key(string key) => key.Length is > 0 and <= 40 && key.All(c => char.IsAsciiLetterOrDigit(c) || c is '.' or '-');
            if (panel.EditorKey.Length>200 || toggles.Length>20 || actions.Length>10 || toggles.Any(t=>!Key(t.Key)||t.Label.Length>60) || actions.Any(a=>!Key(a.Key)||a.Label.Length>40) || toggles.Select(t=>t.Key).Distinct().Count()!=toggles.Length || actions.Select(a=>a.Key).Distinct().Count()!=actions.Length) throw new InvalidDataException("面板控件超限。");
            var rows=panel.Rows.Select(r=>new HelperRow(r.Key,r.Label,r.Detail)).ToArray();if(rows.Length>72 || rows.Any(r=>!Key(r.Key)||r.Label.Length>160||r.Detail.Length>160))throw new InvalidDataException("列表内容超限。");
 publish(new(panel.Title, panel.Summary, fields) { EditorKey=panel.EditorKey, Toggles=toggles, Actions=actions,Rows=rows });
        }
    }
    InstanceIdentity LootInstance()
    {
        lock(gate) { Demand("loot.policy"); return model.Selected ?? throw new InvalidOperationException("请先选择游戏窗口。"); }
    }
    ILootBackend Loot => loot ?? throw new InvalidOperationException("拾取会话尚未连接，请重登游戏。");
    public ValueTask<LootState> ReadLootAsync() => Loot.ReadAsync(LootInstance(), Stopping);
    public ValueTask<LootState> ApplyLootAsync(string binding,LootPolicy policy) => Loot.ApplyAsync(LootInstance(),Generation,binding,policy,Stopping);
    public ValueTask<LootState> StopLootAsync(string binding) => Loot.StopAsync(LootInstance(),Generation,binding,Stopping);
    public ValueTask<UtilityReply> UtilityAsync(UtilityRequest request) { lock(gate){ Demand("player.utility");if(request.Mode=="warehouse-open")Demand("native.ui.warehouse");return (utility??throw new InvalidOperationException("便捷接口未连接")).ExecuteUtilityAsync(model.Selected??throw new InvalidOperationException("请先进入角色"),Generation,request,Stopping); } }
 public Task Revoke()
    {
        lock (gate) {
            if (!active) return cancellation;
            active = false; loot?.Revoke(Generation); foreach (var s in subscriptions) s.Dispose(); subscriptions.Clear();
            return cancellation = stopping.CancelAsync();
        }
    }
    public void Dispose() { _ = Revoke(); stopping.Dispose(); }
}

public sealed record PluginStatus(string Id, string Version, string State, long Generation);
public sealed class PluginManager(InstanceModel model, string publicKey, ILootBackend? loot = null, Func<string,string>? initialSettings = null,IUtilityBackend? utility = null) : IAsyncDisposable
{
    sealed record Loaded(PluginManifest Manifest, PluginLoadContext Load, IHelperPlugin Plugin, PluginContext Context);
    readonly SemaphoreSlim serial = new(1, 1); readonly Dictionary<string, Loaded> active = []; readonly List<Loaded> residents = [];
    long generation;
    bool disposed; bool frozen;
 public void Freeze(){frozen=true;}
    public event Action<string, HelperPanel?>? PanelChanged;
    public event Action<PluginStatus>? StatusChanged;
    public IReadOnlyList<WeakReference> UnloadedContexts => unloaded.ToArray(); readonly List<WeakReference> unloaded = [];
    public int ActiveCount => active.Count;
    internal IHelperContext ContextForTest(string id) => active[id].Context;
    [MethodImpl(MethodImplOptions.NoInlining)]
    Loaded Load(VerifiedPackage package)
    {
        var load = new PluginLoadContext(package);
        try {
            var assembly = load.LoadFromStream(new MemoryStream(package.Images[package.Manifest.Assembly], false));
            var type = assembly.GetType(package.Manifest.Type, throwOnError: true)!;
            if (!typeof(IHelperPlugin).IsAssignableFrom(type) || type.IsAbstract) throw new InvalidDataException("插件入口不符合SDK。");
            var plugin = (IHelperPlugin)(Activator.CreateInstance(type) ?? throw new InvalidDataException());
            var context = new PluginContext(model, Interlocked.Increment(ref generation), package.Manifest.Capabilities, p => PanelChanged?.Invoke(package.Manifest.Id, p), loot,utility);
            return new(package.Manifest, load, plugin, context);
        } catch { load.Unload(); throw; }
    }
    void State(Loaded p, string state) => StatusChanged?.Invoke(new(p.Manifest.Id, p.Manifest.Version, state, p.Context.Generation));
    async Task<string> Stop(Loaded p)
    {
        Task cancellation = p.Context.Revoke(); State(p, "Draining");
        try {
            await cancellation.WaitAsync(TimeSpan.FromSeconds(2));
            string settings = await Task.Run(p.Plugin.ExportSettings).WaitAsync(TimeSpan.FromSeconds(2));
            if (settings.Length > 4096) throw new InvalidDataException("插件设置超限。");
            await Task.Run(async () => { await p.Plugin.StopAsync(); await p.Plugin.DisposeAsync(); }).WaitAsync(TimeSpan.FromSeconds(2));
            p.Context.Dispose(); State(p, "Stopped");
            unloaded.RemoveAll(w => !w.IsAlive); if (unloaded.Count == 128) unloaded.RemoveAt(0);
            unloaded.Add(new WeakReference(p.Load)); p.Load.Unload(); State(p, "UnloadRequested");
            return settings;
        } catch {
            // Capability revocation is immediate. A timed-out managed task is
            // not forcibly aborted or claimed to be physically unloaded.
            residents.Add(p); State(p, "DisabledResident"); throw;
        } finally { PanelChanged?.Invoke(p.Manifest.Id, null); }
    }
    public async Task EnableAsync(string directory)
    {
        // Verify before touching an already running plugin.
        var package = Packages.Verify(directory, publicKey);
        await serial.WaitAsync();
        try {
            ObjectDisposedException.ThrowIf(disposed, this);
 if(frozen)throw new InvalidOperationException("插件已在启动时固定，请退出游戏后修改配置");
            if (residents.Any(p => p.Manifest.Id == package.Manifest.Id)) throw new InvalidOperationException("插件未完全停止，请重启内辅后再启用。");
            var next = Load(package); string settings = initialSettings?.Invoke(package.Manifest.Id) ?? "{}";
            try {
                if (active.Remove(package.Manifest.Id, out var old)) settings = await Stop(old);
                State(next, "Loaded");
                await Task.Run(async () => await next.Plugin.StartAsync(next.Context, settings)).WaitAsync(TimeSpan.FromSeconds(2));
                active.Add(package.Manifest.Id, next); State(next, "Running");
            } catch { try { await Stop(next); } catch { } throw; }
        } finally { serial.Release(); }
    }
    public async Task<string> ExportSettingsAsync(string id)
    {
        await serial.WaitAsync();try {if(!active.TryGetValue(id,out var p))return "{}";string value=p.Plugin.ExportSettings();return value.Length<=4096?value:"{}";}finally{serial.Release();}
    }
    public async Task ExecuteAsync(string id,string action,IReadOnlyDictionary<string,bool> values)
    {
        await serial.WaitAsync();
        try {
            if(!active.TryGetValue(id,out var p) || p.Plugin is not IHelperCommandPlugin commands) throw new InvalidOperationException("插件尚未启用。");
            await Task.Run(async()=>await commands.ExecuteAsync(action,values)).WaitAsync(TimeSpan.FromSeconds(25));
        } finally {serial.Release();}
    }
    public async Task DisableAsync(string id)
    {
        await serial.WaitAsync(); try { if(frozen)throw new InvalidOperationException("请退出游戏后修改插件配置"); if (active.Remove(id, out var p)) await Stop(p); } finally { serial.Release(); }
    }
    public async ValueTask DisposeAsync()
    {
        await serial.WaitAsync();
        try {
            disposed = true;
            foreach (var id in active.Keys.ToArray()) { var p = active[id]; active.Remove(id); try { await Stop(p); } catch { } }
        } finally { serial.Release(); }
        // DisabledResident tasks are retained for the remainder of this host
        // lifetime; disposing does not claim to cancel arbitrary plugin code.
    }
}
