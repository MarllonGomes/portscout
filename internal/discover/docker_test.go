package discover

import (
	"os"
	"testing"
)

func TestParseDocker(t *testing.T) {
	out, err := os.ReadFile("testdata/docker.json")
	if err != nil {
		t.Fatal(err)
	}
	got := ParseDocker(out)

	want := map[int]string{
		3102: "couple-community-postgres-1",
		3103: "couple-community-redis-1",
		3104: "couple-community-mailpit-1",
		3105: "couple-community-mailpit-1",
		8080: "web",
	}

	if len(got) != len(want) {
		t.Fatalf("got %d mappings, want %d: %+v", len(got), len(want), got)
	}
	for port, name := range want {
		if got[port] != name {
			t.Errorf("port %d: got %q, want %q", port, got[port], name)
		}
	}
}

func TestParseDockerHandlesEmptyAndGarbage(t *testing.T) {
	if got := ParseDocker(nil); len(got) != 0 {
		t.Errorf("nil input: got %+v", got)
	}
	if got := ParseDocker([]byte("not json\n")); len(got) != 0 {
		t.Errorf("garbage input: got %+v", got)
	}
}
