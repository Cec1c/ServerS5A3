"""Execute current-image native card completion against explicit row fixtures.

No installation or running-process access. The packet parser/rendering remain
outside this fixture; server tests verify the actual owner-only wire lists.
"""
from pathlib import Path
import argparse, hashlib, json, sys

def main():
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--input',type=Path,required=True)
    ap.add_argument('--output',type=Path,required=True)
    a=ap.parse_args()
    data=a.input.read_bytes()
    sha=hashlib.sha256(data).hexdigest()
    assert sha in {
        '7970c5ddb4253d79d99bcc958a51b2e3a7e0f3bdf9efdcc63f5322f646e52760',
        '71571e228d1cfde78fd2b4d75c7315ab1c7d7755865a1b95e7dcf1bd98a1b6f5',
    }
    sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'party-reward-row'))
    import verify_core as v
    import verify_completion as c
    v.NEW_PE=v.OLD_PE=v.PE(data)
    cases=[]
    for owner in (19,20,21,22):
      for party_only in (True,False):
       for own_bound in (True,False):
        for peer_pending in (False,True):
         m=c.CompletionMachine('member',1,False)
         # One bound owner row per category is the resulting server projection.
         m.w(v.ROOT_UI+0x3C8,c.MAP+0x208)
         m.w(v.ROOT_UI+0x3CC,c.MAP+0x210)
         m.w(c.OWN_ROW+0x3E4,owner if own_bound else 0xffffffff)
         m.w(c.OTHER_ROW+0x3E4,owner)
         if party_only:m.w(c.ROOT2+0x3CC,m.r(c.ROOT2+0x3C8))
         if peer_pending:
            # Negative control: the old shared page has an unresolved peer row.
            m.w(v.ROOT_UI+0x3C8,c.MAP+0x200)
            m.w(v.ROW+0x3E4,0xffffffff)
         observed=m.invoke(0x8ED470,c.MANAGER)
         expected=int(own_bound and not peer_pending)
         assert observed==expected,(owner,party_only,own_bound,peer_pending,observed)
         m.invoke(0x190CC50,c.MANAGER)
         assert m.transitions==[5 if expected else 3]
         cases.append(dict(owner=owner,partyOnly=party_only,ownBound=own_bound,peerPending=peer_pending,complete=bool(observed)))
    result=dict(passed=True,inputSHA256=sha,cases=cases,count=len(cases),nativeCompletion=True,realPacketParser=False,renderer=False,gameAccepted=False)
    a.output.write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(dict(passed=True,count=len(cases),inputSHA256=sha)))

if __name__=='__main__': main()
