package serve

import (
	_ "embed"
	"encoding/json"
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

	"voidedtech.com/stock"
)

var (
	lock = &sync.Mutex{}
	//go:embed template.html
	templateHTML string
)

const (
	onAction  = "on"
	offAction = "off"
	noAction  = ""
	isDisplay = "display"
	endpoint  = "/wit/"
)

type (
	// Result is how html results are shown.
	Result struct {
		Error    string
		Running  string
		System   string
		Time     string
		Manual   string
		Mode     string
		Schedule string
		Build    string
	}
	scheduleTime struct {
		hour   int
		min    int
		action string
	}
	context struct {
		sendIR        string
		device        string
		configFile    string
		version       string
		stateFile     string
		pageTemplate  *template.Template
		errorTemplate *template.Template
	}
	// ThermError are all web therm errors.
	ThermError struct {
		message string
	}
	// Config handles wit configuration.
	Config struct {
		configFile string
		cache      string
		device     string
		irSend     string
		version    string
	}

	// State represents on the current system state to persist to disk.
	State struct {
		ACMode   bool
		Schedule string
		Manual   bool
		Override bool
		Running  bool
	}
)

// NewConfig will create a new configuration.
func NewConfig(configFile, cache, device, irSend, vers string) Config {
	return Config{
		configFile: configFile,
		cache:      cache,
		device:     device,
		irSend:     irSend,
		version:    vers,
	}
}

func (ctx context) getState() (*State, error) {
	lock.Lock()
	defer lock.Unlock()
	if !stock.PathExists(ctx.stateFile) {
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
			if now.Day() != today.Day() {
				if state.Override {
					state.Override = false
					if err := ctx.setState(state); err != nil {
						stock.LogError("unable to writeback override disable", err)
					}
				}
			}
			if !state.Manual {
				if err := doScheduled(ctx); err != nil {
					stock.LogError("scheduler failed", err)
				}
			}
		} else {
			stock.LogError("unable to read state", err)
		}
		today = now
	}
}

func (t *ThermError) Error() string {
	return t.message
}

func newScheduleTime(hr, min int, action string) scheduleTime {
	return scheduleTime{hour: hr, min: min, action: action}
}

func (ctx context) mode(isAC, start bool) string {
	op := "HEAT"
	if isAC {
		op = "AC"
	}
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
	if !stock.PathExists(library) {
		if err := os.MkdirAll(library, 0755); err != nil {
			stock.Die("unable to make library dir", err)
		}
	}
	ctx.device = cfg.device
	ctx.sendIR = cfg.irSend
	ctx.configFile = cfg.configFile
	ctx.version = cfg.version
	ctx.stateFile = filepath.Join(library, "state.json")
	tmpl, err := template.New("error").Parse("<html><body>{{ .Error }}</body></html>")
	if err != nil {
		stock.Die("invalid template for errors", err)
	}
	ctx.errorTemplate = tmpl
	page, err := template.New("page").Parse(templateHTML)
	if err != nil {
		stock.Die("unable to read html template", err)
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
					if err := ctx.actuate(ctx.mode(state.ACMode, isOn)); err != nil {
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
		case "acmode":
			state.ACMode = !state.ACMode
			if err := ctx.setState(state); err != nil {
				return err
			}
		default:
			stock.LogError(fmt.Sprintf("unknown action: %s", action), nil)
			return nil
		}
		return nil
	}
	return nil
}

func parseSchedule(schedule string) (string, error) {
	current := time.Now()
	tracking := newScheduleTime(0, 0, offAction)
	timings := []scheduleTime{tracking}
	for _, line := range strings.Split(strings.TrimSpace(schedule), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(strings.TrimSpace(line), " ")
		if len(parts) != 3 {
			return "", &ThermError{"invalid schedule line, should be 'min hour action'"}
		}
		toggle := parts[2]
		if toggle != onAction && toggle != offAction {
			return "", &ThermError{"schedule can only be 'on' or 'off'"}
		}
		hour, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", err
		}
		if hour < 0 || hour > 23 {
			return "", &ThermError{"hour is invalid"}
		}
		min, err := strconv.Atoi(parts[0])
		if err != nil {
			return "", err
		}
		if min < 0 || min > 59 {
			return "", &ThermError{"minute is invalid"}
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
		stock.LogError("unable to execute template", err)
	}
}

func doActionCall(w http.ResponseWriter, r *http.Request, ctx context) {
	parts := strings.Split(r.URL.String(), "/")
	if len(parts) != 3 {
		stock.LogError("invalid action, not given", nil)
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
	result.Mode = setYes(state.ACMode)
	result.Manual = setYes(state.Manual)
	schedule := state.Schedule
	result.Schedule = schedule
	result.Build = ctx.version
	result.Time = time.Now().Format("2006-01-02T15:04:05")
	acMode := "HEAT"
	if state.ACMode {
		acMode = "A/C"
	}
	result.System = acMode
	doTemplate(w, ctx.pageTemplate, result)
}

func (ctx context) actuate(mode string) error {
	return exec.Command(ctx.sendIR, fmt.Sprintf("--device=%s", ctx.device), "SEND_ONCE", ctx.configFile, mode).Run()
}
