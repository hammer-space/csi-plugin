VERSION ?= $(shell cat ./VERSION)
GITHASH ?= $(shell git describe --match nEvErMatch --always --abbrev=10 --dirty)
NAME=bin/hs-csi-plugin

compile:
	@echo "==> Building the Hammerspace CSI Driver Version ${VERSION}"
	@env GO111MODULE=on go get -d ./
	@env GO111MODULE=on GO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X 'github.com/hammer-space/csi-plugin/pkg/common.Version=${VERSION}' -X 'github.com/hammer-space/csi-plugin/pkg/common.Githash=${GITHASH}'" -o ${NAME} ./

clean:
	@echo "==> Cleaning"
	@env go clean
	rm -rf bin go.sum

unittest:
	@echo "==> Running tests"
	@env go test -v -count 1 -run="[^TestSanity]" ./...

sanity:
	@echo "==> Running sanity functional tests"
	@env go test -timeout=0 -v -run="TestSanity" ./...

build-dev:
	@echo "==> Building Docker Image for Dev Image"
	@docker build -t "hammerspaceinc/csi-plugin-dev:latest" . -f Dockerfile_dev --no-cache

build:
	@echo "==> Building Docker Image Latest"
	@docker build -t "hammerspaceinc/csi-plugin:latest" . -f Dockerfile --no-cache

build-release:
	@echo "==> Building Docker Image ${VERSION} ${GITHASH}"
	@docker build --build-arg version=${VERSION} -t "hammerspaceinc/csi-plugin:${VERSION}" . -f Dockerfile

