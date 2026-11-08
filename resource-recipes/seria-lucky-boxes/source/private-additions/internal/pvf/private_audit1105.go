package pvf
import("bytes";"fmt")
type Audit1105Change struct { Path string `json:"path"`; Before string `json:"before"`; After string `json:"after"` }
func(a *Archive) Audit1105(b *Archive)(map[string]any,error){
 if len(a.items)!=len(b.items){return nil,fmt.Errorf("file count changed")}
 if !bytes.HasPrefix(b.strA,a.strA)||!bytes.HasPrefix(b.strW,a.strW){return nil,fmt.Errorf("original string pools changed")}
 old,err:=a.layout();if err!=nil{return nil,err};next,err:=b.layout();if err!=nil{return nil,err}
 sameChunks:=map[int]bool{};for i:=range a.groups{if i>=len(b.groups){continue};ar,e:=a.rawChunk(old,i);if e!=nil{return nil,e};br,e:=b.rawChunk(next,i);if e!=nil{return nil,e};sameChunks[i]=bytes.Equal(ar,br)}
 changes:=[]Audit1105Change{};kept:=0
 for i,item:=range a.items{if a.files[i].ArchivePath!=b.files[i].ArchivePath{return nil,fmt.Errorf("path identity changed")};if item==b.items[i]&&sameChunks[int(item.chunkIndex)]{kept++;continue};p:=a.files[i].ArchivePath;ar,e:=a.FileRaw(i);if e!=nil{return nil,e};br,e:=b.FileRaw(i);if e!=nil{return nil,e};if bytes.Equal(ar,br){kept++;continue};at,e:=a.FileText(i);if e!=nil{return nil,e};bt,e:=b.FileText(i);if e!=nil{return nil,e};changes=append(changes,Audit1105Change{p,at,bt})}
 return map[string]any{"passed":true,"preserved":kept,"changes":changes,"stringPoolsPreserved":true,"unchangedChunksComparedBytewise":true,"oldSHA256":a.Snapshot().Checksum,"newSHA256":b.Snapshot().Checksum},nil
}
