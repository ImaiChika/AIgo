package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPShutdownDrainsActiveRequestsBeforeReturning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "drained")
	})}
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveHTTPGracefully(ctx, server, func() error { return server.Serve(listener) }, func() { close(stopped) }, time.Second)
	}()
	response := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			defer resp.Body.Close()
			var b []byte
			b, err = io.ReadAll(resp.Body)
			if string(b) != "drained" {
				err = errors.New("incomplete active response")
			}
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler never started")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("workers were not canceled")
	}
	// ListenAndServe returns as soon as Shutdown closes the listener. The
	// application must stay alive until the already accepted handler drains.
	select {
	case err := <-done:
		close(release)
		t.Fatalf("returned before active request drained: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}

func TestHTTPShutdownBindFailureDoesNotWaitForSignal(t *testing.T) {
	server := &http.Server{}
	want := errors.New("bind failure")
	done := make(chan error, 1)
	go func() {
		done <- serveHTTPGracefully(t.Context(), server, func() error { return want }, nil, time.Second)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("bind failure leaked signal waiter")
	}
}

func TestHTTPShutdownDeadlineClosesUnresponsiveConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() })}
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveHTTPGracefully(ctx, server, func() error { return server.Serve(listener) }, nil, 20*time.Millisecond)
	}()
	request := make(chan struct{})
	go func() {
		defer close(request)
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown unbounded")
	}
	select {
	case <-request:
	case <-time.After(time.Second):
		t.Fatal("active connection not closed")
	}
}
