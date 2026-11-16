package main

import (
	"context"
	"fmt"
	"os"

	"longheng.io/server/cmd/server/internal/runner"
	dnf90app "longheng.io/server/internal/app/dnf90"
)

func main() {
	if handled, err := activityArchiveCommand(os.Args[1:], os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.RunContext(ctx, dnf90app.ServiceName, dnf90app.DefaultConfigPath, dnf90app.ComponentsWithShutdown(cancel))
}
