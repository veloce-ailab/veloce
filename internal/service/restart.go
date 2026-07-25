package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var ErrApplicationRestartUnavailable = errors.New("application restart is unavailable")

var applicationRestartRuntime struct {
	sync.Mutex
	handler func() error
}

// RegisterApplicationRestart installs the lifecycle-owned restart callback.
// The application must drain its HTTP server before launching its replacement.
func RegisterApplicationRestart(handler func() error) {
	applicationRestartRuntime.Lock()
	defer applicationRestartRuntime.Unlock()
	applicationRestartRuntime.handler = handler
}

func RequestApplicationRestart() error {
	applicationRestartRuntime.Lock()
	handler := applicationRestartRuntime.handler
	applicationRestartRuntime.Unlock()
	if handler == nil {
		return ErrApplicationRestartUnavailable
	}
	return handler()
}

// RestartCurrentProcess launches a replacement only after the old listener has
// stopped. `go run .` is restarted through Go so its transient build artifact
// is not removed when the original command exits.
func RestartCurrentProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("locate working directory: %w", err)
	}
	command := exec.Command(executable, os.Args[1:]...)
	if strings.Contains(filepath.ToSlash(executable), "/go-build/") {
		command = exec.Command("go", "run", ".")
	}
	command.Dir = workingDirectory
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start replacement process: %w", err)
	}
	return nil
}
