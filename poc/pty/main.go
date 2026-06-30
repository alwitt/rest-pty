// Package main - application entry point
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/session"
	"github.com/apex/log"
	"github.com/creack/pty"
	"github.com/oklog/ulid/v2"
	"golang.org/x/term"
	"gorm.io/datatypes"
)

func PTYPoC() error {
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
				log.Infof("error resizing pty: %s", err)
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
	log.Warn("\n==================== [Direct PTY] Starting shell session =======================\n")
	go func() { _, _ = io.Copy(io.MultiWriter(ptmx, capture), os.Stdin) }()
	_, _ = io.Copy(os.Stdout, ptmx)
	log.Warn("\n==================== [Direct PTY] Ended shell session =======================\n")

	return nil
}

// getRedisParams read REDIS connection parameters from ENV
func getRedisParams() goutilsRedis.ConnectionConfig {
	serverHostStr := os.Getenv("UNITTEST_REDIS_HOST")
	if serverHostStr == "" {
		serverHostStr = "127.0.0.1"
	}
	serverPortStr := os.Getenv("UNITTEST_REDIS_PORT")
	if serverPortStr == "" {
		serverPortStr = "6479"
	}
	serverDBStr := os.Getenv("UNITTEST_REDIS_DB")
	if serverDBStr == "" {
		serverDBStr = "1"
	}

	serverPort, err := strconv.Atoi(serverPortStr)
	if err != nil {
		log.WithError(err).Fatal("UNITTEST_REDIS_PORT is not int")
	}
	if serverPort < 1 || serverPort > 65535 {
		log.WithField("port", serverPort).Fatal("UNITTEST_REDIS_PORT out of range [1, 65535]")
	}

	serverDB, err := strconv.Atoi(serverDBStr)
	if err != nil {
		log.WithError(err).Fatal("UNITTEST_REDIS_DB is not int")
	}
	// Redis ships with 16 logical databases by default (indices 0-15).
	if serverDB < 0 || serverDB >= 16 {
		log.WithField("db", serverDB).Fatal("UNITTEST_REDIS_DB out of range [0, 15]")
	}

	return goutilsRedis.ConnectionConfig{
		Host: serverHostStr, Port: uint16(serverPort), DBNumber: uint32(serverDB),
	}
}

func preparePTYSession(session *models.Session) {
	session.DriverType = models.SessionDriverTypePTY
	// Set PTY metadata
	driverMeta := models.SessionDriverPTYParams{DisplayRows: 100, DisplayCols: 300}
	driverMetadataStr, _ := json.Marshal(&driverMeta)
	session.DriverMetadata = datatypes.JSON(driverMetadataStr)
}

func prepareDockerSession(session *models.Session) {
	session.DriverType = models.SessionDriverTypeDocker
	// Set docker metadata
	driverMeta := models.SessionDriverDockerParams{
		Image:       "rest-pty-helper:latest",
		DisplayRows: 100,
		DisplayCols: 300,
		NetworkMode: "bridge",
		PublishPorts: []models.ContainerPortPublish{
			{
				ContainerPort: 5555,
				Protocol:      "tcp",
				HostPort:      5555,
				HostIP:        "127.0.0.1",
			},
		},
	}
	driverMetadataStr, _ := json.Marshal(&driverMeta)
	session.DriverMetadata = datatypes.JSON(driverMetadataStr)
}

func SessionDriverPoC(sessionType models.SessionDriverTypeENUMType) error {
	log.SetLevel(log.InfoLevel)

	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()

	redisParams := getRedisParams()
	redisClient, err := goutilsRedis.NewClient(ctx, redisParams)
	if err != nil {
		return goutils.NewRuntimeError("failed to prepare REDIS client", err, true)
	}

	bufferCapacity := int64(32 * 1024 * 1024)
	shellSession := models.Session{
		ID:   ulid.Make().String(),
		Name: "poc-session-driver",
		Command: models.SessionCommand{
			Command: "/usr/bin/nc", Arguments: []string{"-v", "-l", "-p", "5555"},
		},
		State:                models.SessionStateReady,
		DriverMetadata:       nil,
		OutputBufferCapacity: bufferCapacity,
	}

	// Perform driver specific setup
	switch sessionType {
	case models.SessionDriverTypePTY:
		preparePTYSession(&shellSession)
	case models.SessionDriverTypeDocker:
		prepareDockerSession(&shellSession)
	default:
		return goutils.NewRuntimeError("unsupported session driver "+string(sessionType), nil, true)
	}

	// Define the driver
	uut, err := session.NewDriver(ctx, shellSession, redisClient, func() {
		log.Info("Core command ended on its own; shutting down")
		ctxCancel()
	})
	if err != nil {
		return goutils.NewRuntimeError("failed to define session driver", err, true)
	}

	if err := uut.Start(ctx); err != nil {
		return goutils.NewRuntimeError("failed to start session driver", err, true)
	}
	defer func() {
		lclCtx, lclCtxCancel := context.WithTimeout(context.Background(), time.Second*10)
		defer lclCtxCancel()
		if err := uut.Stop(lclCtx); err != nil {
			log.WithError(err).Fatal("Session driver stop failed")
		}
	}()

	// Prepare handle to REDIS buffer for INPUT and OUTPUT
	inputBuf, err := redisClient.GetRingBuffer(
		ctx, session.BuildSessionInputBufferName(shellSession.ID), bufferCapacity,
	)
	if err != nil {
		return goutils.NewRuntimeError("failed to open INPUT buffer", err, true)
	}
	outputBuf, err := redisClient.GetRingBuffer(
		ctx, session.BuildSessionOutputBufferName(shellSession.ID), bufferCapacity,
	)
	if err != nil {
		return goutils.NewRuntimeError("failed to open OUTPUT buffer", err, true)
	}

	// Set stdin in raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }() // Best effort.

	log.Warnf(
		"\n==================== [%s Driver] Starting shell session =======================\n",
		sessionType,
	)
	go func() {
		input := inputBuf.AsReadWriteCloser(ctx, 0, time.Millisecond*5)
		_, err := io.Copy(input, os.Stdin)
		if err != nil && !errors.Is(err, syscall.EIO) && !errors.Is(err, os.ErrClosed) {
			log.WithError(err).Fatal("INPUT piping failed")
		}
	}()
	{
		output := outputBuf.AsReadWriteCloser(ctx, 0, time.Millisecond*5)
		_, err := io.Copy(os.Stdout, output)
		if err != nil {
			return goutils.NewRuntimeError("OUTPUT piping failed", err, true)
		}
	}
	log.Warnf(
		"\n==================== [%s Driver] Ended shell session =======================\n",
		sessionType,
	)

	return nil
}

func main() {
	// if err := PTYPoC(); err != nil {
	// 	log.WithError(err).Fatal("PTY Direct Failed")
	// }
	if err := SessionDriverPoC(models.SessionDriverTypeDocker); err != nil {
		log.WithError(err).Fatal("PTY Driver Failed")
	}
}
