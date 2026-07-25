package service

import (
	"errors"
	"testing"
)

func TestRequestApplicationRestart(t *testing.T) {
	RegisterApplicationRestart(nil)
	t.Cleanup(func() { RegisterApplicationRestart(nil) })
	if err := RequestApplicationRestart(); !errors.Is(err, ErrApplicationRestartUnavailable) {
		t.Fatalf("unavailable restart error = %v", err)
	}
	called := false
	RegisterApplicationRestart(func() error {
		called = true
		return nil
	})
	if err := RequestApplicationRestart(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("restart callback was not called")
	}
}
