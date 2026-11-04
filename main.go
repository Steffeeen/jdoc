package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	address := flag.String("listen", ":8080", "HTTP listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *address); err != nil {
		slog.Error("jdoc stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, address string) error {
	l := newLoader()
	c := &catalog{}
	if err := l.discover(ctx, c); err != nil {
		return fmt.Errorf("startup: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := l.discover(ctx, c); err != nil {
					slog.Error("check for new Javadocs", "error", err)
				}
			}
		}
	}()
	server := &http.Server{
		Addr: address, Handler: c,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	serverDone := make(chan error, 1)
	go func() {
		slog.Info("starting HTTP server", "address", address)
		serverDone <- server.ListenAndServe()
	}()
	select {
	case err := <-serverDone:
		cancel()
		<-refreshDone
		return err
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			server.Close()
		}
		<-refreshDone
		serveErr := <-serverDone
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return errors.Join(err, serveErr)
		}
		return err
	}
}
