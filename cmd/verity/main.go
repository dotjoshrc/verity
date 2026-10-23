package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/log"
	"github.com/urfave/cli/v3"
)

func main() {
	log.SetTimeFormat("15:04:05.000")
	log.SetPrefix("[verity]")

	app := newApp()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	err := app.Run(ctx, os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func newApp() *cli.Command {
	app := cli.Command{
		Commands: []*cli.Command{
			newRunCommand(),
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "debug",
				Usage:   "Debug log output.",
				Sources: sourceChain("debug"),
			},
		},
	}
	app.Name = "verity"
	app.Authors = []any{
		"dotjoshrc",
	}

	return &app
}
