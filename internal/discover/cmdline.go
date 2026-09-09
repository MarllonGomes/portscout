package discover

import (
	"strconv"
	"strings"
)

// ParseCmdlines reads the "pid<TAB>cmdline" table the remote script prints for
// every pid ss reported. A pid whose /proc entry vanished between the two reads
// is simply absent.
func ParseCmdlines(out []byte) map[int]string {
	table := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		pidText, cmdline, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if err != nil {
			continue
		}
		if cmdline = strings.TrimSpace(cmdline); cmdline != "" {
			table[pid] = cmdline
		}
	}
	return table
}
