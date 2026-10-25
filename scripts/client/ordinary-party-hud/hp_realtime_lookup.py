"""Current-town compatibility helper: P153 uses the same current-town actor as HUD."""
from pathlib import Path
import sys
from hp_pe_support import Code
BASE=0x6ec1600
def emit():
 c=Code(BASE)
 c.raw('55 8bec 53 56 ff7508');c.call(0x296d730);c.raw('83c404 85c0');c.jne('done')
 # Only ordinary town. Dungeon combat continues using the native lookup.
 c.call(0x1fa2790);c.raw('8bc8');c.call(0x2526b50);c.raw('83f801');c.jne('miss')
 c.raw('8b0d58376a05 85c9');c.je('miss')
 c.raw('8b4170 85c0');c.je('miss')
 c.raw('8bb0c8040000 85f6');c.je('miss')
 c.raw('8b5d08 0fb74020 3bc3');c.je('miss')
 c.raw('53 8bce');c.call(0x2973110);c.raw('83f8ff');c.je('miss')
 c.raw('8b0d68376a05 85c9');c.je('miss')
 c.raw('50');c.call(0x6ec1000);c.raw('85c0');c.je('miss')
 c.raw('0fb75020 3bd3');c.jne('miss')
 c.jump('done');c.label('miss');c.raw('33c0')
 c.label('done');c.raw('5e 5b 8be5 5d c3')
 return c.finish()
