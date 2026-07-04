package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"obsyncd/internal/core"
	"obsyncd/internal/update"
)

func main() {
	defaultConfig := filepath.Join(os.Getenv("HOME"), ".config", "obsyncd", "config.yaml")
	configPath := flag.String("config", defaultConfig, "path to obsyncd YAML config")
	flag.Parse()

	if err := update.MaybeRelaunch("obsyncd", os.Args[1:]); err != nil && os.Getenv("OBSYNCD_UPDATE_VERBOSE") == "1" {
		fmt.Fprintln(os.Stderr, "obsyncd update skipped:", err)
	}

	if flag.NArg() > 0 && flag.Arg(0) == "id" {
		id, err := core.DeviceID(*configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(id)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	daemon, err := core.Start(ctx, *configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := daemon.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
