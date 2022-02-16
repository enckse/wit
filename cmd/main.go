package main

import (
	"flag"
	"net/http"
	"strings"

	"github.com/enckse/basic"
	"github.com/enckse/wit/serve"
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
	home := flag.String("home", "", "url to display as a 'home' link")
	opModes := flag.String("opmodes", "COOL74,HEAT72", "operation modes (comma separated list)")
	flag.Parse()
	cfg := serve.NewConfig(*config, *lib, *device, *irSend, version, *home, strings.Split(*opModes, ","))
	mux := http.NewServeMux()
	if err := cfg.SetupServer(mux); err != nil {
		basic.Die("failed to setup server", err)
	}
	srv := &http.Server{
		Addr:    *binding,
		Handler: mux,
	}
	if err := srv.ListenAndServe(); err != nil {
		basic.LogError("listen and serve failed", err)
	}
}
