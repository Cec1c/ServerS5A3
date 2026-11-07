namespace Arad.Helper.Contracts;

public sealed record InstanceIdentity(int Pid, long CreatedFileTime, string ExecutablePath, string GameId);
public sealed record GameInstance(InstanceIdentity Identity, string WindowTitle, string ExeSha256, bool KnownClient, bool Foreground, bool? Responsive);
public sealed record PanelField(string Label, string Value);
public sealed record HelperPanel(string Title, string Summary, IReadOnlyList<PanelField> Fields)
{
    public string EditorKey { get; init; } = "";
    public IReadOnlyList<HelperToggle> Toggles { get; init; } = [];
    public IReadOnlyList<HelperAction> Actions { get; init; } = [];
 public IReadOnlyList<HelperRow> Rows {get;init;}=[];
}

// Base SDK remains read-only. Optional loot capability uses ordinary player
// authorization held by the host; no raw tokens or pointers cross this API.
public interface IHelperContext
{
    long Generation { get; }
    CancellationToken Stopping { get; }
    IReadOnlyList<GameInstance> Instances { get; }
    InstanceIdentity? SelectedInstance { get; }
    IDisposable Subscribe(Action changed);
    void Publish(HelperPanel panel);
}

public interface IHelperPlugin : IAsyncDisposable
{
    ValueTask StartAsync(IHelperContext context, string settingsJson);
    ValueTask StopAsync();
    string ExportSettings();
}

public sealed record HelperToggle(string Key, string Label, bool Value) { public bool Editable {get;init;} = true; }
public sealed record HelperRow(string Key,string Label,string Detail);
public sealed record HelperAction(string Key, string Label);
public interface IHelperCommandPlugin
{
    ValueTask ExecuteAsync(string action, IReadOnlyDictionary<string, bool> values);
}
public sealed record LootPolicy(bool Gold, byte EquipmentMask, byte StackableMask);
public sealed record LootState(bool Available, string Binding, string CharacterId, string CharacterName, ulong Revision, bool Enabled, bool Paused, LootPolicy Policy, string Message);
public interface IHelperLootContext : IHelperContext
{
    ValueTask<LootState> ReadLootAsync();
    ValueTask<LootState> ApplyLootAsync(string binding, LootPolicy policy);
    ValueTask<LootState> StopLootAsync(string binding);
}

public sealed record UtilityRequest(string Mode,string Kind="",string Binding="",string RequestId="",string Token="",string Room="",string Direction="",byte RarityMask=0,byte ListType=0,short Slot=0,int Page=0);
public sealed record UtilityItem(byte ListType,short Slot,long ItemId,string Name,long Count,long Rarity);
public sealed record UtilityReply(bool Ok,string Binding,string Room,string Message,string? Token,string? Kind,int Count,long Gold,bool Committed,UtilityItem[]? Items,int Total);
public interface IHelperUtilityContext : IHelperContext { ValueTask<UtilityReply> UtilityAsync(UtilityRequest request); }
