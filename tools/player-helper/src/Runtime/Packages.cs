using System.Buffers.Binary;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Diagnostics.CodeAnalysis;

namespace Arad.Helper.Runtime;

public sealed record PackageFile(string Path, int Length, string Sha256);
public sealed record PluginManifest(string Product, string GameId, string Id, string Version, int SdkMajor,
    string Assembly, string Type, string[] Capabilities, PackageFile[] Files);
public sealed record VerifiedPackage(PluginManifest Manifest, IReadOnlyDictionary<string, byte[]> Images);

public static class Packages
{
    public const string Domain = "Arad-Helper-Plugin/1\n";
    public const string GameId = "90-s5a3";
    static readonly JsonSerializerOptions Json = new() { PropertyNameCaseInsensitive = true, UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow };
    static void Require([DoesNotReturnIf(false)] bool value) { if (!value) throw new InvalidDataException("插件签名、版本或文件检查未通过。"); }
    static void NoLink(string path)
    {
        for (string? p = Path.GetFullPath(path); p is not null; p = Path.GetDirectoryName(p))
            if (File.GetAttributes(p).HasFlag(FileAttributes.ReparsePoint)) throw new InvalidDataException("插件目录不能使用链接。");
    }
    static bool Name(string s) => s.Length is > 0 and <= 100 && s.All(c => char.IsAsciiLetterOrDigit(c) || c is '.' or '-' or '_') && !s.StartsWith('.');
    public static VerifiedPackage Verify(string directory, string publicKey)
    {
        directory = Path.GetFullPath(directory); NoLink(directory);
        string envelopePath = Path.Combine(directory, "package.json"); NoLink(envelopePath);
        Require(new FileInfo(envelopePath).Length <= 131072);
        using var envelope = JsonDocument.Parse(File.ReadAllBytes(envelopePath));
        Require(envelope.RootElement.EnumerateObject().Select(x => x.Name).Order().SequenceEqual(new[] { "payload", "signature" }));
        byte[] raw = Convert.FromBase64String(envelope.RootElement.GetProperty("payload").GetString()!);
        byte[] signature = Convert.FromBase64String(envelope.RootElement.GetProperty("signature").GetString()!);
        Require(raw.Length <= 65536 && signature.Length <= 1024);
        using var rsa = RSA.Create(); rsa.ImportFromPem(publicKey);
        Require(rsa.VerifyData(raw, signature, HashAlgorithmName.SHA256, RSASignaturePadding.Pss));
        byte[] prefix = Encoding.UTF8.GetBytes(Domain);
        Require(raw.AsSpan().StartsWith(prefix));
        var m = JsonSerializer.Deserialize<PluginManifest>(raw.AsSpan(prefix.Length), Json) ?? throw new InvalidDataException();
        Require(m.Product == "Arad-Helper-Plugin" && m.GameId == GameId && m.SdkMajor == 1 &&
            m.Id is not null && Name(m.Id) && System.Version.TryParse(m.Version, out var v) && v.Major >= 0 &&
            m.Assembly is not null && Name(m.Assembly) && m.Assembly.EndsWith(".dll", StringComparison.OrdinalIgnoreCase) &&
            !string.IsNullOrWhiteSpace(m.Type) && m.Files is { Length: > 0 and <= 16 } && m.Capabilities is not null);
        Require(m.Capabilities.Distinct(StringComparer.Ordinal).Count() == m.Capabilities.Length &&
            m.Capabilities.All(c => c is "process.summary" or "ui.panel" or "loot.policy" or "player.utility" or "native.ui.warehouse"));
        var files = new Dictionary<string, byte[]>(StringComparer.OrdinalIgnoreCase); long total = 0;
        foreach (var f in m.Files)
        {
            Require(f is not null && f.Path is not null && Name(f.Path) && (f.Path.EndsWith(".dll", StringComparison.OrdinalIgnoreCase) || (m.Id=="utilities" && m.Capabilities.Contains("native.ui.warehouse") && f.Path=="Arad.Helper.NativeUI.exe")) &&
                !f.Path.Equals("Arad.Helper.Contracts.dll", StringComparison.OrdinalIgnoreCase) && f.Length is > 0 and <= 10485760 &&
                f.Sha256 is { Length: 64 } && f.Sha256.All(char.IsAsciiHexDigit) && !files.ContainsKey(f.Path));
            total += f.Length; Require(total <= 10485760);
            string path = Path.Combine(directory, f.Path); NoLink(path);
            using var stream = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
            Require(stream.Length == f.Length); byte[] bytes = new byte[f.Length]; stream.ReadExactly(bytes);
            Require(Convert.ToHexString(SHA256.HashData(bytes)).Equals(f.Sha256, StringComparison.OrdinalIgnoreCase));
            if(f.Path is "Arad.Helper.NativeUI.exe" or "Arad.Helper.NativeUI.dll"){
                Require(m.Id=="utilities" && m.Capabilities.Contains("native.ui.warehouse"));
                Require(bytes.Length>=0x100 && bytes[0]==0x4d && bytes[1]==0x5a);
                int pe=BinaryPrimitives.ReadInt32LittleEndian(bytes.AsSpan(0x3c,4));
                Require(pe>=0x40 && pe<=bytes.Length-32 && BinaryPrimitives.ReadUInt32LittleEndian(bytes.AsSpan(pe,4))==0x4550 && BinaryPrimitives.ReadUInt16LittleEndian(bytes.AsSpan(pe+4,2))==0x14c && BinaryPrimitives.ReadUInt16LittleEndian(bytes.AsSpan(pe+24,2))==0x10b);
                ushort flags=BinaryPrimitives.ReadUInt16LittleEndian(bytes.AsSpan(pe+22,2));
                Require((flags&2)!=0 && ((flags&0x2000)!=0)==f.Path.EndsWith(".dll",StringComparison.Ordinal));
            }
            files.Add(f.Path, bytes);
        }
        Require(files.ContainsKey(m.Assembly));
        if(m.Capabilities.Contains("native.ui.warehouse"))Require(m.Id=="utilities" && files.ContainsKey("Arad.Helper.NativeUI.exe") && files.ContainsKey("Arad.Helper.NativeUI.dll"));
        // The loader consumes these exact verified bytes, never reopens a DLL.
        return new(m, files);
    }
}
