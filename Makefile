# Makefile for building and deploying with Ko

# Container registry configuration
# Override with: make KO_DOCKER_REPO=your-registry.io/your-org build
#KO_DOCKER_REPO ?= ko.local
KO_DOCKER_REPO ?= quay.io/pamvdam

# Export for Ko
export KO_DOCKER_REPO

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: install-ko
install-ko: ## Install Ko if not present
	@which ko > /dev/null || (echo "Installing Ko..." && go install github.com/google/ko@latest)

.PHONY: deps
deps: ## Download Go dependencies
	go mod download
	go mod tidy

.PHONY: build
build: install-ko deps ## Build multi-arch container image with Ko
	@echo "Building for linux/amd64 and linux/arm64..."
	ko build --platform=linux/amd64,linux/arm64 --bare .

.PHONY: build-local
build-local: install-ko deps ## Build for local architecture only
	@echo "Building for local architecture..."
	ko build --bare .

.PHONY: deploy
deploy: install-ko ## Deploy to Kubernetes cluster
	@echo "Deploying to Kubernetes..."
	ko apply -f deployment.yaml

.PHONY: deploy-watch
deploy-watch: install-ko ## Deploy and watch for changes
	@echo "Deploying with file watch..."
	ko apply -f deployment.yaml --watch

.PHONY: resolve
resolve: install-ko ## Resolve Ko references and print YAML
	ko resolve -f deployment.yaml

.PHONY: delete
delete: ## Delete from Kubernetes cluster
	kubectl delete -f deployment.yaml

.PHONY: port-forward
port-forward: ## Port forward to access the dashboard
	kubectl port-forward svc/pod-monitor 8090:80

.PHONY: logs
logs: ## Show pod logs
	kubectl logs -l app=pod-monitor -f

.PHONY: test-local
test-local: ## Run the application locally
	PORT=8090 go run main.go

.PHONY: clean
clean: ## Clean build artifacts
	go clean
	rm -f pod-monitor

# Example deployment commands
.PHONY: deploy-to-dockerhub
deploy-to-dockerhub: ## Deploy to Docker Hub (requires docker login)
	KO_DOCKER_REPO=docker.io/yourusername ko apply -f deployment.yaml

.PHONY: deploy-to-gcr
deploy-to-gcr: ## Deploy to Google Container Registry
	KO_DOCKER_REPO=gcr.io/your-project ko apply -f deployment.yaml

.PHONY: deploy-to-ecr
deploy-to-ecr: ## Deploy to AWS ECR
	KO_DOCKER_REPO=123456789012.dkr.ecr.us-east-1.amazonaws.com ko apply -f deployment.yaml
