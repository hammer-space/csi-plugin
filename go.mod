module github.com/hammer-space/csi-plugin

go 1.24.0

toolchain go1.24.5

require (
	github.com/ameade/spec v0.3.0 // - Apache 2.0 license
	github.com/container-storage-interface/spec v1.9.0 // - Apache 2.0 license
	github.com/google/uuid v1.6.0
	github.com/jpillora/backoff v0.0.0-20180909062703-3050d21c67d7 // - MIT license
	github.com/kubernetes-csi/csi-test v2.2.0+incompatible
	github.com/onsi/ginkgo v1.10.3 // - MIT license
	github.com/onsi/gomega v1.35.1 //  - MIT license
	github.com/sirupsen/logrus v1.9.3 // - MIT license
	go.opentelemetry.io/otel v1.33.0
	go.opentelemetry.io/otel/sdk v1.33.0
	go.opentelemetry.io/otel/trace v1.33.0
	golang.org/x/net v0.38.0
	golang.org/x/sys v0.31.0
	google.golang.org/grpc v1.68.1
	google.golang.org/protobuf v1.36.5
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/kubernetes v1.33.3
	k8s.io/mount-utils v0.27.4
)

require (
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/hpcloud/tail v1.0.0 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel/metric v1.33.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20241209162323-e6fa225c2576 // indirect
	gopkg.in/fsnotify.v1 v1.4.7 // indirect
	gopkg.in/tomb.v1 v1.0.0-20141024135613-dd632973f1e7 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/utils v0.0.0-20241104100929-3ea5e8cea738 // indirect
)
