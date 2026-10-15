package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hammer-space/csi-plugin/operator/internal/operator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

func main() {
	images := operator.Images{}
	for _, key := range operator.ImageKeys {
		images[key] = os.Getenv("RELATED_IMAGE_" + strings.ToUpper(key))
		if images[key] == "" {
			log.Fatalf("RELATED_IMAGE_%s must be set", strings.ToUpper(key))
		}
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	config.Timeout = 30 * time.Second
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}
	id := os.Getenv("POD_NAME")
	namespace := os.Getenv("POD_NAMESPACE")
	if id == "" || namespace == "" {
		log.Fatal("POD_NAME and POD_NAMESPACE must be set")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: ":8081", ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	defer server.Close()
	// OLM installs the operator into the namespace the user picks (OwnNamespace);
	// the driver is deployed alongside it.
	reconciler := &operator.Reconciler{Client: client, Images: images, Namespace: namespace}
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:          &resourcelock.LeaseLock{LeaseMeta: metav1.ObjectMeta{Name: "hammerspace-csi-operator", Namespace: namespace}, Client: kube.CoordinationV1(), LockConfig: resourcelock.ResourceLockConfig{Identity: id}},
		LeaseDuration: 30 * time.Second, RenewDeadline: 20 * time.Second, RetryPeriod: 5 * time.Second,
		// Do not release until process exit: a callback may still be finishing an API call.
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()
				for {
					if err := reconciler.Reconcile(ctx); err != nil {
						log.Printf("reconcile failed: %v", err)
					}
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			},
			OnStoppedLeading: func() {
				if ctx.Err() == nil {
					log.Fatal("leader lease lost")
				}
			},
		},
	})
}
