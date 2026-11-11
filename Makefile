STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@v0.8.2
BIN := bin/rex-tg
INSTALL_DIR ?= $(HOME)/.local/bin

.PHONY: build test check fmt install e2e clean

build:
	go build -o $(BIN) ./cmd/rex-tg

test:
	go test ./...

check:
	@test -z "$$(gofmt -l . | grep -v '^vendor/')" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	$(STATICCHECK) ./...
	go test -race ./...

fmt:
	gofmt -w .

install: build
	install -d $(INSTALL_DIR)
	install -m 0755 $(BIN) $(INSTALL_DIR)/rex-tg

e2e:
	go test -tags e2e -count=1 ./e2e/...

clean:
	rm -rf bin
