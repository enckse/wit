BIN := bin/
WEB := $(BIN)wit

all: $(WEB)

$(WEB): $(shell find . -type f -name "*.go")
	go build -o $@ cmd/main.go
