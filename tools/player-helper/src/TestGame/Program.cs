namespace Arad.Helper.TestGame;
static class Program
{
    [STAThread] static void Main()
    {
        ApplicationConfiguration.Initialize();
        using var form = new Form { Text = "DNF helper isolated lifecycle fixture", Width = 520, Height = 200 };
        using var timer = new System.Windows.Forms.Timer { Interval = 120000 };
        timer.Tick += (_, _) => form.Close(); timer.Start(); Application.Run(form);
    }
}
