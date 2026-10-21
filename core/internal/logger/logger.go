// Package logger captures the process-wide stdout and stderr streams and
// duplicates them into daily-rotating files under a configured directory:
//
//   - app-YYYY-MM-DD.log    — anything written to os.Stdout / os.Stderr by
//     Go code (stdlib log, fmt.Print*, runtime panics, etc).
//   - access-YYYY-MM-DD.log — Gin's HTTP access log (when main wires it up
//     via Access() / AccessError()).
//   - clients/<id>/YYYY-MM-DD.log — log lines uploaded by browser clients
//     through the admin ingest endpoint.
package logger

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	mu             sync.Mutex
	dir            string
	appWriter      *dailyWriter
	accessWriter   *dailyWriter
	origStdout     *os.File
	origStderr     *os.File
	clientWriters  sync.Map // clientId -> *dailyWriter
	initialised    bool
	clientIdPolicy = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Init redirects os.Stdout and os.Stderr through pipes whose readers tee into
// both the original console FD and the daily "app" log. It also prepares the
// "access" daily writer that callers (main.go) hand to gin's writers via
// Access()/AccessError(). Calling Init more than once is a no-op (returns nil).
func Init(logDir string) error {
	mu.Lock()
	if initialised {
		mu.Unlock()
		return nil
	}
	mu.Unlock()

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(logDir, "clients"), 0o755); err != nil {
		return fmt.Errorf("create clients dir: %w", err)
	}

	app := &dailyWriter{dir: logDir, prefix: "app"}
	access := &dailyWriter{dir: logDir, prefix: "access"}

	saveOrigStdout := os.Stdout
	saveOrigStderr := os.Stderr

	rOut, wOut, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		_ = rOut.Close()
		_ = wOut.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	mu.Lock()
	dir = logDir
	appWriter = app
	accessWriter = access
	origStdout = saveOrigStdout
	origStderr = saveOrigStderr
	initialised = true
	mu.Unlock()

	os.Stdout = wOut
	os.Stderr = wErr
	// stdlib log captured the original stderr; point it at the new one.
	log.SetOutput(os.Stderr)

	go forward(rOut, saveOrigStdout, app)
	go forward(rErr, saveOrigStderr, app)

	return nil
}

func forward(r *os.File, sinks ...io.Writer) {
	mw := io.MultiWriter(sinks...)
	_, _ = io.Copy(mw, r)
}

// Dir returns the configured log directory, or empty if Init was not called.
func Dir() string {
	mu.Lock()
	defer mu.Unlock()
	return dir
}

// Access returns an io.Writer that fans out to the original stdout (so
// docker logs keep working) and the access daily file. Intended for
// gin.DefaultWriter.
func Access() io.Writer {
	mu.Lock()
	defer mu.Unlock()
	if !initialised {
		return os.Stdout
	}
	return io.MultiWriter(origStdout, accessWriter)
}

// AccessError mirrors Access() but for stderr. Intended for gin.DefaultErrorWriter.
func AccessError() io.Writer {
	mu.Lock()
	defer mu.Unlock()
	if !initialised {
		return os.Stderr
	}
	return io.MultiWriter(origStderr, accessWriter)
}

// ValidateClientID returns nil if id is a safe identifier for a client log dir.
func ValidateClientID(id string) error {
	if !clientIdPolicy.MatchString(id) {
		return fmt.Errorf("invalid client id")
	}
	return nil
}

// ClientWriter returns (and caches) an io.Writer that appends to
// <dir>/clients/<clientId>/YYYY-MM-DD.log. The clientId must satisfy
// ValidateClientID.
func ClientWriter(clientId string) (io.Writer, error) {
	if err := ValidateClientID(clientId); err != nil {
		return nil, err
	}
	mu.Lock()
	if !initialised {
		mu.Unlock()
		return nil, fmt.Errorf("logger not initialised")
	}
	d := dir
	mu.Unlock()

	if existing, ok := clientWriters.Load(clientId); ok {
		return existing.(*dailyWriter), nil
	}
	clientDir := filepath.Join(d, "clients", clientId)
	if err := os.MkdirAll(clientDir, 0o755); err != nil {
		return nil, fmt.Errorf("create client dir: %w", err)
	}
	w := &dailyWriter{dir: clientDir, prefix: ""}
	actual, _ := clientWriters.LoadOrStore(clientId, w)
	return actual.(*dailyWriter), nil
}

// dailyWriter writes log lines to <prefix>-YYYY-MM-DD.log (or just
// YYYY-MM-DD.log when prefix is empty) inside dir, reopening the file when
// the calendar day rolls over.
type dailyWriter struct {
	dir    string
	prefix string
	mu     sync.Mutex
	file   *os.File
	dayKey string
}

func (d *dailyWriter) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := time.Now().Format("2006-01-02")
	if d.file == nil || key != d.dayKey {
		if d.file != nil {
			_ = d.file.Close()
		}
		name := key + ".log"
		if d.prefix != "" {
			name = d.prefix + "-" + name
		}
		path := filepath.Join(d.dir, name)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		d.file = f
		d.dayKey = key
	}
	return d.file.Write(p)
}

// ListFiles returns every log file found under the configured directory.
// Top-level files (app-*, access-*) are returned by basename; per-client files
// are returned as forward-slash relative paths like "clients/<id>/2026-05-15.log".
// Result is sorted with the newest entries first.
func ListFiles() ([]string, error) {
	d := Dir()
	if d == "" {
		return nil, nil
	}
	var names []string

	topEntries, err := os.ReadDir(d)
	if err != nil {
		return nil, err
	}
	for _, e := range topEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".log") {
			names = append(names, name)
		}
	}

	clientsDir := filepath.Join(d, "clients")
	clientEntries, err := os.ReadDir(clientsDir)
	if err == nil {
		for _, c := range clientEntries {
			if !c.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(clientsDir, c.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".log") {
					continue
				}
				names = append(names, "clients/"+c.Name()+"/"+f.Name())
			}
		}
	}

	sort.Slice(names, func(i, j int) bool {
		// Newest dates appear first; reverse string compare is correct for
		// YYYY-MM-DD-suffixed names. Top-level paths sort before client paths
		// because '/' < most letters, but since names share suffixes the
		// ordering still groups same-day entries together usefully.
		return names[i] > names[j]
	})
	return names, nil
}

// TailLines reads up to n lines from the end of the named log file (a path
// returned by ListFiles) and returns them oldest-first. If search is non-empty,
// only lines that contain it (case-insensitive) are returned. Hard ceiling on n
// is 5000.
func TailLines(name string, n int, search string) ([]string, error) {
	d := Dir()
	if d == "" {
		return nil, fmt.Errorf("logger not initialised")
	}
	// Allow forward-slashes only inside the dedicated "clients/" subtree.
	if strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return nil, fmt.Errorf("invalid file name")
	}
	if !strings.HasSuffix(name, ".log") {
		return nil, fmt.Errorf("invalid file name")
	}
	if strings.Contains(name, "/") && !strings.HasPrefix(name, "clients/") {
		return nil, fmt.Errorf("invalid file name")
	}
	if n <= 0 {
		n = 200
	}
	if n > 5000 {
		n = 5000
	}
	path := filepath.Join(d, filepath.FromSlash(name))
	// Final guard: resolved path must remain under dir.
	absDir, _ := filepath.Abs(d)
	absPath, _ := filepath.Abs(path)
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) && absPath != absDir {
		return nil, fmt.Errorf("invalid file name")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	needle := strings.ToLower(strings.TrimSpace(search))
	buf := make([]string, 0, n)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if needle != "" && !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		if len(buf) < n {
			buf = append(buf, line)
		} else {
			copy(buf, buf[1:])
			buf[n-1] = line
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return buf, nil
}
