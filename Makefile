VERSION := development
DESTDIR :=
BUILD   := bin/
TARGET  := $(BUILD)wit

all: $(TARGET)

$(TARGET): cmd/$@/* go.*
	go build -ldflags '-X main.version=$(VERSION)' -trimpath -buildmode=pie -mod=readonly -modcacherw -o $(TARGET) cmd/main.go

clean:
	rm -rf $(BUILD)

install:
	install -Dm755 $(TARGET) $(DESTDIR)bin/wit
	mkdir -p $(DESTDIR)share/wit
	install -Dm644 bryant.conf $(DESTDIR)share/wit/lirc.bryant.conf
	install -Dm644 wit.json $(DESTDIR)etc/wit.json
