//go:build linux

// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/agent-substrate/substrate/internal/ateompath"
)

// TestNvproxyGlobalArgs checks that runsc is told to enable nvproxy exactly when the
// worker has a GPU. The flag must be present on sandbox creation so the sentry
// initializes GPU support up front; without it the GPU subcontainer crashes.
func TestNvproxyGlobalArgs(t *testing.T) {
	dir := t.TempDir()
	old := gpuDeviceGlob
	gpuDeviceGlob = filepath.Join(dir, "nvidia[0-9]*")
	defer func() { gpuDeviceGlob = old }()

	if got := nvproxyGlobalArgs(); len(got) != 0 {
		t.Fatalf("no GPU: want no flags, got %v", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "nvidia0"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := nvproxyGlobalArgs()
	if len(got) != 1 || got[0] != "--nvproxy" {
		t.Fatalf("GPU: want [--nvproxy], got %v", got)
	}
}

func TestKillArgs(t *testing.T) {
	r := &runsc{
		path:     "/usr/bin/runsc",
		actorUID: "test-actor-123",
	}

	got := r.killArgs("my-container", "SIGTERM")
	want := []string{
		"-log-format", "json",
		"--alsologtostderr",
		"-root", ateompath.RunSCStateDir("test-actor-123"),
		"kill",
		"my-container",
		"SIGTERM",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("killArgs() = %v, want %v", got, want)
	}
}

func TestWaitArgs(t *testing.T) {
	r := &runsc{
		path:     "/usr/bin/runsc",
		actorUID: "test-actor-123",
	}

	got := r.waitArgs("my-container")
	want := []string{
		"-log-format", "json",
		"--alsologtostderr",
		"-root", ateompath.RunSCStateDir("test-actor-123"),
		"wait",
		"my-container",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("waitArgs() = %v, want %v", got, want)
	}
}

// fakeRunscState builds an executable shell script standing in for `runsc`
// that prints stdout (unconditionally, ignoring its args) and exits 0. It
// lets stateJSON's parsing be tested without a real runsc/gVisor sandbox.
func fakeRunscState(t *testing.T, stdout string) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("fake runsc script requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "fake-runsc.sh")
	script := "#!/bin/sh\ncat <<'EOF'\n" + stdout + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake runsc script: %v", err)
	}
	return path
}

func TestRunscStateJSON(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		want    *specs.State
		wantErr bool
	}{
		{
			name:   "running",
			stdout: `{"ociVersion":"1.0.2","id":"agent","status":"running","pid":42,"bundle":"/bundle"}`,
			want:   &specs.State{Version: "1.0.2", ID: "agent", Status: specs.StateRunning, Pid: 42, Bundle: "/bundle"},
		},
		{
			name:   "stopped",
			stdout: `{"ociVersion":"1.0.2","id":"agent","status":"stopped","bundle":"/bundle"}`,
			want:   &specs.State{Version: "1.0.2", ID: "agent", Status: specs.StateStopped, Bundle: "/bundle"},
		},
		{
			name:    "malformed JSON",
			stdout:  `not json`,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := &runsc{
				path:     fakeRunscState(t, test.stdout),
				actorUID: "test-actor-123",
			}

			got, err := r.stateJSON(context.Background(), "agent")
			if test.wantErr {
				if err == nil {
					t.Fatalf("stateJSON() = %+v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("stateJSON() unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("stateJSON() = %+v, want %+v", got, test.want)
			}
		})
	}
}

// TestRunscStateJSON_CommandError checks that a nonzero exit from the runsc
// binary itself (not the JSON body) surfaces as an error, e.g. when the
// container does not exist.
func TestRunscStateJSON_CommandError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-runsc-fail.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing fake runsc script: %v", err)
	}
	r := &runsc{path: path, actorUID: "test-actor-123"}

	if _, err := r.stateJSON(context.Background(), "agent"); err == nil {
		t.Fatal("stateJSON() = nil error, want error for nonzero exit")
	}
}
