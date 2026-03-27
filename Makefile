APP_NAME := taghound
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -s -w -X main.Version=$(VERSION)
GOFLAGS := -buildvcs=false
OUT_DIR  := dist

.PHONY: all clean build test darwin-arm64 darwin-amd64 linux-amd64 windows-amd64

all: clean build

build: darwin-arm64 darwin-amd64 linux-amd64 windows-amd64
	@echo "\n✅ Binaries built in $(OUT_DIR)/"
	@ls -lh $(OUT_DIR)/

darwin-arm64:
	@echo "🔨 Building macOS arm64..."
	@mkdir -p $(OUT_DIR)
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUT_DIR)/$(APP_NAME)-darwin-arm64 .

darwin-amd64:
	@echo "🔨 Building macOS amd64..."
	@mkdir -p $(OUT_DIR)
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUT_DIR)/$(APP_NAME)-darwin-amd64 .

linux-amd64:
	@echo "🔨 Building Linux amd64..."
	@mkdir -p $(OUT_DIR)
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUT_DIR)/$(APP_NAME)-linux-amd64 .

windows-amd64:
	@echo "🔨 Building Windows amd64..."
	@mkdir -p $(OUT_DIR)
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUT_DIR)/$(APP_NAME)-windows-amd64.exe .

test:
	@go test -v -race ./...

clean:
	@rm -rf $(OUT_DIR)

run:
	@go run -ldflags "$(LDFLAGS)" .

install:
	@echo "🔧 Installing $(APP_NAME) to /usr/local/bin/..."
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o /usr/local/bin/$(APP_NAME) .
	@echo "✅ Installed. Run '$(APP_NAME)' from any Git repo."

uninstall:
	@rm -f /usr/local/bin/$(APP_NAME)
	@echo "🗑️  $(APP_NAME) uninstalled."
