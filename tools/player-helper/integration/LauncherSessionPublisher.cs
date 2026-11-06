using System.Buffers.Binary;
using System.Diagnostics;
using System.IO.Pipes;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace Arad.Helper.Integration;

/// <summary>Compile into the existing DNF90.Core launcher; no credentials are persisted.</summary>
public static class LauncherSessionPublisher
{
    static string Capability(string domain, string token) =>
        Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(domain + "\n" + token))).ToLowerInvariant();

    public static async Task PublishAsync(string root, Process game, string loginToken, string apiUrl,
        string certificateSha256, CancellationToken cancel)
    {
        if (!OperatingSystem.IsWindows()) throw new PlatformNotSupportedException();
        if (string.IsNullOrWhiteSpace(loginToken) || certificateSha256.Length != 64 ||
            !certificateSha256.All(char.IsAsciiHexDigit) || !Uri.TryCreate(apiUrl, UriKind.Absolute, out var uri) || uri.Scheme != "https" || uri.UserInfo != "" || uri.Query != "" || uri.Fragment != "")
            throw new ArgumentException("A verified launcher session and pinned HTTPS endpoint are required.");
        string directory = Path.GetFullPath(root);
        string identity = Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(directory.ToUpperInvariant())))[..24];
        byte[] payload = JsonSerializer.SerializeToUtf8Bytes(new {
            pid = game.Id, createdFileTime = game.StartTime.ToUniversalTime().ToFileTimeUtc(),
            executablePath = game.MainModule?.FileName ?? throw new InvalidOperationException("Game image unavailable"),
            credential = Capability("Arad-Helper-Loot/1", loginToken), apiUrl, certificateSha256,
            actionsCredential = Capability("Arad-Helper-Player/2", loginToken)
        });
        if (payload.Length > 4096) throw new InvalidDataException("Helper session envelope exceeds its bound.");
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancel);
        timeout.CancelAfter(TimeSpan.FromSeconds(3));
        await using var pipe = new NamedPipeClientStream(".", "Arad.Helper.Session." + identity,
            PipeDirection.InOut, PipeOptions.Asynchronous);
        try {
            await pipe.ConnectAsync(timeout.Token);
            byte[] header = new byte[4]; BinaryPrimitives.WriteInt32LittleEndian(header, payload.Length);
            await pipe.WriteAsync(header, timeout.Token); await pipe.WriteAsync(payload, timeout.Token);
            await pipe.FlushAsync(timeout.Token);
            byte[] ack = new byte[1]; await pipe.ReadExactlyAsync(ack, timeout.Token);
            if (ack[0] != 1) throw new InvalidDataException("Helper rejected the session.");
        } finally { CryptographicOperations.ZeroMemory(payload); }
    }
}
