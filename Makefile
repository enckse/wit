BIN   := bin/
WEB   := $(BIN)wit
FLAGS := -ldflags "-X main.version=$(shell git log -n 1 --format=%h)" -trimpath -buildmode=pie -mod=readonly -modcacherw

all: $(WEB)

$(WEB): $(shell find . -type f -name "*.go")
	go build $(FLAGS) -o $@ cmd/main.go

clean:
	rm -f $(WEB)
