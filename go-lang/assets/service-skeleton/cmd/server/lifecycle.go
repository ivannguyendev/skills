package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"example.com/skeleton/internal/admin"
	"example.com/skeleton/internal/grpcserver"
	"example.com/skeleton/internal/httpserver"
)

type servers struct {
	admin *admin.Server
	http  *httpserver.Server
	grpc  *grpcserver.Server
	drain chan struct{} // closed before the servers stop; given to streaming handlers
}

// serve binds every listener before reporting ready, runs until ctx is
// cancelled or any server fails, then shuts down in this order:
//
//  1. readiness off and gRPC health NOT_SERVING: traffic starts moving away;
//  2. DrainDelay: endpoint removal propagates (skipped after a failure);
//  3. drain closes, so HTTP streams end, then HTTP and gRPC stop in parallel,
//     each within its own shutdown timeout;
//  4. admin stops last, so probes keep answering throughout the drain.
func serve(ctx context.Context, logger *slog.Logger, cfg config, s servers) error {
	lis, err := listenAll(ctx, s.admin.Addr(), s.http.Addr(), cfg.GRPC.Addr)
	if err != nil {
		return err
	}
	adminLis, httpLis, grpcLis := lis[0], lis[1], lis[2]

	// Both contexts outlive the signal: shutdown is sequenced below, not by
	// whichever goroutine notices ctx first.
	serveCtx, stopServe := context.WithCancel(context.WithoutCancel(ctx))
	adminCtx, stopAdmin := context.WithCancel(context.WithoutCancel(ctx))
	defer stopServe()
	defer stopAdmin()

	failed := make(chan error, 3) // one slot per server: sends never block
	var serving, adminRunning sync.WaitGroup
	start := func(wg *sync.WaitGroup, name string, run func() error) {
		wg.Go(func() {
			if err := run(); err != nil {
				failed <- fmt.Errorf("%s: %w", name, err)
			}
		})
	}
	start(&adminRunning, "admin", func() error { return s.admin.ServeListener(adminCtx, adminLis) })
	start(&serving, "http", func() error { return s.http.ServeListener(serveCtx, httpLis) })
	start(&serving, "grpc", func() error { return s.grpc.ServeListener(serveCtx, grpcLis) })

	s.admin.SetReady(true)
	logger.InfoContext(ctx, "ready", "http", httpLis.Addr().String(),
		"grpc", grpcLis.Addr().String(), "admin", adminLis.Addr().String())

	var cause error // returned (and logged once) by the caller
	select {
	case <-ctx.Done():
	case cause = <-failed:
	}
	logger.InfoContext(ctx, "shutting down", "server_failed", cause != nil)

	s.admin.SetReady(false)
	s.grpc.Health().Shutdown()
	if cause == nil && cfg.DrainDelay > 0 {
		time.Sleep(cfg.DrainDelay)
	}
	close(s.drain)
	stopServe()
	serving.Wait()
	stopAdmin()
	adminRunning.Wait()

	close(failed)
	errs := []error{cause}
	for err := range failed {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// listenAll binds every address or none, so a port conflict fails startup
// cleanly instead of leaving half the servers running.
func listenAll(ctx context.Context, addrs ...string) ([]net.Listener, error) {
	var lc net.ListenConfig
	lis := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		l, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			for _, open := range lis {
				_ = open.Close()
			}
			return nil, fmt.Errorf("listen %s: %w", addr, err)
		}
		lis = append(lis, l)
	}
	return lis, nil
}
