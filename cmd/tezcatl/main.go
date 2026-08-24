package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bornholm/tezcatl/internal/command"
	"github.com/pkg/errors"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	app := command.NewApp()

	if err := app.RunContext(ctx, os.Args); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("fatal error", slog.Any("error", err))
		os.Exit(1)
	}
}
