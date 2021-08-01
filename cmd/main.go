package main

import (
	_ "embed"
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
		Hold     string
		Mode     string
		Schedule string
	}
	scheduleTime struct {
		hour   int
		min    int
		action string
	}
	context struct {
		modeAC        string
		sendIR        string
		device        string
		configFile    string
		lock          string
		schedule      string
		hold          string
		running       string
		pageTemplate  *template.Template
		errorTemplate *template.Template
	}
	// ThermError are all web therm errors.
	ThermError struct {
		message string
	}
)

func doScheduled(ctx context) error {
	lock.Lock()
	defer lock.Unlock()
	b, err := os.ReadFile(ctx.schedule)
	if err != nil {
		return err
	}
	action, err := parseSchedule(string(b))
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
		now := time.Now()
		if now.Day() != today.Day() {
			if exists(ctx.lock) {
				if err := os.Remove(ctx.lock); err != nil {
					onError("unable to remove lock", err)
				}
			}
		}
		if !exists(ctx.hold) {
			if err := doScheduled(ctx); err != nil {
				onError("scheduler failed", err)
			}
		}
		today = now
		time.Sleep(5 * time.Second)
	}
}

func (t *ThermError) Error() string {
	return t.message
}

func newScheduleTime(hr, min int, action string) scheduleTime {
	return scheduleTime{hour: hr, min: min, action: action}
}

func exists(path string) bool {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func (ctx context) mode(start bool) string {
	op := "HEAT"
	if exists(ctx.modeAC) {
		op = "AC"
	}
	postfix := "STOP"
	if start {
		postfix = "START"
	}
	return fmt.Sprintf("%s%s", op, postfix)
}

func onError(message string, err error) {
	errorText := ""
	if err != nil {
		errorText = fmt.Sprintf(" (%v)", err)
	}
	fmt.Printf("%s%s\n", message, errorText)
}

func fatal(message string, err error) {
	onError(message, err)
	os.Exit(1)
}

func main() {
	binding := flag.String("binding", ":8888", "http binding")
	config := flag.String("lirccfg", "BRYANT", "lirc config")
	lib := flag.String("cache", "/var/lib/wit", "cache directory")
	device := flag.String("device", "/run/lirc/lircd", "lircd device")
	irSend := flag.String("irsend", "/usr/bin/irsend", "irsend executable")
	flag.Parse()
	ctx := context{}
	library := *lib
	if !exists(library) {
		if err := os.MkdirAll(library, 0755); err != nil {
			fatal("unable to make library dir", err)
		}
	}
	ctx.modeAC = filepath.Join(library, "acmode")
	ctx.device = *device
	ctx.sendIR = *irSend
	ctx.configFile = *config
	ctx.lock = filepath.Join(library, "lock")
	ctx.schedule = filepath.Join(library, "schedule")
	ctx.hold = filepath.Join(library, "hold")
	ctx.running = filepath.Join(library, "running")
	tmpl, err := template.New("error").Parse("<html><body>{{ .Error }}</body></html>")
	if err != nil {
		fatal("invalid template for errors", err)
	}
	ctx.errorTemplate = tmpl
	page, err := template.New("page").Parse(templateHTML)
	if err != nil {
		fatal("unable to read html template", err)
	}
	ctx.pageTemplate = page
	go schedulerDaemon(ctx)
	host(*binding, ctx)
}

func write(path string) error {
	return os.WriteFile(path, []byte(""), 0644)
}

func act(action string, isChange bool, req *http.Request, ctx context) error {
	webRequest := req != nil
	canChange := true
	if exists(ctx.lock) && !webRequest {
		canChange = false
	}
	if isChange {
		switch action {
		case "calibrate":
			if exists(ctx.running) {
				if err := os.Remove(ctx.running); err != nil {
					return err
				}
			} else {
				if err := write(ctx.running); err != nil {
					return err
				}
			}
		case onAction, offAction:
			if err := ctx.lockNow(webRequest); err != nil {
				return err
			}
			isOn := action == onAction
			if canChange {
				actuating := false
				if isOn {
					if !exists(ctx.running) {
						if err := write(ctx.running); err != nil {
							return err
						}
						actuating = true
					}
				} else {
					if exists(ctx.running) {
						if err := os.Remove(ctx.running); err != nil {
							return err
						}
						actuating = true
					}
				}
				if actuating {
					if err := ctx.actuate(ctx.mode(isOn)); err != nil {
						return err
					}
				}
			}
		case "togglelock":
			if exists(ctx.lock) {
				if err := os.Remove(ctx.lock); err != nil {
					return err
				}
			} else {
				if err := write(ctx.lock); err != nil {
					return err
				}
			}
		case "schedule":
			if err := req.ParseForm(); err != nil {
				return err
			}
			holding := false
			schedule := ""
			for k, v := range req.Form {
				switch k {
				case "hold":
					holding = true
				case "sched":
					schedule = strings.Join(v, "\n")
					if _, err := parseSchedule(schedule); err != nil {
						return err
					}
				}
			}
			if holding {
				if err := write(ctx.hold); err != nil {
					return err
				}
			} else {
				if exists(ctx.hold) {
					if err := os.Remove(ctx.hold); err != nil {
						return err
					}
				}
			}
			if err := os.WriteFile(ctx.schedule, []byte(schedule), 0644); err != nil {
				return err
			}
		case "acmode":
			if exists(ctx.modeAC) {
				if err := os.Remove(ctx.modeAC); err != nil {
					return err
				}
			} else {
				if err := write(ctx.modeAC); err != nil {
					return err
				}
			}
		default:
			onError(fmt.Sprintf("unknown action: %s", action), nil)
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

func (ctx context) lockNow(canLock bool) error {
	if canLock {
		return write(ctx.lock)
	}
	return nil
}

func setYes(path string) string {
	if exists(path) {
		return "YES"
	}
	return "NO"
}

func doTemplate(w http.ResponseWriter, tmpl *template.Template, obj Result) {
	if err := tmpl.Execute(w, obj); err != nil {
		onError("unable to execute template", err)
	}
}

func doActionCall(w http.ResponseWriter, r *http.Request, ctx context) {
	lock.Lock()
	defer lock.Unlock()
	parts := strings.Split(r.URL.String(), "/")
	if len(parts) != 3 {
		onError("invalid action, not given", nil)
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
	result.Running = setYes(ctx.running)
	result.Mode = setYes(ctx.lock)
	result.Hold = setYes(ctx.hold)
	schedule := ""
	if exists(ctx.schedule) {
		b, err := os.ReadFile(ctx.schedule)
		if err != nil {
			onError("unable to read schedule", err)
			return
		}
		schedule = strings.TrimSpace(string(b))
	}
	result.Schedule = schedule
	result.Time = time.Now().Format("2006-01-02T15:04:05")
	acMode := "HEAT"
	if exists(ctx.modeAC) {
		acMode = "A/C"
	}
	result.System = acMode
	doTemplate(w, ctx.pageTemplate, result)
}

func host(binding string, ctx context) {
	http.HandleFunc(endpoint, func(w http.ResponseWriter, r *http.Request) {
		doActionCall(w, r, ctx)
	})

	if err := http.ListenAndServe(binding, nil); err != nil {
		onError("listen and server", err)
	}
}

func (ctx context) actuate(mode string) error {
	return exec.Command(ctx.sendIR, fmt.Sprintf("--device=%s", ctx.device), "SEND_ONCE", ctx.configFile, mode).Run()
}
