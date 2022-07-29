package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	onAction     = "on"
	offAction    = "off"
	noAction     = ""
	isDisplay    = "display"
	endpoint     = "/wit/"
	weekdayType  = "weekday"
	weekendType  = "weekend"
	commandStart = "START"
	commandStop  = "STOP"
)

type (
	// Result is how html results are shown.
	Result struct {
		Error          string
		System         string
		Manual         string
		Override       string
		Schedule       string
		Build          string
		OperationModes []string
	}
	scheduleTime struct {
		hour   int
		min    int
		action string
	}
	context struct {
		cfg           Configuration
		stateFile     string
		pageTemplate  *template.Template
		errorTemplate *template.Template
	}
	// Configuration is the wit configuration file definition.
	Configuration struct {
		Binding  string            `json:"binding"`
		LIRC     LIRCConfiguration `json:"lirc"`
		Cache    string            `json:"cache"`
		lircName string
		opModes  []string
		version  string
	}
	// LIRCConfiguration is the backing LIRC requirements to run lirc.
	LIRCConfiguration struct {
		Socket string   `json:"socket"`
		Config string   `json:"config"`
		IRSend string   `json:"irsend"`
		Daemon bool     `json:"daemon"`
		Args   []string `json:"args"`
	}

	// State represents on the current system state to persist to disk.
	State struct {
		OpMode   string
		Schedule string
		Manual   bool
		Override bool
		Running  bool
	}
)

func parseConfigName(line string) string {
	if strings.HasPrefix(line, "name ") {
		parts := strings.Split(line, " ")
		if len(parts) == 2 {
			return parts[1]
		}
	}
	return ""
}

func (c *Configuration) parseLIRCConfig() error {
	if !pathExists(c.LIRC.Config) {
		return errors.New("config file for lirc does not exist")
	}
	var modes []string
	lircName := ""
	data, err := os.ReadFile(c.LIRC.Config)
	if err != nil {
		return err
	}
	inRaw := false
	lastLine := ""
	modes = []string{}
	uniques := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if lastLine == "begin remote" {
			if name := parseConfigName(trimmed); name != "" {
				lircName = name
			}
		}
		if inRaw {
			if trimmed == "end raw_codes" {
				break
			}
			if name := parseConfigName(trimmed); name != "" {
				if strings.HasSuffix(name, commandStart) {
					name = name[:len(name)-len(commandStart)]
				} else if strings.HasSuffix(name, commandStop) {
					name = name[:len(name)-len(commandStop)]
				} else {
					return errors.New("unknown mode, not start/top")
				}
				val, ok := uniques[name]
				if !ok {
					modes = append(modes, name)
					val = 1
				} else {
					val++
				}
				uniques[name] = val
			}
		} else {
			if trimmed == "begin raw_codes" {
				inRaw = true
			}
		}
		lastLine = trimmed
	}
	if len(modes) == 0 || lircName == "" {
		return errors.New("failed parsing lirc config for necessary values")
	}
	for k, v := range uniques {
		if v != 2 {
			return fmt.Errorf("mismatch start/stop: %s", k)
		}
	}
	sort.Strings(modes)
	c.opModes = modes
	c.lircName = lircName
	return nil
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

func (c Configuration) setupServer(mux *http.ServeMux) error {
	ctx := context{}
	library := c.Cache
	if !pathExists(library) {
		if err := os.MkdirAll(library, 0755); err != nil {
			quit("unable to make library dir", err)
		}
	}
	ctx.cfg = c
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
					valid := false
					for _, m := range ctx.cfg.opModes {
						if m == state.OpMode {
							valid = true
							break
						}
					}
					if !valid {
						return fmt.Errorf("invalid mode: %s", state.OpMode)
					}
					if err := exec.Command(ctx.cfg.LIRC.IRSend, fmt.Sprintf("--device=%s", ctx.cfg.LIRC.Socket), "SEND_ONCE", ctx.cfg.lircName, state.OpMode).Run(); err != nil {
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
			return "", errors.New("invalid schedule line, should be 'min hour action'")
		}
		toggle := parts[3]
		if toggle != onAction && toggle != offAction {
			return "", errors.New("schedule can only be 'on' or 'off'")
		}
		hour, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", err
		}
		if hour < 0 || hour > 23 {
			return "", errors.New("hour is invalid")
		}
		min, err := strconv.Atoi(parts[0])
		if err != nil {
			return "", err
		}
		if min < 0 || min > 59 {
			return "", errors.New("minute is invalid")
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
			return "", errors.New("invalid day type")
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
		if action == "current" {
			state, err := ctx.getState()
			if err != nil {
				doTemplate(w, ctx.errorTemplate, Result{Error: fmt.Sprintf("%v", err)})
				return
			}
			data := []byte(state.runningState())
			w.Write(data)
			return
		}
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
	result.Override = setYes(state.Override)
	result.Manual = setYes(state.Manual)
	result.OperationModes = ctx.cfg.opModes
	schedule := state.Schedule
	result.Schedule = schedule
	result.Build = ctx.cfg.version
	acMode := state.OpMode
	result.System = acMode
	doTemplate(w, ctx.pageTemplate, result)
}

func (s *State) runningState() string {
	return fmt.Sprintf("%s (%s)", setYes(s.Running), time.Now().Format("2006-01-02T15:04:05"))
}

func runLIRCDaemon(args []string) {
	for {
		cmd := exec.Command("lircd", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			logError("lircd failure", err)
		}
		time.Sleep(30 * time.Second)
	}
}

func (c *Configuration) runLIRC() {
	var args []string
	args = append(args, c.LIRC.Args...)
	args = append(args, []string{"-o", c.LIRC.Socket}...)
	args = append(args, c.LIRC.Config)
	go runLIRCDaemon(args)
}

func main() {
	configurationFile := flag.String("config", "/etc/wit.json", "wit configuration file")
	flag.Parse()
	b, err := os.ReadFile(*configurationFile)
	if err != nil {
		quit("unable to read config file", err)
	}
	config := &Configuration{}
	if err := json.Unmarshal(b, &config); err != nil {
		quit("failed to read config json", err)
	}
	config.version = version
	if err := config.parseLIRCConfig(); err != nil {
		quit("unable to parse LIRC config", err)
	}
	mux := http.NewServeMux()
	if err := config.setupServer(mux); err != nil {
		quit("failed to setup server", err)
	}
	srv := &http.Server{
		Addr:    config.Binding,
		Handler: mux,
	}
	if config.LIRC.Daemon {
		config.runLIRC()
	}
	if err := srv.ListenAndServe(); err != nil {
		logError("listen and serve failed", err)
	}
}
