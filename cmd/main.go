package main

import (
	"flag"
	"net/http"
	"strings"

	"voidedtech.com/stock"
	"voidedtech.com/wit/serve"
)

var (
	version = "development"
)

func main() {
	binding := flag.String("binding", ":7801", "http binding")
	config := flag.String("lirccfg", "BRYANT", "lirc config")
	lib := flag.String("cache", "/var/lib/wit", "cache directory")
	device := flag.String("device", "/run/lirc/lircd", "lircd device")
	irSend := flag.String("irsend", "/usr/bin/irsend", "irsend executable")
	opModes := flag.String("opmodes", "COOL74,HEAT72", "operation modes (comma separated list)")
	flag.Parse()
	cfg := serve.NewConfig(*config, *lib, *device, *irSend, version, strings.Split(*opModes, ","))
	mux := http.NewServeMux()
	if err := cfg.SetupServer(mux); err != nil {
		stock.Die("failed to setup server", err)
	}
	srv := &http.Server{
		Addr:    *binding,
		Handler: mux,
	}
	if err := srv.ListenAndServe(); err != nil {
		stock.LogError("listen and serve failed", err)
	}
}
