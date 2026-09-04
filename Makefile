NAME ?= rsync-proxy
VERSION ?= $(shell git describe --tags || echo "unknown")
BUILD_DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
GIT_COMMIT := $(shell git rev-parse HEAD)

GO_LDFLAGS = '-X "github.com/ustclug/rsync-proxy/cmd.Version=$(VERSION)" \
	-X "github.com/ustclug/rsync-proxy/cmd.BuildDate=$(BUILD_DATE)" \
	-X "github.com/ustclug/rsync-proxy/cmd.GitCommit=$(GIT_COMMIT)" \
	-w -s'
GOBUILD = CGO_ENABLED=0 go build -trimpath -ldflags $(GO_LDFLAGS)

OUTDIR := build
PLATFORM_LIST = darwin-amd64 linux-amd64

all: $(PLATFORM_LIST)

darwin-amd64:
	GOARCH=amd64 GOOS=darwin $(GOBUILD) -o $(OUTDIR)/$(NAME)-$(VERSION)-$@/$(NAME)
	cp -r assets/* README.md $(OUTDIR)/$(NAME)-$(VERSION)-$@/

linux-amd64:
	GOARCH=amd64 GOOS=linux $(GOBUILD) -o $(OUTDIR)/$(NAME)-$(VERSION)-$@/$(NAME)
	cp -r assets/* README.md $(OUTDIR)/$(NAME)-$(VERSION)-$@/

gz_releases=$(addsuffix .tar.gz, $(PLATFORM_LIST))

$(gz_releases): %.tar.gz : %
	tar czf $(OUTDIR)/$(NAME)-$(VERSION)-$@ -C $(OUTDIR)/ $(NAME)-$(VERSION)-$</

releases: $(gz_releases)

clean:
	rm -rf $(OUTDIR)/

# --- rsync-gateway (K8s controller + embedded data plane) ---

GATEWAY_IMG ?= ghcr.io/zjusct/rsync-gateway
CONTROLLER_TOOLS_VERSION ?= v0.22.0

.PHONY: lint
lint:
	golangci-lint run

.PHONY: gateway
gateway:
	go build -o bin/rsync-gateway ./cmd/rsync-gateway

.PHONY: gateway-test
gateway-test:
	go test ./api/... ./internal/... ./cmd/...

# Regenerates the CRDs into the Helm chart. The directory must be named
# exactly "crds/" (a hard Helm convention: files there are installed by
# `helm install` but not templated). RBAC is NOT generated here anymore:
# charts/rsync-gateway/templates/clusterrole.yaml is hand-maintained and
# must be kept in sync with the //+kubebuilder:rbac markers in the Go code.
.PHONY: gateway-manifests
gateway-manifests:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION) \
		crd paths=./api/... \
		output:crd:artifacts:config=charts/rsync-gateway/crds

# Lints the Helm chart (strict mode) and checks that it renders.
.PHONY: gateway-chart-lint
gateway-chart-lint:
	helm lint --strict charts/rsync-gateway
	helm template charts/rsync-gateway >/dev/null

.PHONY: gateway-docker
gateway-docker:
	docker build -f Dockerfile.gateway -t $(GATEWAY_IMG) .
