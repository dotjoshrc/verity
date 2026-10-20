package main

import (
	"context"
	"os"

	"github.com/charmbracelet/log"
	"github.com/urfave/cli/v3"
)

func main() {
	log.SetTimeFormat("15:04:05.000")
	log.SetPrefix("[verity]")

	app := newApp()

	err := app.Run(context.Background(), os.Args)
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
