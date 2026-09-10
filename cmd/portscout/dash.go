package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MarllonGomes/portscout/internal/session"
	"github.com/MarllonGomes/portscout/internal/sshmux"
	"github.com/MarllonGomes/portscout/internal/state"
	"github.com/MarllonGomes/portscout/internal/tunnel"
	"github.com/MarllonGomes/portscout/internal/ui"
)

// dashConfig is what `run` hands to the dashboard, so the wiring below can be
// replaced wholesale in a test.
type dashConfig struct {
	host      string
	statePath string
	interval  time.Duration
	showAll   bool
	autoStart bool
	opts      options
}

// dash brings up the ssh master, the supervisor, the session and the terminal,
// in that order, and tears them down in reverse.
func dash(cfg dashConfig) int {
	o := cfg.opts

	// Everything that can fail is resolved before the terminal is taken over, so
	// a misconfigured machine gets a plain error instead of a broken screen.
	client, err := sshmux.New(cfg.host)
	if err != nil {
		fmt.Fprintln(o.stderr, err)
		return 1
	}
	store, err := openStore(cfg.statePath, cfg.host)
	if err != nil {
		fmt.Fprintln(o.stderr, err)
		return 1
	}

	// One context for the whole thing: quitting with q, ctrl+c, or the terminal
	// closing all take the same shutdown path.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	sup := tunnel.NewSupervisor(tunnel.Config{Client: client})
	supDone := make(chan struct{})
	go func() {
		_ = sup.Run(ctx)
		close(supDone)
	}()

	sess, err := session.New(session.Config{
		Host:      cfg.host,
		Forwarder: sup,
		Runner:    client.Runner(),
		Store:     store,
		PortFree:  o.portFree,
		Interval:  cfg.interval,
		ShowAll:   cfg.showAll,
		AutoStart: cfg.autoStart,
	})
	if err != nil {
		fmt.Fprintln(o.stderr, err)
		stop()
		<-supDone
		return 1
	}
	sessDone := make(chan struct{})
	go func() {
		_ = sess.Run(ctx)
		close(sessDone)
	}()

	code := o.runTUI(ctx, sess)

	// Shut down in reverse and wait: leaving an ssh master behind would keep
	// tunnels open that the user believes they closed.
	stop()
	<-sessDone
	<-supDone

	if code == 0 {
		fmt.Fprintf(o.stdout, "%s\n", partingLine(sess.Snapshot()))
	}
	return code
}

// runBubbleTea is the production terminal. Tests swap it out, so no test ever
// needs a TTY.
func runBubbleTea(ctx context.Context, backend ui.Backend) int {
	m := ui.New(ui.Config{Backend: backend})
	if _, err := tea.NewProgram(m, tea.WithContext(ctx)).Run(); err != nil {
		fmt.Println(err)
		return 1
	}
	return 0
}

// partingLine survives in the scrollback, because the alt screen is gone by the
// time it is printed. It is also the honest confirmation that quitting really
// did close the tunnels.
func partingLine(snap session.Snapshot) string {
	n := 0
	for _, r := range snap.Rows {
		if r.State == tunnel.Up {
			n++
		}
	}
	switch n {
	case 0:
		return "nenhum túnel estava aberto"
	case 1:
		return "1 túnel encerrado"
	default:
		return fmt.Sprintf("%d túneis encerrados", n)
	}
}

func openStore(path, host string) (*state.Store, error) {
	if path != "" {
		return state.OpenAt(path, host), nil
	}
	return state.Open(host)
}
