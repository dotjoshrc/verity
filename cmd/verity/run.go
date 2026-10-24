package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/dotjoshrc/verity/internal/core"
	"github.com/urfave/cli/v3"
)

func runAction(ctx context.Context, c *cli.Command) (err error) {
	cfg := c.Metadata["configuration"].(*Configuration)
	runID := strings.ToLower(rand.Text()[:8])
	projectName := "verity-" + runID

	dockerCLI, err := command.NewDockerCli()
	if err != nil {
		return fmt.Errorf("create docker cli: %w", err)
	}
	if err := dockerCLI.Initialize(&flags.ClientOptions{}); err != nil {
		return fmt.Errorf("initialize docker cli: %w", err)
	}
	service, err := compose.NewComposeService(dockerCLI)
	if err != nil {
		return fmt.Errorf("create compose service: %w", err)
	}

	project, err := service.LoadProject(ctx, api.ProjectLoadOptions{
		ConfigPaths: []string{cfg.ComposeFile},
		ProjectName: projectName,
	})
	if err != nil {
		return fmt.Errorf("load compose project: %w", err)
	}
	discovery, err := buildDiscovery(project, runID)
	if err != nil {
		return err
	}
	if err := requireHealthchecks(project, discovery); err != nil {
		return err
	}
	publishLoopback(project, discovery)
	addHostGateway(project)

	var reaper *reaper
	cleaned := false
	if !c.Bool("keep") {
		reaper, err = startReaper(ctx, dockerCLI, projectName)
		if err != nil {
			return fmt.Errorf("start cleanup reaper: %w", err)
		}
		defer func() { reaper.close(cleaned) }()
	}

	// Registered before Up so a partial Up is cleaned too. ctx is cancelled by
	// Ctrl-C, which would make Down fail, so it gets its own.
	defer func() {
		if c.Bool("keep") {
			if enc, encErr := discovery.Encode(); encErr == nil {
				fmt.Printf("kept compose project %s\nexport %s='%s'\n", projectName, core.DiscoveryEnv, strings.ReplaceAll(enc, "'", `'\''`))
			}
			return
		}
		downCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		downErr := service.Down(downCtx, projectName, api.DownOptions{RemoveOrphans: true, Images: "local", Volumes: true})
		cleaned = downErr == nil
		if downErr != nil {
			// Keep the run's own error: it may carry the test exit code.
			log.Errorf("stop compose services: %v", downErr)
			if err == nil {
				err = fmt.Errorf("stop compose services: %w", downErr)
			}
		}
	}()

	// No compose Wait: it fails the whole Up when a verity.wait service exits
	// fast, so awaitReady does the waiting for every service instead.
	if err := service.Up(ctx, project, api.UpOptions{
		Create: api.CreateOptions{Build: &api.BuildOptions{}},
		// Bounds compose's own depends_on waits inside Up.
		Start: api.StartOptions{WaitTimeout: c.Duration("docker-compose-wait-timeout")},
	}); err != nil {
		return fmt.Errorf("start compose services: %w", err)
	}
	if err := awaitReady(ctx, service, project, c.Duration("docker-compose-wait-timeout")); err != nil {
		return err
	}

	containers, err := service.Ps(ctx, projectName, api.PsOptions{All: true})
	if err != nil {
		return fmt.Errorf("list compose containers: %w", err)
	}
	if err := fillAddresses(discovery, containers); err != nil {
		return err
	}
	enc, err := discovery.Encode()
	if err != nil {
		return err
	}

	log.Debugf("running go %v", cfg.GoTestArgs)
	cmd := exec.CommandContext(ctx, "go", cfg.GoTestArgs...)
	cmd.Env = append(os.Environ(), "VERITY_CLI=true", core.DiscoveryEnv+"="+enc)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// go test does not pass a signal on to its test binaries, so the child gets its
	// own process group and cancel interrupts the whole group, as Ctrl-C would.
	// shortcut: Unix only (Setpgid, kill(-pgid)), add a Windows variant if needed.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
	cmd.WaitDelay = 30 * time.Second

	runErr := cmd.Run()
	if cmd.Process != nil {
		// Reap anything in the group that outlived go test (for example a test binary
		// that ignores SIGINT: WaitDelay only kills go itself). ESRCH is expected.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
		return cli.Exit(fmt.Sprintf("go test failed: %v", runErr), exitErr.ExitCode())
	} else if runErr != nil {
		return fmt.Errorf("run go test: %w", runErr)
	}
	return nil
}

// awaitReady waits until every verity.wait service has exited 0 and every other
// service is healthy (or running, when it has no healthcheck). Any other exit,
// or an unhealthy service, fails at once with that service's logs.
func awaitReady(ctx context.Context, svc api.Compose, project *types.Project, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		containers, err := svc.Ps(ctx, project.Name, api.PsOptions{All: true})
		if err != nil {
			return fmt.Errorf("list compose containers: %w", err)
		}

		var pending []string
		for _, c := range containers {
			oneShot := project.Services[c.Service].Labels["verity.wait"] != ""
			state, health := string(c.State), string(c.Health)
			switch {
			case oneShot && state == "exited" && c.ExitCode == 0:
			case state == "exited", state == "dead", health == "unhealthy":
				fmt.Fprintf(os.Stderr, "--- logs for %s ---\n", c.Service)
				_ = svc.Logs(context.WithoutCancel(ctx), project.Name, stderrConsumer{}, api.LogOptions{Project: project, Services: []string{c.Service}, Tail: "50"})
				return fmt.Errorf("service %q failed: %s (exit %d)", c.Service, cmp.Or(health, state), c.ExitCode)
			case oneShot, state != "running", health != "" && health != "healthy":
				pending = append(pending, c.Service)
			}
		}
		if len(pending) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			for _, name := range pending {
				fmt.Fprintf(os.Stderr, "--- logs for %s ---\n", name)
				_ = svc.Logs(context.WithoutCancel(ctx), project.Name, stderrConsumer{}, api.LogOptions{Project: project, Services: []string{name}, Tail: "50"})
			}
			return fmt.Errorf("waiting for %s: %w", strings.Join(pending, ", "), ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type stderrConsumer struct{}

func (stderrConsumer) Log(name, msg string)    { fmt.Fprintf(os.Stderr, "%s | %s\n", name, msg) }
func (stderrConsumer) Err(name, msg string)    { fmt.Fprintf(os.Stderr, "%s | %s\n", name, msg) }
func (stderrConsumer) Status(name, msg string) {}
