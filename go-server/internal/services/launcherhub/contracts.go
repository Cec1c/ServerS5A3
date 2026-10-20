// Package launcherhub owns launcher release metadata and the separate web admin
// listener. It does not implement game/account/GM transactions.
package launcherhub

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const Product = "DNF90-Flutter"
const MaxManifest = 8 << 20

type Config struct {
	AdminListen      string `json:"admin_listen"`
	AdminTokenSHA256 string `json:"admin_token_sha256"`
	DataDir          string `json:"data_dir"`
	SigningKeyFile   string `json:"signing_key_file"`
	ProfileFile      string `json:"profile_file"`
	GMUpstream       string `json:"gm_upstream,omitempty"`
	GMPassword       string `json:"gm_password,omitempty"`
}
type Profile struct {
	ApiURL                 string   `json:"ApiUrl"`
	CertificateSHA256      string   `json:"CertificateSha256"`
	UpdatePublicKey        string   `json:"UpdatePublicKey"`
	SettingsURL            string   `json:"SettingsUrl"`
	GameHost               string   `json:"GameHost"`
	BootstrapPort          int      `json:"BootstrapPort"`
	ExeSHA256              string   `json:"ExeSha256"`
	PvfSHA256              string   `json:"PvfSha256"`
	DefaultClientDirectory string   `json:"DefaultClientDirectory"`
	ChannelHistory         *History `json:"ChannelHistory,omitempty"`
}
type History struct {
	WindowsCodePage      int
	AccountSectionBase64 string
}
type Source struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Endpoint          string `json:"endpoint"`
	PublicKey         string `json:"publicKey"`
	CertificateSHA256 string `json:"certificateSha256"`
	Enabled           bool   `json:"enabled"`
}
type News struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Category string `json:"category"`
	Date     string `json:"date"`
}
type File struct {
	Path         string
	Length       int64
	Sha256       string
	Downloadable bool
	Compression  string
	BlobSha256   string
	BlobLength   int64
}
type UpstreamManifest struct {
	Format                    int
	Product, Channel, Version string
	Sequence                  int64
	Files                     []File
}
type Envelope struct{ Payload, Signature string }
type Content struct {
	Version             string `json:"version"`
	SourceID            string `json:"sourceId"`
	Channel             string `json:"channel"`
	SourceSequence      int64  `json:"sourceSequence"`
	SourcePayloadSHA256 string `json:"sourcePayloadSha256"`
	Endpoint            string `json:"endpoint"`
	CertificateSHA256   string `json:"certificateSha256"`
	Files               []File `json:"files"`
}
type Bundle struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}
type Bootstrap struct {
	Product  string   `json:"product"`
	Format   int      `json:"format"`
	Sequence int64    `json:"sequence"`
	Profile  Profile  `json:"profile"`
	News     []News   `json:"news"`
	Content  *Content `json:"content"`
	Launcher *Bundle  `json:"launcher"`
}
type Candidate struct {
	ID       string          `json:"id"`
	Source   Source          `json:"source"`
	Channel  string          `json:"channel"`
	Envelope json.RawMessage `json:"envelope"`
}

var digestPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func publicKey(raw string) (*rsa.PublicKey, error) {
	b, rest := pem.Decode([]byte(raw))
	if b == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("invalid RSA public key")
	}
	k, e := x509.ParsePKIXPublicKey(b.Bytes)
	if e != nil {
		return nil, errors.New("use a PEM PUBLIC KEY")
	}
	r, ok := k.(*rsa.PublicKey)
	if !ok || r.N.BitLen() < 2048 {
		return nil, errors.New("RSA key must be at least 2048 bits")
	}
	return r, nil
}
func httpsURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("an HTTPS URL without credentials/query/fragment is required")
	}
	return u, nil
}
func (s Source) validate() error {
	if !idPattern.MatchString(s.ID) || len(s.Name) == 0 || len(s.Name) > 80 {
		return errors.New("invalid source ID/name")
	}
	u, e := httpsURL(s.Endpoint)
	if e != nil {
		return e
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("source endpoint must be an origin")
	}
	if s.CertificateSHA256 != "" && !digestPattern.MatchString(s.CertificateSHA256) {
		return errors.New("invalid certificate SHA256")
	}
	_, e = publicKey(s.PublicKey)
	return e
}
func sign(value any, key *rsa.PrivateKey) ([]byte, error) {
	p, e := json.Marshal(value)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(p)
	s, e := rsa.SignPSS(rand.Reader, key, crypto.SHA256, h[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if e != nil {
		return nil, e
	}
	return json.Marshal(Envelope{base64.StdEncoding.EncodeToString(p), base64.StdEncoding.EncodeToString(s)})
}
func verifyUpstream(raw []byte, source Source, channel string) (UpstreamManifest, string, error) {
	var m UpstreamManifest
	fail := func(e error) (UpstreamManifest, string, error) { return m, "", e }
	if len(raw) > MaxManifest || (channel != "stable" && channel != "preview") {
		return fail(errors.New("invalid manifest size/channel"))
	}
	var env Envelope
	if e := json.Unmarshal(raw, &env); e != nil {
		return fail(e)
	}
	p, e := base64.StdEncoding.DecodeString(env.Payload)
	if e != nil {
		return fail(e)
	}
	sig, e := base64.StdEncoding.DecodeString(env.Signature)
	if e != nil {
		return fail(e)
	}
	k, e := publicKey(source.PublicKey)
	if e != nil {
		return fail(e)
	}
	h := sha256.Sum256(p)
	if e = rsa.VerifyPKCS1v15(k, crypto.SHA256, h[:], sig); e != nil {
		return fail(errors.New("upstream signature rejected"))
	}
	if e = json.Unmarshal(p, &m); e != nil {
		return fail(e)
	}
	if m.Format != 1 || m.Product != "YKCN90" || m.Channel != channel || m.Sequence < 1 || len(m.Files) < 2 || len(m.Files) > 20000 {
		return fail(errors.New("unexpected upstream product/format/channel"))
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		lower := strings.ToLower(f.Path)
		if !safePath(f.Path) || seen[lower] || f.Length < 0 || f.Length > 8<<30 || !digestPattern.MatchString(f.Sha256) {
			return fail(errors.New("invalid or duplicate manifest path/hash/length"))
		}
		seen[lower] = true
		if f.Downloadable && f.Compression != "" && f.Compression != "none" && f.Compression != "gzip" {
			return fail(errors.New("unsupported compression"))
		}
		if f.Downloadable && f.Compression == "gzip" && (f.BlobLength < 1 || f.BlobLength > 8<<30 || !digestPattern.MatchString(f.BlobSha256)) {
			return fail(errors.New("invalid compressed blob"))
		}
	}
	return m, hex.EncodeToString(h[:]), nil
}
func safePath(p string) bool {
	if p == "" || len(p) > 240 || path.Clean(p) != p || strings.ContainsAny(p, "\\:\x00<>\"|?*") || strings.HasPrefix(p, "/") || strings.ContainsFunc(p, func(r rune) bool { return r < 32 }) {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." || strings.TrimRight(s, " .") != s {
			return false
		}
		name := strings.ToUpper(strings.SplitN(s, ".", 2)[0])
		if name == "CON" || name == "PRN" || name == "AUX" || name == "NUL" || regexp.MustCompile(`^(COM|LPT)[0-9]$`).MatchString(name) {
			return false
		}
	}
	return true
}
func selectContent(m UpstreamManifest, s Source, p Profile, payloadHash string) (*Content, error) {
	content := &Content{Version: m.Version, SourceID: s.ID, Channel: m.Channel, SourceSequence: m.Sequence, SourcePayloadSHA256: payloadHash, Endpoint: strings.TrimRight(s.Endpoint, "/"), CertificateSHA256: s.CertificateSHA256, Files: []File{}}
	required := map[string]string{"client/dnf.exe": p.ExeSHA256, "client/script.pvf": p.PvfSHA256}
	found := map[string]bool{}
	for _, f := range m.Files {
		lower := strings.ToLower(f.Path)
		expected, isRequired := required[lower]
		if isRequired {
			if !strings.EqualFold(expected, f.Sha256) {
				return nil, fmt.Errorf("%s differs from the server-approved compatibility profile", f.Path)
			}
			found[lower] = true
		}
		allowed := isRequired || lower == "client/dstr.dat" || (strings.HasPrefix(lower, "client/imagepacks2/") && strings.HasSuffix(lower, ".npk"))
		if allowed && (f.Downloadable || isRequired) {
			f.Path = f.Path[len("client/"):]
			content.Files = append(content.Files, f)
		}
	}
	if len(found) != 2 {
		return nil, errors.New("missing paired EXE/PVF")
	}
	if len(content.Files) > 2000 {
		return nil, errors.New("too many content files")
	}
	return content, nil
}
