package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"longheng.io/server/internal/app/gmadmin"
)

func main() {
	path := flag.String("config", "", "protected GM JSON config")
	initSchema := flag.Bool("init-schema", false, "initialize additive GM benefit tables")
	check := flag.Bool("check", false, "validate configuration without connecting or starting services")
	flag.Parse()
	cfg, err := gmadmin.ReadConfig(*path)
	if err != nil {
		slog.Error("GM configuration rejected", "error", err)
		os.Exit(1)
	}
	if *check {
		slog.Info("GM configuration valid")
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = gmadmin.Run(ctx, cfg, *initSchema, slog.Info); err != nil {
		slog.Error("GM stopped", "error", err)
		os.Exit(1)
	}
}
