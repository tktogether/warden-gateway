package cnagent

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// twoContainerList is a Docker Engine API /containers/json response with the
// two shapes the agent must tell apart: a Coolify-managed container (any
// label key starting with "coolify.") and a plain container. Field names and
// shapes follow the real ContainerSummary: Id, Names carrying a leading
// slash, Created as a unix timestamp in seconds.
const twoContainerList = `[
	{
		"Id": "8dfafdbc3a40",
		"Names": ["/webapp"],
		"Image": "ghcr.io/example/webapp:v2",
		"State": "running",
		"Created": 1699999999,
		"Labels": {"coolify.applicationName": "myapp", "coolify.serviceName": "web"}
	},
	{
		"Id": "b0rn2run231",
		"Names": ["/redis-cache"],
		"Image": "redis:7-alpine",
		"State": "running",
		"Created": 1700000061,
		"Labels": {"com.example.vendor": "acme"}
	}
]`

// startFakeDocker serves response over a unix socket at
// t.TempDir()/docker.sock the way the docker daemon does, and returns the
// socket path plus an http.Client wired to dial that socket.
func startFakeDocker(t *testing.T, response string) (string, *http.Client) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			t.Errorf("fake docker got %s %q, want GET /containers/json", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	return socket, client
}

// findWorkload returns the workload with the given name and whether it exists,
// so assertions stay insensitive to the order docker returns containers in.
func findWorkload(workloads []Workload, name string) (Workload, bool) {
	for _, w := range workloads {
		if w.Name == name {
			return w, true
		}
	}
	return Workload{}, false
}

func TestDiscoverWorkloadsParsesContainers(t *testing.T) {
	socket, client := startFakeDocker(t, twoContainerList)
	wantStarted1 := time.Unix(1699999999, 0).UTC().Format(time.RFC3339)
	wantStarted2 := time.Unix(1700000061, 0).UTC().Format(time.RFC3339)

	got, err := DiscoverWorkloads(context.Background(), socket, client)
	if err != nil {
		t.Fatalf("DiscoverWorkloads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(workloads) = %d, want 2; got %+v", len(got), got)
	}

	coolifyOne, ok := findWorkload(got, "webapp")
	if !ok {
		t.Fatalf("workload webapp missing: %+v", got)
	}
	if coolifyOne.Image != "ghcr.io/example/webapp:v2" {
		t.Errorf("webapp Image = %q, want ghcr.io/example/webapp:v2", coolifyOne.Image)
	}
	if coolifyOne.State != "running" {
		t.Errorf("webapp State = %q, want running", coolifyOne.State)
	}
	if coolifyOne.Started != wantStarted1 {
		t.Errorf("webapp Started = %q, want %q (RFC3339 UTC from Created)", coolifyOne.Started, wantStarted1)
	}
	if !coolifyOne.Coolify {
		t.Errorf("webapp Coolify = false, want true (labels contain coolify.applicationName)")
	}

	plain, ok := findWorkload(got, "redis-cache")
	if !ok {
		t.Fatalf("workload redis-cache missing: %+v", got)
	}
	if plain.Image != "redis:7-alpine" {
		t.Errorf("redis-cache Image = %q, want redis:7-alpine", plain.Image)
	}
	if plain.State != "running" {
		t.Errorf("redis-cache State = %q, want running", plain.State)
	}
	if plain.Started != wantStarted2 {
		t.Errorf("redis-cache Started = %q, want %q (RFC3339 UTC from Created)", plain.Started, wantStarted2)
	}
	if plain.Coolify {
		t.Errorf("redis-cache Coolify = true, want false (no coolify.* labels)")
	}
}

// TestDiscoverWorkloadsNilClient covers the production call shape: no
// injected client, so discovery builds its own transport dialing the unix
// socket.
func TestDiscoverWorkloadsNilClient(t *testing.T) {
	socket, _ := startFakeDocker(t, twoContainerList)

	got, err := DiscoverWorkloads(context.Background(), socket, nil)
	if err != nil {
		t.Fatalf("DiscoverWorkloads with nil client: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(workloads) = %d, want 2; got %+v", len(got), got)
	}
}

func TestContainerWithoutNameFallsBackToID(t *testing.T) {
	response := `[
		{
			"Id": "abcdef123456",
			"Image": "nginx:latest",
			"State": "exited",
			"Created": 1710000000,
			"Labels": {}
		}
	]`
	socket, client := startFakeDocker(t, response)

	got, err := DiscoverWorkloads(context.Background(), socket, client)
	if err != nil {
		t.Fatalf("DiscoverWorkloads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(workloads) = %d, want 1; got %+v", len(got), got)
	}
	if got[0].Name != "abcdef123456" {
		t.Errorf("Name = %q, want the container ID abcdef123456 (container has no Names entry)", got[0].Name)
	}
	if got[0].Coolify {
		t.Errorf("Coolify = true, want false for empty labels")
	}
}

func TestMissingSocketIsNotError(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "does-not-exist.sock")

	got, err := DiscoverWorkloads(context.Background(), socket, nil)
	if err != nil {
		t.Fatalf("DiscoverWorkloads on missing socket: err = %v, want nil (no docker is not a discovery failure)", err)
	}
	if got != nil {
		t.Errorf("workloads = %+v, want nil", got)
	}
}
