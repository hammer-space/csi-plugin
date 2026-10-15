.PHONY: build test image generate check-generated
.DEFAULT_GOAL := build

IMAGE ?= hammerspace-csi-operator:dev
VERSION ?= $(shell cat VERSION)
RELEASE ?= 1

generate:
	go run ./cmd/generate-operands

check-generated:
	go run ./cmd/generate-operands -check

build: generate
	CGO_ENABLED=0 go build -trimpath -o bin/hammerspace-csi-operator ./cmd

test: check-generated
	go test ./... -count=1
	python3 -m unittest discover -s hack -p 'test_*.py'

image:
	docker build -f Dockerfile --build-arg VERSION=$(VERSION) --build-arg RELEASE=$(RELEASE) -t $(IMAGE) ..
