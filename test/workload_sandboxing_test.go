package conformance

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	sandboxRuntimeClass *string
	sandboxType         *string
	sandboxImage        *string
	sandboxNamespace    *string
	sandboxTimeout      *time.Duration

	ErrNoSandboxingSolution = errors.New("no sandboxing solution (RuntimeClass or agent-sandbox) detected")
)

func init() {
	sandboxRuntimeClass = flag.String("sandbox-runtime-class", "",
		"Name of the RuntimeClass configured for workload sandboxing (e.g. gvisor, kata, runsc). If empty, auto-detection will be performed.")
	sandboxType = flag.String("sandbox-type", "auto",
		"Type of sandboxing solution to test: 'auto' (detect RuntimeClass or agent-sandbox), 'runtimeclass', or 'agent-sandbox'.")
	sandboxImage = flag.String("sandbox-image", "busybox",
		"Container image used for executing the sandboxing isolation probe.")
	sandboxNamespace = flag.String("sandbox-namespace", "",
		"Namespace for sandboxing test execution. If empty, a temporary namespace is generated and cleaned up.")
	sandboxTimeout = flag.Duration("sandbox-timeout", 3*time.Minute,
		"Timeout for the sandboxed workload to schedule and complete probing.")
}

// SandboxingSolution represents a resolved sandboxing mechanism to test.
type SandboxingSolution struct {
	SolutionType     string // "runtimeclass" or "agent-sandbox"
	RuntimeClassName string
	Handler          string
	AgentSandboxGVR  *schema.GroupVersionResource
}

// SandboxProbeResults captures parsed output from the sandbox isolation probe script.
type SandboxProbeResults struct {
	SchedulingPassed      bool
	PidIsolationPassed    bool
	KernelIsolationPassed bool
	FsIsolationPassed     bool
	NetIsolationPassed    bool
	ProbeCompleted        bool
	KernelInfo            string
	Interfaces            string
	PidCount              int
	RawLogs               string
}

// TestWorkloadSandboxing verifies the Workload Sandboxing requirement (KAR-0020).
// It verifies that the platform provides a sandboxing mechanism (e.g. standard
// Kubernetes RuntimeClass such as gVisor, Kata, or an agent-sandbox solution)
// that isolates untrusted workload execution from the host node kernel, process,
// filesystem, and network namespaces.
// Ref: https://github.com/kubernetes-sigs/ai-conformance/tree/main/kars/0020-workload-sandboxing
func TestWorkloadSandboxing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping cluster E2E test in short mode")
	}
	if !flag.Parsed() {
		flag.Parse()
	}

	clientset := getClientset(t)
	dynamicClient := getDynamicClient(t)

	ctx := context.Background()
	solution, err := discoverSandboxingSolution(ctx, clientset, dynamicClient, *sandboxType, *sandboxRuntimeClass, t.Logf)
	if err != nil {
		if errors.Is(err, ErrNoSandboxingSolution) {
			t.Skipf("Skipping workload sandboxing test: %v. Specify -sandbox-runtime-class or -sandbox-type to run. Platforms where workload sandboxing is not supported may leave these flags unset to opt out.", err)
		}
		t.Fatalf("Failed to resolve sandboxing solution: %v", err)
	}

	t.Logf("Running workload sandboxing test using %s (runtimeClass: %q, handler: %q)",
		solution.SolutionType, solution.RuntimeClassName, solution.Handler)

	namespace := *sandboxNamespace
	cleanupNamespace := false
	if namespace == "" {
		namespace = randomNamespaceName("sandbox-conformance")
		cleanupNamespace = true
		if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("Failed to create namespace %s: %v", namespace, err)
		}
	}

	t.Cleanup(func() {
		if cleanupNamespace {
			if err := deleteNamespaceAndWait(ctx, t, clientset, namespace); err != nil {
				t.Errorf("CLEANUP FAILURE: Failed to delete namespace %s: %v", namespace, err)
			}
		}
	})

	var probePodName string
	if solution.SolutionType == "runtimeclass" {
		podName := "sandboxed-probe-pod"
		probePodName = podName
		pod := buildSandboxedPod(namespace, podName, solution.RuntimeClassName, *sandboxImage)
		t.Cleanup(func() {
			deletePolicy := metav1.DeletePropagationBackground
			_ = clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{PropagationPolicy: &deletePolicy})
		})

		t.Logf("Creating sandboxed pod %s/%s with RuntimeClass %q...", namespace, podName, solution.RuntimeClassName)
		if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatalf("Failed to create sandboxed pod %s: %v", podName, err)
		}
	} else if solution.SolutionType == "agent-sandbox" {
		sandboxName := "sandboxed-probe-cr"
		t.Cleanup(func() {
			deletePolicy := metav1.DeletePropagationBackground
			_ = dynamicClient.Resource(*solution.AgentSandboxGVR).Namespace(namespace).Delete(ctx, sandboxName, metav1.DeleteOptions{PropagationPolicy: &deletePolicy})
		})

		sandboxCR := buildAgentSandboxCR(namespace, sandboxName, *solution.AgentSandboxGVR, solution.RuntimeClassName, *sandboxImage)
		t.Logf("Creating agent-sandbox resource %s/%s...", namespace, sandboxName)
		if _, err := dynamicClient.Resource(*solution.AgentSandboxGVR).Namespace(namespace).Create(ctx, sandboxCR, metav1.CreateOptions{}); err != nil {
			t.Fatalf("Failed to create agent-sandbox resource %s: %v", sandboxName, err)
		}

		podName, err := waitForAgentSandboxPod(ctx, clientset, namespace, sandboxName, *sandboxTimeout)
		if err != nil {
			t.Fatalf("Failed to find Pod created by agent-sandbox %s: %v", sandboxName, err)
		}
		probePodName = podName
	}

	t.Logf("Waiting for probe pod %s to reach Running phase...", probePodName)
	runningPods, err := waitForPodsRunning(ctx, clientset, namespace, []string{probePodName}, *sandboxTimeout)
	if err != nil {
		pod, getErr := clientset.CoreV1().Pods(namespace).Get(ctx, probePodName, metav1.GetOptions{})
		phase := "unknown"
		if getErr == nil {
			phase = string(pod.Status.Phase)
		}
		t.Fatalf("Probe pod %s failed to reach Running phase within %v (current phase: %s): %v", probePodName, *sandboxTimeout, phase, err)
	}

	runningPod := runningPods[probePodName]
	t.Logf("Probe pod %s is Running on node %s", probePodName, runningPod.Spec.NodeName)

	rawLogs, err := fetchPodLogsWithRetry(ctx, clientset, namespace, probePodName, "prober", 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to retrieve logs from probe pod %s: %v", probePodName, err)
	}

	results := parseProbeLogs(rawLogs)

	t.Run("SchedulingAndExecution", func(t *testing.T) {
		if !results.SchedulingPassed {
			t.Fatalf("Sandboxed workload failed to schedule or execute. Raw logs:\n%s", rawLogs)
		}
		t.Logf("PASS: Sandboxed workload successfully scheduled and executed (Kernel: %s)", results.KernelInfo)
	})

	t.Run("ProcessIsolation", func(t *testing.T) {
		if !results.PidIsolationPassed {
			t.Fatalf("Process isolation check failed: host processes detected or PID namespace leaked. Raw logs:\n%s", rawLogs)
		}
		t.Logf("PASS: Process isolation verified (Visible PIDs: %d)", results.PidCount)
	})

	t.Run("KernelAndMemoryIsolation", func(t *testing.T) {
		if !results.KernelIsolationPassed {
			t.Fatalf("Kernel/memory isolation check failed: direct host memory or kernel interface access was permitted. Raw logs:\n%s", rawLogs)
		}
		t.Logf("PASS: Kernel and memory isolation boundary verified")
	})

	t.Run("FilesystemIsolation", func(t *testing.T) {
		if !results.FsIsolationPassed {
			t.Fatalf("Filesystem isolation check failed: host filesystem paths accessible. Raw logs:\n%s", rawLogs)
		}
		t.Logf("PASS: Filesystem isolation boundary verified")
	})

	t.Run("NetworkIsolation", func(t *testing.T) {
		if !results.NetIsolationPassed {
			t.Fatalf("Network isolation check failed: host network interfaces detected in sandbox. Raw logs:\n%s", rawLogs)
		}
		t.Logf("PASS: Network namespace isolation verified (Interfaces: %s)", results.Interfaces)
	})
}

// discoverSandboxingSolution identifies whether a sandboxed RuntimeClass or agent-sandbox is available.
func discoverSandboxingSolution(
	ctx context.Context,
	clientset kubernetes.Interface,
	dynamicClient dynamic.Interface,
	requestedType string,
	requestedClass string,
	logf func(string, ...any),
) (*SandboxingSolution, error) {
	switch requestedType {
	case "runtimeclass":
		return resolveRuntimeClassSolution(ctx, clientset, requestedClass, logf)
	case "agent-sandbox":
		return resolveAgentSandboxSolution(ctx, clientset, requestedClass, logf)
	case "auto":
		if requestedClass != "" {
			return resolveRuntimeClassSolution(ctx, clientset, requestedClass, logf)
		}
		if sol, err := resolveRuntimeClassSolution(ctx, clientset, "", logf); err == nil {
			return sol, nil
		}
		if sol, err := resolveAgentSandboxSolution(ctx, clientset, "", logf); err == nil {
			return sol, nil
		}
		return nil, ErrNoSandboxingSolution
	default:
		return nil, fmt.Errorf("invalid -sandbox-type %q; supported types: 'auto', 'runtimeclass', 'agent-sandbox'", requestedType)
	}
}

// resolveRuntimeClassSolution finds an appropriate RuntimeClass for sandboxing.
func resolveRuntimeClassSolution(ctx context.Context, clientset kubernetes.Interface, requestedClass string, logf func(string, ...any)) (*SandboxingSolution, error) {
	if requestedClass != "" {
		rc, err := clientset.NodeV1().RuntimeClasses().Get(ctx, requestedClass, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("specified RuntimeClass %q not found: %w", requestedClass, err)
		}
		logf("Found specified sandboxed RuntimeClass %q (handler: %s)", rc.Name, rc.Handler)
		return &SandboxingSolution{
			SolutionType:     "runtimeclass",
			RuntimeClassName: rc.Name,
			Handler:          rc.Handler,
		}, nil
	}

	rcList, err := clientset.NodeV1().RuntimeClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list RuntimeClasses: %w", err)
	}

	for _, rc := range rcList.Items {
		if isKnownSandboxedRuntime(&rc) {
			logf("Discovered sandboxed RuntimeClass %q (handler: %s)", rc.Name, rc.Handler)
			return &SandboxingSolution{
				SolutionType:     "runtimeclass",
				RuntimeClassName: rc.Name,
				Handler:          rc.Handler,
			}, nil
		}
	}

	return nil, errors.New("no known sandboxed RuntimeClass found")
}

// resolveAgentSandboxSolution checks for the kubernetes-sigs/agent-sandbox API group.
func resolveAgentSandboxSolution(ctx context.Context, clientset kubernetes.Interface, requestedClass string, logf func(string, ...any)) (*SandboxingSolution, error) {
	groups, err := clientset.Discovery().ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("failed to discover server groups: %w", err)
	}

	var foundGV *schema.GroupVersion
	for _, g := range groups.Groups {
		if g.Name == "agents.x-k8s.io" {
			if len(g.Versions) > 0 {
				foundGV = &schema.GroupVersion{Group: g.Name, Version: g.Versions[0].Version}
				break
			}
		}
	}
	if foundGV == nil {
		return nil, errors.New("API group agents.x-k8s.io not found")
	}

	gvr := schema.GroupVersionResource{
		Group:    foundGV.Group,
		Version:  foundGV.Version,
		Resource: "sandboxes",
	}

	logf("Discovered agent-sandbox API %s", gvr.String())
	return &SandboxingSolution{
		SolutionType:     "agent-sandbox",
		RuntimeClassName: requestedClass,
		AgentSandboxGVR:  &gvr,
	}, nil
}

// isKnownSandboxedRuntime checks whether a RuntimeClass corresponds to known sandboxed runtimes.
func isKnownSandboxedRuntime(rc *nodev1.RuntimeClass) bool {
	if rc == nil {
		return false
	}
	name := strings.ToLower(rc.Name)
	handler := strings.ToLower(rc.Handler)

	knownKeywords := []string{
		"gvisor",
		"runsc",
		"kata",
		"sandboxed",
		"quark",
		"crun-krun",
		"krun",
	}

	for _, kw := range knownKeywords {
		if strings.Contains(name, kw) || strings.Contains(handler, kw) {
			return true
		}
	}
	return false
}

// buildSandboxedPod constructs a Pod with the specified runtime class and isolation prober.
func buildSandboxedPod(ns, name, runtimeClassName, image string) *corev1.Pod {
	var rcName *string
	if runtimeClassName != "" {
		rcName = &runtimeClassName
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"ai-conformance.kubernetes.io/test": "workload-sandboxing",
			},
		},
		Spec: corev1.PodSpec{
			RuntimeClassName: rcName,
			RestartPolicy:    corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "prober",
					Image:   image,
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{sandboxProbeScript() + "\nsleep 3600"},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("50m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("200m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
	}
}

// buildAgentSandboxCR creates an unstructured Sandbox resource for kubernetes-sigs/agent-sandbox.
func buildAgentSandboxCR(ns, name string, gvr schema.GroupVersionResource, runtimeClassName, image string) *unstructured.Unstructured {
	podSpec := map[string]interface{}{
		"restartPolicy": "Never",
		"containers": []interface{}{
			map[string]interface{}{
				"name":    "prober",
				"image":   image,
				"command": []interface{}{"/bin/sh", "-c"},
				"args":    []interface{}{sandboxProbeScript() + "\nsleep 3600"},
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"cpu":    "50m",
						"memory": "64Mi",
					},
					"limits": map[string]interface{}{
						"cpu":    "200m",
						"memory": "128Mi",
					},
				},
			},
		},
	}
	if runtimeClassName != "" {
		podSpec["runtimeClassName"] = runtimeClassName
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": gvr.Group + "/" + gvr.Version,
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": ns,
				"labels": map[string]interface{}{
					"ai-conformance.kubernetes.io/test": "workload-sandboxing",
				},
			},
			"spec": map[string]interface{}{
				"podTemplate": map[string]interface{}{
					"spec": podSpec,
				},
			},
		},
	}
}

// waitForAgentSandboxPod locates the Pod spawned by an agent-sandbox CR.
func waitForAgentSandboxPod(ctx context.Context, c kubernetes.Interface, ns, sandboxName string, timeout time.Duration) (string, error) {
	var podName string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pods, err := c.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, p := range pods.Items {
			for _, owner := range p.OwnerReferences {
				if owner.Kind == "Sandbox" && owner.Name == sandboxName {
					podName = p.Name
					return true, nil
				}
			}
			if p.Labels["agents.x-k8s.io/sandbox-name"] == sandboxName || strings.HasPrefix(p.Name, sandboxName) {
				podName = p.Name
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("timed out waiting for Pod created by Sandbox %s: %w", sandboxName, err)
	}
	return podName, nil
}

// fetchPodLogsWithRetry reads logs from the prober container until the completion marker is observed.
func fetchPodLogsWithRetry(ctx context.Context, c kubernetes.Interface, ns, podName, containerName string, timeout time.Duration) (string, error) {
	var logs string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		rawLogs, err := c.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{Container: containerName}).DoRaw(ctx)
		if err != nil {
			return false, nil
		}
		logs = string(rawLogs)
		if strings.Contains(logs, "SANDBOX_PROBE: COMPLETED") {
			return true, nil
		}
		return false, nil
	})
	if err != nil && logs == "" {
		return "", fmt.Errorf("failed to retrieve complete probe logs for pod %s: %w", podName, err)
	}
	return logs, nil
}

// parseProbeLogs parses the output of the sandbox probe script into structured results.
func parseProbeLogs(logs string) *SandboxProbeResults {
	res := &SandboxProbeResults{RawLogs: logs}
	for _, line := range strings.Split(logs, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "SANDBOX_PROBE: SCHEDULING=PASS":
			res.SchedulingPassed = true
		case line == "SANDBOX_PROBE: PID_ISOLATION=PASS":
			res.PidIsolationPassed = true
		case line == "SANDBOX_PROBE: KERNEL_ISOLATION=PASS":
			res.KernelIsolationPassed = true
		case line == "SANDBOX_PROBE: FS_ISOLATION=PASS":
			res.FsIsolationPassed = true
		case line == "SANDBOX_PROBE: NET_ISOLATION=PASS":
			res.NetIsolationPassed = true
		case line == "SANDBOX_PROBE: COMPLETED":
			res.ProbeCompleted = true
		case strings.HasPrefix(line, "SANDBOX_INFO: KERNEL="):
			res.KernelInfo = strings.TrimPrefix(line, "SANDBOX_INFO: KERNEL=")
		case strings.HasPrefix(line, "SANDBOX_INFO: INTERFACES="):
			res.Interfaces = strings.TrimSpace(strings.TrimPrefix(line, "SANDBOX_INFO: INTERFACES="))
		case strings.HasPrefix(line, "SANDBOX_INFO: PID_COUNT="):
			if count, err := strconv.Atoi(strings.TrimPrefix(line, "SANDBOX_INFO: PID_COUNT=")); err == nil {
				res.PidCount = count
			}
		}
	}
	return res
}

// sandboxProbeScript returns the shell script executed inside the sandboxed container to probe isolation.
func sandboxProbeScript() string {
	return `count=0
echo "SANDBOX_PROBE: SCHEDULING=PASS"
uname_info=$(uname -a 2>/dev/null || cat /proc/version 2>/dev/null || echo "unknown")
echo "SANDBOX_INFO: KERNEL=$uname_info"

# 1. PID Isolation: check visible process count and ensure no host daemons
pid_pass=1
pid_count=0
if [ -d /proc ]; then
  pid_count=$(ls -d /proc/[0-9]* 2>/dev/null | wc -l)
  for p in /proc/[0-9]*/cmdline /proc/[0-9]*/comm; do
    [ -f "$p" ] || continue
    if grep -q -E "(kubelet|containerd|dockerd|systemd-journald|crio)" "$p" 2>/dev/null; then
      pid_pass=0
      break
    fi
  done
fi
echo "SANDBOX_INFO: PID_COUNT=$pid_count"
if [ "$pid_pass" -eq 1 ]; then
  echo "SANDBOX_PROBE: PID_ISOLATION=PASS"
else
  echo "SANDBOX_PROBE: PID_ISOLATION=FAIL"
fi

# 2. Kernel & Memory Isolation: check raw memory device access
kernel_pass=1
if head -c 1 /dev/mem 2>/dev/null; then
  kernel_pass=0
fi
if head -c 1 /dev/kmem 2>/dev/null; then
  kernel_pass=0
fi
if [ "$kernel_pass" -eq 1 ]; then
  echo "SANDBOX_PROBE: KERNEL_ISOLATION=PASS"
else
  echo "SANDBOX_PROBE: KERNEL_ISOLATION=FAIL"
fi

# 3. Filesystem Isolation: ensure host paths are not mounted
fs_pass=1
for host_path in /etc/kubernetes /var/lib/kubelet /var/log/pods; do
  if [ -d "$host_path" ]; then
    fs_pass=0
  fi
done
if [ "$fs_pass" -eq 1 ]; then
  echo "SANDBOX_PROBE: FS_ISOLATION=PASS"
else
  echo "SANDBOX_PROBE: FS_ISOLATION=FAIL"
fi

# 4. Network Isolation: ensure host interfaces/bridges are not present
net_pass=1
iface_list=""
if [ -d /sys/class/net ]; then
  for iface in /sys/class/net/*; do
    [ -e "$iface" ] || continue
    b=$(basename "$iface")
    iface_list="$iface_list $b"
    case "$b" in
      docker0|cbr0|flannel*|cni0|br-*|bond*|dummy*) net_pass=0 ;;
    esac
  done
fi
echo "SANDBOX_INFO: INTERFACES=$iface_list"
if [ "$net_pass" -eq 1 ]; then
  echo "SANDBOX_PROBE: NET_ISOLATION=PASS"
else
  echo "SANDBOX_PROBE: NET_ISOLATION=FAIL"
fi

echo "SANDBOX_PROBE: COMPLETED"
`
}

// getDynamicClient creates a dynamic Kubernetes client using the kubeconfig flag.
func getDynamicClient(t *testing.T) dynamic.Interface {
	t.Helper()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if *kubeconfig != "" {
		loadingRules.ExplicitPath = *kubeconfig
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Fatalf("Error building kubeconfig: %v", err)
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatalf("Error creating dynamic kubernetes client: %v", err)
	}
	return dynamicClient
}
