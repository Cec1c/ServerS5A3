// Publisher-only RSA-PSS signer. Private key bytes never leave this process.
package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		panic("sign-helper <gateway-config> <helper-payload> <public-envelope>")
	}
	raw, err := os.ReadFile(os.Args[2])
	if err != nil {
		panic("helper payload unavailable")
	}
	if len(raw) > 65536 || !(bytes.HasPrefix(raw, []byte("Arad-Helper-Plugin/1\n")) || bytes.HasPrefix(raw, []byte("Arad-Helper-Host/1\n"))) {
		panic("helper product prefix required")
	}
	var config struct {
		Hub struct {
			KeyFile string `json:"signing_key_file"`
		} `json:"launcher_hub"`
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil || json.Unmarshal(data, &config) != nil || config.Hub.KeyFile == "" {
		panic("publisher configuration unavailable")
	}
	encoded, err := os.ReadFile(config.Hub.KeyFile)
	if err != nil {
		panic("publisher key unavailable")
	}
	block, _ := pem.Decode(encoded)
	if block == nil {
		panic("publisher key format invalid")
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			key, _ = parsed.(*rsa.PrivateKey)
		}
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		panic("publisher RSA key invalid")
	}
	digest := sha256.Sum256(raw)
	signature, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		panic("helper signature failed")
	}
	output, err := json.Marshal(map[string]string{"payload": base64.StdEncoding.EncodeToString(raw), "signature": base64.StdEncoding.EncodeToString(signature)})
	if err != nil {
		panic("public envelope invalid")
	}
	if os.WriteFile(os.Args[3], output, 0644) != nil {
		panic("public envelope write failed")
	}
	fmt.Println("Signed helper payload; private key remained on publisher")
}
