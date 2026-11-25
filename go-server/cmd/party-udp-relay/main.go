package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"longheng.io/server/internal/partyudprelay"
)

func main() {
	var (
		controlListen = flag.String("control-listen", "127.0.0.1:10250", "TCP address for v2 relay control channel")
		udpIP         = flag.String("udp-ip", "127.0.0.1", "Concrete IPv4 address to bind relay UDP leg sockets")
		portMin       = flag.Int("port-min", 30000, "First UDP port in relay allocation pool (inclusive)")
		portMax       = flag.Int("port-max", 30255, "Last UDP port in relay allocation pool (inclusive)")
		secretEnv     = flag.String("secret-env", "DNFBRIDGE_PARTY_UDP_RELAY_SECRET", "Environment variable carrying the 64-hex shared secret")
		secretFile    = flag.String("secret-file", "", "Optional file path carrying the 64-hex shared secret (overrides -secret-env)")
		leaseTTL      = flag.Duration("lease-ttl", 30*time.Second, "Room lease TTL before automatic expiry")
		cooldown      = flag.Duration("cooldown", 60*time.Second, "Port quarantine cooldown for party rooms")
		pvpCooldown   = flag.Duration("pvp-cooldown", 180*time.Second, "Port quarantine cooldown for PVP rooms")
		socketBuffer  = flag.Int("socket-buffer", 0, "Optional UDP socket SO_RCVBUF/SO_SNDBUF bytes")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	secretRaw, secretSource, err := loadSecretSource(*secretFile, *secretEnv)
	if err != nil {
		logger.Error("party-udp-relay secret load failed", "source", secretSource, "error", err)
		os.Exit(1)
	}
	secret, secretState := partyudprelay.ParseSecretHex(secretRaw)
	if secretState != "configured" {
		logger.Error("party-udp-relay secret unusable", "source", secretSource, "secret_state", secretState)
		os.Exit(1)
	}

	srv, err := partyudprelay.Start(partyudprelay.Config{
		ControlListen:     *controlListen,
		UDPIp:             *udpIP,
		PortMin:           *portMin,
		PortMax:           *portMax,
		Secret:            secret,
		LeaseTTL:          *leaseTTL,
		PartyCooldown:     *cooldown,
		PVPCooldown:       *pvpCooldown,
		SocketBufferBytes: *socketBuffer,
		Logger:            logger,
	})
	if err != nil {
		logger.Error("party-udp-relay start failed", "error", err)
		os.Exit(1)
	}

	logger.Info("party-udp-relay started",
		"control_listen", srv.ControlAddr(),
		"udp_ip", *udpIP,
		"port_min", *portMin,
		"port_max", *portMax,
		"lease_ttl", leaseTTL.String(),
		"cooldown", cooldown.String(),
		"pvp_cooldown", pvpCooldown.String(),
		"secret_source", secretSource,
		"secret_state", secretState,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	stats := srv.Stats()
	if err := srv.Close(); err != nil {
		logger.Error("party-udp-relay close error", "error", err)
		os.Exit(1)
	}
	logger.Info("party-udp-relay stopped",
		"forwarded", stats.Forwarded,
		"rejected_source", stats.RejectedSource,
		"rate_limited", stats.RateLimited,
		"write_failed", stats.WriteFailed,
		"expired_rooms", stats.ExpiredRooms,
	)
}

func loadSecretSource(secretFile, secretEnv string) (string, string, error) {
	if secretFile != "" {
		raw, err := os.ReadFile(secretFile)
		if err != nil {
			return "", "file", fmt.Errorf("read secret file: %w", err)
		}
		return string(raw), "file", nil
	}
	return os.Getenv(secretEnv), "env", nil
}
