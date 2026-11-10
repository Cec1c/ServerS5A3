"""Run unchanged NOTI12/370 receivers; stop before chat layout/rendering."""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from test_party_chat_v179 import PartyChatNative

EXE_SHA='9276348410bbf955f41a4d9e407f5288db883ff6edd68e66f30c83f56dfe3250'

class ChatNative(PartyChatNative):
    def __init__(self,data):
        super().__init__(data)
        self.records=[]
        self.u.mem_write(self.ACTOR,b'p\0o\0u\0u\0t\0\0\0')
        self.put(self.ACTOR+0x100,self.ACTOR)
        self.hooks.update({0x296D730:lambda:self.ret(value=self.ACTOR),
            0x3C1BEA0:lambda:self.ret(value=self.ACTOR+0x100),
            0xE91820:lambda:self.ret(value=0),
            0x2A04000:self.capture,
            0x37DA4D0:lambda:self.ret(),0x21E7B70:lambda:self.ret(),
            0x29EFE20:self.item_rows})
    def capture(self):
        record=self.arg()
        self.records.append(dict(uid=self.read(record+12)&65535,
            serverGroup=self.read(record+0x1C),messageType=self.read(record+0x18),
            message=self.wide(self.read(record+4)).decode('utf-16le')))
        self.ret(4)
    def item_rows(self):
        start,remaining=self.read(0x57F9AD0),self.read(0x57F9AD4)
        count=self.u.mem_read(start,1)[0]
        size=1+148*count
        assert size<=remaining
        self.put(0x57F9AD0,start+size,remaining-size)
        self.ret()
    def receive(self,body,linked):
        self.u.mem_write(self.PACKET,body)
        self.put(0x57F9AD0,self.PACKET,len(body),0,0)
        self.run(0x2A042F0 if linked else 0x2035370)
        assert self.read(0x57F9AD4)==0,'receiver must consume exact body'
        return self.records

def main():
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--exe',type=Path,required=True)
    ap.add_argument('--packets',type=Path)
    ap.add_argument('--output',type=Path,required=True)
    args=ap.parse_args();data=args.exe.read_bytes()
    assert hashlib.sha256(data).hexdigest()==EXE_SHA
    rows=[]
    for linked in (False,True):
        for group in (0,7,249):
            if args.packets:
                file=args.packets/f'chat-linked-{str(linked).lower()}-group-{group}.bin'
                body=file.read_bytes();origin='Go production handler export'
            else:
                body=struct.pack('<BHB',3,1,group)+struct.pack('<I',5)+b'hello'
                if linked:body+=b'\x01'+bytes([1])*148
                body+=bytes(4);origin='synthetic wire fixture'
            records=ChatNative(data).receive(body,linked)
            assert records==[dict(uid=1,serverGroup=group,messageType=3,message='hello')],records
            # Negative control retains the original bug: channel42 is stored
            # as server group42 instead of the sender's actual group.
            wrong=body[:3]+b'\x2a'+body[4:]
            old=ChatNative(data).receive(wrong,linked)
            assert old[0]['serverGroup']==42 and old[0]['serverGroup']!=group
            rows.append(dict(linked=linked,group=group,origin=origin,packetSHA256=hashlib.sha256(body).hexdigest(),
                nativeRecord=records[0],oldChannelProjection=old[0],exactConsumption=True))
    result=dict(passed=True,exeSHA256=EXE_SHA,cases=rows,caseCount=len(rows),
        nativeExecuted=['2035370 NOTI12','2A042F0 NOTI370','21E08B0 record initializer'],
        fixtures=['scene actor/name lookup','DSTR conversion','item-row storage','final chat layout'],
        gameAcceptance=False,rendered=False,exeModified=False)
    args.output.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n','utf8')
    print(json.dumps(dict(passed=True,caseCount=len(rows),goExports=bool(args.packets))))

if __name__=='__main__':main()
