using System.Buffers.Binary;
using System.Diagnostics;
using System.IO.Pipes;
using System.Net.Http.Json;
using System.Net.Security;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Arad.Helper.Contracts;
using Arad.Helper.Runtime;

namespace Arad.Helper.Host;

sealed partial class SessionBroker : ILootBackend,IUtilityBackend, IAsyncDisposable
{
    sealed record Session(int Pid,long CreatedFileTime,string ExecutablePath,string Credential,string ApiUrl,string CertificateSha256,string? ActionsCredential=null);
    sealed class Auth(Session session,HttpClient? http=null) : IDisposable {
        public readonly Session Data=session;
        public readonly HttpClient Http=http??new(new HttpClientHandler { UseProxy=false,AllowAutoRedirect=false,
            ServerCertificateCustomValidationCallback=(_,cert,_,errors)=>cert is not null && (errors&SslPolicyErrors.RemoteCertificateNameMismatch)==0 && DateTime.UtcNow>=cert.NotBefore.ToUniversalTime() && DateTime.UtcNow<=cert.NotAfter.ToUniversalTime() && Convert.ToHexString(SHA256.HashData(cert.RawData)).Equals(session.CertificateSha256,StringComparison.OrdinalIgnoreCase) });
        public void Dispose()=>Http.Dispose();
    }
    sealed record Lease(Auth Auth,InstanceIdentity Instance,long Generation,string Binding,string Owner,string Secret,ulong Sequence,LootPolicy Policy,ulong? RecoveryRevision=null,bool Reconnecting=false);
    sealed class HelperRequestException(System.Net.HttpStatusCode status,string message) : InvalidOperationException(message) {public System.Net.HttpStatusCode Status{get;}=status;}
    sealed record Request(int Version,string Action,string Binding="",string Owner="",string Lease="",ulong Revision=0,ulong Sequence=0,LootPolicy? Policy=null);
    sealed record Reply(bool Ok,bool Available,string Binding,string CharacterId,string CharacterName,ulong Revision,bool Enabled,bool Paused,LootPolicy Policy,string? Lease,string? Message,bool Recoverable=false,bool CurrentLease=false);
    static readonly JsonSerializerOptions Json=new(JsonSerializerDefaults.Web);
    NativeWarehouse? pinnedNative;Func<InstanceIdentity,CancellationToken,Task>? openNativeWarehouse;
    public void ConfigureNativeWarehouse(VerifiedPackage package){if(pinnedNative is not null)throw new InvalidOperationException("原生组件启动后固定");pinnedNative=new(root,package);openNativeWarehouse=(id,cancel)=>pinnedNative.OpenAsync(id,((GameProcesses)processes).Window(id),cancel);}
    readonly string root;readonly IGameProcesses processes;readonly CancellationTokenSource stopping=new();
    readonly object gate=new();readonly Dictionary<InstanceIdentity,Auth> auth=[];readonly Dictionary<InstanceIdentity,Lease> leases=[];readonly HashSet<long> revoked=[];
    readonly SemaphoreSlim serial=new(1,1);readonly Task pipeTask,renewTask;
    public SessionBroker(string root,GameProcesses processes) : this(root,processes,true) { }
    SessionBroker(string root,IGameProcesses processes,bool background) {this.root=root;this.processes=processes;pipeTask=background?Task.Run(Accept):Task.CompletedTask;renewTask=background?Task.Run(Renew):Task.CompletedTask;}
    static string PipeName(string root)=>"Arad.Helper.Session."+Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(Path.GetFullPath(root).ToUpperInvariant())))[..24];
    bool Valid(InstanceIdentity id)=>processes.Scan().Any(x=>x.Identity.Pid==id.Pid && x.Identity.CreatedFileTime==id.CreatedFileTime && string.Equals(x.Identity.ExecutablePath,id.ExecutablePath,StringComparison.OrdinalIgnoreCase) && x.KnownClient);
    Auth Get(InstanceIdentity id) {if(!Valid(id))throw new InvalidOperationException("游戏窗口已变化或客户端尚未适配。");lock(gate)return auth.GetValueOrDefault(id)??throw new InvalidOperationException("请从原登录器重登，关联拾取会话。");}
    async Task Accept()
    {
        while(!stopping.IsCancellationRequested) try {
            await using var pipe=new NamedPipeServerStream(PipeName(root),PipeDirection.InOut,1,PipeTransmissionMode.Byte,PipeOptions.Asynchronous|PipeOptions.CurrentUserOnly);
            await pipe.WaitForConnectionAsync(stopping.Token);
            using var deadline=CancellationTokenSource.CreateLinkedTokenSource(stopping.Token);deadline.CancelAfter(TimeSpan.FromSeconds(3));
            if(!GetNamedPipeClientProcessId(pipe.SafePipeHandle.DangerousGetHandle(),out uint pid))continue;
            using var peer=Process.GetProcessById((int)pid);using var self=Process.GetCurrentProcess();
            if(peer.SessionId!=self.SessionId || !string.Equals(peer.MainModule?.FileName,Path.Combine(AppContext.BaseDirectory,"DNF90.Core.exe"),StringComparison.OrdinalIgnoreCase))continue;
            byte[] header=new byte[4];await pipe.ReadExactlyAsync(header,deadline.Token);int length=BinaryPrimitives.ReadInt32LittleEndian(header);if(length is <=0 or >4096)continue;
            byte[] bytes=new byte[length];await pipe.ReadExactlyAsync(bytes,deadline.Token);var data=JsonSerializer.Deserialize<Session>(bytes,Json)??throw new InvalidDataException();
            var id=new InstanceIdentity(data.Pid,data.CreatedFileTime,data.ExecutablePath,Packages.GameId);
            if(data.Credential.Length!=64 || !data.Credential.All(char.IsAsciiHexDigit) || data.CertificateSha256.Length!=64 || !data.CertificateSha256.All(char.IsAsciiHexDigit) || !Uri.TryCreate(data.ApiUrl,UriKind.Absolute,out var uri) || uri.Scheme!="https" || uri.UserInfo!="" || uri.Query!="" || uri.Fragment!="" || !Valid(id))continue;
            if(data.ActionsCredential is {} ac && (ac.Length!=64 || !ac.All(char.IsAsciiHexDigit)))continue;
 var actual=processes.Scan().First(x=>x.Identity.Pid==id.Pid && x.Identity.CreatedFileTime==id.CreatedFileTime && string.Equals(x.Identity.ExecutablePath,id.ExecutablePath,StringComparison.OrdinalIgnoreCase));
            var next=new Auth(data);lock(gate){if(auth.Remove(actual.Identity,out var old))old.Dispose();auth[actual.Identity]=next;}
            await pipe.WriteAsync(new byte[]{1},deadline.Token);
        } catch(OperationCanceledException)when(stopping.IsCancellationRequested){break;}
        catch(Exception e)when(e is IOException or UnauthorizedAccessException or InvalidDataException or JsonException or InvalidOperationException or ArgumentException or System.ComponentModel.Win32Exception or OperationCanceledException){ }
    }
    static async Task<Reply> Send(Auth a,Request body,CancellationToken cancel)
    {
        using var deadline=CancellationTokenSource.CreateLinkedTokenSource(cancel);deadline.CancelAfter(TimeSpan.FromSeconds(2));
        using var request=new HttpRequestMessage(HttpMethod.Post,a.Data.ApiUrl.TrimEnd('/')+"/api/account/helper"){Content=JsonContent.Create(body,options:Json)};
        request.Headers.Add("X-DNF-Helper",a.Data.Credential);
        using var response=await a.Http.SendAsync(request,HttpCompletionOption.ResponseHeadersRead,deadline.Token);
        if(!response.IsSuccessStatusCode)throw new HelperRequestException(response.StatusCode,response.StatusCode==System.Net.HttpStatusCode.Unauthorized?"等待角色登录；如已进城，请重登关联会话。":"角色或拾取租约已变化。");
        await using var stream=await response.Content.ReadAsStreamAsync(deadline.Token);using var bounded=new MemoryStream();byte[] block=new byte[1024];int n;
        while((n=await stream.ReadAsync(block,deadline.Token))!=0){if(bounded.Length+n>8192)throw new InvalidDataException("拾取响应超限。");bounded.Write(block,0,n);}
        var r=JsonSerializer.Deserialize<Reply>(bounded.ToArray(),Json);if(r?.Ok!=true || r.Binding.Length!=32 || r.Policy is null)throw new InvalidDataException("拾取响应无效。");return r;
    }

    public async ValueTask<UtilityReply> ExecuteUtilityAsync(InstanceIdentity id,long generation,UtilityRequest body,CancellationToken cancel)
    {
        var a=Get(id);lock(gate)if(revoked.Contains(generation))throw new OperationCanceledException();
        if(a.Data.ActionsCredential is not {Length:64})throw new InvalidOperationException("请从新版登录器重新进入游戏");
        using var deadline=CancellationTokenSource.CreateLinkedTokenSource(cancel);deadline.CancelAfter(TimeSpan.FromSeconds(15));
        using var request=new HttpRequestMessage(HttpMethod.Post,a.Data.ApiUrl.TrimEnd('/')+"/api/account/helper"){Content=JsonContent.Create(new{version=2,action="utility",operation=body},options:Json)};
        request.Headers.Add("X-DNF-Helper",a.Data.ActionsCredential);
        using var response=await a.Http.SendAsync(request,HttpCompletionOption.ResponseHeadersRead,deadline.Token);
        await using var stream=await response.Content.ReadAsStreamAsync(deadline.Token);using var buffer=new MemoryStream();byte[] block=new byte[4096];int n;
        while((n=await stream.ReadAsync(block,deadline.Token))!=0){if(buffer.Length+n>131072)throw new InvalidDataException("便捷接口响应超限");buffer.Write(block,0,n);}
        UtilityReply? result=null;try{result=JsonSerializer.Deserialize<UtilityReply>(buffer.ToArray(),Json);}catch(JsonException){}
        if(!response.IsSuccessStatusCode || result?.Ok!=true)throw new InvalidOperationException(result?.Message is {Length:>0} m?m:"角色会话暂不可用，请重登后再试");
        if(result.Binding.Length!=32)throw new InvalidDataException("角色绑定无效");
        if(body.Mode=="warehouse-open"){
            lock(gate)if(revoked.Contains(generation)||auth.GetValueOrDefault(id)!=a)throw new OperationCanceledException();
            if(result.Kind!="native-warehouse" || result.Binding!=body.Binding || !processes.Scan().Any(x=>x.Identity==id&&x.KnownClient&&x.Foreground))throw new InvalidOperationException("角色或游戏窗口已变化，请重试");
            await (openNativeWarehouse??throw new InvalidOperationException("请更新原生仓库组件"))(id,cancel);
            return result with{Message="已打开游戏原生仓库",Items=[]};
        }
        return result;
    }

    static LootState Public(Reply r)=>new(r.Available,r.Binding,r.CharacterId,r.CharacterName,r.Revision,r.Enabled,r.Paused,r.Policy,r.Message??"");
    public async ValueTask<LootState> ReadAsync(InstanceIdentity id,CancellationToken cancel) {var a=Get(id);var state=Public(await Send(a,new(1,"status"),cancel));lock(gate)if(leases.TryGetValue(id,out var lease)&&lease.Reconnecting)state=state with{Message="自动拾取连接恢复中；品级设置保留。"};return state;}
    public async ValueTask<LootState> ApplyAsync(InstanceIdentity id,long generation,string binding,LootPolicy policy,CancellationToken cancel)
    {
        await serial.WaitAsync(cancel);
        try {
            var a=Get(id);lock(gate)if(revoked.Contains(generation))throw new OperationCanceledException();
            var current=await Send(a,new(1,"status"),cancel);if(current.Binding!=binding)throw new InvalidOperationException("角色已切换，请刷新后重新选择。");
            string owner=Guid.NewGuid().ToString("N");var reply=await Send(a,new(1,"apply",binding,owner,Revision:current.Revision,Policy:policy),cancel);
            if(reply.Lease is not {Length:32})throw new InvalidDataException("缺少拾取租约。");
            var lease=new Lease(a,id,generation,binding,owner,reply.Lease,0,policy);bool stale;
            lock(gate){stale=revoked.Contains(generation)||cancel.IsCancellationRequested;if(!stale)leases[id]=lease;}
            if(stale){ReleaseLater(lease);throw new OperationCanceledException();}return Public(reply);
        } finally {serial.Release();}
    }
    public async ValueTask<LootState> StopAsync(InstanceIdentity id,long generation,string binding,CancellationToken cancel)
    {
        await serial.WaitAsync(cancel);
        try {
            Lease? lease;lock(gate){lease=leases.GetValueOrDefault(id);if(lease is not null && lease.Generation==generation)leases.Remove(id);}
            if(lease is null || lease.Generation!=generation || lease.Binding!=binding)throw new InvalidOperationException("本次内辅没有该角色的有效拾取租约；也可在游戏输入 !loot off。");
            return Public(await Send(lease.Auth,new(1,"release",binding,lease.Owner,lease.Secret),cancel));
        } finally {serial.Release();}
    }
    public void Revoke(long generation)
    {
        Lease[] old;lock(gate){revoked.Add(generation);old=leases.Values.Where(x=>x.Generation==generation).ToArray();foreach(var l in old)leases.Remove(l.Instance);}
        foreach(var l in old)ReleaseLater(l);
    }
    public void UpdateInstances(IReadOnlyList<GameInstance> instances)
    {
        Lease[] old;Auth[] stale;
        lock(gate){var ids=instances.Select(x=>x.Identity).ToHashSet();old=leases.Values.Where(x=>!ids.Contains(x.Instance)).ToArray();foreach(var l in old)leases.Remove(l.Instance);var keys=auth.Keys.Where(x=>!ids.Contains(x)).ToArray();stale=keys.Select(x=>auth[x]).ToArray();foreach(var key in keys)auth.Remove(key);}
        foreach(var l in old)ReleaseLater(l);
        if(stale.Length>0)_=Task.Run(async()=>{await Task.Delay(2500);foreach(var a in stale)a.Dispose();});
    }
    static void ReleaseLater(Lease l)=>_ = Task.Run(async()=>{try{await Send(l.Auth,new(1,"release",l.Binding,l.Owner,l.Secret),CancellationToken.None);}catch(Exception){ }});
    static bool Transient(Exception e)=>e is HttpRequestException or IOException or OperationCanceledException || e is HelperRequestException r && ((int)r.Status>=500 || (int)r.Status is 408 or 429);
    bool Current(Lease lease) {lock(gate)return !stopping.IsCancellationRequested&&!revoked.Contains(lease.Generation)&&leases.GetValueOrDefault(lease.Instance)==lease;}
    bool Store(Lease old,Lease next) {lock(gate){if(!Current(old))return false;leases[old.Instance]=next;return true;}}
    void Drop(Lease lease) {Lease? active;lock(gate){active=leases.GetValueOrDefault(lease.Instance);if(active is null||active.Owner!=lease.Owner||active.Generation!=lease.Generation)return;leases.Remove(lease.Instance);}ReleaseLater(active);}
    void ConnectionEvent(string kind,string reason)
    {
        try{
            int active,recovering;lock(gate){active=leases.Count;recovering=leases.Values.Count(x=>x.Reconnecting);}
            string dir=Path.Combine(root,"helper/state");Directory.CreateDirectory(dir);string path=Path.Combine(dir,"connection.json"),temp=path+"."+Guid.NewGuid().ToString("N")+".tmp";
            File.WriteAllText(temp,JsonSerializer.Serialize(new{utc=DateTime.UtcNow,kind,reason,activeLeases=active,recoveringLeases=recovering}));File.Move(temp,path,true);
        }catch(Exception e)when(e is IOException or UnauthorizedAccessException){}
    }
    async Task Recover(Lease lease)
    {
        if(!Current(lease))return;
        if(lease.RecoveryRevision is null){
            var status=await Send(lease.Auth,new(1,"status",lease.Binding,lease.Owner,lease.Secret),stopping.Token);
            if(!Current(lease))return;
            if(status.Binding==lease.Binding && status.Available && status.Enabled && !status.Paused && status.CurrentLease){
                Store(lease,lease with{Reconnecting=true});ConnectionEvent("retry","matching-lease-still-active");return;
            }
            // A character change, full bag pause or another active owner needs
            // an explicit user action. Recovery never takes that lease over.
            if(status.Binding!=lease.Binding || !status.Available || status.Paused || status.Enabled || !status.Recoverable){Drop(lease);ConnectionEvent("stopped","binding-pause-or-other-owner");return;}
            var pending=lease with{RecoveryRevision=status.Revision,Reconnecting=true};if(!Store(lease,pending))return;lease=pending;
        }
        // Persist the old revision and owner before sending. A lost response
        // retries exactly this apply, so server idempotency cannot overwrite a
        // newer owner or create a second successful acquisition.
        try{
            var reply=await Send(lease.Auth,new(1,"apply",lease.Binding,lease.Owner,Revision:lease.RecoveryRevision!.Value,Policy:lease.Policy),stopping.Token);
            if(reply.Binding!=lease.Binding || reply.Lease is not {Length:32})throw new InvalidDataException("拾取恢复响应无效");
            var next=lease with{Secret=reply.Lease,Sequence=0,RecoveryRevision=null,Reconnecting=false};
            if(!Store(lease,next))ReleaseLater(next);else ConnectionEvent("recovered","same-bound-character");
        }catch(HelperRequestException e)when(e.Status==System.Net.HttpStatusCode.Conflict){
            if(!Store(lease,lease with{RecoveryRevision=null,Reconnecting=true}))return;
            ConnectionEvent("retry","recovery-revision-changed");
        }
    }
    async Task<bool> RenewPass()
    {
        bool retry=false;await serial.WaitAsync(stopping.Token);
        try{
            Lease[] current;lock(gate)current=leases.Values.ToArray();
            foreach(var lease in current){
                if(!Current(lease))continue;
                try{
                    if(!Valid(lease.Instance)){Drop(lease);ConnectionEvent("stopped","game-instance-changed");continue;}
                    if(lease.RecoveryRevision is not null){await Recover(lease);continue;}
                    try{
                        var next=lease with{Sequence=lease.Sequence+1,Reconnecting=false};
                        await Send(lease.Auth,new(1,"renew",lease.Binding,lease.Owner,lease.Secret,Sequence:next.Sequence),stopping.Token);
                        if(Store(lease,next)&&lease.Reconnecting)ConnectionEvent("recovered","renewed-existing-lease");
                    }catch(HelperRequestException e)when(e.Status==System.Net.HttpStatusCode.Conflict){await Recover(lease);}
                }catch(Exception e)when(Transient(e)&&!stopping.IsCancellationRequested){
                    lock(gate){var active=leases.GetValueOrDefault(lease.Instance);if(active is not null&&active.Owner==lease.Owner&&active.Generation==lease.Generation&&!revoked.Contains(lease.Generation))leases[lease.Instance]=active with{Reconnecting=true};}
                    retry=true;ConnectionEvent("retry",e is OperationCanceledException?"request-timeout":"transport-unavailable");
                }catch(OperationCanceledException)when(stopping.IsCancellationRequested){throw;}
                catch(Exception){Drop(lease);ConnectionEvent("stopped","invalid-session-or-response");}
            }
            lock(gate)return retry||leases.Values.Any(x=>x.Reconnecting);
        }finally{serial.Release();}
    }
    async Task Renew()
    {
        bool retry=false;
        while(!stopping.IsCancellationRequested)try{
            await Task.Delay(TimeSpan.FromSeconds(retry?1:5),stopping.Token);retry=await RenewPass();
        }catch(OperationCanceledException)when(stopping.IsCancellationRequested){break;}
        catch(Exception){retry=true;ConnectionEvent("retry","renew-loop-error");}
    }
    public async ValueTask DisposeAsync()
    {
        Lease[] old;lock(gate){old=leases.Values.ToArray();leases.Clear();}
        foreach(var l in old)ReleaseLater(l);pinnedNative?.Dispose();stopping.Cancel();try{await Task.WhenAll(pipeTask,renewTask).WaitAsync(TimeSpan.FromSeconds(3));}catch(Exception){ }
        // Outstanding best-effort releases use only host records. The server's
        // 15-second lease is the backstop even after abrupt host termination.
    }
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetNamedPipeClientProcessId(nint pipe,out uint pid);
}
