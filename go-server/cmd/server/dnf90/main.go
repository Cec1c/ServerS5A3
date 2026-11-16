package main

import (
	"context"
	"fmt"
	"os"

	"longheng.io/server/cmd/server/internal/runner"
	dnf90app "longheng.io/server/internal/app/dnf90"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if handled, err := runOptimizationSchemaCommand(ctx, os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	runner.RunContext(ctx, dnf90app.ServiceName, dnf90app.DefaultConfigPath, dnf90app.ComponentsWithShutdown(cancel))
}
