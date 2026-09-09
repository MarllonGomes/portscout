// Command portscout discovers listening ports on a remote host and writes them
// into the tunnel9 config. It never opens a tunnel itself.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/MarllonGomes/portscout/internal/discover"
	"github.com/MarllonGomes/portscout/internal/plan"
	"github.com/MarllonGomes/portscout/internal/tunnel9"
)

// options are the seams the tests replace; production fills them in main.
type options struct {
	runner     discover.Runner
	configPath string
	stdout     io.Writer
	stderr     io.Writer
	portFree   func(int) bool
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
	case "list", "scan", "watch":
	default:
		fmt.Fprintf(o.stderr, "comando desconhecido: %s\n\n%s\n", command, usage)
		return 2
	}

	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(o.stderr)
	all := fs.Bool("all", false, "não filtrar portas de sistema")
	prune := fs.Bool("prune", false, "remover entradas gerenciadas cuja porta sumiu")
	tag := fs.String("tag", "auto", "tag das entradas gerenciadas")
	configPath := fs.String("config", "", "caminho do YAML do tunnel9")
	every := fs.Duration("every", 15*time.Second, "intervalo do watch")

	// flag stops at the first positional, so "scan dev --all" would drop --all.
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
	if o.configPath == "" {
		o.configPath = *configPath
	}
	if o.configPath == "" {
		o.configPath = tunnel9.DefaultPath()
	}

	if command == "watch" {
		for {
			if code := once(o, host, command, *all, *prune, *tag); code != 0 {
				return code
			}
			time.Sleep(*every)
		}
	}
	return once(o, host, command, *all, *prune, *tag)
}

func once(o options, host, command string, all, prune bool, tag string) int {
	ports, err := discover.Discover(context.Background(), o.runner, host, all)
	if err != nil {
		fmt.Fprintln(o.stderr, err)
		return 1
	}
	if len(ports) == 0 {
		fmt.Fprintf(o.stdout, "nenhuma porta em LISTEN encontrada em %s\n", host)
		return 0
	}

	if command == "list" {
		names := plan.Aliases(ports)
		for _, p := range ports {
			fmt.Fprintf(o.stdout, "  %-6d %s\n", p.Port, names[p.Port])
		}
		return 0
	}

	cfg, err := tunnel9.Load(o.configPath)
	if err != nil {
		fmt.Fprintf(o.stderr, "%v\naponte outro arquivo com --config\n", err)
		return 1
	}
	assignments := plan.Build(ports, cfg.TakenLocalPorts(), o.portFree)
	changes := cfg.Merge(host, assignments, tag, prune)
	// Report the remap from the entry the merge actually wrote: on a rescan the
	// planner reassigns ports that the merge discards, and announcing those
	// would be a lie.
	for _, c := range changes {
		if c.Kind == tunnel9.Added && c.Entry.LocalPort != c.Entry.RemotePort {
			fmt.Fprintf(o.stdout, "  porta %d remapeada para %d (a local estava ocupada)\n",
				c.Entry.RemotePort, c.Entry.LocalPort)
		}
	}
	if len(changes) == 0 {
		fmt.Fprintln(o.stdout, "nada mudou")
		return 0
	}
	if err := cfg.Save(o.configPath); err != nil {
		fmt.Fprintln(o.stderr, err)
		return 1
	}
	for _, c := range changes {
		fmt.Fprintf(o.stdout, "  %s %d -> %d  %s\n",
			verb(c.Kind), c.Entry.RemotePort, c.Entry.LocalPort, c.Entry.Alias)
	}
	fmt.Fprintf(o.stdout, "%s atualizado\n", o.configPath)
	return 0
}

func verb(k tunnel9.ChangeKind) string {
	switch k {
	case tunnel9.Added:
		return "+"
	case tunnel9.Pruned:
		return "-"
	default:
		return "~"
	}
}

const usage = `uso:
  portscout list  <ssh-host>    descobre e imprime, sem escrever
  portscout scan  <ssh-host>    descobre e atualiza o YAML do tunnel9
  portscout watch <ssh-host>    scan em loop

flags: --all --prune --tag=auto --config=CAMINHO --every=15s`
