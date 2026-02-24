.PHONY: all build build-mcp build-dashboard test lint docker-build generate helm-lint clean

all: clean generate build build-mcp build-dashboard test

build:
	go build -o bin/koptimizer ./cmd/optimizer

build-mcp:
	go build -o bin/koptimizer-mcp ./cmd/mcp

build-dashboard:
	go build -o bin/koptimizer-dash ./cmd/dashboard

test:
	go test ./... -v -cover

lint:
	golangci-lint run

docker-build:
	docker build -t koptimizer:latest .

generate:
	controller-gen object paths="./api/..." output:dir=./api/v1alpha1
	controller-gen crd:allowDangerousTypes=true paths="./api/..." output:crd:dir=./deploy/helm/koptimizer/templates/crds

helm-lint:
	helm lint deploy/helm/koptimizer

clean:
	rm -rf bin/
