package main

import (
	"os"
	"os/exec"

	"github.com/urfave/cli/v3"
)

func newTestCommand(c *cli.Command) *exec.Cmd {
	config := c.Metadata["configuration"].(*Configuration)

	cmd := exec.Command("go", config.GoTestArgs...)
	cmd.Env = append(os.Environ(), "VERITY_CLI=true")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd
}
