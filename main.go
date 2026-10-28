// earshot: record the room mic while you're away, listen live or later from your phone.
package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

type config struct {
	addr     string
	password string
	recDir   string
	keepDays int
	bitrate  string
	inhibit  string
}

func loadConfig() config {
	home, _ := os.UserHomeDir()
	c := config{
		addr:     env("EARSHOT_ADDR", ":8787"),
		password: os.Getenv("EARSHOT_PASSWORD"),
		recDir:   env("EARSHOT_DIR", filepath.Join(home, "Recordings", "earshot")),
		bitrate:  env("EARSHOT_BITRATE", "32k"),
		inhibit:  env("EARSHOT_INHIBIT", "sleep:idle:handle-lid-switch"),
	}
	c.keepDays, _ = strconv.Atoi(env("EARSHOT_KEEP_DAYS", "14"))
	return c
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// hub fans the live MP3 stream out to every connected listener.
type hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func newHub() *hub { return &hub{clients: map[chan []byte]struct{}{}} }

func (h *hub) subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *hub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- b:
		default: // listener can't keep up; drop it rather than stall recording
			delete(h.clients, ch)
			close(ch)
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	for ch := range h.clients {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// recorder owns the ffmpeg process: Opus segments to disk, MP3 to stdout for live.
type recorder struct {
	cfg  config
	hub  *hub
	mu   sync.Mutex
	cmd  *exec.Cmd
	quit io.WriteCloser // ffmpeg stdin; writing "q" stops it cleanly
	done chan struct{}
	// since is when the current recording started; zero when idle.
	since       time.Time
	restoreMute bool
}

// inhibitSleep blocks suspend until the returned closer is closed. The inhibitor
// lives as long as `cat` reads our pipe, so it is released even if we crash.
func inhibitSleep(what string) io.Closer {
	cmd := exec.Command("systemd-inhibit", "--what="+what, "--who=earshot",
		"--why=Recording audio", "--mode=block", "cat")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		log.Printf("sleep inhibitor: %v", err)
		return io.NopCloser(nil)
	}
	go cmd.Wait()
	return in
}

func (r *recorder) running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd != nil
}

func (r *recorder) start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return nil
	}
	if err := os.MkdirAll(r.cfg.recDir, 0o750); err != nil {
		return err
	}

	r.restoreMute = false
	if out, err := exec.Command("wpctl", "get-volume", "@DEFAULT_AUDIO_SOURCE@").Output(); err == nil &&
		strings.Contains(string(out), "MUTED") {
		if exec.Command("wpctl", "set-mute", "@DEFAULT_AUDIO_SOURCE@", "0").Run() == nil {
			r.restoreMute = true
		}
	}

	args := []string{
		"-hide_banner", "-loglevel", "warning",
		// wallclock timestamps jitter and make the encoders emit non-monotonic DTS;
		// skipping the probe cuts startup from ~2.6s to ~0.2s
		"-f", "pulse", "-wallclock", "0", "-fragment_size", "4096",
		"-probesize", "32", "-analyzeduration", "0",
		"-i", "default", "-ac", "1", "-ar", "48000",
		// archive: hourly Opus files aligned to the clock
		"-map", "0:a", "-c:a", "libopus", "-b:a", r.cfg.bitrate, "-application", "audio",
		"-f", "segment", "-segment_time", "3600", "-segment_atclocktime", "1",
		"-segment_format", "ogg", "-reset_timestamps", "1", "-strftime", "1",
		filepath.Join(r.cfg.recDir, "%Y-%m-%d_%H-%M-%S.opus"),
		// live: MP3 on stdout, plays in every mobile browser
		"-map", "0:a", "-c:a", "libmp3lame", "-b:a", "64k", "-f", "mp3", "pipe:1",
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	// own process group so a Ctrl-C on the daemon doesn't kill ffmpeg before it finalizes
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	quit, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		r.unmuteRestore()
		return err
	}
	inhibitor := inhibitSleep(r.cfg.inhibit)
	r.cmd, r.quit, r.since, r.done = cmd, quit, time.Now(), make(chan struct{})
	log.Printf("recording started (pid %d)", cmd.Process.Pid)

	go func(done chan struct{}) {
		defer inhibitor.Close()
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				r.hub.broadcast(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				break
			}
		}
		err := cmd.Wait()
		r.hub.closeAll()
		r.mu.Lock()
		r.cmd, r.since = nil, time.Time{}
		r.unmuteRestore()
		r.mu.Unlock()
		log.Printf("recording stopped (%v)", err)
		close(done)
	}(r.done)
	return nil
}

// unmuteRestore puts the mic back to muted if we unmuted it. Caller holds r.mu.
func (r *recorder) unmuteRestore() {
	if r.restoreMute {
		exec.Command("wpctl", "set-mute", "@DEFAULT_AUDIO_SOURCE@", "1").Run()
		r.restoreMute = false
	}
}

func (r *recorder) stop() {
	r.mu.Lock()
	cmd, quit, done := r.cmd, r.quit, r.done
	r.mu.Unlock()
	if cmd == nil {
		return
	}
	// "q" is handled even if ffmpeg is still starting up, unlike SIGINT.
	io.WriteString(quit, "q")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		<-done
	}
}

type recording struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Live     bool      `json:"live"`
}

func (r *recorder) list() ([]recording, error) {
	entries, err := os.ReadDir(r.cfg.recDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	recs := []recording{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".opus") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		recs = append(recs, recording{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name > recs[j].Name })
	if len(recs) > 0 && r.running() {
		recs[0].Live = true
	}
	return recs, nil
}

func (r *recorder) prune() {
	if r.cfg.keepDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -r.cfg.keepDays)
	recs, _ := r.list()
	for _, rec := range recs {
		if !rec.Live && rec.Modified.Before(cutoff) {
			if err := os.Remove(filepath.Join(r.cfg.recDir, rec.Name)); err == nil {
				log.Printf("pruned %s", rec.Name)
			}
		}
	}
}

func validName(name string) bool {
	return name == filepath.Base(name) && strings.HasSuffix(name, ".opus") && !strings.HasPrefix(name, ".")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// auth requires basic auth (any username) for non-loopback clients when a password is set.
func auth(password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host, _, _ := net.SplitHostPort(req.RemoteAddr)
		if ip := net.ParseIP(host); password != "" && (ip == nil || !ip.IsLoopback()) {
			_, pw, ok := req.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="earshot"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, req)
	})
}

func main() {
	cfg := loadConfig()
	h := newHub()
	rec := &recorder{cfg: cfg, hub: h}

	status := func(w http.ResponseWriter, _ *http.Request) {
		rec.mu.Lock()
		since := rec.since
		rec.mu.Unlock()
		elapsed := 0
		if !since.IsZero() {
			elapsed = int(time.Since(since).Seconds())
		}
		writeJSON(w, map[string]any{
			"recording": !since.IsZero(),
			"since":     since,
			"elapsed":   elapsed,
			"listeners": h.count(),
		})
	}
	control := func(fn func() error) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			if err := fn(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			status(w, req)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", status)
	mux.HandleFunc("POST /api/start", control(rec.start))
	mux.HandleFunc("POST /api/stop", control(func() error { rec.stop(); return nil }))
	mux.HandleFunc("POST /api/toggle", control(func() error {
		if rec.running() {
			rec.stop()
			return nil
		}
		return rec.start()
	}))
	mux.HandleFunc("GET /api/recordings", func(w http.ResponseWriter, _ *http.Request) {
		recs, err := rec.list()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, recs)
	})
	mux.HandleFunc("DELETE /api/recordings/{name}", func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("name")
		if !validName(name) {
			http.Error(w, "bad name", http.StatusBadRequest)
			return
		}
		if err := os.Remove(filepath.Join(cfg.recDir, name)); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /rec/{name}", func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("name")
		if !validName(name) {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "audio/ogg; codecs=opus")
		http.ServeFile(w, req, filepath.Join(cfg.recDir, name))
	})
	mux.HandleFunc("GET /live.mp3", func(w http.ResponseWriter, req *http.Request) {
		if !rec.running() {
			http.Error(w, "not recording", http.StatusConflict)
			return
		}
		ch := h.subscribe()
		defer h.unsubscribe(ch)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Cache-Control", "no-store")
		flusher, _ := w.(http.Flusher)
		for {
			select {
			case b, ok := <-ch:
				if !ok {
					return
				}
				if _, err := w.Write(b); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			case <-req.Context().Done():
				return
			}
		}
	})
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(static))

	srv := &http.Server{Addr: cfg.addr, Handler: auth(cfg.password, mux)}

	go func() {
		rec.prune()
		for range time.Tick(time.Hour) {
			rec.prune()
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		rec.stop()
		shutdown, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		srv.Shutdown(shutdown)
	}()

	if os.Getenv("EARSHOT_AUTOSTART") == "1" {
		if err := rec.start(); err != nil {
			log.Printf("autostart: %v", err)
		}
	}
	log.Printf("earshot listening on %s, saving to %s", cfg.addr, cfg.recDir)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
