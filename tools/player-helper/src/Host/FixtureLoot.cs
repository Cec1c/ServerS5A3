using Arad.Helper.Contracts;
using Arad.Helper.Runtime;
namespace Arad.Helper.Host;
// Only used by the explicit isolated UI harness; no game, network or assets.
sealed class FixtureLoot : ILootBackend
{
    public LootPolicy Policy=new(false,0,0);public int LastPid,Applies;public bool Enabled;
    static string Binding(InstanceIdentity id)=>id.Pid.ToString("x32");
    LootState State(InstanceIdentity id)=>new(true,Binding(id),id.Pid.ToString(),"测试角色 "+id.Pid,0,Enabled,false,Policy,"");
    public ValueTask<LootState> ReadAsync(InstanceIdentity id,CancellationToken cancel)=>ValueTask.FromResult(State(id));
    public ValueTask<LootState> ApplyAsync(InstanceIdentity id,long generation,string binding,LootPolicy policy,CancellationToken cancel){if(binding!=Binding(id))throw new InvalidOperationException();Policy=policy;LastPid=id.Pid;Enabled=true;Applies++;return ValueTask.FromResult(State(id));}
    public ValueTask<LootState> StopAsync(InstanceIdentity id,long generation,string binding,CancellationToken cancel){Enabled=false;return ValueTask.FromResult(State(id));}
    public void Revoke(long generation){Enabled=false;}
}
