using System.Runtime.InteropServices;
namespace Arad.Helper.Host;
sealed class GameHotkey : IDisposable
{
    readonly nint window;readonly Func<bool> eligible;readonly Action<string> invoke;readonly Dictionary<int,(string Command,uint Mods,uint Key)> keys=[];readonly HashSet<int> registered=[];readonly WinEvent callback;nint hook;bool disposed;
    public static IReadOnlyDictionary<string,(uint Mod,uint Key)> Parse(Dictionary<string,string> input)
    {
        var result=new Dictionary<string,(uint,uint)>();var seen=new HashSet<string>();
        foreach(var item in input){if(item.Key is not("panel" or "pickup" or "sell" or "disjoint" or "warehouse" or "repair" or "room.left" or "room.right" or "room.up" or "room.down"))throw new InvalidDataException("未知快捷键功能");var parts=item.Value.Split('+',StringSplitOptions.TrimEntries);if(parts.Length!=2||!parts[0].Equals(item.Key=="panel"?"Ctrl":"Alt",StringComparison.OrdinalIgnoreCase))throw new InvalidDataException("面板使用Ctrl，功能使用Alt组合键");uint key=parts[1].ToUpperInvariant() switch{"LEFT"=>0x25,"UP"=>0x26,"RIGHT"=>0x27,"DOWN"=>0x28,_=>parts[1].Length==1&&char.IsAsciiLetter(parts[1][0])?(uint)char.ToUpperInvariant(parts[1][0]):0u};uint mod=item.Key=="panel"?2u:1u;if(key==0||!seen.Add($"{mod}:{key}"))throw new InvalidDataException("快捷键无效或重复");result.Add(item.Key,(mod,key));}
        return result;
    }
    public GameHotkey(nint window,Func<bool> eligible,Dictionary<string,string> bindings,Action<string> invoke)
    {
        this.window=window;this.eligible=eligible;this.invoke=invoke;int id=0x4a21;foreach(var item in Parse(bindings))keys.Add(id++,(item.Key,item.Value.Mod,item.Value.Key));callback=(_,_,_,_,_,_,_)=>Refresh();hook=SetWinEventHook(3,3,0,callback,0,0,0);Refresh();
    }
    public void Refresh(){if(disposed)return;bool wanted=eligible();foreach(var pair in keys){if(!wanted&&registered.Remove(pair.Key))UnregisterHotKey(window,pair.Key);if(wanted&&!registered.Contains(pair.Key)&&RegisterHotKey(window,pair.Key,pair.Value.Mods|0x4000,pair.Value.Key))registered.Add(pair.Key);}}
    public bool HandleMessage(int msg,nint value){if(msg!=0x312||!keys.TryGetValue((int)value,out var binding))return false;if(!disposed&&eligible())invoke(binding.Command);return true;}
    public void Dispose(){disposed=true;foreach(int id in registered)UnregisterHotKey(window,id);if(hook!=0)UnhookWinEvent(hook);registered.Clear();}
    delegate void WinEvent(nint hook,uint evt,nint hwnd,int obj,int child,uint thread,uint time);
    [DllImport("user32.dll")]static extern nint SetWinEventHook(uint min,uint max,nint module,WinEvent callback,uint process,uint thread,uint flags);
    [DllImport("user32.dll")]static extern bool UnhookWinEvent(nint hook);
    [DllImport("user32.dll")]static extern bool RegisterHotKey(nint window,int id,uint mod,uint key);
    [DllImport("user32.dll")]static extern bool UnregisterHotKey(nint window,int id);
}
