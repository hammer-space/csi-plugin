VERSION ?= $(shell cat ./VERSION)
# Overridable from the environment for builds without a .git directory;
# otherwise evaluated once rather than on every use.
ifeq ($(origin GITHASH),undefined)
GITHASH := $(shell git describe --match nEvErMatch --always --abbrev=10 --dirty)
endif
RELEASE ?= 1
NAME=bin/hs-csi-plugin
RUNTIME_BASE ?= runtime
IMAGE ?= hammerspaceinc/csi-plugin:latest
BUILD_FLAGS ?=
.PHONY: compile clean unittest sanity build-dev build build-release test-image verify-certification-image

compile:
	@echo "==> Building the Hammerspace CSI Driver Version ${VERSION}"
	@go mod download
	@env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags "-X 'github.com/hammer-space/csi-plugin/pkg/common.Version=${VERSION}' -X 'github.com/hammer-space/csi-plugin/pkg/common.Githash=${GITHASH}'" -o ${NAME} ./

clean:
	@echo "==> Cleaning"
	@env go clean
	rm -rf bin

unittest:
	@echo "==> Running tests"
	@env go test -v -count 1 . ./pkg/...

sanity:
	@echo "==> Running sanity functional tests"
	@go test -timeout=0 -v ./test/sanity/...

build-dev:
	@echo "==> Building Docker Image for Dev Image"
	@docker build $(BUILD_FLAGS) -t "hammerspaceinc/csi-plugin-dev:latest" . -f Dockerfile_dev --no-cache


build:
	@echo "==> Building Docker Image $(IMAGE)"
	@docker build $(BUILD_FLAGS) --build-arg RUNTIME_BASE="$(RUNTIME_BASE)" --build-arg version="$(VERSION)" --build-arg release="$(RELEASE)" --build-arg githash="$(GITHASH)" -t "$(IMAGE)" . -f Dockerfile

# Release images always rebuild from a freshly pulled base: a cached layer would
# skip the package upgrade and ship RPMs with fixes already published.
build-release:
	@echo "==> Building Docker Image ${VERSION} ${GITHASH}"
	@docker build --no-cache --pull $(BUILD_FLAGS) --build-arg RUNTIME_BASE="$(RUNTIME_BASE)" --build-arg version="$(VERSION)" --build-arg release="$(RELEASE)" --build-arg githash="$(GITHASH)" -t "hammerspaceinc/csi-plugin:${VERSION}" . -f Dockerfile
	@hack/verify_certification_image.sh "hammerspaceinc/csi-plugin:${VERSION}"

test-image:
	@hack/test_runtime_image.sh "$(IMAGE)"

verify-certification-image:
	@hack/verify_certification_image.sh "$(IMAGE)"
