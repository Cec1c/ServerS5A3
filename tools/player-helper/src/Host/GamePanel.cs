using System.Drawing.Drawing2D;
using System.Runtime.InteropServices;
using Arad.Helper.Contracts;

namespace Arad.Helper.Host;

// An owned desktop composition surface inside the game's client rectangle.
// No game renderer addresses, remote threads, memory writes, or DLL injection.
// Ownership is immutable: create a fresh surface when switching game windows.
sealed class GamePanel : Form
{
    readonly nint game;
    readonly Func<nint> currentWindow;
    readonly Func<string,string,string,IReadOnlyDictionary<string,bool>,Task> execute;
 readonly Dictionary<string,HelperPanel> panels=[]; readonly Dictionary<string,string> tabNames; string selected="autoloot";readonly FlowLayoutPanel tabs=new(){Dock=DockStyle.Top,Height=34};

    readonly System.Windows.Forms.Timer tracking=new(){Interval=50};
    readonly Label heading=new(){Dock=DockStyle.Top,Height=30,Text="阿拉德内辅",ForeColor=Color.Wheat,BackColor=Color.Transparent,TextAlign=ContentAlignment.MiddleCenter};
    readonly Label status=new(){AutoSize=false,Height=28,Dock=DockStyle.Top,TextAlign=ContentAlignment.MiddleLeft};
    readonly Label summary=new(){AutoSize=false,Height=36,Dock=DockStyle.Top};
    readonly Panel editor=new(){Dock=DockStyle.Fill,AutoScroll=true};
    readonly Dictionary<string,CheckBox> checks=[];
    readonly Dictionary<string,Button> buttons=[];NoFocusListBox? activeTable;
    string editorKey="",schema="";
    bool requested,working,dragging;
    Point dragStart,offset;
    bool positioned;
    readonly string characterScope;
    public string Scope=>characterScope;
    public bool OwnsForeground=>Visible && GetForegroundWindow()==Handle;
    public nint GameHandle=>game;
    public bool Requested=>requested;
    sealed record GameOwner(nint Handle) : IWin32Window;
    public GamePanel(nint game,Func<nint> currentWindow,string scope,Func<string,string,string,IReadOnlyDictionary<string,bool>,Task> execute,IReadOnlyList<string> plugins,IReadOnlyDictionary<string,string> bindings)
    {
        this.game=game;this.currentWindow=currentWindow;this.execute=execute;tabNames=plugins.ToDictionary(x=>x,x=>x=="autoloot"?"自动拾取":x=="utilities"?"便捷功能":"状态");selected=plugins.FirstOrDefault()??"autoloot";characterScope=scope;
        SetStyle(ControlStyles.Selectable,false);TabStop=false;AutoScaleMode=AutoScaleMode.None;FormBorderStyle=FormBorderStyle.None;ShowInTaskbar=false;StartPosition=FormStartPosition.Manual;
        Text="阿拉德内辅 · 游戏面板";Font=new Font("Microsoft YaHei UI",10);ForeColor=Color.Gainsboro;BackColor=Color.FromArgb(17,18,22);ClientSize=new Size(720,440);Padding=new Padding(12,2,12,10);KeyPreview=true;DoubleBuffered=true;
        var content=new Panel{Dock=DockStyle.Fill,Padding=new Padding(0,8,0,0)};
        content.Controls.Add(editor);content.Controls.Add(summary);content.Controls.Add(status);
        var footer=new Panel{Dock=DockStyle.Bottom,Height=43};
        string Key(string id)=>bindings.GetValueOrDefault(id,"未配置").Replace("Left","←",StringComparison.OrdinalIgnoreCase).Replace("Right","→",StringComparison.OrdinalIgnoreCase).Replace("Up","↑",StringComparison.OrdinalIgnoreCase).Replace("Down","↓",StringComparison.OrdinalIgnoreCase);
        var roomKeys=new Label{Name="RoomShortcutHint",Text="顺图："+string.Join(" / ",new[]{"room.left","room.up","room.right","room.down"}.Select(Key))+" · 清怪后可用，队长带队换房",Dock=DockStyle.Top,Height=21,ForeColor=Color.Wheat};
        footer.Controls.Add(roomKeys);
        footer.Controls.Add(new Label{Text=Key("panel")+" 开关 · 键盘继续控制游戏 · 品级可在面板设置",Dock=DockStyle.Top,Height=21,ForeColor=Color.DarkGray});
        var header=new Panel{Dock=DockStyle.Top,Height=30,BackColor=Color.Transparent};heading.Dock=DockStyle.Fill;heading.Font=new Font(Font,FontStyle.Bold);
        var close=new NoFocusButton{Text="×",FlatStyle=FlatStyle.Flat,Width=28,Dock=DockStyle.Right,TabStop=true,AccessibleName="收起内辅",ForeColor=Color.Wheat,BackColor=Color.FromArgb(13,38,56)};close.FlatAppearance.BorderSize=0;close.Click+=(_,_)=>Dismiss();header.Controls.Add(heading);header.Controls.Add(close);
        foreach(var pair in tabNames){var b=ActionButton(pair.Value);string id=pair.Key;b.Click+=(_,_)=>SelectPlugin(id);tabs.Controls.Add(b);}
 Controls.Add(content);Controls.Add(footer);Controls.Add(tabs);Controls.Add(header);
        heading.MouseDown+=(_,e)=>{if(e.Button==MouseButtons.Left){dragging=true;dragStart=Cursor.Position;heading.Capture=true;}};
        heading.MouseMove+=(_,_)=>{if(dragging){var now=Cursor.Position;offset.Offset(now.X-dragStart.X,now.Y-dragStart.Y);dragStart=now;Track();}};
        heading.MouseUp+=(_,_)=>{dragging=false;heading.Capture=false;};
        tracking.Tick+=(_,_)=>Track();tracking.Start();
        FormClosing+=(_,e)=>{if(e.CloseReason==CloseReason.UserClosing){e.Cancel=true;Dismiss();}};
    }
    protected override CreateParams CreateParams {get{var p=base.CreateParams;p.Parent=game;p.Style|=unchecked((int)0x80000000);p.Style&=~0x40000000;p.ExStyle|=0x08000080;return p;}}
    protected override bool ShowWithoutActivation=>true;
    protected override void WndProc(ref Message m){if(m.Msg==0x21){m.Result=3;return;}base.WndProc(ref m);}
    protected override void OnPaintBackground(PaintEventArgs e)
    {
        base.OnPaintBackground(e);
        using var fill=new LinearGradientBrush(new Rectangle(0,0,Width,32),Color.FromArgb(20,82,111),Color.FromArgb(5,19,36),90);e.Graphics.FillRectangle(fill,0,0,Width,32);
        using var border=new Pen(Color.FromArgb(151,126,69));e.Graphics.DrawRectangle(border,0,0,Width-1,Height-1);
    }
    public void Toggle(){if(requested){Dismiss();return;}requested=true;Track();}
    public void Dismiss(bool focusGame=true){requested=false;Hide();}
    void Track()
    {
        if(IsDisposed)return;
        if(currentWindow()!=game || !IsWindow(game)){requested=false;Hide();return;}
        nint foreground=GetForegroundWindow();
        if(!requested || IsIconic(game) || !IsWindowVisible(game) || (foreground!=game && !IsGameSurface(game,foreground) && (!IsHandleCreated || foreground!=Handle))){Hide();return;}
        if(!GetClientRect(game,out var r))return;
        Point origin=new();if(!ClientToScreen(game,ref origin))return;
        int w=Math.Min(720,r.Right-r.Left-16),h=Math.Min(440,r.Bottom-r.Top-16);
        if(w<540 || h<250){Hide();return;}
        if(!positioned){offset=new Point((r.Right-w)/2,Math.Max(8,(r.Bottom-h)/3));positioned=true;}
        offset=new Point(Math.Clamp(offset.X,8,r.Right-w-8),Math.Clamp(offset.Y,8,r.Bottom-h-8));
        SetBounds(origin.X+offset.X,origin.Y+offset.Y,w,h);
        if(!Visible)Show(new GameOwner(game));
        // Owned popups remain above this game without a global topmost window.
    }
    // A game can itself have an owner. The absolute root-owner need not be
    // the game window, so walk the chain and match this exact game surface.
    internal static bool IsGameSurface(nint game,nint window)
    {
        if(game==0 || window==0)return false;
        window=GetAncestor(window,2);
        for(int i=0;i<32&&window!=0;i++,window=GetWindow(window,4))if(window==game)return true;
        return false;
    }
    static Color GradeColor(string key)=>key.Split('.').Last() switch{"0"=>Color.Gainsboro,"1"=>Color.FromArgb(77,191,243),"2"=>Color.FromArgb(179,115,241),"3"=>Color.FromArgb(246,100,196),"4"=>Color.FromArgb(245,203,70),"6"=>Color.FromArgb(242,147,76),_=>Color.FromArgb(230,182,102)};
    static Button ActionButton(string text)=>new NoFocusButton(){Text=text,AutoSize=true,MinimumSize=new Size(110,30),FlatStyle=FlatStyle.Flat,ForeColor=Color.Wheat,BackColor=Color.FromArgb(13,43,61),Margin=new Padding(0,8,12,2)};
    public void SetPanel(string id,HelperPanel? panel){if(panel is null)panels.Remove(id);else panels[id]=panel;if(selected==id)UpdatePanel(panel);}
 public void SelectPlugin(string id){if(!tabNames.ContainsKey(id))return;selected=id;editorKey="";schema="";UpdatePanel(panels.GetValueOrDefault(id));}
 public void UpdatePanel(HelperPanel? panel)
    {
        if(IsDisposed)return;
        status.Text=panel is null?"等待关联角色…":string.Join("  ·  ",panel.Fields.Where(x=>x.Label is "当前角色" or "拾取状态").Select(x=>x.Value));
        summary.Text=panel?.Summary??"请从原登录器进入角色。";
        string next=panel is null?"":string.Join('|',panel.Toggles.Select(x=>x.Key+":"+x.Value).Concat(panel.Actions.Select(x=>x.Key)).Concat(panel.Rows.Select(x=>x.Key+":"+x.Label+":"+x.Detail)));
        if(panel?.EditorKey==editorKey && next==schema)return;
        editorKey=panel?.EditorKey??"";schema=next;
        foreach(Control c in editor.Controls.Cast<Control>().ToArray())c.Dispose();checks.Clear();buttons.Clear();
        if(panel is null)return;
        var rows=new FlowLayoutPanel{Dock=DockStyle.Fill,AutoScroll=true,FlowDirection=FlowDirection.TopDown,WrapContents=false};
        foreach(var (prefix,label) in (panel.Toggles.Count==0?Array.Empty<(string,string)>():new[]{("gold","金币"),("equip.","装备"),("stack.","材料／消耗品"),("sell.","出售"),("disjoint.","分解")})){
            if(!panel.Toggles.Any(x=>prefix=="gold"?x.Key==prefix:x.Key.StartsWith(prefix,StringComparison.Ordinal)))continue;
            var row=new FlowLayoutPanel{Width=650,Height=30,WrapContents=false,Margin=new Padding(0,0,0,2)};
            row.Controls.Add(new Label{Text=label,Width=96,Height=27,TextAlign=ContentAlignment.MiddleLeft,ForeColor=Color.FromArgb(91,206,161)});
            foreach(var toggle in panel.Toggles.Where(x=>prefix=="gold"?x.Key==prefix:x.Key.StartsWith(prefix,StringComparison.Ordinal))){
                string text=prefix=="gold"?"拾取金币":toggle.Label.Split('·').Last().Trim();
                var check=new RarityCheckBox{Text=text,Checked=toggle.Value,Width=prefix=="gold"?110:64,Height=27,Enabled=toggle.Editable,ForeColor=prefix=="gold"?Color.Wheat:GradeColor(toggle.Key),AccessibleName=toggle.Label,Margin=Padding.Empty};checks[toggle.Key]=check;row.Controls.Add(check);
            }
            rows.Controls.Add(row);
        }
        foreach(var field in panel.Fields.Where(x=>x.Label is not("当前角色" or "拾取状态")))rows.Controls.Add(new Label{Text=field.Label+"："+field.Value,AutoSize=true,MaximumSize=new Size(650,0),ForeColor=Color.Wheat});
        NoFocusListBox? table=null;activeTable=null;
 if(panel.Rows.Count>0){table=new NoFocusListBox{Width=650,Height=130,BackColor=BackColor,ForeColor=Color.Gainsboro,Font=Font};foreach(var row in panel.Rows)table.Items.Add(new RowChoice(row.Key,row.Label+"  "+row.Detail));rows.Controls.Add(table);activeTable=table;}
 var actions=new FlowLayoutPanel{Width=650,AutoSize=true,WrapContents=true,Margin=Padding.Empty};
        string captured=editorKey;
        foreach(var action in panel.Actions){
            var button=ActionButton(action.Label);buttons[action.Key]=button;string command=action.Key;
            button.Click+=async(_,_)=>{
                if(working || captured!=editorKey || currentWindow()!=game)return;
                working=true;foreach(var b in buttons.Values)b.Enabled=false;
                try{var values=checks.ToDictionary(x=>x.Key,x=>x.Value.Checked);if(table?.SelectedItem is RowChoice choice)values[choice.Key]=true;await execute(selected,captured,command,values);}
                catch(Exception e){summary.Text=e.Message;}
                finally{working=false;if(!IsDisposed)foreach(var b in buttons.Values)b.Enabled=true;}
            };actions.Controls.Add(button);
        }
        rows.Controls.Add(actions);editor.Controls.Add(rows);
    }
    sealed record RowChoice(string Key,string Text){public override string ToString()=>Text;}
    public void ShowError(string message)=>summary.Text=message;
    internal string FixtureRoomHint=>Controls.Find("RoomShortcutHint",true).Single().Text;
    internal bool FixtureReadOnlyFilters()=>checks.Count==15&&checks.Values.All(x=>!x.Enabled);
    internal void FixtureClick(string key)=>((NoFocusButton)buttons[key]).Trigger();
    internal void FixtureMouseClick(string key){Control target=key=="blank"?this:key=="row"?activeTable??throw new InvalidOperationException():checks.TryGetValue(key,out var check)?check:buttons[key];Point point=target.PointToScreen(key=="blank"?new Point(Width-5,Height-50):new Point(Math.Min(35,target.Width/2),Math.Min(12,target.Height/2)));SetCursorPos(point.X,point.Y);mouse_event(2,0,0,0,0);mouse_event(4,0,0,0,0);}
    internal void FixtureWheel(){if(activeTable is not {} t)return;Point p=t.PointToScreen(new Point(35,12));SetCursorPos(p.X,p.Y);mouse_event(0x800,0,0,120,0);}
    internal string FixtureRowText()=>activeTable?.Items.Cast<object>().FirstOrDefault()?.ToString()??"";
    internal void FixtureCapture(string path){using var b=new Bitmap(Width,Height);DrawToBitmap(b,ClientRectangle);b.Save(path);}
    protected override void Dispose(bool disposing){if(disposing)tracking.Dispose();base.Dispose(disposing);}
    [StructLayout(LayoutKind.Sequential)]struct Rect{public int Left,Top,Right,Bottom;}
    [DllImport("user32.dll")]static extern bool GetClientRect(nint window,out Rect rect);
    [DllImport("user32.dll")]static extern bool ClientToScreen(nint window,ref Point point);
    [DllImport("user32.dll")]static extern bool IsWindow(nint window);
    [DllImport("user32.dll")]static extern bool IsWindowVisible(nint window);
    [DllImport("user32.dll")]static extern bool IsIconic(nint window);
    [DllImport("user32.dll")]static extern nint GetForegroundWindow();
    [DllImport("user32.dll")]static extern nint GetAncestor(nint window,uint flags);
    [DllImport("user32.dll")]static extern nint GetWindow(nint window,uint command);
    internal bool FixtureOwnedByGame()=>GetWindow(Handle,4)==game;
    [DllImport("user32.dll")]static extern bool SetCursorPos(int x,int y);
    [DllImport("user32.dll")]static extern void mouse_event(uint flags,uint x,uint y,uint data,nuint extra);
}

sealed class RarityCheckBox : CheckBox
{
    public RarityCheckBox(){SetStyle(ControlStyles.UserPaint|ControlStyles.AllPaintingInWmPaint|ControlStyles.OptimizedDoubleBuffer,true);SetStyle(ControlStyles.Selectable,false);TabStop=false;}
    protected override void OnPaint(PaintEventArgs e)
    {
        e.Graphics.Clear(BackColor);
        var box=new Rectangle(3,(Height-13)/2,13,13);
        using var fill=new SolidBrush(Color.FromArgb(34,31,24));e.Graphics.FillRectangle(fill,box);
        using var border=new Pen(Focused?Color.Wheat:Color.FromArgb(112,99,69));e.Graphics.DrawRectangle(border,box);
        if(Checked){using var tick=new Pen(Color.FromArgb(248,220,137),2);e.Graphics.DrawLines(tick,new Point[]{new(box.X+3,box.Y+6),new(box.X+6,box.Y+9),new(box.X+11,box.Y+3)});}
        TextRenderer.DrawText(e.Graphics,Text,Font,new Rectangle(21,0,Width-21,Height),ForeColor,TextFormatFlags.Left|TextFormatFlags.VerticalCenter|TextFormatFlags.SingleLine|TextFormatFlags.NoPadding);
        if(Focused && ShowFocusCues)ControlPaint.DrawFocusRectangle(e.Graphics,ClientRectangle,ForeColor,BackColor);
    }
}

// Mouse controls must not activate the companion or take keyboard focus from
// the game. They retain ordinary click and scrolling behavior.
sealed class NoFocusButton : Button
{
 public NoFocusButton(){SetStyle(ControlStyles.Selectable,false);TabStop=false;}
 protected override void WndProc(ref Message m){if(m.Msg==0x21){m.Result=3;return;}base.WndProc(ref m);}
 internal void Trigger()=>OnClick(EventArgs.Empty);
}
// A painted mouse-only list. Native LISTBOX calls SetFocus on mouse-down even
// when its popup owner declines activation, which interrupts game input.
sealed class NoFocusListBox : Control
{
 public List<object> Items{get;}=[];
 int selected=-1,top,dragY,dragTop;bool dragging;
 public object? SelectedItem=>selected>=0&&selected<Items.Count?Items[selected]:null;
 int RowHeight=>Math.Max(24,Font.Height+8);
 int VisibleRows=>Math.Max(1,(Height-2)/RowHeight);
 int MaximumTop=>Math.Max(0,Items.Count-VisibleRows);
 int ThumbHeight=>Math.Min(Height-2,Math.Max(18,(Height-2)*VisibleRows/Math.Max(1,Items.Count)));
 int ThumbY=>1+(Height-2-ThumbHeight)*top/Math.Max(1,MaximumTop);
 public NoFocusListBox(){SetStyle(ControlStyles.UserPaint|ControlStyles.AllPaintingInWmPaint|ControlStyles.OptimizedDoubleBuffer|ControlStyles.ResizeRedraw,true);SetStyle(ControlStyles.Selectable,false);TabStop=false;}
 protected override void WndProc(ref Message m){if(m.Msg==0x21){m.Result=3;return;}base.WndProc(ref m);}
 protected override void OnPaint(PaintEventArgs e){
  e.Graphics.Clear(BackColor);
  for(int n=0;n<VisibleRows&&top+n<Items.Count;n++){
   var rect=new Rectangle(2,1+n*RowHeight,Width-22,RowHeight);
   if(top+n==selected){using var brush=new SolidBrush(Color.FromArgb(24,73,94));e.Graphics.FillRectangle(brush,rect);}
   TextRenderer.DrawText(e.Graphics,Items[top+n].ToString(),Font,rect,ForeColor,TextFormatFlags.Left|TextFormatFlags.VerticalCenter|TextFormatFlags.SingleLine|TextFormatFlags.EndEllipsis);
  }
  using var border=new Pen(Color.FromArgb(112,99,69));e.Graphics.DrawRectangle(border,0,0,Width-1,Height-1);
  if(MaximumTop>0){using var rail=new SolidBrush(Color.FromArgb(34,31,24));e.Graphics.FillRectangle(rail,Width-18,1,17,Height-2);using var thumb=new SolidBrush(Color.FromArgb(151,126,69));e.Graphics.FillRectangle(thumb,Width-16,ThumbY,13,ThumbHeight);}
 }
 protected override void OnMouseDown(MouseEventArgs e){
  if(e.Button!=MouseButtons.Left)return;
  if(e.X>=Width-18&&MaximumTop>0){if(e.Y>=ThumbY&&e.Y<ThumbY+ThumbHeight){dragging=true;dragY=e.Y;dragTop=top;Capture=true;}else{top=Math.Clamp(top+(e.Y<ThumbY?-VisibleRows:VisibleRows),0,MaximumTop);Invalidate();}return;}
  int index=top+(e.Y-1)/RowHeight;if(index>=0&&index<Items.Count){selected=index;Invalidate();}
 }
 protected override void OnMouseMove(MouseEventArgs e){if(dragging){top=Math.Clamp(dragTop+(e.Y-dragY)*MaximumTop/Math.Max(1,Height-2-ThumbHeight),0,MaximumTop);Invalidate();}}
 protected override void OnMouseUp(MouseEventArgs e){if(dragging){dragging=false;Capture=false;}}
 protected override void OnMouseCaptureChanged(EventArgs e){if(!Capture)dragging=false;base.OnMouseCaptureChanged(e);}
 protected override void OnMouseWheel(MouseEventArgs e){top=Math.Clamp(top-e.Delta/120*3,0,MaximumTop);Invalidate();}
}
