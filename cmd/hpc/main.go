// Command hpc builds and runs a companion (docs/concepts/modular-architecture.md
// §5.6, §5.12): "hpc build" compiles the modules of build.yaml into one
// program; the operations on a companion's data (backup, restore, migrate,
// secrets, config check) are the companion's own commands, which hpc runs.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

const usage = `usage: hpc <command> [flags]

commands:
  build [-f build.yaml] [-o companion] [--shell dir | --no-shell] [--work dir] [--version v]
                  compile the modules of build.yaml into one program
  apply [-c data/companion.yaml] [-f docker-compose.yml] [-o compose.generated.yml] [--yes] [--dry-run] [--no-up]
                  start the services of companion.yaml with docker compose,
                  after showing and confirming their rights
  gen [-openapi dir] [-ts dir] [-check] [packages]
                  adapters, OpenAPI and TypeScript types from the capabilities
                  (default ./capability/...)
  backup, restore, migrate, secrets, config, modules, version
                  run the companion's command of that name
                  (the program: $HPC_COMPANION, ./companion or companion on PATH)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "build":
		err = buildCmd(args)
	case "gen":
		err = genCmd(args)
	case "apply":
		err = applyCmd(args)
	case "backup", "restore", "migrate", "secrets", "config", "modules", "version":
		err = forward(cmd, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "hpc: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "hpc:", err)
		os.Exit(1)
	}
}

// forward runs the companion's own command.
func forward(cmd string, args []string) error {
	bin, err := companionBinary()
	if err != nil {
		return err
	}
	c := exec.Command(bin, append([]string{cmd}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func companionBinary() (string, error) {
	if b := os.Getenv("HPC_COMPANION"); b != "" {
		return b, nil
	}
	if fi, err := os.Stat("companion"); err == nil && !fi.IsDir() {
		return "./companion", nil
	}
	if b, err := exec.LookPath("companion"); err == nil {
		return b, nil
	}
	return "", errors.New("no companion program: build one with \"hpc build\", or set HPC_COMPANION")
}
