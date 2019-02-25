VERSION ?= $(shell cat ./VERSION)
GITHASH ?= $(shell git describe --match nEvErMatch --always --abbrev=10 --dirty)
NAME=bin/hs-csi-plugin

compile:
	@echo "==> Building the Hammerspace CSI Driver Version ${VERSION}"
	@env vgo get -d ./
	@env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 vgo build -ldflags "-X 'github.com/hammer-space/hammerspace-csi-plugin/pkg/common.Version=${VERSION}' -X 'github.com/hammer-space/hammerspace-csi-plugin/pkg/common.Githash=${GITHASH}'" -o ${NAME} ./

clean:
	@echo "==> Cleaning"
	@env vgo clean
	rm -rf bin go.sum

unittest:
	@echo "==> Running tests"
	@env vgo test -v -count 1 -run="[^TestSanity]" ./...

sanity:
	@echo "==> Running sanity functional tests"
	@env vgo test -timeout=0 -v -run="TestSanity" ./...

build:
	@echo "==> Building Docker Image Latest"
	@docker build -t "hs-csi-plugin:latest" . -f Dockerfile

build-release:
	@echo "==> Building Docker Image ${VERSION}"
	@docker build --build-arg version=${VERSION} -t "hs-csi-plugin:${VERSION}" . -f Dockerfile
