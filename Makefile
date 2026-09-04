VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
MODEL    ?= base

.PHONY: build test fmt vet lint run serve model docker docker-cuda up up-cuda down clean

build: ## compila ./bin/vozgo
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/vozgo ./cmd/vozgo

test:
	go test ./...

fmt:
	gofmt -w cmd internal web

vet:
	go vet ./...

check: fmt vet test

model: ## descarga el modelo ggml (MODEL=base|small|medium|large-v3-turbo)
	./scripts/download-model.sh $(MODEL)

serve: build ## servidor local en :8080
	./bin/vozgo serve -model models/ggml-$(MODEL).bin

docker: ## imagen CPU
	docker build --target cpu --build-arg VERSION=$(VERSION) -t vozgo:cpu .

docker-cuda: ## imagen CUDA (CUDA_ARCH=75 para GTX 16xx/RTX 20xx)
	docker build --target cuda --build-arg VERSION=$(VERSION) \
		--build-arg CUDA_ARCH=$(or $(CUDA_ARCH),75) \
		--build-arg CUDA_BUILD_JOBS=$(or $(CUDA_BUILD_JOBS),2) -t vozgo:cuda .

up:
	docker compose --profile cpu up --build

up-cuda:
	docker compose --profile cuda up --build

down:
	docker compose --profile cpu --profile cuda --profile cli down

clean:
	rm -rf bin
