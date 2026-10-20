// Command jousthunt searches a directory tree for byte patterns in every
// encoding a DNF client asset can use (UTF-8, EUC-KR, GB18030, UTF-16LE). It
// exists as a forensic scratch tool for locating the origin of client UI text
// that is neither PVF string-table text nor ASCII (for example the Korean
// strings of a mis-triggered Chinese event window).
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func main() {
	root := flag.String("root", "", "directory to scan")
	workers := flag.Int("workers", 8, "parallel file readers")
	maxSize := flag.Int64("max-size", 400<<20, "skip files larger than this many bytes")
	exts := flag.String("ext", "", "comma separated extension filter, empty means all")
	encs := flag.String("enc", "utf8,euckr,utf16le,gb18030", "encodings to search")
	hexNeedles := flag.String("hex", "", "comma separated hex byte patterns")
	context := flag.Int("context", 0, "also print this many bytes of hex context around a hit")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "need -root")
		os.Exit(2)
	}
	needles := make([]string, 0, flag.NArg())
	for _, needle := range flag.Args() {
		if needle = strings.TrimSpace(needle); needle != "" {
			needles = append(needles, needle)
		}
	}
	if len(needles) == 0 && strings.TrimSpace(*hexNeedles) == "" {
		fmt.Fprintln(os.Stderr, "need at least one needle")
		os.Exit(2)
	}

	type pattern struct {
		needle string
		enc    string
		bytes  []byte
	}
	patterns := make([]pattern, 0, len(needles)*4)
	enabled := map[string]bool{}
	for _, name := range strings.Split(*encs, ",") {
		enabled[strings.ToLower(strings.TrimSpace(name))] = true
	}
	euckr := korean.EUCKR.NewEncoder()
	gbk := simplifiedchinese.GB18030.NewEncoder()
	for _, needle := range needles {
		if enabled["utf8"] {
			patterns = append(patterns, pattern{needle: needle, enc: "utf8", bytes: []byte(needle)})
		}
		if enabled["euckr"] {
			if encoded, err := euckr.Bytes([]byte(needle)); err == nil && len(encoded) > 0 {
				patterns = append(patterns, pattern{needle: needle, enc: "euckr", bytes: encoded})
			}
		}
		if enabled["gb18030"] {
			if encoded, err := gbk.Bytes([]byte(needle)); err == nil && len(encoded) > 0 {
				patterns = append(patterns, pattern{needle: needle, enc: "gb18030", bytes: encoded})
			}
		}
		if enabled["utf16le"] {
			le := make([]byte, 0, len(needle)*2)
			for _, r := range needle {
				if r > 0xffff {
					continue
				}
				le = append(le, byte(r), byte(r>>8))
			}
			if len(le) > 0 {
				patterns = append(patterns, pattern{needle: needle, enc: "utf16le", bytes: le})
			}
		}
	}
	for _, item := range strings.Split(*hexNeedles, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		decoded, err := hex.DecodeString(strings.ReplaceAll(item, " ", ""))
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad hex pattern %q: %v\n", item, err)
			os.Exit(2)
		}
		patterns = append(patterns, pattern{needle: "hex:" + item, enc: "hex", bytes: decoded})
	}
	maxPattern := 0
	for _, p := range patterns {
		if len(p.bytes) > maxPattern {
			maxPattern = len(p.bytes)
		}
	}
	overlap := maxPattern - 1
	if overlap < 0 {
		overlap = 0
	}
	extFilter := map[string]bool{}
	if strings.TrimSpace(*exts) != "" {
		for _, e := range strings.Split(*exts, ",") {
			e = strings.ToLower(strings.TrimSpace(e))
			if e != "" {
				if !strings.HasPrefix(e, ".") {
					e = "." + e
				}
				extFilter[e] = true
			}
		}
	}

	files := make(chan string, 256)
	var scanned, hits int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	const chunk = 4 << 20
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buffer := make([]byte, 0, chunk+overlap)
			for path := range files {
				file, err := os.Open(path) //nolint:gosec // G304: forensic scratch tool
				if err != nil {
					continue
				}
				info, statErr := file.Stat()
				if statErr != nil || info.IsDir() || info.Size() > *maxSize {
					_ = file.Close()
					continue
				}
				atomic.AddInt64(&scanned, 1)
				offset := int64(-1)
				buffer = buffer[:0]
				for {
					read := make([]byte, chunk)
					n, readErr := io.ReadFull(file, read)
					if n > 0 {
						buffer = append(buffer, read[:n]...)
						for _, p := range patterns {
							searchFrom := 0
							for {
								index := bytes.Index(buffer[searchFrom:], p.bytes)
								if index < 0 {
									break
								}
								index += searchFrom
								absolute := offset + 1 + int64(index)
								mu.Lock()
								fmt.Printf("MATCH needle=%s enc=%s path=%s off=%d\n", p.needle, p.enc, path, absolute)
								if *context > 0 {
									start := index - *context
									if start < 0 {
										start = 0
									}
									end := index + len(p.bytes) + *context
									if end > len(buffer) {
										end = len(buffer)
									}
									fmt.Printf("  CTX %s\n", hex.EncodeToString(buffer[start:end]))
								}
								mu.Unlock()
								atomic.AddInt64(&hits, 1)
								searchFrom = index + 1
							}
						}
						if overlap > 0 && len(buffer) > overlap {
							tail := append([]byte(nil), buffer[len(buffer)-overlap:]...)
							buffer = buffer[:0]
							buffer = append(buffer, tail...)
						} else {
							buffer = buffer[:0]
						}
						offset += int64(n)
					}
					if readErr != nil {
						break
					}
				}
				_ = file.Close()
			}
		}()
	}
	walkErr := filepath.WalkDir(*root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if len(extFilter) > 0 && !extFilter[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		files <- path
		return nil
	})
	close(files)
	wg.Wait()
	if walkErr != nil {
		fmt.Fprintf(os.Stderr, "walk error: %v\n", walkErr)
	}
	fmt.Fprintf(os.Stderr, "scanned=%d hits=%d root=%s\n", atomic.LoadInt64(&scanned), atomic.LoadInt64(&hits), *root)
}
