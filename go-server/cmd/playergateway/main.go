// playergateway exposes only player account APIs and release files. Database,
// SSH and management credentials never enter the player distribution.
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"longheng.io/server/internal/services/launcherhub"
)

type config struct {
	Listen          string              `json:"listen"`
	Certificate     string              `json:"certificate"`
	Key             string              `json:"key"`
	Upstream        string              `json:"upstream"`
	ProxySecret     string              `json:"proxy_secret"`
	ReleaseDir      string              `json:"release_dir"`
	BootstrapListen string              `json:"bootstrap_listen"`
	BootstrapTarget string              `json:"bootstrap_target"`
	Hub             *launcherhub.Config `json:"launcher_hub,omitempty"`
}

var releaseFile = regexp.MustCompile(`^/launcher/(settings\.json|release\.json|ServerS5A3-[0-9.]+\.zip|DNF90-Portable-[0-9]+\.[0-9]+\.[0-9]+\.exe(?:\.sha256)?)$`)

func handler(c config) http.Handler {
	return handlerWithHub(c, nil)
}

func handlerWithHub(c config, hub *launcherhub.Hub) http.Handler {
	u, err := url.Parse(c.Upstream)
	if err != nil {
		panic(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		r.Header.Del("X-Forwarded-For")
		r.Header.Del("Forwarded")
		r.Header.Del("Cookie")
		r.Header.Del("Authorization")
		r.Header.Set("X-Player-IP", ip)
		r.Header.Set("X-Player-Proxy-Key", c.ProxySecret)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "service unavailable", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		switch {
		case hub != nil && strings.HasPrefix(r.URL.Path, "/launcher/v2/"):
			hub.PublicHandler().ServeHTTP(w, r)
		case r.Method == "POST" && (r.URL.Path == "/api/account/login" || r.URL.Path == "/api/account/register"):
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			proxy.ServeHTTP(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/status":
			proxy.ServeHTTP(w, r)
		case (r.Method == "GET" || r.Method == "HEAD") && releaseFile.MatchString(r.URL.Path):
			path := filepath.Join(c.ReleaseDir, filepath.Base(r.URL.Path))
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, path)
		default:
			http.NotFound(w, r)
		}
	})
}

func bootstrap(listen, target string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsPrivate() {
		log.Fatal("bootstrap must bind a private address")
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	slots := make(chan struct{}, 64)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func() {
					defer func() { <-slots }()
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(5 * time.Minute))
					remote, err := net.DialTimeout("tcp", target, 5*time.Second)
					if err != nil {
						return
					}
					defer remote.Close()
					done := make(chan struct{})
					go func() { io.Copy(remote, conn); remote.(*net.TCPConn).CloseWrite(); close(done) }()
					io.Copy(conn, remote)
					conn.Close()
					<-done
				}()
			default:
				conn.Close()
			}
		}
	}()
	return nil
}

func main() {
	path := flag.String("config", "gateway.json", "private deployment configuration")
	flag.Parse()
	raw, err := os.ReadFile(*path)
	if err != nil {
		log.Fatal(err)
	}
	var c config
	if err = json.Unmarshal(raw, &c); err != nil {
		log.Fatal(err)
	}
	if len(c.ProxySecret) < 32 {
		log.Fatal("missing proxy secret")
	}
	if err = bootstrap(c.BootstrapListen, c.BootstrapTarget); err != nil {
		log.Fatal(err)
	}
	var hub *launcherhub.Hub
	if c.Hub != nil {
		hub, err = launcherhub.New(*c.Hub)
		if err != nil {
			log.Fatal("launcher hub configuration is invalid: ", err)
		}
		admin := &http.Server{Addr: c.Hub.AdminListen, Handler: hub.AdminHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
		go func() { log.Fatal(admin.ListenAndServeTLS(c.Certificate, c.Key)) }()
	}
	s := &http.Server{Addr: c.Listen, Handler: handlerWithHub(c, hub), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	log.Fatal(s.ListenAndServeTLS(c.Certificate, c.Key))
}
