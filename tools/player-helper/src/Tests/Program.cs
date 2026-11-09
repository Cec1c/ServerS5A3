using System.Runtime.CompilerServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Arad.Helper.Contracts;
using Arad.Helper.Runtime;

static class Checks
{
    static int count;
    static void Check(bool value, string label) { if (!value) throw new Exception(label); count++; Console.WriteLine("PASS " + label); }
    static async Task Reject(Func<Task> action, string label) { bool failed = false; try { await action(); } catch { failed = true; } Check(failed, label); }
    static string Root = "", Status = "", Fixture = "", Loot = "", Utility="";
    static RSA Key = null!;
    static string Public => Key.ExportSubjectPublicKeyInfoPem();
    static readonly GameInstance A = new(new(100, 1, "C:/one/DNF.exe", Packages.GameId), "one", "abc", true, true, true);
    static readonly GameInstance B = new(new(101, 2, "C:/two/DNF.exe", Packages.GameId), "two", "def", true, false, true);
    static string Package(string id, string version = "0.1.0", string? type = null, Func<PluginManifest, PluginManifest>? modify = null, string? domain = null,bool native=false,ushort nativeMachine=0x14c)
    {
        bool fixture = type is not null; string source = type == "Arad.Helper.Loot.LootPlugin" ? Loot : type=="Arad.Helper.Utility.UtilityPlugin"?Utility:fixture ? Fixture : Status;
        string dir = Path.Combine(Root, Guid.NewGuid().ToString("N")); Directory.CreateDirectory(dir);
        string file = Path.GetFileName(source); byte[] bytes = File.ReadAllBytes(source); File.WriteAllBytes(Path.Combine(dir, file), bytes);
        var m = new PluginManifest("Arad-Helper-Plugin", Packages.GameId, id, version, 1, file, type ?? "Arad.Helper.Status.StatusPlugin", ["process.summary", "ui.panel"], [new(file, bytes.Length, Convert.ToHexString(SHA256.HashData(bytes)))]);
        if(native){
            foreach(var name in new[]{"Arad.Helper.NativeUI.exe","Arad.Helper.NativeUI.dll"}){
                byte[] pe=new byte[256];pe[0]=0x4d;pe[1]=0x5a;BitConverter.GetBytes(0x80).CopyTo(pe,0x3c);BitConverter.GetBytes(0x4550).CopyTo(pe,0x80);BitConverter.GetBytes(nativeMachine).CopyTo(pe,0x84);BitConverter.GetBytes((ushort)(2|(name.EndsWith(".dll")?0x2000:0))).CopyTo(pe,0x96);BitConverter.GetBytes((ushort)0x10b).CopyTo(pe,0x98);File.WriteAllBytes(Path.Combine(dir,name),pe);
                m=m with{Files=[..m.Files,new(name,pe.Length,Convert.ToHexString(SHA256.HashData(pe)))]};
            }
        }
        if (modify is not null) m = modify(m);
        byte[] raw = Encoding.UTF8.GetBytes((domain ?? Packages.Domain) + JsonSerializer.Serialize(m));
        File.WriteAllText(Path.Combine(dir, "package.json"), JsonSerializer.Serialize(new { payload = Convert.ToBase64String(raw), signature = Convert.ToBase64String(Key.SignData(raw, HashAlgorithmName.SHA256, RSASignaturePadding.Pss)) }));
        return dir;
    }
    [MethodImpl(MethodImplOptions.NoInlining)]
    static async Task Lifecycle()
    {
        var model = new InstanceModel(); model.Update([A, B], A.Identity);
        await using var manager = new PluginManager(model, Public); HelperPanel? panel = null;
        manager.PanelChanged += (_, p) => panel = p;
        string first = Package("status"), second = Package("status", "0.1.1");
        await manager.EnableAsync(first); Check(manager.ActiveCount == 1 && model.SubscriptionCount == 1, "enabled once");
        Check(panel!.Fields.Any(f => f.Value == "100"), "first instance selected");
        var old = manager.ContextForTest("status"); var cancellation = old.Stopping; long generation = old.Generation;
        model.Update([A, B], B.Identity); Check(panel.Fields.Any(f => f.Value == "101"), "second instance isolated");
        var reused = A with { Identity = A.Identity with { CreatedFileTime = 999 } };
        model.Update([reused], A.Identity); Check(model.Selected is null, "PID reuse clears stale selection");
        await manager.EnableAsync(second);
        Check(manager.ActiveCount == 1 && model.SubscriptionCount == 1 && manager.ContextForTest("status").Generation != generation, "hot reload replaces generation without duplicate subscription");
        Check(cancellation.IsCancellationRequested, "old generation cancelled");
        await Reject(() => { old.Publish(new("late", "late", [])); return Task.CompletedTask; }, "late publisher rejected");
        foreach (var fault in new[] { "domain", "sdk", "game", "capability", "traversal", "duplicate", "tamper", "signature", "contracts", "length" }) {
            var path = Package("status", modify: m => fault switch {
                "sdk" => m with { SdkMajor = 2 }, "game" => m with { GameId = "other" },
                "capability" => m with { Capabilities = ["inventory.arbitrary"] },
                "traversal" => m with { Files = [m.Files[0] with { Path = "../escape.dll" }] },
                "duplicate" => m with { Files = [m.Files[0], m.Files[0]] },
                "contracts" => m with { Files = [m.Files[0] with { Path = "Arad.Helper.Contracts.dll" }] },
                "length" => m with { Files = [m.Files[0] with { Length = 10485761 }] }, _ => m }, domain: fault == "domain" ? "Other-Product/1\n" : null);
            if (fault == "tamper") File.AppendAllText(Path.Combine(path, Path.GetFileName(Status)), "changed");
            if (fault == "signature") { var e = JsonSerializer.Deserialize<Dictionary<string, string>>(File.ReadAllText(Path.Combine(path, "package.json")))!; e["signature"] = Convert.ToBase64String(new byte[256]); File.WriteAllText(Path.Combine(path, "package.json"), JsonSerializer.Serialize(e)); }
            await Reject(() => manager.EnableAsync(path), "reject " + fault);
            Check(manager.ActiveCount == 1 && model.SubscriptionCount == 1, "bad " + fault + " preserves running plugin");
        }
        await manager.DisableAsync("status"); Check(manager.ActiveCount == 0 && model.SubscriptionCount == 0 && panel is null, "disable revokes subscriptions and panel");
        model.Update([A], A.Identity); Check(panel is null, "stopped plugin cannot publish on later event");
        await manager.DisableAsync("status"); Check(model.SubscriptionCount == 0, "duplicate stop is safe");
        await Reject(() => manager.EnableAsync(Package("fault", type: "Arad.Helper.TestPlugin.StartFailure")), "failed start is disabled");
        Check(manager.ActiveCount == 0 && model.SubscriptionCount == 0, "failed start cleaned up");
        await manager.EnableAsync(Package("slow", type: "Arad.Helper.TestPlugin.StopTimeout"));
        bool resident = false; manager.StatusChanged += s => resident |= s.State == "DisabledResident";
        await Reject(() => manager.DisableAsync("slow"), "cooperative stop timeout bounded");
        Check(resident && model.SubscriptionCount == 0 && manager.ActiveCount == 0, "timed-out plugin logically revoked, not falsely unloaded");
        await Reject(() => manager.EnableAsync(Package("slow")), "resident generation cannot restart");
    }
    [MethodImpl(MethodImplOptions.NoInlining)]
    static async Task<WeakReference[]> RepeatedReloads()
    {
        var model = new InstanceModel(); model.Update([A], A.Identity);
        await using var manager = new PluginManager(model, Public); string path = Package("status");
        for (int i = 0; i < 50; i++) { await manager.EnableAsync(path); Check(model.SubscriptionCount == 1, "reload subscription " + i); }
        await manager.DisableAsync("status"); Check(model.SubscriptionCount == 0, "all reload subscriptions released");
        return manager.UnloadedContexts.ToArray();
    }
    sealed class LootFixtureBackend : ILootBackend
    {
        public InstanceIdentity? Last;public LootPolicy? Policy;public long ActiveGeneration;public bool Active;public int ReadTimeouts,Reads;
        public ValueTask<LootState> ReadAsync(InstanceIdentity id,CancellationToken cancel){Interlocked.Increment(ref Reads);if(ReadTimeouts>0){Interlocked.Decrement(ref ReadTimeouts);return ValueTask.FromException<LootState>(new TaskCanceledException("fixture request timeout"));}return ValueTask.FromResult(new LootState(true,id.Pid.ToString("x32"),id.Pid.ToString(),"fixture",0,Active,false,Policy??new(false,0,0),""));}
        public ValueTask<LootState> ApplyAsync(InstanceIdentity id,long generation,string binding,LootPolicy policy,CancellationToken cancel){if(binding!=id.Pid.ToString("x32"))throw new InvalidOperationException();Last=id;Policy=policy;ActiveGeneration=generation;Active=true;return ReadAsync(id,cancel);}
        public ValueTask<LootState> StopAsync(InstanceIdentity id,long generation,string binding,CancellationToken cancel){Active=false;return ReadAsync(id,cancel);}
        public void Revoke(long generation){if(generation==ActiveGeneration)Active=false;}
    }
    static async Task LootModule()
    {
        var model=new InstanceModel();model.Update([A,B],A.Identity);var backend=new LootFixtureBackend();
        await using var manager=new PluginManager(model,Public,backend,_=>JsonSerializer.Serialize(new LootPolicy(false,16,0)));HelperPanel? panel=null;manager.PanelChanged+=(_,p)=>panel=p;
        string path=Package("autoloot",type:"Arad.Helper.Loot.LootPlugin",modify:m=>m with{Capabilities=["process.summary","ui.panel","loot.policy"]});
        await manager.EnableAsync(path);
        async Task Ready(){for(int i=0;i<50 && panel?.Toggles.Count!=15;i++)await Task.Delay(20);Check(panel?.Toggles.Count==15,"loot panel has separate gold and fourteen rarity filters");}
        await Ready();
        var values=panel!.Toggles.ToDictionary(t=>t.Key,t=>t.Key=="equip.4");
        await manager.ExecuteAsync("autoloot","apply",values);Check(backend.Policy==new LootPolicy(false,16,0)&&backend.Last==A.Identity,"only selected epic equipment and selected window enabled");
        Check(panel!.Toggles.All(x=>!x.Editable),"pickup filters are startup fixed");await manager.ExecuteAsync("autoloot","apply",new Dictionary<string,bool>());Check(backend.Policy==new LootPolicy(false,16,0),"runtime values cannot replace startup pickup filter");
        model.Update([A,B],B.Identity);await Ready();await manager.ExecuteAsync("autoloot","apply",values);Check(backend.Last==B.Identity,"selection change targets only the new bound window");
        await manager.ExecuteAsync("autoloot","stop",values);Check(!backend.Active,"explicit stop revokes pickup");
        await manager.ExecuteAsync("autoloot","apply",values);string saved=await manager.ExportSettingsAsync("autoloot");Check(saved.Contains("EquipmentMask"),"only filter preferences exported");
        await manager.DisableAsync("autoloot");Check(!backend.Active&&model.SubscriptionCount==0,"unload revokes pickup and model subscription");
    }
    static async Task LootTimeoutRecovery()
    {
        var model=new InstanceModel();model.Update([A],A.Identity);var backend=new LootFixtureBackend{ReadTimeouts=1};HelperPanel? panel=null;
        await using var manager=new PluginManager(model,Public,backend,_=>JsonSerializer.Serialize(new LootPolicy(true,16,3)));manager.PanelChanged+=(_,p)=>panel=p;
        await manager.EnableAsync(Package("autoloot",type:"Arad.Helper.Loot.LootPlugin",modify:m=>m with{Capabilities=["process.summary","ui.panel","loot.policy"]}));
        for(int i=0;i<160&&panel?.Toggles.Count!=15;i++)await Task.Delay(50);
        Check(backend.Reads>=2&&panel?.Toggles.Count==15,"request timeout does not terminate the pickup polling task");
        await manager.ExecuteAsync("autoloot","apply",new Dictionary<string,bool>());Check(backend.Active,"pickup can still be enabled after a transient timeout");
        await manager.DisableAsync("autoloot");Check(!backend.Active&&model.SubscriptionCount==0,"real cancellation still stops polling and revokes pickup");
    }
    sealed class UtilityBackend : IUtilityBackend
    {
        public readonly List<UtilityRequest> Requests=[];public int Commits;
        public ValueTask<UtilityReply> ExecuteUtilityAsync(InstanceIdentity id,long generation,UtilityRequest q,CancellationToken cancel){
            Requests.Add(q);string binding=id.Pid.ToString("x32");
            if(q.Mode!="status"&&q.Binding!=binding)throw new InvalidOperationException("binding");
            if(q.Mode=="commit")Commits++;
            return ValueTask.FromResult(new UtilityReply(true,binding,"room-fixture",q.Mode=="status"?"便捷功能已就绪":"fixture",q.Mode=="preview"?new string('a',32):null,q.Mode=="warehouse-open"?"native-warehouse":q.Kind,1,100,q.Mode=="commit",q.Mode=="warehouse"?[new(q.ListType,65,700,"fixture item",1,0)]:null,1));
        }
    }
    static async Task NativePackagePolicy()
    {
        Func<PluginManifest,PluginManifest> allow=m=>m with{Capabilities=["process.summary","ui.panel","player.utility","native.ui.warehouse"]};
        var good=Packages.Verify(Package("utilities",type:"Arad.Helper.Utility.UtilityPlugin",modify:allow,native:true),Public);Check(good.Images.Count==3,"signed native capability admits its two x86 components");
        foreach(var test in new[]{("missing-files",Package("utilities",type:"Arad.Helper.Utility.UtilityPlugin",modify:allow)),("missing-capability",Package("utilities",type:"Arad.Helper.Utility.UtilityPlugin",native:true)),("wrong-plugin",Package("status",modify:allow,native:true)),("wrong-architecture",Package("utilities",type:"Arad.Helper.Utility.UtilityPlugin",modify:allow,native:true,nativeMachine:0x8664))})await Reject(()=>{Packages.Verify(test.Item2,Public);return Task.CompletedTask;},"reject native "+test.Item1);
        var model=new InstanceModel();model.Update([A],A.Identity);var backend=new UtilityBackend();await using var manager=new PluginManager(model,Public,utility:backend);
        await manager.EnableAsync(Package("utilities",type:"Arad.Helper.Utility.UtilityPlugin",modify:m=>m with{Capabilities=["process.summary","ui.panel","player.utility"]}));
        await Reject(()=>manager.ExecuteAsync("utilities","cargo",new Dictionary<string,bool>()),"native warehouse requires explicit plugin capability");Check(!backend.Requests.Any(q=>q.Mode=="warehouse-open"),"unauthorized plugin cannot send native opening request");
    }
    static async Task UtilityModule()
    {
        var model=new InstanceModel();model.Update([A,B],A.Identity);var backend=new UtilityBackend();HelperPanel? panel=null;
        await using var manager=new PluginManager(model,Public,utility:backend);manager.PanelChanged+=(_,p)=>panel=p;
        string path=Package("utilities",type:"Arad.Helper.Utility.UtilityPlugin",modify:m=>m with{Capabilities=["process.summary","ui.panel","player.utility","native.ui.warehouse"]},native:true);
        await manager.EnableAsync(path);manager.Freeze();
        for(int n=0;n<120&&panel?.Summary!="便捷功能已就绪";n++)await Task.Delay(50);
        Check(panel?.Summary=="便捷功能已就绪","successful status clears the initial waiting message");
        await Reject(()=>manager.DisableAsync("utilities"),"startup plugin set frozen");await Reject(()=>manager.EnableAsync(path),"runtime reload disabled");
        var choices=new Dictionary<string,bool>{{"sell.4",true},{"disjoint.2",true}};
        await manager.ExecuteAsync("utilities","settings",choices);
        Check(panel!.Toggles.Single(x=>x.Key=="sell.4").Value,"runtime sale rarity configured on game panel");
        await manager.ExecuteAsync("utilities","sell",new Dictionary<string,bool>());
        Check(backend.Commits==1&&backend.Requests.Last(x=>x.Mode=="preview").RarityMask==16,"sale hotkey uses configured epic filter and commits once");
        await manager.ExecuteAsync("utilities","disjoint",new Dictionary<string,bool>());
        Check(backend.Commits==2&&backend.Requests.Last(x=>x.Mode=="preview").RarityMask==4,"disjoint uses its separate rarity mask");
        await manager.ExecuteAsync("utilities","repair",new Dictionary<string,bool>());Check(backend.Commits==2&&panel!.Actions.Any(x=>x.Key=="confirm"),"repair quotes before spending");
        await manager.ExecuteAsync("utilities","cancel",new Dictionary<string,bool>());
        await manager.ExecuteAsync("utilities","cargo",new Dictionary<string,bool>());Check(panel!.Rows.Count==0 && backend.Requests.Last().Mode=="warehouse-open","warehouse opens native UI without a helper inventory list");
        model.Update([A,B],B.Identity);await manager.ExecuteAsync("utilities","room.right",new Dictionary<string,bool>());
        Check(backend.Requests.Last().Direction=="right"&&backend.Requests.Last().Binding==B.Identity.Pid.ToString("x32"),"directional room action follows current bound window");
        string prefs=await manager.ExportSettingsAsync("utilities");Check(prefs.Contains("SellMask")&&!prefs.Contains("Credential"),"only rarity preferences exported");
    }
    static async Task Main(string[] args)
    {
        if (args[0] == "--verify") {
            var package = Packages.Verify(args[1], File.ReadAllText(args[2]));
            Console.WriteLine(JsonSerializer.Serialize(new { passed = true, id = package.Manifest.Id, version = package.Manifest.Version, signedFiles = package.Images.Count })); return;
        }
        Loot = args.Length>3?Path.GetFullPath(args[3]):"";Utility=args.Length>4?Path.GetFullPath(args[4]):"";
        Root = Path.GetFullPath(args[0]); Directory.CreateDirectory(Root); Status = Path.GetFullPath(args[1]); Fixture = Path.GetFullPath(args[2]); Key = RSA.Create(2048);
        try {
            await Lifecycle(); if(Loot!=""){await LootModule();await LootTimeoutRecovery();}if(Utility!="")await NativePackagePolicy();await UtilityModule(); var weak = await RepeatedReloads();
            for (int i = 0; i < 8 && weak.Any(x => x.IsAlive); i++) { GC.Collect(); GC.WaitForPendingFinalizers(); GC.Collect(); await Task.Delay(30); }
            Check(weak.All(x => !x.IsAlive), "collectible contexts physically released");
            var report = new { passed = true, checks = count, reloads = 50, collectedContexts = weak.Length, realGameTouched = false, playerAssetsTouched = false, runtime = Environment.Version.ToString() };
            File.WriteAllText(Path.Combine(Root, "checks.json"), JsonSerializer.Serialize(report)); Console.WriteLine(JsonSerializer.Serialize(report));
        } finally { Key.Dispose(); foreach (var d in Directory.GetDirectories(Root)) Directory.Delete(d, true); }
    }
}
