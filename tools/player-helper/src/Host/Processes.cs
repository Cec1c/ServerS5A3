using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using Microsoft.Win32.SafeHandles;
using Arad.Helper.Contracts;
using Arad.Helper.Runtime;

namespace Arad.Helper.Host;

static class ClientFiles
{
    public static string Resolve(string root) => new[] { "client/client/DNF.exe", "client/DNF.exe", "DNF.exe" }
        .Select(p => Path.Combine(root, p)).FirstOrDefault(File.Exists) ?? Path.Combine(root, "client/client/DNF.exe");
    public static bool ValidRoot(string root) => File.Exists(Path.Combine(root, "launcher/launcher.id")) ||
        File.Exists(Path.Combine(root, "launcher.id")) || File.Exists(Resolve(root));
}

interface IGameProcesses { GameInstance[] Scan(); }
sealed class GameProcesses(string client) : IGameProcesses
{
    readonly Dictionary<InstanceIdentity, string> hashes = [];
    const string Known = "bb7022670b546ab200bc6f9718a47e51ff1e8cc78b4049231229db72b6aa3552";
    public static uint ForegroundPid { get { GetWindowThreadProcessId(GetForegroundWindow(), out uint pid); return pid; } }
    public GameInstance[] Scan()
    {
        lock (hashes) return ScanLocked();
    }
    GameInstance[] ScanLocked()
    {
        var result = new List<GameInstance>();
        using var self = Process.GetCurrentProcess();
        foreach (var process in Process.GetProcessesByName("DNF"))
        {
            using (process) try {
                if (process.SessionId != self.SessionId) continue;
                using var handle = OpenProcess(0x1000, false, process.Id); // query only, no VM access
                if (handle.IsInvalid || !GetProcessTimes(handle, out long created, out _, out _, out _)) continue;
                var buffer = new StringBuilder(32768); int length = buffer.Capacity;
                if (!QueryFullProcessImageName(handle, 0, buffer, ref length) || !Path.GetFullPath(buffer.ToString()).Equals(Path.GetFullPath(client), StringComparison.OrdinalIgnoreCase)) continue;
                var id = new InstanceIdentity(process.Id, created, buffer.ToString(), Packages.GameId);
                if (!hashes.TryGetValue(id, out string? hash)) {
                    using var file = new FileStream(id.ExecutablePath, FileMode.Open, FileAccess.Read, FileShare.Read);
                    hash = Convert.ToHexString(SHA256.HashData(file)).ToLowerInvariant(); hashes[id] = hash;
                }
                result.Add(new(id, string.IsNullOrWhiteSpace(process.MainWindowTitle) ? "游戏正在启动" : process.MainWindowTitle,
                    hash, hash == Known, ForegroundPid == process.Id,
                    process.MainWindowHandle == 0 ? null : !IsHungAppWindow(process.MainWindowHandle)));
            } catch (Exception e) when (e is Win32Exception or InvalidOperationException or IOException or UnauthorizedAccessException) { }
        }
        foreach (var stale in hashes.Keys.Except(result.Select(x => x.Identity)).ToArray()) hashes.Remove(stale);
        return result.OrderBy(x => x.Identity.CreatedFileTime).ToArray();
    }
    public bool Focus(InstanceIdentity identity)
    {
        // PID reuse or an installation/path change invalidates the selection.
        if (!Scan().Any(x => x.Identity == identity)) return false;
        using var p = Process.GetProcessById(identity.Pid); nint window = p.MainWindowHandle;
        if (window == 0) return false;
        ShowWindow(window, 9); return SetForegroundWindow(window);
    }
    public nint Window(InstanceIdentity identity)
    {
        try {
            using var p=Process.GetProcessById(identity.Pid);
            using var self=Process.GetCurrentProcess();
            if(p.SessionId!=self.SessionId)return 0;
            using var handle=OpenProcess(0x1000,false,p.Id);
            if(handle.IsInvalid || !GetProcessTimes(handle,out long created,out _,out _,out _) || created!=identity.CreatedFileTime)return 0;
            var path=new StringBuilder(32768);int length=path.Capacity;
            if(!QueryFullProcessImageName(handle,0,path,ref length) || !Path.GetFullPath(path.ToString()).Equals(Path.GetFullPath(identity.ExecutablePath),StringComparison.OrdinalIgnoreCase))return 0;
            return p.MainWindowHandle;
        }catch(Exception e)when(e is Win32Exception or InvalidOperationException or ArgumentException){return 0;}
    }
    [DllImport("kernel32.dll")] static extern SafeProcessHandle OpenProcess(uint access, bool inherit, int pid);
    [DllImport("kernel32.dll")] static extern bool GetProcessTimes(SafeProcessHandle h, out long created, out long exited, out long kernel, out long user);
    [DllImport("kernel32.dll", EntryPoint = "QueryFullProcessImageNameW", CharSet = CharSet.Unicode)] static extern bool QueryFullProcessImageName(SafeProcessHandle h, uint flags, StringBuilder path, ref int size);
    [DllImport("user32.dll")] static extern nint GetForegroundWindow();
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(nint window, out uint pid);
    [DllImport("user32.dll")] static extern bool IsHungAppWindow(nint window);
    [DllImport("user32.dll")] static extern bool SetForegroundWindow(nint window);
    [DllImport("user32.dll")] static extern bool ShowWindow(nint window, int command);
}
