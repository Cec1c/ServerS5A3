"""精确d112客户端仅新增C0/1001准入，保留所有原生处理器。"""
import hashlib, struct

INPUT_SHA256=INPUT_SHA='d11237feaaf8535429eed59545d69a36e67c5760370ae07185eac85db8e55853'
OUTPUT_SHA256='a59db943c5f44c82e7db0e02305b3403aac3a16d0ec025b396e97da491a42231'
INPUT_SIZE=112549888
ENTRY=0x6f8b500
FALLBACK=0x6f8b400
RAW=112547072
C0=(999,1005,1006,1011,1012,1014,1015,1016)
C1=(1034,1038,1040,1041,1042,1043,1044,1047,1048,1049,1050,1051,1052,1053)
RANGES=((216,220),(4064,4068),(RAW,RAW+304))
def sha(data):return hashlib.sha256(data).hexdigest()
def require(ok,message):
 if not ok:raise ValueError(message)
def adapter(extra):
 c0=(999,1001,*C0[1:]) if extra else C0
 code=bytearray();labels={};fix=[]
 def jump(op,label):code.extend(bytes.fromhex(op));fix.append((len(code),label));code.extend(bytes(4))
 def compare(ids):
  for value in ids:code.append(0x3d);code.extend(struct.pack('<I',value));jump('0f84','allow')
 code.extend(bytes.fromhex('803f00'));jump('0f84','c0')
 code.extend(bytes.fromhex('803f01'));jump('0f85','fallback')
 code.extend(bytes.fromhex('0fb7442404'));compare(C1);jump('e9','fallback')
 labels['c0']=len(code);code.extend(bytes.fromhex('0fb7442404'));compare(c0);jump('e9','fallback')
 labels['allow']=len(code);code.extend(bytes.fromhex('b801000000c20400'))
 labels['fallback']=len(code);code.append(0xe9);code.extend(struct.pack('<i',FALLBACK-(ENTRY+len(code)+4)))
 for at,label in fix:struct.pack_into('<i',code,at,labels[label]-at-4)
 require(len(code)==(304 if extra else 293),'适配器布局不符')
 return bytes(code)
def checksum(data):
 values=memoryview(data).cast('H');value=sum(values)-values[108]-values[109]
 while value>>16:value=(value&65535)+(value>>16)
 return value+len(data)
def build(data):
 require(len(data)==INPUT_SIZE and sha(data)==INPUT_SHA,'拒绝未知或重复补丁输入')
 require(data[RAW:RAW+293]==adapter(False) and data[RAW+293:RAW+304]==bytes(11),'原准入或新增零预留改变')
 require(struct.unpack_from('<I',data,4064)[0]==0x625 and struct.unpack_from('<I',data,4092)[0]==0x60000020,'末节布局或RX权限改变')
 require(struct.unpack_from('<I',data,216)[0]==checksum(data),'输入PE校验不符')
 out=bytearray(data);out[RAW:RAW+304]=adapter(True)
 struct.pack_into('<I',out,4064,0x630);struct.pack_into('<I',out,216,0);struct.pack_into('<I',out,216,checksum(out))
 restored=bytearray(out)
 for lo,hi in RANGES:restored[lo:hi]=data[lo:hi]
 require(restored==data,'范围外字节改变')
 require(sha(out)==OUTPUT_SHA256,'戒指通知输出与已审定版本不符')
 report=dict(status='passed',input_sha256=sha(data),output_sha256=sha(out),input_size=len(data),output_size=len(out),
  changed_ranges=[list(row) for row in RANGES],new_pairs=[[0,1001]],existing_fallback=hex(FALLBACK),helper_size=304,
  interface_modified=False,added_dll=False,all_other_bytes_preserved=True)
 return bytes(out),report
