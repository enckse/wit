package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	version = "development"
	lock    = &sync.Mutex{}
	//go:embed template.html
	templateHTML string
)

const (
	onAction    = "on"
	offAction   = "off"
	noAction    = ""
	isDisplay   = "display"
	endpoint    = "/wit/"
	weekdayType = "weekday"
	weekendType = "weekend"
)

type (
	// Result is how html results are shown.
	Result struct {
		Error          string
		Running        string
		System         string
		Time           string
		Manual         string
		Override       string
		Schedule       string
		Build          string
		OperationModes []string
		HasReturn      bool
		ReturnTo       string
	}
	scheduleTime struct {
		hour   int
		min    int
		action string
	}
	context struct {
		cfg           Config
		version       string
		stateFile     string
		pageTemplate  *template.Template
		errorTemplate *template.Template
	}
	// Config handles wit configuration.
	Config struct {
		lircName  string
		cache     string
		device    string
		irSend    string
		version   string
		opModes   []string
		returnURL string
	}

	// State represents on the current system state to persist to disk.
	State struct {
		OpMode   string
		Schedule string
		Manual   bool
		Override bool
		Running  bool
	}

	internalError struct {
		message string
	}
)

func (err *internalError) Error() string {
	return err.message
}

// NewConfig will create a new configuration.
func NewConfig(lircName, cache, device, irSend, vers, returnURL string, opModes []string) (Config, error) {
	modes := opModes
	if len(modes) == 1 && pathExists(modes[0]) {
		modes = []string{}
		data, err := os.ReadFile(modes[0])
		if err != nil {
			return Config{}, err
		}
		inRaw := false
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if inRaw {
				if trimmed == "end raw_codes" {
					break
				}
				if strings.HasPrefix(trimmed, "name ") {
					parts := strings.Split(trimmed, " ")
					if len(parts) == 2 {
						modes = append(modes, parts[1])
					}
				}
			} else {
				if trimmed == "begin raw_codes" {
					inRaw = true
				}
			}
		}
	}
	return Config{
		lircName:  lircName,
		cache:     cache,
		device:    device,
		irSend:    irSend,
		version:   vers,
		opModes:   modes,
		returnURL: returnURL,
	}, nil
}

func (ctx context) getState() (*State, error) {
	lock.Lock()
	defer lock.Unlock()
	if !pathExists(ctx.stateFile) {
		return &State{}, nil
	}
	b, err := os.ReadFile(ctx.stateFile)
	if err != nil {
		return nil, err
	}
	obj := &State{}
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func pathExists(path string) bool {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func (ctx context) setState(s *State) error {
	lock.Lock()
	defer lock.Unlock()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(ctx.stateFile, b, 0644)
}

func doScheduled(ctx context) error {
	state, err := ctx.getState()
	if err != nil {
		return err
	}
	action, err := parseSchedule(state.Schedule)
	if err != nil {
		return err
	}
	if action != noAction {
		return act(action, true, nil, ctx)
	}
	return nil
}

func schedulerDaemon(ctx context) {
	today := time.Now()
	fmt.Println("scheduler started")
	for {
		time.Sleep(5 * time.Second)
		now := time.Now()
		state, err := ctx.getState()
		if err == nil {
			if now.Day() != today.Day() || state.Manual {
				if state.Override {
					state.Override = false
					if err := ctx.setState(state); err != nil {
						logError("unable to writeback override disable", err)
					}
				}
			}
			if !state.Manual {
				if err := doScheduled(ctx); err != nil {
					logError("scheduler failed", err)
				}
			}
		} else {
			logError("unable to read state", err)
		}
		today = now
	}
}

func logError(message string, err error) {
	msg := message
	if err != nil {
		msg = fmt.Sprintf("%s (%v)", msg, err)
	}
	fmt.Fprintln(os.Stderr, msg)
}

func quit(message string, err error) {
	logError(message, err)
	os.Exit(1)
}

func newScheduleTime(hr, min int, action string) scheduleTime {
	return scheduleTime{hour: hr, min: min, action: action}
}

func (ctx context) mode(targetMode string, start bool) string {
	op := targetMode
	postfix := "STOP"
	if start {
		postfix = "START"
	}
	return fmt.Sprintf("%s%s", op, postfix)
}

// SetupServer will prepare the wit server.
func (cfg Config) SetupServer(mux *http.ServeMux) error {
	ctx := context{}
	library := cfg.cache
	if !pathExists(library) {
		if err := os.MkdirAll(library, 0755); err != nil {
			quit("unable to make library dir", err)
		}
	}
	ctx.cfg = cfg
	ctx.version = cfg.version
	ctx.stateFile = filepath.Join(library, "state.json")
	tmpl, err := template.New("error").Parse("<html><body>{{ .Error }}</body></html>")
	if err != nil {
		quit("invalid template for errors", err)
	}
	ctx.errorTemplate = tmpl
	page, err := template.New("page").Parse(templateHTML)
	if err != nil {
		quit("unable to read html template", err)
	}
	ctx.pageTemplate = page
	go schedulerDaemon(ctx)
	mux.HandleFunc(endpoint, func(w http.ResponseWriter, r *http.Request) {
		doActionCall(w, r, ctx)
	})

	return nil
}

func act(action string, isChange bool, req *http.Request, ctx context) error {
	webRequest := req != nil
	canChange := true
	state, err := ctx.getState()
	if err != nil {
		return err
	}
	if state.Override && !webRequest {
		canChange = false
	}
	if isChange {
		switch action {
		case "calibrate":
			state.Running = !state.Running
			if err := ctx.setState(state); err != nil {
				return err
			}
		case onAction, offAction:
			if !state.Manual {
				if webRequest {
					state.Override = true
					if err := ctx.setState(state); err != nil {
						return err
					}
				}
			}
			isOn := action == onAction
			if canChange {
				actuating := false
				if isOn {
					if !state.Running {
						actuating = true
					}
				} else {
					if state.Running {
						actuating = true
					}
				}
				if actuating {
					if err := ctx.actuate(ctx.mode(state.OpMode, isOn)); err != nil {
						return err
					}
					state.Running = !state.Running
					if err := ctx.setState(state); err != nil {
						return err
					}
				}
			}
		case "togglelock":
			state.Override = !state.Override
			if err := ctx.setState(state); err != nil {
				return err
			}
		case "schedule":
			if err := req.ParseForm(); err != nil {
				return err
			}
			isManual := false
			schedule := ""
			for k, v := range req.Form {
				switch k {
				case "opmode":
					selectedMode := strings.TrimSpace(strings.Join(v, ""))
					if selectedMode != "noop" {
						state.OpMode = selectedMode
					}
				case "manual":
					isManual = true
				case "sched":
					schedule = strings.Join(v, "\n")
					if _, err := parseSchedule(schedule); err != nil {
						return err
					}
				}
			}
			state.Manual = isManual
			state.Schedule = strings.TrimSpace(schedule)
			if err := ctx.setState(state); err != nil {
				return err
			}
		default:
			logError(fmt.Sprintf("unknown action: %s", action), nil)
			return nil
		}
		return nil
	}
	return nil
}

func newError(message string) error {
	return &internalError{message}
}

func parseSchedule(schedule string) (string, error) {
	current := time.Now()
	isWeekend := false
	if weekday := current.Weekday(); weekday == time.Sunday || weekday == time.Saturday {
		isWeekend = true
	}
	tracking := newScheduleTime(0, 0, offAction)
	timings := []scheduleTime{tracking}
	for _, line := range strings.Split(strings.TrimSpace(schedule), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(strings.TrimSpace(line), " ")
		if len(parts) != 4 {
			return "", newError("invalid schedule line, should be 'min hour action'")
		}
		toggle := parts[3]
		if toggle != onAction && toggle != offAction {
			return "", newError("schedule can only be 'on' or 'off'")
		}
		hour, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", err
		}
		if hour < 0 || hour > 23 {
			return "", newError("hour is invalid")
		}
		min, err := strconv.Atoi(parts[0])
		if err != nil {
			return "", err
		}
		if min < 0 || min > 59 {
			return "", newError("minute is invalid")
		}
		dayType := parts[2]
		if dayType == weekendType || dayType == weekdayType {
			isDayTypeWeekend := dayType == weekendType
			if isWeekend {
				if !isDayTypeWeekend {
					continue
				}
			} else {
				if isDayTypeWeekend {
					continue
				}
			}
		} else {
			return "", newError("invalid day type")
		}
		lineTrack := newScheduleTime(hour, min, toggle)
		timings = append(timings, lineTrack)
	}
	match := noAction
	curr := newScheduleTime(current.Hour(), current.Minute(), "")
	for _, timing := range timings {
		if curr.min >= timing.min && curr.hour >= timing.hour {
			match = timing.action
		}
		if match != noAction {
			if curr.min < timing.min && curr.hour < timing.hour {
				break
			}
		}
	}
	return match, nil
}

func setYes(toggled bool) string {
	if toggled {
		return "YES"
	}
	return "NO"
}

func doTemplate(w http.ResponseWriter, tmpl *template.Template, obj Result) {
	if err := tmpl.Execute(w, obj); err != nil {
		logError("unable to execute template", err)
	}
}

func doActionCall(w http.ResponseWriter, r *http.Request, ctx context) {
	parts := strings.Split(r.URL.String(), "/")
	if len(parts) != 3 {
		logError("invalid action, not given", nil)
		return
	}
	action := parts[2]
	isPost := r.Method == "POST"
	if action != isDisplay {
		if err := act(action, isPost, r, ctx); err != nil {
			doTemplate(w, ctx.errorTemplate, Result{Error: fmt.Sprintf("%v", err)})
			return
		}
	}
	if isPost {
		http.Redirect(w, r, fmt.Sprintf("%s%s", endpoint, isDisplay), http.StatusSeeOther)
		return
	}
	result := Result{}
	state, err := ctx.getState()
	if err != nil {
		doTemplate(w, ctx.errorTemplate, Result{Error: fmt.Sprintf("%v", err)})
		return
	}
	result.Running = setYes(state.Running)
	result.Override = setYes(state.Override)
	result.Manual = setYes(state.Manual)
	result.OperationModes = ctx.cfg.opModes
	schedule := state.Schedule
	result.Schedule = schedule
	result.Build = ctx.version
	result.Time = time.Now().Format("2006-01-02T15:04:05")
	acMode := state.OpMode
	result.System = acMode
	result.HasReturn = len(ctx.cfg.returnURL) > 0
	result.ReturnTo = ctx.cfg.returnURL
	doTemplate(w, ctx.pageTemplate, result)
}

func (ctx context) actuate(mode string) error {
	return exec.Command(ctx.cfg.irSend, fmt.Sprintf("--device=%s", ctx.cfg.device), "SEND_ONCE", ctx.cfg.lircName, mode).Run()
}

func main() {
	binding := flag.String("binding", ":7801", "http binding")
	lircName := flag.String("lircname", "BRYANT", "lirc config name")
	lib := flag.String("cache", "/var/lib/wit", "cache directory")
	device := flag.String("device", "/run/lirc/lircd", "lircd device")
	irSend := flag.String("irsend", "/usr/bin/irsend", "irsend executable")
	home := flag.String("home", "", "url to display as a 'home' link")
	opModes := flag.String("opmodes", "", "operation modes (comma separated list, or a lirc config file to parse/read")
	flag.Parse()
	cfg, err := NewConfig(*lircName, *lib, *device, *irSend, version, *home, strings.Split(*opModes, ","))
	if err != nil {
		quit("failed to create config", err)
	}
	mux := http.NewServeMux()
	if err := cfg.SetupServer(mux); err != nil {
		quit("failed to setup server", err)
	}
	srv := &http.Server{
		Addr:    *binding,
		Handler: mux,
	}
	if err := srv.ListenAndServe(); err != nil {
		logError("listen and serve failed", err)
	}
}
