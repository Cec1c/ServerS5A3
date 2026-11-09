"""Build an isolated source snapshot; never installs or publishes player files."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True, help='new directory outside this source tree')
    parser.add_argument('--dotnet', default='dotnet', help='.NET SDK executable (8 or newer)')
    parser.add_argument('--zig', type=Path, help='optional Zig 0.13.0 for native x86 client components')
    args = parser.parse_args()
    root = Path(__file__).resolve().parent
    out = args.out.resolve()
    if out == root or root in out.parents:
        parser.error('--out must be outside the source tree')
    out.mkdir(parents=True, exist_ok=False)
    source = out / 'source'
    shutil.copytree(root, source, ignore=shutil.ignore_patterns('bin', 'obj', '__pycache__', '.git'))
    hashes = {str(p.relative_to(source)): hashlib.sha256(p.read_bytes()).hexdigest()
              for p in sorted(source.rglob('*')) if p.is_file()}
    env = dict(os.environ, DOTNET_CLI_TELEMETRY_OPTOUT='1', DOTNET_NOLOGO='1')
    dotnet = shutil.which(args.dotnet) or str(Path(args.dotnet).resolve())

    def run(name, command, cwd=source):
        with (out / (name + '.log')).open('wb') as log:
            subprocess.run(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)

    for name, project in [('sdk-build', 'src/Tests/Helper.Tests.csproj'),
                          ('host-build', 'src/Host/Helper.Host.csproj'),
                          ('launcher-adapter-build', 'integration/LauncherSessionPublisher.csproj')]:
        run(name, [dotnet, 'build', project, '-c', 'Release', '-p:NuGetAudit=false', '-m:1', '-nr:false'])
    plugins = [('StatusPlugin', 'Arad.Helper.Status'), ('TestPlugin', 'Arad.Helper.TestPlugin'),
               ('LootPlugin', 'Arad.Helper.Loot'), ('UtilityPlugin', 'Arad.Helper.Utility')]
    run('sdk-checks', [dotnet, '--roll-forward', 'Major', str(source / 'src/Tests/bin/Release/net8.0/Arad.Helper.Tests.dll'),
                       str(out / 'checks'), *[str(source / f'src/{p}/bin/Release/net8.0/{a}.dll') for p, a in plugins]])
    if args.zig:
        run('native-build', [sys.executable, 'build.py', '--out', str(out / 'native'), '--zig', str(args.zig.resolve())],
            source / 'native-ui')
    record = {'sourceSHA256': hashes, 'sdk': subprocess.check_output([dotnet, '--version'], text=True).strip(),
              'sdkChecks': json.loads((out / 'checks/checks.json').read_text()),
              'hostCrossBuilt': True, 'nativeCrossBuilt': bool(args.zig),
              'windowsRuntimeTested': False, 'gameExecuted': False, 'published': False}
    (out / 'build.json').write_text(json.dumps(record, indent=2) + '\n')
    print(json.dumps({k: v for k, v in record.items() if k != 'sourceSHA256'}, indent=2))


if __name__ == '__main__':
    main()
