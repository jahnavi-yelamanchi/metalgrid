CLUSTER := metalgrid

.PHONY: kind-up
kind-up:
	kind get clusters | grep -qx $(CLUSTER) || kind create cluster --config hack/kind-config.yaml
	kubectl cluster-info --context kind-$(CLUSTER)

.PHONY: kind-down
kind-down:
	kind delete cluster --name $(CLUSTER)

.PHONY: dev-up
dev-up:
	docker compose up -d

.PHONY: dev-down
dev-down:
	docker compose down

.PHONY: build
build:
	go build ./...

.PHONY: test
test:
	go test ./...

.PHONY: lint
lint:
	go vet ./...

.PHONY: run
run:
	go run ./cmd/apiserver
