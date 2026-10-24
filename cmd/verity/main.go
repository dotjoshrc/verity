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

	args, goArgs := splitDashDash(os.Args)
	app := newApp(goArgs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	err := app.Run(ctx, args)
	if err != nil {
		log.Fatal(err)
	}
}

func newApp(goArgs []string) *cli.Command {
	app := cli.Command{
		Commands: []*cli.Command{
			newRunCommand(goArgs),
			newDownCommand(goArgs),
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
