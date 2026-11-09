using System.Net;
using System.Net.Http.Json;
using System.Text.Json;
using Arad.Helper.Contracts;
using Arad.Helper.Runtime;

namespace Arad.Helper.Host;

sealed partial class SessionBroker
{
    sealed class FixtureProcesses(InstanceIdentity identity) : IGameProcesses
    {
        public bool Present=true,Foreground=true;
        public GameInstance[] Scan()=>Present?[new(identity,"fixture","fixture",true,Foreground,true)]:[];
    }
    sealed class FixtureHttp : HttpMessageHandler
    {
        public string Binding=new('b',32),Owner="",Secret="";
        public ulong Revision,Sequence;
        public bool Enabled,Paused,Recoverable,Available=true,TimeoutRenew,ConflictRenew,BadGatewayRenew,LoseApplyResponse,InterruptedRead;
        public int Applied,Released;
        public LootPolicy Policy=new(true,88,3);
        readonly object sync=new();
        public void Expire(){lock(sync){Enabled=false;Recoverable=true;Owner=Secret="";Revision++;}}
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request,CancellationToken cancel)
        {
            var q=await request.Content!.ReadFromJsonAsync<Request>(Json,cancel)??throw new InvalidDataException();
            lock(sync){
                HttpResponseMessage Error(HttpStatusCode code)=>new(code){Content=new StringContent("fixture")};
                HttpResponseMessage Reply(bool lease=false)=>new(HttpStatusCode.OK){Content=JsonContent.Create(new Reply(true,Available,Binding,"55","fixture",Revision,Enabled,Paused,Policy,lease?Secret:null,"",Recoverable,Enabled&&q.Owner==Owner&&q.Lease==Secret&&Secret.Length==32),options:Json)};
                if(q.Action!="status"&&q.Binding!=Binding)return Error(HttpStatusCode.Conflict);
                switch(q.Action){
                    case "status":return Reply();
                    case "renew":
                        if(ConflictRenew){ConflictRenew=false;return Error(HttpStatusCode.Conflict);}
                        if(TimeoutRenew){TimeoutRenew=false;throw new TaskCanceledException("fixture timeout");}
                        if(InterruptedRead){InterruptedRead=false;throw new IOException("fixture interrupted response");}
                        if(BadGatewayRenew){BadGatewayRenew=false;return Error(HttpStatusCode.BadGateway);}
                        if(!Enabled||q.Owner!=Owner||q.Lease!=Secret||q.Sequence<Sequence)return Error(HttpStatusCode.Conflict);
                        Sequence=q.Sequence;return Reply();
                    case "apply":
                        if(Enabled&&q.Owner==Owner&&q.Policy==Policy)return Reply(true);
                        if(q.Revision!=Revision)return Error(HttpStatusCode.Conflict);
                        Revision++;Owner=q.Owner;Secret=Guid.NewGuid().ToString("N");Sequence=0;Policy=q.Policy!;Enabled=true;Paused=false;Recoverable=false;Applied++;
                        if(LoseApplyResponse){LoseApplyResponse=false;throw new TaskCanceledException("response lost after commit");}
                        return Reply(true);
                    case "release":
                        if(!Enabled||q.Owner!=Owner||q.Lease!=Secret)return Error(HttpStatusCode.Conflict);
                        Enabled=false;Recoverable=false;Owner=Secret="";Revision++;Released++;return Reply();
                    default:return Error(HttpStatusCode.BadRequest);
                }
            }
        }
    }
    // Only the explicit owned UI fixture calls this. Requests use an in-memory
    // handler and dummy capability; they cannot contact a real game or account.
    internal static async Task<Dictionary<string,bool>> ExerciseRecovery(string root)
    {
        var results=new Dictionary<string,bool>();
        foreach(string scenario in new[]{"conflict-still-owned","timeout","bad-gateway","read-interrupted","expired","lost-apply-response","lost-response-other-owner","manual-off","manual-off-after-expiry","manual-off-after-lost-response","character-changed","paused","other-owner","revoked","explicit-stop","process-exit"}){
            var id=new InstanceIdentity(123,456,"C:/fixture/DNF.exe",Packages.GameId);var process=new FixtureProcesses(id);var http=new FixtureHttp();
            await using var broker=new SessionBroker(Path.Combine(root,scenario),process,false);
            var session=new Session(id.Pid,id.CreatedFileTime,id.ExecutablePath,new('0',64),"https://fixture.invalid",new('0',64));
            broker.auth[id]=new Auth(session,new HttpClient(http));
            var policy=new LootPolicy(false,16,3);
            await broker.ApplyAsync(id,1,http.Binding,policy,CancellationToken.None);
            bool passed;
            switch(scenario){
                case "conflict-still-owned":case "timeout":case "bad-gateway":case "read-interrupted":
                    http.ConflictRenew=scenario=="conflict-still-owned";
                    http.TimeoutRenew=scenario=="timeout";http.BadGatewayRenew=scenario=="bad-gateway";http.InterruptedRead=scenario=="read-interrupted";
                    await broker.RenewPass();
                    passed=broker.leases.Count==1&&broker.leases[id].Reconnecting&&http.Enabled&&http.Released==0;
                    await broker.RenewPass();passed&=broker.leases.Count==1&&!broker.leases[id].Reconnecting&&http.Applied==1&&http.Policy==policy;
                    break;
                case "expired":case "lost-apply-response":
                    http.Expire();http.LoseApplyResponse=scenario=="lost-apply-response";
                    await broker.RenewPass();
                    if(scenario=="lost-apply-response"){
                        passed=broker.leases[id].RecoveryRevision is not null&&http.Enabled&&http.Applied==2;
                        await broker.RenewPass();
                    }else passed=true;
                    passed&=broker.leases.Count==1&&!broker.leases[id].Reconnecting&&broker.leases[id].RecoveryRevision is null&&http.Enabled&&http.Applied==2&&http.Policy==policy&&broker.leases[id].Secret==http.Secret;
                    break;
                case "lost-response-other-owner":
                    http.Expire();http.LoseApplyResponse=true;await broker.RenewPass();
                    string other=Guid.NewGuid().ToString("N");http.Owner=other;http.Secret=Guid.NewGuid().ToString("N");http.Revision++;
                    await broker.RenewPass();await broker.RenewPass();passed=broker.leases.Count==0&&http.Enabled&&http.Owner==other&&http.Applied==2;break;
                case "manual-off":case "manual-off-after-expiry":case "manual-off-after-lost-response":
                    if(scenario=="manual-off-after-expiry")http.Expire();
                    if(scenario=="manual-off-after-lost-response"){http.Expire();http.LoseApplyResponse=true;await broker.RenewPass();}
                    http.Enabled=false;http.Recoverable=false;http.Owner=http.Secret="";http.Revision++;
                    await broker.RenewPass();await broker.RenewPass();
                    passed=broker.leases.Count==0&&!http.Enabled&&http.Applied==(scenario=="manual-off-after-lost-response"?2:1);break;
                case "character-changed":
                    http.Binding=new('c',32);await broker.RenewPass();passed=broker.leases.Count==0&&http.Applied==1;break;
                case "paused":
                    http.Expire();http.Paused=true;await broker.RenewPass();passed=broker.leases.Count==0&&http.Applied==1&&!http.Enabled;break;
                case "other-owner":
                    http.Owner=Guid.NewGuid().ToString("N");http.Secret=Guid.NewGuid().ToString("N");http.Revision++;
                    await broker.RenewPass();passed=broker.leases.Count==0&&http.Enabled&&http.Applied==1;break;
                case "revoked":
                    http.TimeoutRenew=true;await broker.RenewPass();broker.Revoke(1);await broker.RenewPass();passed=broker.leases.Count==0&&http.Applied==1;break;
                case "explicit-stop":
                    http.TimeoutRenew=true;await broker.RenewPass();await broker.StopAsync(id,1,http.Binding,CancellationToken.None);await broker.RenewPass();passed=broker.leases.Count==0&&!http.Enabled&&http.Applied==1;break;
                case "process-exit":
                    process.Present=false;broker.UpdateInstances([]);await broker.RenewPass();passed=broker.leases.Count==0&&broker.auth.Count==0&&http.Applied==1;break;
                default:throw new InvalidOperationException();
            }
            results.Add(scenario,passed);
        }
        return results;
    }
}

// A second partial keeps HTTP permission ordering independent of native hooks.
sealed partial class SessionBroker
{
    sealed class FixtureNativeHttp : HttpMessageHandler
    {
        public string Binding=new('b',32),Kind="native-warehouse";public int Calls;public bool Reject,Timeout;public Action? BeforeReply;
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage message,CancellationToken cancel)
        {
            using var body=JsonDocument.Parse(await message.Content!.ReadAsStringAsync(cancel));
            var q=body.RootElement.GetProperty("operation").Deserialize<UtilityRequest>(Json)!;
            if(body.RootElement.GetProperty("version").GetInt32()!=2 || body.RootElement.GetProperty("action").GetString()!="utility" || q.Mode!="warehouse-open" || !message.Headers.GetValues("X-DNF-Helper").Single().Equals(new string('a',64)))throw new InvalidDataException("fixture scoped utility request");
            Calls++;BeforeReply?.Invoke();if(Timeout)throw new TaskCanceledException("fixture timeout");
            return new(Reject?HttpStatusCode.Conflict:HttpStatusCode.OK){Content=JsonContent.Create(new UtilityReply(!Reject,Binding,"fixture",Reject?"loading":"ready",null,Kind,0,0,false,null,0),options:Json)};
        }
    }
    internal static async Task<Dictionary<string,bool>> ExerciseNativeWarehouse(string root)
    {
        var results=new Dictionary<string,bool>();
        foreach(string scenario in new[]{"allowed","declined","wrong-binding","wrong-permit","foreground-changed","exited","revoked","auth-replaced","missing-actions","timeout","native-error"}){
            var id=new InstanceIdentity(123,456,"C:/fixture/DNF.exe",Packages.GameId);var process=new FixtureProcesses(id);var http=new FixtureNativeHttp();
            await using var broker=new SessionBroker(Path.Combine(root,scenario),process,false);
            var session=new Session(id.Pid,id.CreatedFileTime,id.ExecutablePath,new('0',64),"https://fixture.invalid",new('0',64),scenario=="missing-actions"?null:new('a',64));
            broker.auth[id]=new Auth(session,new HttpClient(http));int nativeCalls=0;
            broker.openNativeWarehouse=(identity,cancel)=>{if(identity!=id)throw new InvalidDataException();nativeCalls++;if(scenario=="native-error")throw new InvalidOperationException("fixture native failure");return Task.CompletedTask;};
            switch(scenario){
                case "declined":http.Reject=true;break;
                case "wrong-binding":http.Binding=new('c',32);break;
                case "wrong-permit":http.Kind="other";break;
                case "foreground-changed":http.BeforeReply=()=>process.Foreground=false;break;
                case "exited":http.BeforeReply=()=>process.Present=false;break;
                case "revoked":http.BeforeReply=()=>broker.Revoke(1);break;
                case "auth-replaced":http.BeforeReply=()=>broker.auth[id]=new Auth(session,new HttpClient());break;
                case "timeout":http.Timeout=true;break;
            }
            bool failed=false;UtilityReply? reply=null;
            try{reply=await broker.ExecuteUtilityAsync(id,1,new("warehouse-open",Binding:new('b',32),RequestId:new('1',32)),CancellationToken.None);}catch(Exception){failed=true;}
            bool expectedSuccess=scenario=="allowed";
            results.Add(scenario,failed!=expectedSuccess&&nativeCalls==(scenario is "allowed" or "native-error"?1:0)&&http.Calls==(scenario=="missing-actions"?0:1)&&(!expectedSuccess||reply?.Message=="已打开游戏原生仓库"&&reply.Items?.Length==0));
        }
        return results;
    }
}
