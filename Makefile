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

# Chaos drills (hack/chaos/*.sh) — each is a self-contained, reproducible
# failure injection against whatever cluster your kubeconfig currently
# points at. See docs/postmortems/ for a real incident from one of these.
.PHONY: chaos-kill-leader
chaos-kill-leader:
	bash hack/chaos/kill-leader.sh

.PHONY: chaos-drain-node
chaos-drain-node:
	bash hack/chaos/drain-node.sh $(NODE)

.PHONY: chaos-delete-pods
chaos-delete-pods:
	bash hack/chaos/delete-pods.sh

.PHONY: chaos-kill-nats
chaos-kill-nats:
	bash hack/chaos/kill-nats.sh $(URL)

.PHONY: chaos-exhaust-quota
chaos-exhaust-quota:
	bash hack/chaos/exhaust-quota.sh

.PHONY: chaos-duplicate-submit
chaos-duplicate-submit:
	bash hack/chaos/duplicate-submit.sh $(URL)

.PHONY: chaos-corrupt-checkpoint
chaos-corrupt-checkpoint:
	bash hack/chaos/corrupt-checkpoint.sh

.PHONY: chaos-unavailable-accelerator
chaos-unavailable-accelerator:
	bash hack/chaos/unavailable-accelerator.sh
