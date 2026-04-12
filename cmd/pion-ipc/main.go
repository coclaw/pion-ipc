package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/coclaw/pion-ipc/internal/ipc"
	"github.com/coclaw/pion-ipc/internal/service"
)

func main() {
	// Protect stdout: all Go code that uses fmt.Println / os.Stdout
	// will write to stderr instead. Only IPC frames go to real stdout.
	protocolOut := os.Stdout
	os.Stdout = os.Stderr

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// OS signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	reader := ipc.NewReader(os.Stdin)
	writer := ipc.NewWriter(protocolOut)

	svc := service.New(logger, reader, writer)

	// Stdin EOF detection: when the parent process closes its end,
	// we should shut down gracefully.
	go func() {
		select {
		case <-ctx.Done():
			return
		case sig := <-sigCh:
			logger.Info("received signal, shutting down", "signal", sig)
			cancel()
		}
	}()

	logger.Info("pion-ipc started")

	if err := svc.Run(ctx); err != nil && err != io.EOF {
		logger.Error("service exited with error", "error", err)
		os.Exit(1)
	}

	logger.Info("pion-ipc exited")
}
