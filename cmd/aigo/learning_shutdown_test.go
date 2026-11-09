package main

import (
	"testing"
	"time"
)

func TestStopLearningWaitsForCanceledResultWriteBeforeReturning(t *testing.T) {
	canceled, done, persisted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		<-canceled
		close(persisted)
		close(done)
	}()
	if !stopLearningAndWait(func() { close(canceled) }, done, time.Second) {
		t.Fatal("cooperative worker did not finish")
	}
	select {
	case <-persisted:
	default:
		t.Fatal("shutdown returned before result persistence")
	}
}

func TestStopLearningWaitIsBoundedForUncooperativeClient(t *testing.T) {
	canceled := false
	if stopLearningAndWait(func() { canceled = true }, make(chan struct{}), 5*time.Millisecond) || !canceled {
		t.Fatal("shutdown did not cancel or ignored its bound")
	}
}
