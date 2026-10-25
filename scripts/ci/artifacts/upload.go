// This standalone helper uses only the Go standard library. It uploads the same
// dist/ ZIP as GitLab Runner, but finishes compression before starting the HTTP
// request so proxies receive a fixed Content-Length instead of a chunked stream.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

type countedBody struct {
	reader io.Reader
	read   atomic.Int64
}

func (r *countedBody) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read.Add(int64(n))
	return n, err
}

func archiveDist(root string, dst io.Writer) error {
	w := zip.NewWriter(dst)
	count := 0
	err := filepath.WalkDir(filepath.Join(root, "dist"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact is not a regular file: %s", entry.Name())
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name, header.Method = filepath.ToSlash(rel), zip.Deflate
		out, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := in.Close()
		count++
		return errors.Join(copyErr, closeErr)
	})
	closeErr := w.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if count == 0 {
		return errors.New("dist contains no build artifacts")
	}
	return nil
}

func endpoint(api, job string) (string, error) {
	u, err := url.Parse(api)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("CI_API_V4_URL must be an HTTPS API URL without credentials or query")
	}
	if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(job) {
		return "", errors.New("invalid CI_JOB_ID")
	}
	u.Path += "/jobs/" + job + "/artifacts"
	u.RawQuery = url.Values{"artifact_format": {"zip"}, "artifact_type": {"archive"}, "expire_in": {"7 days"}}.Encode()
	return u.String(), nil
}

func artifactClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Keep fixed-length artifacts on HTTP/1.1: the affected upload route accepts
	// the HTTP/2 request body but times out before returning an artifact receipt.
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	// Clone initializes DefaultTransport's HTTP/2 support and copies its ALPN
	// list. Protocols alone does not remove the inherited "h2" advertisement.
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.ForceAttemptHTTP2 = false
	return &http.Client{Transport: transport, Timeout: 150 * time.Second}
}

// Classify transport failures without logging endpoints, headers or raw errors.
func transportFailure(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, io.EOF) {
		return "eof"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected_eof"
	}
	s := strings.ToLower(err.Error())
	for _, kind := range []string{"connection reset by peer", "broken pipe", "malformed http response", "http: server gave http response to https client", "tls:", "timeout", "connection refused"} {
		if strings.Contains(s, kind) {
			return strings.ReplaceAll(kind, " ", "_")
		}
	}
	return "other"
}

func upload(ctx context.Context, client *http.Client, target, token, name string, archive *os.File, pause func(context.Context, time.Duration) error) error {
	if token == "" {
		return errors.New("CI_JOB_TOKEN is required")
	}
	stat, err := archive.Stat()
	if err != nil {
		return err
	}
	var prefix bytes.Buffer
	mw := multipart.NewWriter(&prefix)
	if _, err := mw.CreateFormFile("file", name); err != nil {
		return err
	}
	head := append([]byte(nil), prefix.Bytes()...)
	prefix.Reset()
	if err := mw.Close(); err != nil {
		return err
	}
	tail := prefix.Bytes()
	contentLength := int64(len(head)) + stat.Size() + int64(len(tail))
	// Redirects must never forward the job token to another origin.
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for attempt := 1; attempt <= 3; attempt++ {
		body := &countedBody{reader: io.MultiReader(bytes.NewReader(head), io.NewSectionReader(archive, 0, stat.Size()), bytes.NewReader(tail))}
		var wroteRequest atomic.Bool
		var wroteAfter atomic.Int64
		var alpn atomic.Value
		alpn.Store("none")
		started := time.Now()
		trace := &httptrace.ClientTrace{
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				wroteRequest.Store(info.Err == nil)
				wroteAfter.Store(time.Since(started).Milliseconds())
			},
			TLSHandshakeDone: func(state tls.ConnectionState, _ error) { alpn.Store(state.NegotiatedProtocol) },
		}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, target, body)
		if err != nil {
			return errors.New("cannot create artifact upload request")
		}
		req.ContentLength = contentLength
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("JOB-TOKEN", token)
		resp, sendErr := boundedClient.Do(req)
		code := 0
		protocol := "none"
		if resp != nil {
			code = resp.StatusCode
			protocol = resp.Proto
			// Never log response bodies or raw transport errors: they may contain secrets.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}
		fmt.Printf("Artifact transfer: attempt=%d elapsed=%.3fs request_read=%d/%d wrote_request=%t wrote_after_ms=%d alpn=%q protocol=%s HTTP=%d failure=%s\n",
			attempt, time.Since(started).Seconds(), body.read.Load(), contentLength, wroteRequest.Load(), wroteAfter.Load(), alpn.Load(), protocol, code, transportFailure(sendErr))
		if sendErr == nil && code == http.StatusCreated {
			fmt.Printf("Artifact upload completed: HTTP 201, %d archive bytes\n", stat.Size())
			return nil
		}
		retryable := sendErr != nil || code == 408 || code == 429 || code >= 500
		if !retryable || attempt == 3 {
			return fmt.Errorf("artifact upload failed after %d attempt(s), HTTP %d", attempt, code)
		}
		fmt.Printf("Artifact upload attempt %d failed (HTTP %d); retrying the same ZIP\n", attempt, code)
		if err := pause(ctx, time.Duration(attempt)*5*time.Second); err != nil {
			return errors.New("artifact upload canceled")
		}
	}
	return errors.New("artifact upload exhausted retries")
}

func run() error {
	target, err := endpoint(os.Getenv("CI_API_V4_URL"), os.Getenv("CI_JOB_ID"))
	if err != nil {
		return err
	}
	name := "dnf90-" + os.Getenv("TARGET_OS") + "-" + os.Getenv("TARGET_ARCH") + "-" + os.Getenv("CI_COMMIT_SHORT_SHA") + ".zip"
	if !regexp.MustCompile(`^dnf90-(linux|windows)-amd64-[a-f0-9]{7,40}\.zip$`).MatchString(name) {
		return errors.New("invalid artifact target or commit identity")
	}
	f, err := os.CreateTemp("", "dnf90-ci-artifacts-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	digest := sha256.New()
	if err := archiveDist(".", io.MultiWriter(f, digest)); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	fmt.Printf("Prepared %s: %d bytes, SHA256 %x\n", name, info.Size(), digest.Sum(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	pause := func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	return upload(ctx, artifactClient(), target, os.Getenv("CI_JOB_TOKEN"), name, f, pause)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
