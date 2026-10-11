package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
)

func main() {
	a, err := platformpvf.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	for _, path := range os.Args[2:] {
		if idx := a.FindFileIndex(path); idx >= 0 {
			b, _ := json.Marshal(a.Files()[idx])
			fmt.Println(string(b))
		}
		raw, err := a.ReadRaw(path)
		if err != nil {
			panic(err)
		}
		fmt.Printf("PATH %s bytes %d\n", path, len(raw))
		for i := 0; i+5 <= len(raw); i += 5 {
			value := int32(binary.LittleEndian.Uint32(raw[i+1 : i+5]))
			typ := raw[i]
			if len(raw) < 3000 || i < 50 || i > len(raw)-100 || value == 490704152 || value == 490701876 {
				row := map[string]any{"offset": i, "type": typ, "value": value}
				if typ == 3 || typ == 5 || typ == 6 || typ == 7 {
					row["text"] = a.ResolveString(int(value))
				}
				b, _ := json.Marshal(row)
				fmt.Println(string(b))
			}
		}
	}
}
