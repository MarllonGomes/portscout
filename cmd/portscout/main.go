// Command portscout discovers listening ports on a remote host and forwards the
// ones you pick, from a terminal dashboard.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/MarllonGomes/portscout/internal/discover"
	"github.com/MarllonGomes/portscout/internal/plan"
)

// options are the seams the tests replace; production fills them in main.
type options struct {
	runner   discover.Runner
	stdout   io.Writer
	stderr   io.Writer
	portFree func(int) bool
}

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

	command := args[0]
	switch command {
	case "list":
	default:
		fmt.Fprintf(o.stderr, "comando desconhecido: %s\n\n%s\n", command, usage)
		return 2
	}

	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(o.stderr)
	all := fs.Bool("all", false, "não filtrar portas de sistema")

	// flag stops at the first positional, so "list dev --all" would drop --all.
	// Parsing repeatedly, peeling one positional off each round, honours flags
	// wherever the user typed them.
	var positional []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
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

	if o.runner == nil {
		o.runner = discover.SSHRunner{}
	}
	if o.portFree == nil {
		o.portFree = plan.LocalPortFree
	}

	return list(o, host, *all)
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
  portscout list <ssh-host>    descobre e imprime

flags: --all`
