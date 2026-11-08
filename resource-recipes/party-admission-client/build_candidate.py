"""Private cumulative client recipe: fresh native party connection admission."""
from pathlib import Path
import runpy

if __name__ == '__main__':
    runpy.run_path(str(Path(__file__).resolve().parents[2] / 'scripts/client/party-connection-admission/build_candidate.py'), run_name='__main__')
