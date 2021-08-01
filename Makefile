BIN := bin/
WEB := $(BIN)webtherm

all: $(WEB)

$(WEB): $(shell find cmd/ -type f)
	go build -o $@ cmd/main.go
