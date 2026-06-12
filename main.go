// Package main - application entry point
package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func test() error {
	// Create arbitrary command.
	c := exec.Command("bash")

	// Start the command with a pty.
	ptmx, err := pty.Start(c)
	if err != nil {
		return err
	}
	// Make sure to close the pty at the end.
	defer func() { _ = ptmx.Close() }() // Best effort.

	// Handle pty size.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
				log.Printf("error resizing pty: %s", err)
			}
		}
	}()
	ch <- syscall.SIGWINCH                        // Initial resize.
	defer func() { signal.Stop(ch); close(ch) }() // Cleanup signals when done.

	// Set stdin in raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }() // Best effort.

	// Capture every raw byte read from stdin into a file for later inspection.
	capture, err := os.Create("stdin-capture.bin")
	if err != nil {
		return err
	}
	defer func() { _ = capture.Close() }() // Best effort.

	// Copy stdin to the pty and the pty to stdout.
	// Tee stdin into the capture file so we record the raw bytes (Ctrl+#, backspace,
	// del, CR, escape sequences) without the kernel line discipline interpreting them.
	// NOTE: The goroutine will keep reading until the next keystroke before returning.
	fmt.Printf("========================= Starting shell session =================================\n")
	go func() { _, _ = io.Copy(io.MultiWriter(ptmx, capture), os.Stdin) }()
	_, _ = io.Copy(os.Stdout, ptmx)
	fmt.Printf("========================= Ended shell session =================================\n")

	return nil
}

func main() {
	if err := test(); err != nil {
		log.Fatal(err)
	}
}
