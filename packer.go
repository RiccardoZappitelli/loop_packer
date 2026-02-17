//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

//go:embed payload.exe
var embeddedExe []byte

func main() {
	tempDir, err := os.MkdirTemp("", "packer-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	// defer os.RemoveAll(tempDir) // only if you really want cleanup

	exeName := "target.exe"
	exePath := filepath.Join(tempDir, exeName)

	if err := os.WriteFile(exePath, embeddedExe, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to extract payload: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Payload extracted → %s\n", exePath)

	var (
		mu          sync.Mutex
		isAlive     bool
		exited      chan struct{} // will be recreated each start
		currentProc *os.Process
	)

	start := func() {
		mu.Lock()
		// Do NOT close old exited channel here — the waiter already did (or will)
		isAlive = false
		if currentProc != nil {
			_ = currentProc.Release()
			currentProc = nil
		}
		// Create brand new channel for this new process instance
		exited = make(chan struct{})
		mu.Unlock()

		cmd := exec.Command(exePath)
		// cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // optional

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "Start failed: %v\n", err)
			return
		}

		mu.Lock()
		currentProc = cmd.Process
		isAlive = true
		mu.Unlock()

		fmt.Printf("Started %s (PID %d)\n", exeName, cmd.Process.Pid)

		// Watch for exit — this goroutine owns closing the channel
		go func(p *os.Process, done chan struct{}) {
			state, err := p.Wait()

			mu.Lock()
			defer mu.Unlock()

			// Only act if this is still the current (most recent) process
			if currentProc == p {
				isAlive = false

				exitCode := -1
				if state != nil {
					exitCode = state.ExitCode()
				}

				if err != nil {
					fmt.Printf("%s exited with error: %v (code %d)\n", exeName, err, exitCode)
				} else {
					fmt.Printf("%s exited normally (code %d)\n", exeName, exitCode)
				}

				// Safe close: only close if channel is not already closed
				select {
				case <-done:
					// already closed → do nothing
				default:
					close(done)
				}
			}
		}(cmd.Process, exited)
	}

	// First start
	start()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			mu.Lock()
			alive := isAlive
			mu.Unlock()

			if !alive {
				fmt.Printf("%s not running → restarting...\n", exeName)
				start()
			}

		case <-exited:
			fmt.Printf("%s exited → restarting now\n", exeName)
			start()
		}
	}
}
