using System.Text.Json;
using Arad.Helper.Contracts;
namespace Arad.Helper.Host;
sealed record UtilitySettings(byte SellMask=0,byte DisjointMask=0);
sealed record StartupConfiguration
{
    public int Format{get;init;}=1;
    public string[] EnabledPlugins{get;init;}=[];
    public LootPolicy Pickup{get;init;}=new(false,0,0);
    public UtilitySettings Utilities{get;init;}=new();
    public Dictionary<string,string> Hotkeys{get;init;}=new(){["panel"]="Ctrl+Z",["pickup"]="Alt+Q",["sell"]="Alt+S",["disjoint"]="Alt+D",["warehouse"]="Alt+F",["repair"]="Alt+W",["room.left"]="Alt+Left",["room.right"]="Alt+Right",["room.up"]="Alt+Up",["room.down"]="Alt+Down"};
    public static StartupConfiguration Load(string root)
    {
        string dir=Path.Combine(root,"helper"),path=Path.Combine(dir,"config.json");Directory.CreateDirectory(dir);
        var options=new JsonSerializerOptions{PropertyNameCaseInsensitive=true,WriteIndented=true};
        if(!File.Exists(path)){
            var defaults=new StartupConfiguration();
            try{string old=Path.Combine(dir,"settings/autoloot.json");if(File.Exists(old))defaults=defaults with{Pickup=JsonSerializer.Deserialize<LootPolicy>(File.ReadAllText(old),options)??defaults.Pickup};}catch(JsonException){}
            using var stream=new FileStream(path,FileMode.CreateNew,FileAccess.Write,FileShare.None);JsonSerializer.Serialize(stream,defaults,options);
        }
        if(new FileInfo(path).Length>16384)throw new InvalidDataException("内辅配置过大");
        var c=JsonSerializer.Deserialize<StartupConfiguration>(File.ReadAllText(path),options)??throw new InvalidDataException("内辅配置为空");
        if(c.Format!=1||c.EnabledPlugins.Distinct().Count()!=c.EnabledPlugins.Length||c.EnabledPlugins.Length>8||c.EnabledPlugins.Any(x=>x.Length is <1 or >40 || !x.All(v=>char.IsAsciiLetterOrDigit(v)||v is '-' or '.'))||c.Pickup.EquipmentMask>127||c.Pickup.StackableMask>127||c.Utilities.SellMask>127||c.Utilities.DisjointMask>127)throw new InvalidDataException("内辅配置无效，请在游戏启动前检查 helper/config.json");
        _=GameHotkey.Parse(c.Hotkeys);return c;
    }
    public string PluginSettings(string id)=>id switch{"autoloot"=>JsonSerializer.Serialize(Pickup),"utilities"=>JsonSerializer.Serialize(Utilities),_=>"{}"};
}
