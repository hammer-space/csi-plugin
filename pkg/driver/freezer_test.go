package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDriverNamespace(t *testing.T) {
	for _, tc := range []struct {
		name, env, file, want string
	}{
		{"downward API wins", "hammerspace-csi", "other-namespace\n", "hammerspace-csi"},
		{"service account", "", "hammerspace-csi\n", "hammerspace-csi"},
		{"manual deployment", "", "kube-system\n", "kube-system"},
		{"missing namespace sources", "", "", "kube-system"},
		{"empty namespace file", "", "\n", "kube-system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POD_NAMESPACE", tc.env)
			path := filepath.Join(t.TempDir(), "namespace")
			if tc.file != "" {
				if err := os.WriteFile(path, []byte(tc.file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := driverNamespace(path); got != tc.want {
				t.Fatalf("namespace = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFreezerFindsNodeInDriverNamespace(t *testing.T) {
	for _, namespace := range []string{"hammerspace-csi", "kube-system"} {
		t.Run(namespace, func(t *testing.T) {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "pv-example"},
				Spec: corev1.PersistentVolumeSpec{
					ClaimRef: &corev1.ObjectReference{Namespace: "application", Name: "data"},
					PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
						Driver: "com.hammerspace.csi", VolumeHandle: "volume-example",
					}},
				},
			}
			app := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "application", UID: "application-uid"},
				Spec: corev1.PodSpec{NodeName: "worker-1", Volumes: []corev1.Volume{{
					Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
				}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}
			node := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "csi-node-example", Namespace: namespace, Labels: map[string]string{"app": "csi-node"}},
				Spec:       corev1.PodSpec{NodeName: "worker-1"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			}
			// An unrelated installation must never be selected for privileged exec.
			other := node.DeepCopy()
			other.Namespace = "other-installation"
			client := fake.NewSimpleClientset(pv, app, node, other)
			freezer := &Freezer{clientset: client, namespace: namespace}
			targets, err := freezer.findMountsForVolumeHandle(context.Background(), "volume-example")
			if err != nil {
				t.Fatal(err)
			}
			if len(targets) != 1 {
				t.Fatalf("found %d freeze targets, want 1", len(targets))
			}
			target := targets[0]
			if target.Namespace != namespace || target.PodName != node.Name || target.Container != "hs-csi-plugin-node" ||
				target.MountPath != "/var/lib/kubelet/pods/application-uid/volumes/kubernetes.io~csi/pv-example/mount" {
				t.Fatalf("incorrect freeze target: %+v", target)
			}
		})
	}
}
