using System.Reflection;
using System.Runtime.InteropServices;
using System.Text.Json;
using Arad.Helper.Contracts;
using Arad.Helper.Runtime;

namespace Arad.Helper.Host;

static class Program
{
    static string? Argument(string[] args, string key) { int i = Array.IndexOf(args, key); return i >= 0 && i + 1 < args.Length ? args[i + 1] : null; }
    [STAThread] static int Main(string[] args)
    {
        ApplicationConfiguration.Initialize();
        try {
            string root = Path.GetFullPath(Argument(args, "--root") ?? throw new InvalidDataException("请从本服登录器进入游戏。"));
            if (!ClientFiles.ValidRoot(root)) throw new InvalidDataException("找不到本服客户端目录。");
            if (args.Contains("--probe")) {
                string output = Argument(args, "--output") ?? throw new ArgumentException();
                var instances = new GameProcesses(ClientFiles.Resolve(root)).Scan();
                File.WriteAllText(output, JsonSerializer.Serialize(new { readOnly = true, memoryAccess = false, instances })); return 0;
            }
            string? fixture = Argument(args, "--ui-test");
            bool background = args.Contains("--background");
            string client = Path.GetFullPath(Argument(args, "--client") ?? ClientFiles.Resolve(root));
            if (!client.Equals(Path.GetFullPath(ClientFiles.Resolve(root)), StringComparison.OrdinalIgnoreCase)) throw new InvalidDataException("客户端目录不匹配。");
            string identity = Convert.ToHexString(System.Security.Cryptography.SHA256.HashData(System.Text.Encoding.UTF8.GetBytes(root.ToUpperInvariant())))[..16];
            using var mutex = new Mutex(true, "Local\\Arad.Helper." + Environment.UserName + "." + identity + (fixture is null ? "" : ".fixture"), out bool first);
            if (!first && fixture is null) return 0;
            if (background && (!int.TryParse(Argument(args, "--game-pid"), out int pid) || !long.TryParse(Argument(args, "--game-created"), out long created) ||
                !new GameProcesses(client).Scan().Any(x => x.Identity.Pid == pid && x.Identity.CreatedFileTime == created))) return 0;
            using var stream = Assembly.GetExecutingAssembly().GetManifestResourceStream("Arad.Helper.PublicKey")!;
            using var reader = new StreamReader(stream);
            Application.Run(new HelperWindow(root, reader.ReadToEnd(), fixture, background)); return 0;
        } catch (Exception e) {
            if (!args.Contains("--background")) MessageBox.Show(e.Message, "阿拉德内辅", MessageBoxButtons.OK, MessageBoxIcon.Information); return 1;
        }
    }
}

// Message pump only. There is deliberately no desktop management window.
sealed class HelperWindow : Form
{
    readonly InstanceModel model = new();
    readonly PluginManager plugins;
    readonly GameProcesses processes;
    readonly SessionBroker? broker;
    readonly FixtureLoot? fixtureLoot;
    readonly StartupConfiguration config;
    readonly System.Windows.Forms.Timer timer = new() { Interval = 700 };
    readonly string root, key;
    readonly string? fixture;
    readonly bool background;
    readonly Dictionary<string, HelperPanel> panels = [];
    GameInstance[] instances = [];
    GamePanel? gamePanel;
    GameHotkey? hotkey;
    DateTime? emptySince;
    bool scanning, closing, exitReady, invoking;int previousCount=-1;

    public HelperWindow(string root, string key, string? fixture, bool background)
    {
        this.root=root; this.key=key; this.fixture=fixture; this.background=background;
        config=StartupConfiguration.Load(root);
        ShowInTaskbar=false; FormBorderStyle=FormBorderStyle.None;
        processes=new(ClientFiles.Resolve(root));
        broker=fixture is null?new SessionBroker(root,processes):null;
        fixtureLoot=fixture is not null?new FixtureLoot():null;
        plugins=new(model,key,(ILootBackend?)broker??fixtureLoot,config.PluginSettings,broker);
        plugins.PanelChanged+=(id,p)=>Ui(()=>{if(p is null)panels.Remove(id);else panels[id]=p;gamePanel?.SetPanel(id,p);});
        timer.Tick+=async(_,_)=>await Scan();
        FormClosing+=async(_,e)=>{
            if(exitReady)return;
            e.Cancel=true;if(closing)return;closing=true;timer.Stop();hotkey?.Dispose();gamePanel?.Dismiss(false);
            await plugins.DisposeAsync();if(broker is not null)await broker.DisposeAsync();Receipt("stopped");exitReady=true;BeginInvoke(new Action(Close));
        };
    }
    protected override void SetVisibleCore(bool value){if(!IsHandleCreated)CreateHandle();base.SetVisibleCore(false);}
    protected override void OnHandleCreated(EventArgs e){base.OnHandleCreated(e);BeginInvoke(new Action(async()=>await Start()));}
    async Task Start()
    {
        try {
            // Immutable signed package selection and settings for this process.
            var found=new Dictionary<string,(Version Version,string Path)>();
            foreach(string directory in DiscoverPackages(Path.Combine(root,"helper/plugins")).Concat(DiscoverPackages(Path.Combine(AppContext.BaseDirectory,"helper-plugins")))) {
                try {var p=Packages.Verify(directory,key);if(!Version.TryParse(p.Manifest.Version,out var v))continue;
                    if(!found.TryGetValue(p.Manifest.Id,out var old)||v>old.Version)found[p.Manifest.Id]=(v,directory);
                }catch(Exception e)when(e is IOException or UnauthorizedAccessException or System.Security.Cryptography.CryptographicException or JsonException or FormatException){}
            }
            if(broker is not null && config.EnabledPlugins.Contains("utilities") && found.TryGetValue("utilities",out var nativePackage)){var verified=Packages.Verify(nativePackage.Path,key);if(verified.Manifest.Capabilities.Contains("native.ui.warehouse"))broker.ConfigureNativeWarehouse(verified);}
            await Scan();
            foreach(string id in config.EnabledPlugins){if(!found.TryGetValue(id,out var p))throw new InvalidDataException("缺少已签名插件："+id);await plugins.EnableAsync(p.Path);}
            plugins.Freeze();Receipt("background");
            if(fixture is not null){await ExerciseUi();Close();return;}
            hotkey=new GameHotkey(Handle,()=>processes.Scan().Any(x=>x.Foreground&&x.KnownClient)||gamePanel?.OwnsForeground==true,config.Hotkeys,command=>_ = InvokeHotkey(command));
            timer.Start();
        } catch(Exception e){Receipt("startup-error",e.Message);Close();}
    }
    static IEnumerable<string> DiscoverPackages(string directory)
    {
        if(!Directory.Exists(directory))yield break;
        int count=0;
        foreach(var id in Directory.EnumerateDirectories(directory).Take(20)){
            if(File.GetAttributes(id).HasFlag(FileAttributes.ReparsePoint))continue;
            foreach(var version in Directory.EnumerateDirectories(id).Take(10))if(File.Exists(Path.Combine(version,"package.json"))){if(++count>32)yield break;yield return version;}
        }
    }
    void Ui(Action action){if(IsDisposed)return;if(InvokeRequired){if(IsHandleCreated)BeginInvoke(new Action(()=>{if(!IsDisposed)action();}));}else action();}
    async Task Scan()
    {
        if(scanning||closing)return;scanning=true;
        try {
            instances=fixture is null?await Task.Run(processes.Scan):[
                new(new(10001,101,"C:/fixture/client/DNF.exe",Packages.GameId),"测试窗口A","fixture",true,true,true),
                new(new(10002,102,"C:/fixture/client/DNF.exe",Packages.GameId),"测试窗口B","fixture",true,false,true)];
            var selected=instances.FirstOrDefault(x=>x.Foreground&&x.KnownClient)?.Identity??model.Selected;
            if(!instances.Any(x=>x.Identity==selected&&x.KnownClient))selected=instances.FirstOrDefault(x=>x.KnownClient)?.Identity;
            broker?.UpdateInstances(instances);model.Update(instances,selected);
            hotkey?.Refresh();
            if(gamePanel is not null && (selected is null||gamePanel.Scope!=$"{selected.Pid}:{selected.CreatedFileTime}")){gamePanel.Dispose();gamePanel=null;}
            if(previousCount!=instances.Length){previousCount=instances.Length;Receipt("tracking");}
            if(background&&fixture is null){emptySince=instances.Length==0?emptySince??DateTime.UtcNow:null;if(emptySince is {} empty&&DateTime.UtcNow-empty>TimeSpan.FromSeconds(5))Close();}
        }finally{scanning=false;}
    }
    GamePanel? Panel()
    {
        var id=model.Selected;if(id is null||!instances.Any(x=>x.Identity==id&&x.KnownClient))return null;
        nint window=processes.Window(id);if(window==0)return null;
        string scope=$"{id.Pid}:{id.CreatedFileTime}";
        if(gamePanel is null||gamePanel.IsDisposed||gamePanel.Scope!=scope||gamePanel.GameHandle!=window){
            gamePanel?.Dispose();gamePanel=new(window,()=>model.Selected==id?processes.Window(id):0,scope,async(plugin,editor,action,values)=>{
                if(model.Selected!=id||panels.GetValueOrDefault(plugin)?.EditorKey!=editor)throw new InvalidOperationException("角色已变化，请等待面板刷新");
                await plugins.ExecuteAsync(plugin,action,values);if(plugin=="utilities"&&action=="cargo")gamePanel?.Dismiss();if(plugin=="utilities"&&action=="settings")SaveUtilitySettings(await plugins.ExportSettingsAsync(plugin));
            },config.EnabledPlugins,config.Hotkeys);
            foreach(var pair in panels)gamePanel.SetPanel(pair.Key,pair.Value);
        }
        return gamePanel;
    }
    async Task InvokeHotkey(string command)
    {
        if(invoking||closing)return;invoking=true;
        try {
            await Scan();var panel=Panel();if(panel is null)return;
            if(command=="panel"){panel.Toggle();Receipt(panel.Requested?"visible":"hidden");return;}
            string plugin=command=="pickup"?"autoloot":"utilities";
            if(!config.EnabledPlugins.Contains(plugin))return;
            if(command=="warehouse")gamePanel?.Dismiss();
            if(command=="repair"){
                panel.SelectPlugin(plugin);if(!panel.Requested)panel.Toggle();
            }
            await plugins.ExecuteAsync(plugin,command=="pickup"?"toggle":command=="warehouse"?"cargo":command,new Dictionary<string,bool>());
        }catch(Exception e){ReportHotkeyError(Panel(),command,e.Message);}
        finally{invoking=false;}
    }
    static void ReportHotkeyError(GamePanel? panel,string command,string message)
    {
        if(panel is null)return;
        panel.ShowError(message);
        // A rejected room move is normal while monsters or a closed gate remain.
        // Keep the player's panel visibility; Ctrl+Z remains the explicit opener.
        if(!panel.Requested&&!command.StartsWith("room.",StringComparison.Ordinal))panel.Toggle();
    }
    void SaveUtilitySettings(string json)
    {
        string path=Path.Combine(root,"helper/config.json");
        var options=new JsonSerializerOptions{PropertyNameCaseInsensitive=true,WriteIndented=true};
        var latest=JsonSerializer.Deserialize<StartupConfiguration>(File.ReadAllText(path),options)??config;
        var utility=JsonSerializer.Deserialize<UtilitySettings>(json)??throw new InvalidDataException("配置无效");
        File.WriteAllText(path+".tmp",JsonSerializer.Serialize(latest with{Utilities=utility},options));File.Move(path+".tmp",path,true);
    }
    void Receipt(string phase,string? error=null)
    {
        if(fixture is not null)return;
        try{string dir=Path.Combine(root,"helper/state");Directory.CreateDirectory(dir);string path=Path.Combine(dir,"session.json");
            File.WriteAllText(path+".tmp",JsonSerializer.Serialize(new{phase,hostPid=Environment.ProcessId,gameCount=instances.Length,background,utc=DateTime.UtcNow,memoryAccess=false,activePlugins=plugins.ActiveCount,startupFixed=true,error}));File.Move(path+".tmp",path,true);
        }catch(Exception e)when(e is IOException or UnauthorizedAccessException){}
    }
    protected override void WndProc(ref Message m){if(hotkey?.HandleMessage(m.Msg,m.WParam)==true)return;base.WndProc(ref m);}
    async Task ExerciseUi()
    {
        string output=Path.GetFullPath(fixture!);Directory.CreateDirectory(output);
        try{
            bool hidden=!Visible&&!ShowInTaskbar;
            bool frozen=false;try{await plugins.DisableAsync(config.EnabledPlugins[0]);}catch(InvalidOperationException){frozen=true;}
            using var fake=new KeyboardFixtureForm{Text="Owned game surface fixture",ClientSize=new Size(960,600),StartPosition=FormStartPosition.Manual,Location=new Point(80,80),BackColor=Color.FromArgb(44,54,50)};
            fake.Show();fake.Activate();await Task.Delay(100);
            bool invoked=false;
            using var panel=new GamePanel(fake.Handle,()=>fake.IsDisposed?0:fake.Handle,"fixture",(plugin,editor,action,values)=>{invoked=plugin=="utilities"&&action=="repair";return Task.CompletedTask;},["autoloot","utilities"],config.Hotkeys);
            var toggles=new List<HelperToggle>{new("gold","金币",true){Editable=false}};
            foreach(var (bit,name) in new[]{(0,"普通"),(1,"高级"),(2,"稀有"),(3,"神器"),(6,"传说"),(4,"史诗"),(5,"勇者")})foreach(var (prefix,label) in new[]{("equip.","装备"),("stack.","材料/消耗品")})toggles.Add(new(prefix+bit,label+" · "+name,false){Editable=false});
            panel.SetPanel("autoloot",new("自动拾取","品级在启动前配置；Alt+Q 开关拾取",[new("当前角色","窗口测试角色"),new("拾取状态","未开启")]){EditorKey="fixture",Toggles=toggles,Actions=[new("apply","开启拾取"),new("stop","暂停拾取")]});
            var utilityData=new HelperPanel("便捷功能","便捷功能已就绪",[]){EditorKey="fixture-u",Toggles=[new("sell.0","出售 · 普通",false),new("sell.1","出售 · 高级",true)],Rows=[new("row.0.9","测试装备","×1")],Actions=[new("repair","装备维修"),new("sell","一键出售"),new("disjoint","一键分解"),new("cargo","远程仓库")]};panel.SetPanel("utilities",utilityData);
            panel.Toggle();for(int attempt=0;attempt<30&&!panel.Visible;attempt++){SetForegroundWindow(fake.Handle);await Task.Delay(100);}
            bool visible=panel.Visible&&fake.RectangleToScreen(fake.ClientRectangle).Contains(panel.Bounds);
            bool roomErrorsKeepPanelState=true;
            foreach(string direction in new[]{"left","up","right","down"}){
                panel.Dismiss();ReportHotkeyError(panel,"room."+direction,"房间未清理");
                await Task.Delay(30);roomErrorsKeepPanelState&=!panel.Requested&&!panel.Visible&&GetForegroundWindow()==fake.Handle;
                panel.Toggle();ReportHotkeyError(panel,"room."+direction,"没有可通行的房间");
                await Task.Delay(30);roomErrorsKeepPanelState&=panel.Requested&&panel.Visible&&GetForegroundWindow()==fake.Handle;
            }
            bool shortcutHints=panel.FixtureRoomHint.Contains("Alt+←")&&panel.FixtureRoomHint.Contains("Alt+↑")&&panel.FixtureRoomHint.Contains("Alt+→")&&panel.FixtureRoomHint.Contains("Alt+↓")&&panel.FixtureRoomHint.Contains("清怪后")&&panel.FixtureRoomHint.Contains("队长");
            using(var custom=new GamePanel(fake.Handle,()=>fake.Handle,"fixture-hints",(_,_,_,_)=>Task.CompletedTask,["utilities"],new Dictionary<string,string>(config.Hotkeys){["room.left"]="Alt+J"})){shortcutHints&=custom.FixtureRoomHint.Contains("Alt+J")&&!custom.FixtureRoomHint.Contains("Alt+←");}
            bool readOnly=panel.FixtureReadOnlyFilters();panel.FixtureCapture(Path.Combine(output,"game-panel.png"));
            var chordDefinitions=new (string Name,byte[] Modifiers,byte Key)[]{("Ctrl+A",[0x11],0x41),("Ctrl+Q",[0x11],0x51),("Shift+Q",[0x10],0x51),("Alt+E",[0x12],0x45),("Alt+Z",[0x12],0x5a),("Ctrl+Shift+Z",[0x11,0x10],0x5a)};
            var chordBaseline=new Dictionary<string,(bool Foreground,Keys[] Down,Keys[] Up)>();
            foreach(var chord in chordDefinitions){fake.KeysSeen.Clear();fake.KeysReleased.Clear();PressChord(chord.Modifiers,chord.Key);await Task.Delay(80);chordBaseline.Add(chord.Name,(GetForegroundWindow()==fake.Handle,fake.KeysSeen.ToArray(),fake.KeysReleased.ToArray()));}
            fake.KeysSeen.Clear();fake.KeysReleased.Clear();
            var observed=new HashSet<string>();hotkey=new GameHotkey(Handle,()=>GetForegroundWindow()==fake.Handle||GamePanel.IsGameSurface(fake.Handle,GetForegroundWindow())||panel.OwnsForeground,config.Hotkeys,c=>observed.Add(c));
            PressKey(0x27);await Task.Delay(80);bool keysWhileOpen=GetForegroundWindow()==fake.Handle&&fake.KeysSeen.SequenceEqual(new[]{Keys.Right});
            panel.SelectPlugin("utilities");panel.FixtureMouseClick("sell.0");await Task.Delay(80);PressKey(0x5a);await Task.Delay(80);bool keysAfterCheckbox=GetForegroundWindow()==fake.Handle&&fake.KeysSeen.LastOrDefault()==Keys.Z&&fake.KeysSeen.Count==2;
            panel.FixtureMouseClick("repair");await Task.Delay(80);PressKey(0x58);await Task.Delay(80);bool keysAfterButton=invoked&&GetForegroundWindow()==fake.Handle&&fake.KeysSeen.LastOrDefault()==Keys.X&&fake.KeysSeen.Count==3;
            panel.FixtureMouseClick("row");await Task.Delay(80);PressKey(0x27);await Task.Delay(80);bool keysAfterList=GetForegroundWindow()==fake.Handle&&fake.KeysSeen.LastOrDefault()==Keys.Right&&fake.KeysSeen.Count==4;
            panel.FixtureWheel();await Task.Delay(60);PressKey(0x41);await Task.Delay(80);bool keysAfterWheel=GetForegroundWindow()==fake.Handle&&fake.KeysSeen.LastOrDefault()==Keys.A&&fake.KeysSeen.Count==5;
            panel.FixtureMouseClick("blank");await Task.Delay(60);PressKey(0x25);await Task.Delay(80);bool keysAfterBackground=GetForegroundWindow()==fake.Handle&&fake.KeysSeen.LastOrDefault()==Keys.Left&&fake.KeysSeen.Count==6;
            byte[] basicKeys=[..Enumerable.Range(0x41,26).Select(x=>(byte)x),..Enumerable.Range(0x30,10).Select(x=>(byte)x),..Enumerable.Range(0x70,12).Select(x=>(byte)x),..Enumerable.Range(0x60,10).Select(x=>(byte)x),0x6a,0x6b,0x6d,0x6e,0x6f,0x08,0x09,0x0d,0x1b,0x20,0x21,0x22,0x23,0x24,0x25,0x26,0x27,0x28,0x2d,0x2e,0xba,0xbb,0xbc,0xbd,0xbe,0xbf,0xc0,0xdb,0xdc,0xdd,0xde];
            var keyboardRows=new List<object>();
            async Task<bool> ProbeBasicKeys(string context,Action? click=null){
                click?.Invoke();await Task.Delay(60);fake.KeysSeen.Clear();fake.KeysReleased.Clear();
                foreach(byte k in basicKeys){PressKey(k);await Task.Delay(12);}
                await Task.Delay(60);var expected=basicKeys.Select(x=>(Keys)x).ToArray();
                bool passed=GetForegroundWindow()==fake.Handle&&fake.KeysSeen.SequenceEqual(expected)&&fake.KeysReleased.SequenceEqual(expected)&&observed.Count==0;
                keyboardRows.Add(new{context,passed,keys=expected.Select(x=>x.ToString()).ToArray(),down=fake.KeysSeen.Select(x=>x.ToString()).ToArray(),up=fake.KeysReleased.Select(x=>x.ToString()).ToArray(),helperCommands=observed.ToArray()});return passed;
            }
            bool allBasicKeys=await ProbeBasicKeys("panel-open")&await ProbeBasicKeys("checkbox",()=>panel.FixtureMouseClick("sell.0"))&await ProbeBasicKeys("button",()=>panel.FixtureMouseClick("repair"))&await ProbeBasicKeys("list",()=>panel.FixtureMouseClick("row"))&await ProbeBasicKeys("wheel",()=>panel.FixtureWheel())&await ProbeBasicKeys("background",()=>panel.FixtureMouseClick("blank"));
            var chordRows=new List<object>();bool unboundChords=true;
            foreach(var (name,modifiers,keyCode) in chordDefinitions){
                fake.KeysSeen.Clear();fake.KeysReleased.Clear();PressChord(modifiers,keyCode);await Task.Delay(80);
                var baseline=chordBaseline[name];bool deliveredToGame=fake.KeysSeen.Contains((Keys)keyCode)&&fake.KeysReleased.Contains((Keys)keyCode);
                bool passed=GetForegroundWindow()==fake.Handle&&baseline.Foreground&&fake.KeysSeen.SequenceEqual(baseline.Down)&&fake.KeysReleased.SequenceEqual(baseline.Up)&&observed.Count==0;unboundChords&=passed;chordRows.Add(new{name,passed,deliveredToGame,baselineDeliveredToGame=baseline.Down.Contains((Keys)keyCode)&&baseline.Up.Contains((Keys)keyCode),baselineDown=baseline.Down.Select(x=>x.ToString()).ToArray(),baselineUp=baseline.Up.Select(x=>x.ToString()).ToArray(),foreground=GetForegroundWindow()==fake.Handle,down=fake.KeysSeen.Select(x=>x.ToString()).ToArray(),up=fake.KeysReleased.Select(x=>x.ToString()).ToArray(),helperCommands=observed.ToArray()});
            }
            fake.KeysSeen.Clear();fake.KeysReleased.Clear();keybd_event(0x27,0,0,0);await Task.Delay(30);
            foreach(byte k in new byte[]{0x51,0x57,0x45,0x52,0x41,0x53,0x44,0x46,0x5a,0x58}){PressKey(k);await Task.Delay(12);}
            keybd_event(0x27,0,2,0);await Task.Delay(80);
            bool heldMovementWithSkills=GetForegroundWindow()==fake.Handle&&fake.KeysSeen.SequenceEqual(new[]{Keys.Right,Keys.Q,Keys.W,Keys.E,Keys.R,Keys.A,Keys.S,Keys.D,Keys.F,Keys.Z,Keys.X})&&fake.KeysReleased.SequenceEqual(new[]{Keys.Q,Keys.W,Keys.E,Keys.R,Keys.A,Keys.S,Keys.D,Keys.F,Keys.Z,Keys.X,Keys.Right})&&observed.Count==0;
            bool ownedByGame=panel.FixtureOwnedByGame(),ownedChatRetainsKeyboard=false,ownedChatCtrlZ=false;object? ownedChatTrace=null;
            using(var chat=new KeyboardFixtureForm{Text="Owned game chat fixture",ClientSize=new Size(332,600),ShowInTaskbar=false,StartPosition=FormStartPosition.Manual,Location=new Point(fake.Right,fake.Top)}){
                chat.Show(fake);chat.Activate();for(int attempt=0;attempt<20&&GetForegroundWindow()!=chat.Handle;attempt++){SetForegroundWindow(chat.Handle);await Task.Delay(50);}await Task.Delay(100);
                foreach(byte k in basicKeys){PressKey(k);await Task.Delay(12);}
                await Task.Delay(60);var expected=basicKeys.Select(x=>(Keys)x).ToArray();ownedChatRetainsKeyboard=panel.Visible&&GetForegroundWindow()==chat.Handle&&chat.KeysSeen.SequenceEqual(expected)&&chat.KeysReleased.SequenceEqual(expected);
                Press(0x11,0x5a);await Task.Delay(100);ownedChatCtrlZ=observed.SetEquals(new[]{"panel"});ownedChatTrace=new{visible=panel.Visible,foreground=GetForegroundWindow()==chat.Handle,ownedByGame=GamePanel.IsGameSurface(fake.Handle,chat.Handle),down=chat.KeysSeen.Select(x=>x.ToString()).ToArray(),up=chat.KeysReleased.Select(x=>x.ToString()).ToArray(),commands=observed.ToArray()};observed.Clear();chat.Close();fake.Activate();await Task.Delay(100);
            }
            panel.SetPanel("utilities",utilityData with{Rows=[new("row.0.9","测试装备","×2")]});bool rowsRefreshed=panel.FixtureRowText().Contains("×2");panel.FixtureCapture(Path.Combine(output,"utilities.png"));
            var before=panel.Location;fake.Location=new Point(fake.Left+30,fake.Top+20);await Task.Delay(120);bool moved=panel.Location==new Point(before.X+30,before.Y+20);
            fake.WindowState=FormWindowState.Minimized;await Task.Delay(120);bool minimized=!panel.Visible;
            fake.WindowState=FormWindowState.Normal;fake.Activate();await Task.Delay(120);bool restored=panel.Visible;panel.Dismiss();bool dismissed=!panel.Visible&&!panel.Requested;
            var keys=GameHotkey.Parse(config.Hotkeys);bool hotkeys=keys["panel"]==(2u,0x5au)&&keys.Where(x=>x.Key!="panel").All(x=>x.Value.Mod==1u)&&keys["room.left"].Key==0x25;
            fake.Activate();await Task.Delay(150);foreach(var binding in keys.Values){Press((byte)(binding.Mod==2?0x11:0x12),(byte)binding.Key);await Task.Delay(80);}
            bool nativeHotkeys=observed.SetEquals(keys.Keys);hotkey.Dispose();hotkey=null;
            bool allow=false;observed.Clear();hotkey=new GameHotkey(Handle,()=>allow,config.Hotkeys,c=>observed.Add(c));allow=true;hotkey.Refresh();Press(0x11,0x5a);await Task.Delay(80);bool periodicHotkeyRefresh=observed.SetEquals(new[]{"panel"});hotkey.Dispose();hotkey=null;
            var leaseRecovery=await SessionBroker.ExerciseRecovery(Path.Combine(output,"connection-fixtures"));
            var nativeWarehouse=await SessionBroker.ExerciseNativeWarehouse(Path.Combine(output,"native-permission-fixtures"));
            File.WriteAllText(Path.Combine(output,"ui-result.json"),JsonSerializer.Serialize(new{passed=roomErrorsKeepPanelState&&shortcutHints&&hidden&&frozen&&visible&&readOnly&&invoked&&moved&&minimized&&restored&&dismissed&&hotkeys&&nativeHotkeys&&keysWhileOpen&&keysAfterCheckbox&&keysAfterButton&&keysAfterList&&keysAfterWheel&&keysAfterBackground&&rowsRefreshed&&allBasicKeys&&unboundChords&&heldMovementWithSkills&&ownedByGame&&ownedChatRetainsKeyboard&&ownedChatCtrlZ&&periodicHotkeyRefresh&&leaseRecovery.Values.All(x=>x)&&nativeWarehouse.Values.All(x=>x),roomErrorsKeepPanelState,shortcutHints,hidden,frozen,visible,readOnly,invoked,moved,minimized,restored,dismissed,hotkeys,nativeHotkeys,keysWhileOpen,keysAfterCheckbox,keysAfterButton,keysAfterList,keysAfterWheel,keysAfterBackground,rowsRefreshed,allBasicKeys,unboundChords,heldMovementWithSkills,ownedByGame,ownedChatRetainsKeyboard,ownedChatCtrlZ,periodicHotkeyRefresh,leaseRecovery,nativeWarehouse,basicKeyCount=basicKeys.Length,ownedChatTrace,keyboardRows,chordRows,standaloneManager=false,realGameTouched=false}));
        }catch(Exception e){File.WriteAllText(Path.Combine(output,"error.txt"),e.ToString());}
    }
    static void PressKey(byte key){keybd_event(key,0,0,0);keybd_event(key,0,2,0);}
    static void Press(byte modifier,byte key){keybd_event(modifier,0,0,0);keybd_event(key,0,0,0);keybd_event(key,0,2,0);keybd_event(modifier,0,2,0);}
    static void PressChord(byte[] modifiers,byte key){foreach(byte m in modifiers)keybd_event(m,0,0,0);PressKey(key);foreach(byte m in modifiers.Reverse())keybd_event(m,0,2,0);}
    [DllImport("user32.dll")]static extern void keybd_event(byte key,byte scan,uint flags,nuint extra);
    [DllImport("user32.dll")]static extern nint GetForegroundWindow();
    [DllImport("user32.dll")]static extern nint GetAncestor(nint window,uint flags);
    [DllImport("user32.dll")]static extern bool SetForegroundWindow(nint window);
    protected override void Dispose(bool disposing){if(disposing){gamePanel?.Dispose();hotkey?.Dispose();timer.Dispose();}base.Dispose(disposing);}
}

sealed class KeyboardFixtureForm : Form
{
 internal List<Keys> KeysSeen{get;}=[];
 internal List<Keys> KeysReleased{get;}=[];
 protected override bool IsInputKey(Keys keyData)=>true;
 protected override void OnKeyDown(KeyEventArgs e){KeysSeen.Add(e.KeyCode);base.OnKeyDown(e);}
 protected override void OnKeyUp(KeyEventArgs e){KeysReleased.Add(e.KeyCode);base.OnKeyUp(e);}
 protected override void WndProc(ref Message m){if(m.Msg==0x112&&((long)m.WParam&0xfff0)==0xf100)return;base.WndProc(ref m);}
}
