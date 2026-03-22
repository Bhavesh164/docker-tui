APP := docker-tui
DIST := dist
GO ?= go
CGO_ENABLED ?= 0
BUILD_FLAGS ?= -trimpath
LDFLAGS ?= -s -w

.PHONY: build run tidy test clean release release-darwin-arm64 release-darwin-amd64 release-linux-amd64 release-linux-arm64

build:
	$(GO) build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o $(APP) .

run: build
	./$(APP)

tidy:
	$(GO) mod tidy

test:
	$(GO) test ./...

clean:
	rm -rf $(APP) $(DIST)

release: clean release-darwin-arm64 release-darwin-amd64 release-linux-amd64 release-linux-arm64
	@echo "Release binaries created in $(DIST)/"

release-darwin-arm64:
	mkdir -p $(DIST)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=darwin GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(APP)_darwin_arm64 .

release-darwin-amd64:
	mkdir -p $(DIST)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=darwin GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(APP)_darwin_amd64 .

release-linux-amd64:
	mkdir -p $(DIST)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(APP)_linux_amd64 .

release-linux-arm64:
	mkdir -p $(DIST)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(APP)_linux_arm64 .
