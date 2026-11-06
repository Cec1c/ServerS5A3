using System.Diagnostics;
using System.Security.Cryptography;
using System.Text.Json;
using Arad.Helper.Contracts;
using Arad.Helper.Runtime;

namespace Arad.Helper.Host;

// Snapshots and pins the exact signed native files selected at startup.
// The public surface has one operation, with a verified game identity.
sealed class NativeWarehouse : IDisposable
{
    readonly string executable,directory;
    readonly List<FileStream> pins=[];
    public NativeWarehouse(string root,VerifiedPackage package)
    {
        if(package.Manifest.Id!="utilities" || !package.Manifest.Capabilities.Contains("native.ui.warehouse"))throw new InvalidDataException("插件没有原生仓库能力");
        string[] names=["Arad.Helper.NativeUI.exe","Arad.Helper.NativeUI.dll"];
        if(names.Any(x=>!package.Images.ContainsKey(x)))throw new InvalidDataException("原生仓库组件不完整");
        string identity=Convert.ToHexString(SHA256.HashData(names.SelectMany(x=>package.Images[x]).ToArray()))[..24];
        directory=Path.Combine(root,"helper/state/native",identity);Directory.CreateDirectory(directory);
        for(string? p=directory;p is not null;p=Path.GetDirectoryName(p))if(File.GetAttributes(p).HasFlag(FileAttributes.ReparsePoint))throw new InvalidDataException("原生组件目录不能是链接");
        try{
            foreach(string name in names){
                string path=Path.Combine(directory,name);byte[] expected=package.Images[name];
                if(!File.Exists(path)){string temp=path+"."+Guid.NewGuid().ToString("N")+".tmp";File.WriteAllBytes(temp,expected);try{File.Move(temp,path,false);}finally{File.Delete(temp);}}
                if(File.GetAttributes(path).HasFlag(FileAttributes.ReparsePoint))throw new InvalidDataException("原生组件不能是链接");
                var pin=new FileStream(path,FileMode.Open,FileAccess.Read,FileShare.Read);pins.Add(pin);
                if(pin.Length!=expected.Length || !SHA256.HashData(pin).SequenceEqual(SHA256.HashData(expected)))throw new InvalidDataException("原生组件校验失败");
            }
        }catch{Dispose();throw;}
        executable=Path.Combine(directory,names[0]);
    }
    public async Task OpenAsync(InstanceIdentity id,nint window,CancellationToken cancel)
    {
        if(window==0)throw new InvalidOperationException("请等待游戏窗口加载完成");
        var start=new ProcessStartInfo(executable){WorkingDirectory=directory,UseShellExecute=false,CreateNoWindow=true,RedirectStandardOutput=true,RedirectStandardError=true};
        foreach(string arg in new[]{"--pid",id.Pid.ToString(),"--created",id.CreatedFileTime.ToString(),"--window",unchecked((uint)window.ToInt64()).ToString(),"--client",id.ExecutablePath})start.ArgumentList.Add(arg);
        using var process=Process.Start(start)??throw new InvalidOperationException("原生仓库组件未启动");
        using var deadline=CancellationTokenSource.CreateLinkedTokenSource(cancel);deadline.CancelAfter(TimeSpan.FromSeconds(12));
        try{
            Task<string> output=process.StandardOutput.ReadToEndAsync(deadline.Token),error=process.StandardError.ReadToEndAsync(deadline.Token);
            await process.WaitForExitAsync(deadline.Token);string text=await output;await error;
            if(text.Length>4096)throw new InvalidDataException("原生仓库响应超限");
            using var reply=JsonDocument.Parse(text);
            if(process.ExitCode!=0 || !reply.RootElement.GetProperty("ok").GetBoolean())throw new InvalidOperationException("原生仓库暂未打开，请确认当前游戏处于前台后重试");
        }catch(OperationCanceledException){if(!process.HasExited)process.Kill();throw new InvalidOperationException("原生仓库响应超时，请稍后重试");}
        finally{if(!process.HasExited)process.Kill();}
    }
    public void Dispose(){foreach(var p in pins)p.Dispose();pins.Clear();}
}
