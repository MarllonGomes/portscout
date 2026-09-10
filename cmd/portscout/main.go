// Command portscout discovers listening ports on a remote host and forwards the
// ones you pick, from a terminal dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/MarllonGomes/portscout/internal/discover"
	"github.com/MarllonGomes/portscout/internal/plan"
	"github.com/MarllonGomes/portscout/internal/ui"
)

// options are the seams the tests replace; production fills them in main.
type options struct {
	runner   discover.Runner
	stdout   io.Writer
	stderr   io.Writer
	portFree func(int) bool
	// runTUI is replaced in tests so nothing ever needs a terminal.
	runTUI func(context.Context, ui.Backend) int
	// runDash is replaced in tests so dispatch and flag parsing can be asserted
	// without opening an ssh connection.
	runDash func(dashConfig) int
}

// commands are the verbs. Anything else in the first position is a host, so
// `portscout dev` works — which is the whole point of the dashboard being the
// default. A host genuinely named "list" is still reachable as `portscout up list`.
var commands = map[string]bool{"list": true, "up": true}

func main() {
	os.Exit(run(os.Args[1:], options{}))
}

func run(args []string, o options) int {
	if o.stdout == nil {
		o.stdout = os.Stdout
	}
	if o.stderr == nil {
		o.stderr = os.Stderr
	}
	if len(args) == 0 {
		fmt.Fprintln(o.stderr, usage)
		return 2
	}

	command, rest := "up", args
	if commands[args[0]] {
		command, rest = args[0], args[1:]
	}

	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(o.stderr)
	all := fs.Bool("all", false, "não filtrar portas de sistema")
	every := fs.Duration("every", 10*time.Second, "intervalo entre varreduras")
	statePath := fs.String("state", "", "caminho do arquivo de escolhas")
	noAuto := fs.Bool("no-autostart", false, "não subir os túneis lembrados na abertura")

	// flag stops at the first positional, so "dev --all" would drop --all.
	// Parsing repeatedly, peeling one positional off each round, honours flags
	// wherever the user typed them.
	var positional []string
	for {
		if err := fs.Parse(rest); err != nil {
			// `--help` exiting non-zero is a bug people notice in scripts.
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(o.stdout, usage)
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) < 1 {
		fmt.Fprintf(o.stderr, "falta o host ssh\n\n%s\n", usage)
		return 2
	}
	host := positional[0]

	if o.portFree == nil {
		o.portFree = plan.LocalPortFree
	}
	if o.runTUI == nil {
		o.runTUI = runBubbleTea
	}
	if o.runDash == nil {
		o.runDash = dash
	}

	if command == "list" {
		if o.runner == nil {
			o.runner = discover.SSHRunner{}
		}
		return list(o, host, *all)
	}
	return o.runDash(dashConfig{
		host:      host,
		statePath: *statePath,
		interval:  *every,
		showAll:   *all,
		autoStart: !*noAuto,
		opts:      o,
	})
}

func list(o options, host string, all bool) int {
	ports, err := discover.Discover(context.Background(), o.runner, host, all)
	if err != nil {
		fmt.Fprintln(o.stderr, err)
		return 1
	}
	if len(ports) == 0 {
		fmt.Fprintf(o.stdout, "nenhuma porta em LISTEN encontrada em %s\n", host)
		return 0
	}
	names := plan.Aliases(ports)
	for _, p := range ports {
		fmt.Fprintf(o.stdout, "  %-6d %s\n", p.Port, names[p.Port])
	}
	return 0
}

const usage = `uso:
  portscout <ssh-host>          abre o painel e os túneis (padrão)
  portscout list <ssh-host>     descobre e imprime, sem tunelar
  portscout up <ssh-host>       igual ao padrão, para um host chamado "list"

flags: --all --every=10s --state=CAMINHO --no-autostart`
