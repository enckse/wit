BIN := bin/
WEB := $(BIN)wit

all: $(WEB)

$(WEB): $(shell find cmd/ -type f)
	go build -o $@ cmd/main.go
