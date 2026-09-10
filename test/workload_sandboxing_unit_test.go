package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestParseProbeLogs(t *testing.T) {
	tests := []struct {
		name                   string
		logs                   string
		wantSchedulingPassed   bool
		wantPidIsolationPassed bool
		wantKernelPassed       bool
		wantFsPassed           bool
		wantNetPassed          bool
		wantProbeCompleted     bool
		wantKernelInfo         string
		wantInterfaces         string
		wantPidCount           int
	}{
		{
			name: "full pass",
			logs: `SANDBOX_PROBE: SCHEDULING=PASS
SANDBOX_INFO: KERNEL=Linux 4.4.0 #1 SMP Sun Jan 10 00:00:00 PST 2016 x86_64 gVisor
SANDBOX_INFO: PID_COUNT=4
SANDBOX_PROBE: PID_ISOLATION=PASS
SANDBOX_PROBE: KERNEL_ISOLATION=PASS
SANDBOX_PROBE: FS_ISOLATION=PASS
SANDBOX_INFO: INTERFACES=eth0 lo
SANDBOX_PROBE: NET_ISOLATION=PASS
SANDBOX_PROBE: COMPLETED
`,
			wantSchedulingPassed:   true,
			wantPidIsolationPassed: true,
			wantKernelPassed:       true,
			wantFsPassed:           true,
			wantNetPassed:          true,
			wantProbeCompleted:     true,
			wantKernelInfo:         "Linux 4.4.0 #1 SMP Sun Jan 10 00:00:00 PST 2016 x86_64 gVisor",
			wantInterfaces:         "eth0 lo",
			wantPidCount:           4,
		},
		{
			name: "pid isolation failure",
			logs: `SANDBOX_PROBE: SCHEDULING=PASS
SANDBOX_INFO: KERNEL=Linux 6.6.0
SANDBOX_INFO: PID_COUNT=120
SANDBOX_PROBE: PID_ISOLATION=FAIL
SANDBOX_PROBE: KERNEL_ISOLATION=PASS
SANDBOX_PROBE: FS_ISOLATION=PASS
SANDBOX_PROBE: NET_ISOLATION=PASS
SANDBOX_PROBE: COMPLETED
`,
			wantSchedulingPassed:   true,
			wantPidIsolationPassed: false,
			wantKernelPassed:       true,
			wantFsPassed:           true,
			wantNetPassed:          true,
			wantProbeCompleted:     true,
			wantKernelInfo:         "Linux 6.6.0",
			wantPidCount:           120,
		},
		{
			name: "network and kernel failure",
			logs: `SANDBOX_PROBE: SCHEDULING=PASS
SANDBOX_PROBE: PID_ISOLATION=PASS
SANDBOX_PROBE: KERNEL_ISOLATION=FAIL
SANDBOX_PROBE: FS_ISOLATION=FAIL
SANDBOX_INFO: INTERFACES=docker0 eth0 lo
SANDBOX_PROBE: NET_ISOLATION=FAIL
SANDBOX_PROBE: COMPLETED
`,
			wantSchedulingPassed:   true,
			wantPidIsolationPassed: true,
			wantKernelPassed:       false,
			wantFsPassed:           false,
			wantNetPassed:          false,
			wantProbeCompleted:     true,
			wantInterfaces:         "docker0 eth0 lo",
		},
		{
			name:                 "empty logs",
			logs:                 "",
			wantSchedulingPassed: false,
			wantProbeCompleted:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := parseProbeLogs(tc.logs)
			if res.SchedulingPassed != tc.wantSchedulingPassed {
				t.Errorf("SchedulingPassed = %v, want %v", res.SchedulingPassed, tc.wantSchedulingPassed)
			}
			if res.PidIsolationPassed != tc.wantPidIsolationPassed {
				t.Errorf("PidIsolationPassed = %v, want %v", res.PidIsolationPassed, tc.wantPidIsolationPassed)
			}
			if res.KernelIsolationPassed != tc.wantKernelPassed {
				t.Errorf("KernelIsolationPassed = %v, want %v", res.KernelIsolationPassed, tc.wantKernelPassed)
			}
			if res.FsIsolationPassed != tc.wantFsPassed {
				t.Errorf("FsIsolationPassed = %v, want %v", res.FsIsolationPassed, tc.wantFsPassed)
			}
			if res.NetIsolationPassed != tc.wantNetPassed {
				t.Errorf("NetIsolationPassed = %v, want %v", res.NetIsolationPassed, tc.wantNetPassed)
			}
			if res.ProbeCompleted != tc.wantProbeCompleted {
				t.Errorf("ProbeCompleted = %v, want %v", res.ProbeCompleted, tc.wantProbeCompleted)
			}
			if tc.wantKernelInfo != "" && res.KernelInfo != tc.wantKernelInfo {
				t.Errorf("KernelInfo = %q, want %q", res.KernelInfo, tc.wantKernelInfo)
			}
			if tc.wantInterfaces != "" && res.Interfaces != tc.wantInterfaces {
				t.Errorf("Interfaces = %q, want %q", res.Interfaces, tc.wantInterfaces)
			}
			if tc.wantPidCount != 0 && res.PidCount != tc.wantPidCount {
				t.Errorf("PidCount = %d, want %d", res.PidCount, tc.wantPidCount)
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
		{name: "sandboxed containers", rcName: "sandboxed-containers", handler: "kata", want: true},
		{name: "quark runtime", rcName: "quark", handler: "quark", want: true},
		{name: "krun runtime", rcName: "krun", handler: "krun", want: true},
		{name: "standard runc", rcName: "runc", handler: "runc", want: false},
		{name: "crun runtime", rcName: "crun", handler: "crun", want: false},
		{name: "default runtime", rcName: "default", handler: "", want: false},
		{name: "nil runtimeclass", rcName: "", handler: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rc *nodev1.RuntimeClass
			if tc.name != "nil runtimeclass" {
				rc = &nodev1.RuntimeClass{
					ObjectMeta: metav1.ObjectMeta{Name: tc.rcName},
					Handler:    tc.handler,
				}
			}
			if got := isKnownSandboxedRuntime(rc); got != tc.want {
				t.Errorf("isKnownSandboxedRuntime() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildSandboxedPod(t *testing.T) {
	pod := buildSandboxedPod("test-ns", "test-pod", "gvisor", "busybox")

	if pod.Name != "test-pod" || pod.Namespace != "test-ns" {
		t.Errorf("Unexpected metadata: %s/%s", pod.Namespace, pod.Name)
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "gvisor" {
		t.Errorf("Unexpected RuntimeClassName: %v", pod.Spec.RuntimeClassName)
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("Expected 1 container, got %d", len(pod.Spec.Containers))
	}
	c := pod.Spec.Containers[0]
	if c.Name != "prober" || c.Image != "busybox" {
		t.Errorf("Unexpected container attributes: %s, %s", c.Name, c.Image)
	}
	if len(c.Args) == 0 || !strings.Contains(c.Args[0], "SANDBOX_PROBE: SCHEDULING=PASS") {
		t.Errorf("Container missing probe command: %v", c.Args)
	}
	if c.Resources.Requests.Cpu().IsZero() || c.Resources.Requests.Memory().IsZero() {
		t.Errorf("Container missing resource requests: %v", c.Resources)
	}
}

func TestBuildAgentSandboxCR(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1alpha1", Resource: "sandboxes"}
	cr := buildAgentSandboxCR("test-ns", "test-cr", gvr, "kata", "busybox")

	if cr.GetAPIVersion() != "agents.x-k8s.io/v1alpha1" {
		t.Errorf("Unexpected APIVersion: %s", cr.GetAPIVersion())
	}
	if cr.GetKind() != "Sandbox" {
		t.Errorf("Unexpected Kind: %s", cr.GetKind())
	}
	if cr.GetName() != "test-cr" || cr.GetNamespace() != "test-ns" {
		t.Errorf("Unexpected metadata: %s/%s", cr.GetNamespace(), cr.GetName())
	}
}

func TestDiscoverSandboxingSolution(t *testing.T) {
	ctx := context.Background()

	t.Run("auto discovers known RuntimeClass", func(t *testing.T) {
		client := k8sfake.NewSimpleClientset(
			&nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "kata"},
				Handler:    "kata-qemu",
			},
		)
		dynClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

		sol, err := discoverSandboxingSolution(ctx, client, dynClient, "auto", "", t.Logf)
		if err != nil {
			t.Fatalf("discoverSandboxingSolution failed: %v", err)
		}
		if sol.SolutionType != "runtimeclass" || sol.RuntimeClassName != "kata" {
			t.Errorf("Unexpected solution: %+v", sol)
		}
	})

	t.Run("explicit RuntimeClass succeeds when present", func(t *testing.T) {
		client := k8sfake.NewSimpleClientset(
			&nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "custom-sandbox"},
				Handler:    "custom-handler",
			},
		)
		dynClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

		sol, err := discoverSandboxingSolution(ctx, client, dynClient, "runtimeclass", "custom-sandbox", t.Logf)
		if err != nil {
			t.Fatalf("discoverSandboxingSolution failed: %v", err)
		}
		if sol.RuntimeClassName != "custom-sandbox" {
			t.Errorf("Unexpected RuntimeClassName: %s", sol.RuntimeClassName)
		}
	})

	t.Run("explicit RuntimeClass fails when missing", func(t *testing.T) {
		client := k8sfake.NewSimpleClientset()
		dynClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

		_, err := discoverSandboxingSolution(ctx, client, dynClient, "runtimeclass", "missing-class", t.Logf)
		if err == nil {
			t.Fatal("Expected error for missing RuntimeClass, got nil")
		}
	})

	t.Run("auto returns ErrNoSandboxingSolution when none detected", func(t *testing.T) {
		client := k8sfake.NewSimpleClientset(
			&nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "runc"},
				Handler:    "runc",
			},
		)
		dynClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

		_, err := discoverSandboxingSolution(ctx, client, dynClient, "auto", "", t.Logf)
		if !errors.Is(err, ErrNoSandboxingSolution) {
			t.Fatalf("Expected ErrNoSandboxingSolution, got %v", err)
		}
	})
}

func TestSandboxProbeScriptStructure(t *testing.T) {
	script := sandboxProbeScript()

	requiredMarkers := []string{
		"SANDBOX_PROBE: SCHEDULING=PASS",
		"SANDBOX_PROBE: PID_ISOLATION=PASS",
		"SANDBOX_PROBE: KERNEL_ISOLATION=PASS",
		"SANDBOX_PROBE: FS_ISOLATION=PASS",
		"SANDBOX_PROBE: NET_ISOLATION=PASS",
		"SANDBOX_PROBE: COMPLETED",
		"/proc/[0-9]*/cmdline",
		"/dev/mem",
		"/dev/kmem",
		"/etc/kubernetes",
		"/sys/class/net",
	}

	for _, marker := range requiredMarkers {
		if !strings.Contains(script, marker) {
			t.Errorf("Probe script missing required check marker: %q", marker)
		}
	}
}
