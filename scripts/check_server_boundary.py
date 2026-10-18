"""The tracked server tree and executable must not contain GM implementation."""
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
BLOCKED = ('go-server/cmd/gmtool/', 'go-server/internal/app/gmadmin/',
           'go-server/internal/services/gmtool/', 'go-server/internal/modules/dnf/testbenefits/',
           'go-server/internal/services/dnfbridge/gmtool_service.go',
           'go-server/internal/services/dnfbridge/gm_administrative_membership.go',
           'deploy/docker/Dockerfile.gm', 'deploy/templates/gm.example.json',
           'gm-tool/', 'gmtool/')
tracked = subprocess.check_output(['git','ls-files'],cwd=ROOT,text=True).splitlines()
bad = [p for p in tracked if p.startswith(BLOCKED)]
assert not bad, 'GM implementation must live in Cec1c/DNF90GM: ' + ', '.join(bad)
deps = subprocess.check_output(['go','list','-buildvcs=false','-mod=readonly','-deps',
                                './cmd/server/dnf90'],cwd=ROOT/'go-server',text=True)
assert '/services/gmtool' not in deps and '/app/gmadmin' not in deps and '/dnf/testbenefits' not in deps
print('PASS: server tree and executable are independent of GM')
