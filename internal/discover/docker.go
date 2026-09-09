package discover

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// The host port is always the number immediately before "->", which sidesteps
// having to parse the address in front of it (":::8080->80/tcp" and
// "127.0.0.1:3102->5432/tcp" both work). An unpublished "5432/tcp" has no
// arrow and is correctly ignored.
var dockerHostPort = regexp.MustCompile(`(\d+)->\d+/tcp`)

// ParseDocker reads `docker ps --format '{{json .}}'` output and maps each
// published host port to its container name. Unparseable lines are skipped:
// losing one container's label must not fail the scan.
func ParseDocker(out []byte) map[int]string {
	byPort := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			Names string `json:"Names"`
			Ports string `json:"Ports"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Names == "" {
			continue
		}
		for _, m := range dockerHostPort.FindAllStringSubmatch(row.Ports, -1) {
			port, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			byPort[port] = row.Names
		}
	}
	return byPort
}
