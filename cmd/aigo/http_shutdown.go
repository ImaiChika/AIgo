package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

func serveHTTPGracefully(ctx context.Context, server *http.Server, serve func() error, stopWorkers context.CancelFunc, budget time.Duration) error {
	serveDone := make(chan struct{})
	shutdownDone := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			if stopWorkers != nil {
				stopWorkers()
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			err := server.Shutdown(shutdownCtx)
			if err != nil {
				// A handler that ignores the drain budget must not keep the
				// process or its database connections alive indefinitely.
				_ = server.Close()
			}
			shutdownDone <- err
		case <-serveDone:
			shutdownDone <- nil
		}
	}()
	err := serve()
	close(serveDone)
	shutdownErr := <-shutdownDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return shutdownErr
}
