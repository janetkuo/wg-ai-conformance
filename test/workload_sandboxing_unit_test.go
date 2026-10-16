package conformance

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Unit tests for the workload sandboxing helpers (fake clientset, no cluster).

func TestParseProbeLogs(t *testing.T) {
	tests := []struct {
		name string
		logs string
		want sandboxProbeResults
	}{
		{
			name: "full pass",
			logs: `SANDBOX_INFO: KERNEL_RELEASE=4.4.0
SANDBOX_INFO: KERNEL_VERSION=Linux version 4.4.0 #1 SMP Sun Jan 10 15:06:54 PST 2016
SANDBOX_INFO: BOOT_ID=3f0c6a36-1c1e-4b7e-9a2b-8f4b0a4b2c11
SANDBOX_INFO: PID_COUNT=4
SANDBOX_PROBE: PID_ISOLATION=PASS
SANDBOX_PROBE: KERNEL_ISOLATION=PASS
SANDBOX_PROBE: FS_ISOLATION=PASS
SANDBOX_INFO: INTERFACES= eth0 lo
SANDBOX_PROBE: NET_ISOLATION=PASS
SANDBOX_PROBE: COMPLETED
`,
			want: sandboxProbeResults{
				PidIsolationPassed:    true,
				KernelIsolationPassed: true,
				FsIsolationPassed:     true,
				NetIsolationPassed:    true,
				ProbeCompleted:        true,
				Kernel: kernelIdentity{
					Release: "4.4.0",
					Version: "Linux version 4.4.0 #1 SMP Sun Jan 10 15:06:54 PST 2016",
					BootID:  "3f0c6a36-1c1e-4b7e-9a2b-8f4b0a4b2c11",
				},
				Interfaces: "eth0 lo",
				PidCount:   4,
			},
		},
		{
			name: "pid isolation failure and unreadable boot_id",
			logs: `SANDBOX_INFO: KERNEL_RELEASE=6.6.0
SANDBOX_INFO: BOOT_ID=unknown
SANDBOX_INFO: PID_COUNT=120
SANDBOX_PROBE: PID_ISOLATION=FAIL
SANDBOX_PROBE: KERNEL_ISOLATION=PASS
SANDBOX_PROBE: FS_ISOLATION=PASS
SANDBOX_PROBE: NET_ISOLATION=PASS
SANDBOX_PROBE: COMPLETED
`,
			want: sandboxProbeResults{
				KernelIsolationPassed: true,
				FsIsolationPassed:     true,
				NetIsolationPassed:    true,
				ProbeCompleted:        true,
				Kernel:                kernelIdentity{Release: "6.6.0"},
				PidCount:              120,
			},
		},
		{
			name: "network, kernel and fs failure",
			logs: `SANDBOX_PROBE: PID_ISOLATION=PASS
SANDBOX_PROBE: KERNEL_ISOLATION=FAIL
SANDBOX_PROBE: FS_ISOLATION=FAIL
SANDBOX_INFO: INTERFACES=docker0 eth0 lo
SANDBOX_PROBE: NET_ISOLATION=FAIL
SANDBOX_PROBE: COMPLETED
`,
			want: sandboxProbeResults{
				PidIsolationPassed: true,
				ProbeCompleted:     true,
				Interfaces:         "docker0 eth0 lo",
			},
		},
		{
			name: "probe died before completing",
			logs: `SANDBOX_INFO: KERNEL_RELEASE=6.6.0
`,
			want: sandboxProbeResults{
				Kernel: kernelIdentity{Release: "6.6.0"},
			},
		},
		{
			name: "empty logs",
			logs: "",
			want: sandboxProbeResults{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := *parseProbeLogs(tc.logs)
			got.RawLogs = ""
			if got != tc.want {
				t.Errorf("parseProbeLogs() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSandboxProbeScriptRuns executes the probe script on the local host and
// checks that it runs to completion and reports every check (PASS or FAIL;
// the host is not expected to be a sandbox). This catches shell syntax
// errors and missing markers without a cluster.
func TestSandboxProbeScriptRuns(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found; skipping probe script smoke test")
	}
	out, err := exec.Command(sh, "-c", sandboxProbeScript()).Output()
	if err != nil {
		t.Fatalf("probe script failed: %v\nOutput:\n%s", err, out)
	}
	logs := string(out)
	res := parseProbeLogs(logs)
	if !res.ProbeCompleted {
		t.Errorf("probe script did not print %q. Output:\n%s", sandboxProbeCompletedMarker, logs)
	}
	for _, check := range []string{"PID_ISOLATION", "KERNEL_ISOLATION", "FS_ISOLATION", "NET_ISOLATION"} {
		if !strings.Contains(logs, "SANDBOX_PROBE: "+check+"=PASS") && !strings.Contains(logs, "SANDBOX_PROBE: "+check+"=FAIL") {
			t.Errorf("probe script did not report %s. Output:\n%s", check, logs)
		}
	}
	if res.Kernel.Release == "" {
		t.Errorf("probe script did not report a kernel release. Output:\n%s", logs)
	}
}

func TestKernelIdentitiesDiffer(t *testing.T) {
	host := kernelIdentity{
		Release: "6.6.87+",
		Version: "Linux version 6.6.87+ (builder@host) #1 SMP",
		BootID:  "aaaaaaaa-0000-0000-0000-000000000000",
	}
	tests := []struct {
		name       string
		sandboxed  kernelIdentity
		wantDiffer bool
		wantFields []string
	}{
		{
			name:       "gVisor reports its own kernel release, version and boot_id",
			sandboxed:  kernelIdentity{Release: "4.4.0", Version: "Linux version 4.4.0 #1 SMP Sun Jan 10 15:06:54 PST 2016", BootID: "bbbbbbbb-0000-0000-0000-000000000000"},
			wantDiffer: true,
			wantFields: []string{"release", "version", "boot_id"},
		},
		{
			name:       "Kata guest with the same kernel release still has a different boot_id",
			sandboxed:  kernelIdentity{Release: host.Release, Version: host.Version, BootID: "cccccccc-0000-0000-0000-000000000000"},
			wantDiffer: true,
			wantFields: []string{"boot_id"},
		},
		{
			name:       "plain runc container shares the host kernel identity",
			sandboxed:  host,
			wantDiffer: false,
		},
		{
			name:       "unreadable fields are ignored rather than counted as different",
			sandboxed:  kernelIdentity{Release: host.Release, Version: "", BootID: ""},
			wantDiffer: false,
		},
		{
			name:       "release alone is enough when nothing else is readable",
			sandboxed:  kernelIdentity{Release: "4.4.0"},
			wantDiffer: true,
			wantFields: []string{"release"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			differ, fields := kernelIdentitiesDiffer(tc.sandboxed, host)
			if differ != tc.wantDiffer {
				t.Fatalf("kernelIdentitiesDiffer() = %v, want %v (fields %v)", differ, tc.wantDiffer, fields)
			}
			if strings.Join(fields, ",") != strings.Join(tc.wantFields, ",") {
				t.Errorf("differing fields = %v, want %v", fields, tc.wantFields)
			}
		})
	}
}

func TestIsKnownSandboxedRuntime(t *testing.T) {
	tests := []struct {
		name    string
		rcName  string
		handler string
		want    bool
	}{
		{name: "gvisor by name", rcName: "gvisor", handler: "runsc", want: true},
		{name: "runsc by handler", rcName: "sandboxed-runtime", handler: "runsc", want: true},
		{name: "kata by name", rcName: "kata", handler: "kata", want: true},
		{name: "kata-qemu by name", rcName: "kata-qemu", handler: "kata-qemu", want: true},
		{name: "kata-clh by name", rcName: "kata-clh", handler: "kata-clh", want: true},
		{name: "AKS pod sandboxing", rcName: "kata-mshv-vm-isolation", handler: "kata-mshv-vm-isolation", want: true},
		{name: "sandboxed containers", rcName: "sandboxed-containers", handler: "kata", want: true},
		{name: "quark runtime", rcName: "quark", handler: "quark", want: true},
		{name: "krun runtime", rcName: "krun", handler: "krun", want: true},
		{name: "crun-krun handler", rcName: "libkrun", handler: "crun-krun", want: true},
		{name: "standard runc", rcName: "runc", handler: "runc", want: false},
		{name: "crun runtime", rcName: "crun", handler: "crun", want: false},
		{name: "default runtime", rcName: "default", handler: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rc := &nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{Name: tc.rcName},
				Handler:    tc.handler,
			}
			if got := isKnownSandboxedRuntime(rc); got != tc.want {
				t.Errorf("isKnownSandboxedRuntime() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("nil runtimeclass", func(t *testing.T) {
		if isKnownSandboxedRuntime(nil) {
			t.Error("isKnownSandboxedRuntime(nil) = true, want false")
		}
	})
}

func TestBuildProbePod(t *testing.T) {
	sandboxed := buildProbePod("test-ns", "sandboxed", "gvisor", "")
	if sandboxed.Spec.RuntimeClassName == nil || *sandboxed.Spec.RuntimeClassName != "gvisor" {
		t.Errorf("sandboxed pod RuntimeClassName = %v, want gvisor", sandboxed.Spec.RuntimeClassName)
	}
	if sandboxed.Spec.NodeName != "" || len(sandboxed.Spec.Tolerations) != 0 {
		t.Errorf("sandboxed pod must go through the scheduler; got nodeName %q, tolerations %v", sandboxed.Spec.NodeName, sandboxed.Spec.Tolerations)
	}

	control := buildProbePod("test-ns", "control", "", "node-a")
	if control.Spec.RuntimeClassName != nil {
		t.Errorf("control pod must not set a RuntimeClass; got %q", *control.Spec.RuntimeClassName)
	}
	if control.Spec.NodeName != "node-a" {
		t.Errorf("control pod nodeName = %q, want node-a", control.Spec.NodeName)
	}
	if len(control.Spec.Tolerations) != 1 || control.Spec.Tolerations[0].Operator != "Exists" {
		t.Errorf("control pod must tolerate NoExecute taints on the sandbox node; got %v", control.Spec.Tolerations)
	}
}

func TestResolveSandboxedRuntimeClass(t *testing.T) {
	ctx := context.Background()

	t.Run("auto-detects a known RuntimeClass", func(t *testing.T) {
		client := fake.NewClientset(
			&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "runc"}, Handler: "runc"},
			&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "kata"}, Handler: "kata-qemu"},
		)

		rc, err := resolveSandboxedRuntimeClass(ctx, client, "", t.Logf)
		if err != nil {
			t.Fatalf("resolveSandboxedRuntimeClass failed: %v", err)
		}
		if rc.Name != "kata" || rc.Handler != "kata-qemu" {
			t.Errorf("Unexpected RuntimeClass: %+v", rc)
		}
	})

	t.Run("explicit RuntimeClass is used even if not a known name", func(t *testing.T) {
		client := fake.NewClientset(
			&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "custom-sandbox"}, Handler: "custom-handler"},
		)

		rc, err := resolveSandboxedRuntimeClass(ctx, client, "custom-sandbox", t.Logf)
		if err != nil {
			t.Fatalf("resolveSandboxedRuntimeClass failed: %v", err)
		}
		if rc.Name != "custom-sandbox" || rc.Handler != "custom-handler" {
			t.Errorf("Unexpected RuntimeClass: %+v", rc)
		}
	})

	t.Run("explicit RuntimeClass fails when missing", func(t *testing.T) {
		client := fake.NewClientset()

		_, err := resolveSandboxedRuntimeClass(ctx, client, "missing-class", t.Logf)
		if err == nil {
			t.Fatal("Expected error for missing RuntimeClass, got nil")
		}
		if errors.Is(err, errNoSandboxedRuntimeClass) {
			t.Errorf("a missing explicit RuntimeClass must fail, not skip; got %v", err)
		}
	})

	t.Run("returns errNoSandboxedRuntimeClass when none detected", func(t *testing.T) {
		client := fake.NewClientset(
			&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "runc"}, Handler: "runc"},
		)

		_, err := resolveSandboxedRuntimeClass(ctx, client, "", t.Logf)
		if !errors.Is(err, errNoSandboxedRuntimeClass) {
			t.Fatalf("Expected errNoSandboxedRuntimeClass, got %v", err)
		}
	})
}
