// Clearword's local transcription server. Requires ffmpeg and ffprobe on PATH.
package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	maxUpload       = int64(20 << 30)
	chunkTarget     = int64(20 << 20)
	chunkSeconds    = 18 * 60
	chunkOverlap    = 12.0
	defaultModel    = "gpt-4o-transcribe-diarize"
	openAIEndpoint  = "https://api.openai.com/v1/audio/transcriptions"
	openAIModelsURL = "https://api.openai.com/v1/models"
	assemblyModelUniversal2      = "assemblyai-universal-2"
	assemblyModelUniversal35Pro = "assemblyai-universal-3-5-pro"
	assemblyModelUniversal2Diarized = "assemblyai-universal-2-diarize"
	assemblyModelUniversal35ProDiarized = "assemblyai-universal-3-5-pro-diarize"
	assemblyUpload  = "https://api.assemblyai.com/v2/upload"
	assemblyAPI     = "https://api.assemblyai.com/v2/transcript"
	maxSettingsBody = 16 << 10
	maxTranslationBody = 8 << 20
)

var (
	models      = map[string]bool{"gpt-4o-transcribe": true, "whisper-1": true, defaultModel: true, assemblyModelUniversal2: true, assemblyModelUniversal35Pro: true, assemblyModelUniversal2Diarized: true, assemblyModelUniversal35ProDiarized: true}
	dataDir     = filepath.Join("data")
	audioDir    = filepath.Join(dataDir, "audio")
	database    *sql.DB
	jobsMu      sync.RWMutex
	jobs        = map[string]map[string]any{}
	jobIDRE     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	wordRE      = regexp.MustCompile(`\S+`)
	cleanWordRE = regexp.MustCompile(`[^\pL\pN_']`)
	silenceRE   = regexp.MustCompile(`silence_(start|end):\s*([0-9.]+)`)
	languageCodeRE = regexp.MustCompile(`^[a-zA-Z-]{2,12}$`)
)

type config struct {
	Provider      string `json:"provider"`
	APIKey        string `json:"api_key"`
	AssemblyAIKey string `json:"assemblyai_api_key"`
	Model         string `json:"model"`
	Language      string `json:"language"`
	Prompt        string `json:"prompt"`
}

func defaultConfig() config { return config{Provider: "openai", Model: defaultModel} }

func providerLabel(provider string) string {
	if provider == "assemblyai" {
		return "AssemblyAI"
	}
	return "OpenAI"
}

func isAssemblyModel(model string) bool {
	return model == assemblyModelUniversal2 || model == assemblyModelUniversal35Pro || model == assemblyModelUniversal2Diarized || model == assemblyModelUniversal35ProDiarized
}

func isAssemblyDiarizationModel(model string) bool {
	return model == assemblyModelUniversal2Diarized || model == assemblyModelUniversal35ProDiarized
}

func main() {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			log.Fatalf("Missing required media tool: %s. Install ffmpeg and try again.", tool)
		}
	}
	if err := initializeDatabase(); err != nil {
		log.Fatalf("initialize database: %v", err)
	}
	defer database.Close()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8766"
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	server := &http.Server{
		Addr:              host + ":" + port,
		Handler:           routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("server listening address=http://%s", server.Addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server stopped: %v", err)
	}
}

func initializeDatabase() error {
	if err := os.MkdirAll(audioDir, 0700); err != nil {
		return err
	}
	_ = os.Chmod(dataDir, 0700)
	_ = os.Chmod(audioDir, 0700)
	absDB, err := filepath.Abs(filepath.Join(dataDir, "transcriptions.sqlite3"))
	if err != nil {
		return err
	}
	database, err = sql.Open("sqlite", "file:"+filepath.ToSlash(absDB)+"?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	if err = database.Ping(); err != nil {
		return err
	}
	_, err = database.Exec(`CREATE TABLE IF NOT EXISTS transcriptions (
		id TEXT PRIMARY KEY, status TEXT NOT NULL, progress INTEGER NOT NULL DEFAULT 0,
		message TEXT NOT NULL DEFAULT '', transcript TEXT NOT NULL DEFAULT '',
		current_part INTEGER, total_parts INTEGER, duration REAL, filename TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '', file_size INTEGER NOT NULL, model TEXT NOT NULL,
		language TEXT NOT NULL DEFAULT '',
		created REAL NOT NULL, updated REAL NOT NULL)`)
	if err != nil {
		return err
	}
	cols, err := database.Query(`PRAGMA table_info(transcriptions)`)
	if err != nil {
		return err
	}
	hasName, hasLanguage := false, false
	for cols.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err = cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			cols.Close()
			return err
		}
		if name == "name" {
			hasName = true
		}
		if name == "language" {
			hasLanguage = true
		}
	}
	if err = cols.Err(); err != nil {
		cols.Close()
		return err
	}
	cols.Close()
	if !hasName {
		if _, err = database.Exec(`ALTER TABLE transcriptions ADD COLUMN name TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !hasLanguage {
		if _, err = database.Exec(`ALTER TABLE transcriptions ADD COLUMN language TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = database.Exec(`UPDATE transcriptions SET name=filename WHERE name=''`)
	if err != nil {
		return err
	}
	_, err = database.Exec(`CREATE INDEX IF NOT EXISTS transcriptions_updated ON transcriptions(updated DESC)`)
	if err != nil {
		return err
	}
	_, err = database.Exec(`CREATE TABLE IF NOT EXISTS settings (setting_key TEXT PRIMARY KEY, setting_value TEXT NOT NULL)`)
	if err != nil {
		return err
	}
	_, err = database.Exec(`UPDATE transcriptions SET status='interrupted', message='Server restarted before this transcription finished. Any completed part was saved.', updated=? WHERE status IN ('processing','queued')`, time.Now().UnixNano()/1e9)
	if err != nil {
		return err
	}
	_ = os.Chmod(absDB, 0600)
	return nil
}

func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", configHandler)
	mux.HandleFunc("/api/config/validate", validateAPIKeyHandler)
	mux.HandleFunc("/api/translate", translateHandler)
	mux.HandleFunc("/api/jobs", jobsHandler)
	mux.HandleFunc("/api/jobs/", jobHandler)
	mux.HandleFunc("/api/v1/transcriptions", v1CollectionHandler)
	mux.HandleFunc("/api/v1/transcriptions/", v1JobHandler)
	webDir, err := filepath.Abs("web")
	if err != nil {
		log.Fatal(err)
	}
	files := http.FileServer(http.Dir(webDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/" {
			http.ServeFile(w, r, filepath.Join(webDir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
	return requestLogger(mux)
}

type loggedResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *loggedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggedResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += int64(n)
	return n, err
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		logged := &loggedResponseWriter{ResponseWriter: w}
		next.ServeHTTP(logged, r)
		status := logged.status
		if status == 0 {
			status = http.StatusOK
		}
		// Job polling is frequent, so log successful GETs only when they fail.
		if r.Method != http.MethodGet || status >= http.StatusBadRequest {
			log.Printf("http method=%s path=%q status=%d bytes=%d duration=%s", r.Method, r.URL.Path, status, logged.bytes, time.Since(started).Round(time.Millisecond))
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	if status >= http.StatusInternalServerError {
		log.Printf("backend error status=%d message=%q", status, message)
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func configHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c, err := loadConfig()
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, c)
	case http.MethodPost:
		if r.ContentLength <= 0 || r.ContentLength > maxSettingsBody {
			writeError(w, 413, "Invalid settings payload.")
			return
		}
		defer r.Body.Close()
		var values map[string]any
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody))
		if err := dec.Decode(&values); err != nil || values == nil {
			writeError(w, 400, "Invalid settings.")
			return
		}
		c := defaultConfig()
		if v, ok := values["provider"]; ok {
			c.Provider, _ = v.(string)
		}
		if v, ok := values["api_key"]; ok {
			c.APIKey = fmt.Sprint(v)
		}
		if v, ok := values["assemblyai_api_key"]; ok {
			c.AssemblyAIKey = fmt.Sprint(v)
		}
		if v, ok := values["model"]; ok {
			c.Model, _ = v.(string)
		}
		if v, ok := values["language"]; ok {
			c.Language = fmt.Sprint(v)
		}
		if v, ok := values["prompt"]; ok {
			c.Prompt = fmt.Sprint(v)
		}
		c.APIKey = clip(strings.TrimSpace(c.APIKey), 500)
		c.AssemblyAIKey = clip(strings.TrimSpace(c.AssemblyAIKey), 500)
		if c.Provider != "openai" && c.Provider != "assemblyai" {
			writeError(w, 400, "Unsupported transcription provider.")
			return
		}
		c.Language = clip(strings.TrimSpace(c.Language), 20)
		c.Prompt = clip(strings.TrimSpace(c.Prompt), 4000)
		if !models[c.Model] || (c.Provider == "assemblyai" && !isAssemblyModel(c.Model)) || (c.Provider == "openai" && isAssemblyModel(c.Model)) {
			writeError(w, 400, "Unsupported transcription model.")
			return
		}
		saved, err := saveConfig(c)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, saved)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, 405, "Method not allowed.")
	}
}

func validateAPIKeyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	settings, err := loadConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read saved settings.")
		return
	}
	key := strings.TrimSpace(settings.APIKey)
	if settings.Provider == "assemblyai" {
		key = strings.TrimSpace(settings.AssemblyAIKey)
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, "Add an API key in Settings before validating it.")
		return
	}
	endpoint, providerName := openAIModelsURL, "OpenAI"
	if settings.Provider == "assemblyai" {
		endpoint, providerName = assemblyAPI+"?limit=1", "AssemblyAI"
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not prepare API key validation.")
		return
	}
	if settings.Provider == "assemblyai" {
		request.Header.Set("Authorization", key)
	} else {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	started := time.Now()
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		log.Printf("%s API key validation failed elapsed=%s error=%q", providerName, time.Since(started).Round(time.Millisecond), err.Error())
		writeError(w, http.StatusBadGateway, "Could not reach "+providerName+" to verify the key. Check your connection and try again.")
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	log.Printf("%s API key validation response status=%d elapsed=%s", providerName, response.StatusCode, time.Since(started).Round(time.Millisecond))
	switch response.StatusCode {
	case http.StatusOK:
		writeJSON(w, http.StatusOK, map[string]any{"valid": true, "message": providerName + " accepted the API key."})
	case http.StatusUnauthorized:
		writeError(w, http.StatusUnauthorized, providerName+" rejected this API key. Check the key saved in Settings.")
	case http.StatusForbidden:
		writeError(w, http.StatusForbidden, providerName+" denied access. The key may be valid but restricted; check its permissions.")
	case http.StatusTooManyRequests:
		writeError(w, http.StatusTooManyRequests, providerName+" rate-limited the key check. Wait a moment and try again.")
	default:
		writeError(w, http.StatusBadGateway, fmt.Sprintf("%s could not verify the key right now (HTTP %d). Try again.", providerName, response.StatusCode))
	}
}

type translateRequest struct {
	Text   string `json:"text"`
	Source string `json:"source"`
	Target string `json:"target"`
}

func translateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxTranslationBody)
	var request translateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid translation request.")
		return
	}
	request.Text = strings.TrimSpace(request.Text)
	request.Source = strings.TrimSpace(request.Source)
	request.Target = strings.TrimSpace(request.Target)
	if request.Text == "" || request.Source == "" || request.Target == "" {
		writeError(w, http.StatusBadRequest, "Provide transcript text and both languages.")
		return
	}
	if !languageCodeRE.MatchString(request.Source) || !languageCodeRE.MatchString(request.Target) || request.Source == request.Target {
		writeError(w, http.StatusBadRequest, "Choose two different supported languages.")
		return
	}
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("LIBRETRANSLATE_URL")), "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:5001"
	}
	endpoint += "/translate"
	client := &http.Client{Timeout: 10 * time.Minute}
	chunks := splitTranslationText(request.Text, 4500)
	translated := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		form := url.Values{"q": {chunk}, "source": {request.Source}, "target": {request.Target}, "format": {"text"}}
		outbound, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			writeError(w, http.StatusBadGateway, "Could not connect to the local translation engine.")
			return
		}
		outbound.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(outbound)
		if err != nil {
			writeError(w, http.StatusBadGateway, "Could not reach the local translation engine. Restart Clearword and try again.")
			return
		}
		var result struct {
			TranslatedText string `json:"translatedText"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result)
		_ = response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 || decodeErr != nil || result.TranslatedText == "" {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("The local engine could not translate this text (HTTP %d). Check that the selected languages are supported.", response.StatusCode))
			return
		}
		translated = append(translated, result.TranslatedText)
	}
	writeJSON(w, http.StatusOK, map[string]string{"translated_text": strings.Join(translated, "\n\n")})
}

func splitTranslationText(text string, limit int) []string {
	runes := []rune(text)
	var chunks []string
	for len(runes) > limit {
		cut := limit
		for cut > limit*3/4 && !isTranslationBoundary(runes[cut]) {
			cut--
		}
		if cut <= limit*3/4 {
			cut = limit
			for cut > 0 && !isTranslationBoundary(runes[cut]) {
				cut--
			}
		}
		if cut == 0 {
			cut = limit
		}
		if chunk := strings.TrimSpace(string(runes[:cut])); chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = runes[cut:]
		for len(runes) > 0 && isTranslationBoundary(runes[0]) {
			runes = runes[1:]
		}
	}
	if chunk := strings.TrimSpace(string(runes)); chunk != "" {
		chunks = append(chunks, chunk)
	}
	return chunks
}

func isTranslationBoundary(r rune) bool { return r == ' ' || r == '\n' || r == '\r' || r == '\t' }

func loadConfig() (config, error) {
	c := defaultConfig()
	rows, err := database.Query(`SELECT setting_key, setting_value FROM settings`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return c, err
		}
		switch k {
		case "api_key":
			c.APIKey = v
		case "assemblyai_api_key":
			c.AssemblyAIKey = v
		case "provider":
			c.Provider = v
		case "model":
			c.Model = v
		case "language":
			c.Language = v
		case "prompt":
			c.Prompt = v
		}
	}
	return c, rows.Err()
}

func saveConfig(c config) (config, error) {
	tx, err := database.Begin()
	if err != nil {
		return c, err
	}
	for _, item := range [][2]string{{"provider", c.Provider}, {"api_key", c.APIKey}, {"assemblyai_api_key", c.AssemblyAIKey}, {"model", c.Model}, {"language", c.Language}, {"prompt", c.Prompt}} {
		if _, err = tx.Exec(`INSERT INTO settings(setting_key,setting_value) VALUES(?,?) ON CONFLICT(setting_key) DO UPDATE SET setting_value=excluded.setting_value`, item[0], item[1]); err != nil {
			_ = tx.Rollback()
			return c, err
		}
	}
	return c, tx.Commit()
}

func jobsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/jobs" {
		writeError(w, 404, "Not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := listJobs()
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"jobs": jobs})
	case http.MethodPost:
		createJob(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, 405, "Method not allowed.")
	}
}

func v1CollectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/transcriptions" {
		writeError(w, 404, "Not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobsHandler(w, cloneRequestPath(r, "/api/jobs"))
	case http.MethodPost:
		createJob(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, 405, "Method not allowed.")
	}
}

func cloneRequestPath(r *http.Request, path string) *http.Request {
	c := r.Clone(r.Context())
	u := *r.URL
	u.Path = path
	c.URL = &u
	return c
}

func createJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, 405, "Method not allowed.")
		return
	}
	if r.ContentLength <= 0 || r.ContentLength > maxUpload {
		writeError(w, 413, "Choose a media file smaller than 20 GB.")
		return
	}
	c, err := loadConfig()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	key := c.APIKey
	if c.Provider == "assemblyai" {
		key = c.AssemblyAIKey
	}
	if key == "" {
		writeError(w, 400, "Add your "+providerLabel(c.Provider)+" API key first.")
		return
	}
	model := r.Header.Get("X-Model")
	if model == "" {
		model = c.Model
	}
	if !models[model] || (c.Provider == "assemblyai" && !isAssemblyModel(model)) || (c.Provider == "openai" && isAssemblyModel(model)) {
		writeError(w, 400, "Unsupported transcription model.")
		return
	}
	language := r.Header.Get("X-Language")
	if language == "" {
		language = c.Language
	}
	prompt := r.Header.Get("X-Prompt")
	if prompt == "" {
		prompt = c.Prompt
	}
	prompt = clip(strings.TrimSpace(prompt), 4000)
	filename := r.Header.Get("X-Filename")
	if filename == "" {
		filename = "media-file"
	}
	filename = clip(decodeHeader(filename), 255)
	name := clip(strings.TrimSpace(decodeHeader(r.Header.Get("X-Name"))), 200)
	if name == "" {
		name = clip(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)), 200)
	}
	if name == "" {
		name = "Untitled transcription"
	}
	suffix := filepath.Ext(filename)
	if suffix == "" || len([]rune(suffix)) > 12 {
		suffix = ".media"
	}
	id, err := randomID()
	if err != nil {
		writeError(w, 500, "Could not create transcription ID.")
		return
	}
	work, err := os.MkdirTemp("", "transcription-")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	_ = os.Chmod(work, 0700)
	source := filepath.Join(work, "source"+suffix)
	out, err := os.OpenFile(source, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err == nil {
		var written int64
		written, err = io.Copy(out, http.MaxBytesReader(w, r.Body, maxUpload))
		if err == nil && written != r.ContentLength {
			err = errors.New("incomplete upload")
		}
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		_ = os.RemoveAll(work)
		writeError(w, 400, "Upload ended before the whole file was received.")
		return
	}
	job := map[string]any{"status": "processing", "progress": 1, "message": "Upload received…", "transcript": "", "created": float64(time.Now().UnixNano()) / 1e9, "filename": filename, "name": name, "file_size": r.ContentLength, "model": model, "language": language}
	if err = saveJob(id, job, true); err != nil {
		_ = os.RemoveAll(work)
		writeError(w, 500, "Could not save this transcription to SQLite: "+err.Error())
		return
	}
	jobsMu.Lock()
	jobs[id] = job
	jobsMu.Unlock()
	log.Printf("transcription accepted id=%s model=%s language=%q upload_bytes=%d", id, model, language, r.ContentLength)
	go transcribeJob(id, source, work, key, c.Provider, model, strings.TrimSpace(language), prompt)
	writeJSON(w, 202, map[string]string{"id": id, "name": name})
}

func jobHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/v1/transcriptions/") {
		path = "/api/jobs/" + strings.TrimPrefix(path, "/api/v1/transcriptions/")
	}
	if strings.HasSuffix(path, "/audio") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/jobs/"), "/audio")
		if !jobIDRE.MatchString(id) {
			writeError(w, 404, "Not found")
			return
		}
		serveAudio(w, r, id)
		return
	}
	if strings.HasSuffix(path, "/transcript") {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "Method not allowed.")
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/jobs/"), "/transcript")
		serveTranscript(w, id)
		return
	}
	id := strings.TrimPrefix(path, "/api/jobs/")
	if strings.Contains(id, "/") || !jobIDRE.MatchString(id) {
		writeError(w, 404, "Not found")
		return
	}
	if r.Method == http.MethodDelete {
		deleteJob(w, id)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, DELETE")
		writeError(w, 405, "Method not allowed.")
		return
	}
	jobsMu.RLock()
	cached := cloneMap(jobs[id])
	jobsMu.RUnlock()
	if cached == nil {
		var err error
		cached, err = loadJob(id)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
	}
	if cached == nil {
		writeError(w, 404, "This transcription is no longer available.")
		return
	}
	cached["id"] = id
	_, statErr := os.Stat(storedAudioPath(id))
	cached["audio_available"] = statErr == nil
	if r.URL.Query().Get("detail") != "1" {
		delete(cached, "transcript")
	}
	delete(cached, "created")
	delete(cached, "updated")
	writeJSON(w, 200, cached)
}

func deleteJob(w http.ResponseWriter, id string) {
	// Do not remove active jobs: their processing cannot currently be cancelled.
	jobsMu.Lock()
	result, err := database.Exec(`DELETE FROM transcriptions WHERE id=? AND status IN ('complete','error','interrupted')`, id)
	if err != nil {
		jobsMu.Unlock()
		writeError(w, 500, "Could not delete the transcription.")
		return
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		jobsMu.Unlock()
		writeError(w, 500, "Could not confirm deletion of the transcription.")
		return
	}
	if deleted == 0 {
		jobsMu.Unlock()
		job, loadErr := loadJob(id)
		if loadErr != nil {
			writeError(w, 500, "Could not check the transcription status.")
		} else if job == nil {
			writeError(w, 404, "This transcription is no longer available.")
		} else {
			writeError(w, 409, "A transcription can be deleted after processing finishes.")
		}
		return
	}
	delete(jobs, id)
	jobsMu.Unlock()
	if err := os.Remove(storedAudioPath(id)); err != nil && !os.IsNotExist(err) {
		log.Printf("remove audio for deleted transcription %s: %v", id, err)
	}
	log.Printf("transcription deleted id=%s", id)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func v1JobHandler(w http.ResponseWriter, r *http.Request) {
	if strings.TrimPrefix(r.URL.Path, "/api/v1/transcriptions/") == r.URL.Path {
		writeError(w, 404, "Not found")
		return
	}
	jobHandler(w, r)
}

func serveTranscript(w http.ResponseWriter, id string) {
	if !jobIDRE.MatchString(id) {
		writeError(w, 404, "This transcription is no longer available.")
		return
	}
	job, err := loadJob(id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if job == nil {
		writeError(w, 404, "This transcription is no longer available.")
		return
	}
	name, _ := job["name"].(string)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Transcription-ID", id)
	w.Header().Set("X-Transcription-Name", url.QueryEscape(name))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="transcript-%s.txt"`, id))
	_, _ = io.WriteString(w, fmt.Sprint(job["transcript"]))
}

func serveAudio(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, 405, "Method not allowed.")
		return
	}
	var exists int
	err := database.QueryRow(`SELECT 1 FROM transcriptions WHERE id=?`, id).Scan(&exists)
	path := storedAudioPath(id)
	f, openErr := os.Open(path)
	if err != nil || openErr != nil {
		if f != nil {
			f.Close()
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(404)
		} else {
			writeError(w, 404, "Audio is not available yet.")
		}
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	size := stat.Size()
	start, end, status := int64(0), size-1, http.StatusOK
	if requested := r.Header.Get("Range"); requested != "" {
		var first, last string
		if !strings.HasPrefix(requested, "bytes=") || strings.Contains(strings.TrimPrefix(requested, "bytes="), ",") {
			audioRangeError(w, size)
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(requested, "bytes="), "-", 2)
		if len(parts) != 2 {
			audioRangeError(w, size)
			return
		}
		first, last = parts[0], parts[1]
		if first == "" {
			n, e := strconv.ParseInt(last, 10, 64)
			if e != nil || n <= 0 {
				audioRangeError(w, size)
				return
			}
			start = max64(0, size-n)
		} else {
			n, e := strconv.ParseInt(first, 10, 64)
			if e != nil || n < 0 {
				audioRangeError(w, size)
				return
			}
			start = n
			if last != "" {
				epos, e := strconv.ParseInt(last, 10, 64)
				if e != nil || epos < start {
					audioRangeError(w, size)
					return
				}
				end = min64(epos, size-1)
			}
		}
		if start >= size || start > end {
			audioRangeError(w, size)
			return
		}
		status = http.StatusPartialContent
	}
	length := end - start + 1
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=transcription-%s.mp3", id))
	if status == http.StatusPartialContent {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = f.Seek(start, io.SeekStart)
	_, _ = io.CopyN(w, f, length)
}

func audioRangeError(w http.ResponseWriter, size int64) {
	w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
}

func listJobs() ([]map[string]any, error) {
	rows, err := database.Query(`SELECT id,status,progress,message,current_part,total_parts,duration,filename,name,file_size,model,language,created,updated FROM transcriptions ORDER BY created DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, status, message, filename, name, model, language string
		var progress int
		var cp, tp sql.NullInt64
		var duration sql.NullFloat64
		var size int64
		var created, updated float64
		if err := rows.Scan(&id, &status, &progress, &message, &cp, &tp, &duration, &filename, &name, &size, &model, &language, &created, &updated); err != nil {
			return nil, err
		}
		item := map[string]any{"id": id, "status": status, "progress": progress, "message": message, "filename": filename, "name": name, "file_size": size, "model": model, "language": language, "created": created, "updated": updated, "current_part": nullInt(cp), "total_parts": nullInt(tp), "duration": nullFloat(duration)}
		_, e := os.Stat(storedAudioPath(id))
		item["audio_available"] = e == nil
		result = append(result, item)
	}
	return result, rows.Err()
}

func saveJob(id string, values map[string]any, create bool) error {
	now := float64(time.Now().UnixNano()) / 1e9
	if create {
		defaults := map[string]any{"status": "processing", "progress": 1, "message": "Upload received…", "transcript": "", "created": now}
		for k, v := range defaults {
			if _, ok := values[k]; !ok {
				values[k] = v
			}
		}
		values["updated"] = now
		_, err := database.Exec(`INSERT INTO transcriptions(id,status,progress,message,transcript,current_part,total_parts,duration,filename,name,file_size,model,language,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, values["status"], values["progress"], values["message"], values["transcript"], values["current_part"], values["total_parts"], values["duration"], values["filename"], values["name"], values["file_size"], values["model"], values["language"], values["created"], values["updated"])
		return err
	}
	allowed := map[string]bool{"status": true, "progress": true, "message": true, "transcript": true, "current_part": true, "total_parts": true, "duration": true, "filename": true, "name": true, "file_size": true, "model": true, "language": true}
	sets := []string{}
	args := []any{}
	for _, key := range []string{"status", "progress", "message", "transcript", "current_part", "total_parts", "duration", "filename", "name", "file_size", "model", "language"} {
		if value, ok := values[key]; ok && allowed[key] {
			sets = append(sets, key+"=?")
			args = append(args, value)
		}
	}
	sets = append(sets, "updated=?")
	args = append(args, now, id)
	_, err := database.Exec(`UPDATE transcriptions SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return err
}

func loadJob(id string) (map[string]any, error) {
	var status, message, transcript, filename, name, model, language string
	var progress int
	var cp, tp sql.NullInt64
	var duration sql.NullFloat64
	var size int64
	var created, updated float64
	err := database.QueryRow(`SELECT id,status,progress,message,transcript,current_part,total_parts,duration,filename,name,file_size,model,language,created,updated FROM transcriptions WHERE id=?`, id).Scan(new(string), &status, &progress, &message, &transcript, &cp, &tp, &duration, &filename, &name, &size, &model, &language, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": status, "progress": progress, "message": message, "transcript": transcript, "current_part": nullInt(cp), "total_parts": nullInt(tp), "duration": nullFloat(duration), "filename": filename, "name": name, "file_size": size, "model": model, "language": language, "created": created, "updated": updated}, nil
}

func updateJob(id string, values map[string]any) error {
	jobsMu.Lock()
	if j := jobs[id]; j != nil {
		for k, v := range values {
			j[k] = v
		}
	}
	jobsMu.Unlock()
	return saveJob(id, values, false)
}

func transcribeJob(id, source, work, key, provider, model, language, prompt string) {
	defer os.RemoveAll(work)
	started := time.Now()
	log.Printf("transcription started id=%s provider=%s model=%s language=%q", id, provider, model, language)
	fail := func(err error) {
		log.Printf("transcription failed id=%s elapsed=%s error=%q", id, time.Since(started).Round(time.Millisecond), err.Error())
		_ = updateJob(id, map[string]any{"status": "error", "message": err.Error(), "progress": 0})
	}
	_ = updateJob(id, map[string]any{"status": "processing", "message": "Reading media information…", "progress": 3})
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", source).Output()
	if err != nil {
		fail(errors.New("Could not read this media file. Make sure ffmpeg and ffprobe are installed and the file is supported."))
		return
	}
	duration, e := strconv.ParseFloat(strings.TrimSpace(string(probe)), 64)
	if e != nil {
		duration = 0
	}
	if duration < 1 && e == nil {
		duration = 1
	}
	_ = updateJob(id, map[string]any{"duration": duration})
	_ = updateJob(id, map[string]any{"message": "Extracting clear speech audio…", "progress": 7})
	audio := filepath.Join(work, "audio.mp3")
	if err := runCommand([]string{"ffmpeg", "-y", "-i", source, "-vn", "-ac", "1", "-ar", "44100", "-b:a", "128k", "-map_metadata", "-1", audio}); err != nil {
		fail(err)
		return
	}
	if err := persistAudio(id, audio); err != nil {
		fail(err)
		return
	}
	log.Printf("transcription audio ready id=%s duration_seconds=%.1f", id, duration)
	chunks, err := createChunks(audio, work, duration)
	if err != nil {
		fail(err)
		return
	}
	total := len(chunks)
	log.Printf("transcription chunks ready id=%s count=%d", id, total)
	responses := []map[string]any{}
	previous := ""
	for i, chunk := range chunks {
		_ = updateJob(id, map[string]any{"message": fmt.Sprintf("Transcribing audio part %d of %d…", i+1, total), "progress": 12 + int(83*float64(i)/float64(total)), "current_part": i + 1, "total_parts": total})
		chunkStarted := time.Now()
		if info, statErr := os.Stat(chunk.Path); statErr == nil {
			log.Printf("transcription chunk started id=%s part=%d/%d bytes=%d", id, i+1, total, info.Size())
		} else {
			log.Printf("transcription chunk started id=%s part=%d/%d", id, i+1, total)
		}
		var response map[string]any
		var err error
		if provider == "assemblyai" {
			response, err = assemblyAITranscribe(chunk.Path, key, model, language)
		} else {
			response, err = apiTranscribe(chunk.Path, fmt.Sprintf("part-%03d.mp3", i+1), key, model, language, prompt)
		}
		if err != nil {
			fail(fmt.Errorf("Transcription part %d of %d failed: %w", i+1, total, err))
			return
		}
		log.Printf("transcription chunk completed id=%s part=%d/%d elapsed=%s", id, i+1, total, time.Since(chunkStarted).Round(time.Millisecond))
		partial := ""
		isDiarizedModel := model == defaultModel || isAssemblyDiarizationModel(model)
		if isDiarizedModel {
			responses = append(responses, response)
			partial = diarizedTranscript(chunks[:i+1], responses)
		} else {
			text, _ := response["text"].(string)
			text = strings.TrimSpace(text)
			if previous != "" {
				previous = mergeOverlap(previous, text)
			} else {
				previous = text
			}
			partial = previous
		}
		_ = updateJob(id, map[string]any{"progress": 12 + int(83*float64(i+1)/float64(total)), "transcript": partial})
	}
	transcript := previous
	if model == defaultModel || isAssemblyDiarizationModel(model) {
		hasSegments := false
		for _, response := range responses {
			if segs, ok := response["segments"].([]any); ok && len(segs) > 0 {
				hasSegments = true
			}
		}
		if !hasSegments {
			fail(errors.New("The diarization response did not include speaker segments. Try the regular transcription model or another recording format."))
			return
		}
		transcript = diarizedTranscript(chunks, responses)
	}
	partWord := "parts"
	if total == 1 {
		partWord = "part"
	}
	if err := updateJob(id, map[string]any{"status": "complete", "progress": 100, "message": fmt.Sprintf("Transcript ready · %d %s", total, partWord), "transcript": transcript, "duration": duration}); err != nil {
		log.Printf("save completed job %s: %v", id, err)
		return
	}
	log.Printf("transcription completed id=%s parts=%d duration_seconds=%.1f elapsed=%s", id, total, duration, time.Since(started).Round(time.Millisecond))
}

type audioChunk struct {
	Path                           string
	Start, End, KeepStart, KeepEnd float64
}

func runCommand(command []string) error {
	cmd := exec.Command(command[0], command[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := stderr.String()
		if len(detail) > 1800 {
			detail = detail[len(detail)-1800:]
		}
		return fmt.Errorf("Media conversion failed. Check that the file is valid and ffmpeg supports its format. %s", strings.TrimSpace(detail))
	}
	return nil
}

func findSilencePoints(audio string) []float64 {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-i", audio, "-af", "silencedetect=noise=-35dB:d=0.4", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	points := []float64{}
	var start *float64
	for _, m := range silenceRE.FindAllStringSubmatch(stderr.String(), -1) {
		v, e := strconv.ParseFloat(m[2], 64)
		if e != nil {
			continue
		}
		if m[1] == "start" {
			x := v
			start = &x
		} else if start != nil && v > *start {
			points = append(points, (*start+v)/2)
			start = nil
		}
	}
	return points
}

func planBoundaries(duration float64, silences []float64, target int) []float64 {
	bounds := []float64{0}
	nominal := float64(target)
	for nominal < duration-60 {
		choices := []float64{}
		for _, point := range silences {
			if point >= nominal-45 && point <= nominal+45 && point > bounds[len(bounds)-1]+60 {
				choices = append(choices, point)
			}
		}
		boundary := nominal
		if len(choices) > 0 {
			sort.Slice(choices, func(i, j int) bool { return abs(choices[i]-nominal) < abs(choices[j]-nominal) })
			boundary = choices[0]
		}
		bounds = append(bounds, boundary)
		nominal = boundary + float64(target)
	}
	return append(bounds, duration)
}

func createChunks(audio, work string, duration float64) ([]audioChunk, error) {
	info, err := os.Stat(audio)
	if err != nil {
		return nil, err
	}
	if info.Size() <= chunkTarget && duration <= chunkSeconds {
		return []audioChunk{{Path: audio, Start: 0, End: duration, KeepStart: 0, KeepEnd: duration}}, nil
	}
	silences := findSilencePoints(audio)
	target := chunkSeconds
	for attempt := 0; attempt < 3; attempt++ {
		bounds := planBoundaries(duration, silences, target)
		chunks := []audioChunk{}
		failed := false
		for i := 0; i < len(bounds)-1; i++ {
			cutStart, cutEnd := bounds[i], bounds[i+1]
			windowStart := cutStart
			if i > 0 {
				windowStart = maxFloat(0, cutStart-chunkOverlap)
			}
			windowEnd := cutEnd
			if i < len(bounds)-2 {
				windowEnd = minFloat(duration, cutEnd+chunkOverlap)
			}
			path := filepath.Join(work, fmt.Sprintf("chunk-%03d.mp3", i+1))
			cmd := []string{"ffmpeg", "-y", "-ss", fmt.Sprintf("%.3f", windowStart), "-i", audio, "-t", fmt.Sprintf("%.3f", windowEnd-windowStart), "-vn", "-ac", "1", "-ar", "44100", "-b:a", "128k", "-map_metadata", "-1", path}
			if err := runCommand(cmd); err != nil {
				return nil, err
			}
			chunks = append(chunks, audioChunk{path, windowStart, windowEnd, cutStart, cutEnd})
		}
		for _, chunk := range chunks {
			stat, e := os.Stat(chunk.Path)
			if e != nil || stat.Size() >= 24_000_000 {
				failed = true
				break
			}
		}
		if !failed {
			return chunks, nil
		}
		for _, chunk := range chunks {
			_ = os.Remove(chunk.Path)
		}
		target = int(float64(target) * .75)
	}
	return nil, errors.New("Could not make audio chunks small enough for the 25 MB API upload limit.")
}

func apiTranscribe(path, filename, key, model, language, prompt string) (map[string]any, error) {
	// Audio is split into bounded chunks (under 24 MB) before reaching this
	// function, so build the multipart body synchronously in memory. Streaming it
	// through io.Pipe lets an early HTTP/API response close the reader while the
	// multipart writer is still writing, surfacing the unhelpful
	// "io: read/write on closed pipe" error instead of the real response.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{"model": model, "response_format": "json"}
	if model == defaultModel {
		fields["response_format"] = "diarized_json"
		fields["chunking_strategy"] = "auto"
	}
	if language != "" {
		fields["language"] = language
	}
	if prompt != "" && model != defaultModel {
		fields["prompt"] = prompt
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return nil, fmt.Errorf("Could not prepare the audio upload: %w", err)
		}
	}
	safe := regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(filename, "_")
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, safe))
	header.Set("Content-Type", "audio/mpeg")
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, fmt.Errorf("Could not prepare the audio upload: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Could not open the audio chunk: %w", err)
	}
	_, copyErr := io.Copy(part, f)
	closeErr := f.Close()
	if copyErr != nil {
		return nil, fmt.Errorf("Could not prepare the audio upload: %w", copyErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("Could not close the audio chunk: %w", closeErr)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("Could not finish the audio upload: %w", err)
	}
	request, err := http.NewRequest(http.MethodPost, openAIEndpoint, &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{Timeout: 15 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Could not reach the OpenAI API: %v", err)
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if readErr != nil {
		return nil, fmt.Errorf("Could not read the OpenAI response: %w", readErr)
	}
	var result map[string]any
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var detail struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(payload, &detail)
		if detail.Error.Message != "" {
			return nil, errors.New(detail.Error.Message)
		}
		return nil, fmt.Errorf("OpenAI returned HTTP %d.", response.StatusCode)
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func assemblyAITranscribe(path, key, model, language string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Could not open the audio chunk: %w", err)
	}
	defer f.Close()
	uploadReq, err := http.NewRequest(http.MethodPost, assemblyUpload, f)
	if err != nil {
		f.Close()
		return nil, err
	}
	uploadReq.Header.Set("Authorization", key)
	client := &http.Client{Timeout: 15 * time.Minute}
	uploadResp, err := client.Do(uploadReq)
	if err != nil {
		return nil, fmt.Errorf("Could not reach AssemblyAI to upload audio: %v", err)
	}
	var uploaded struct {
		UploadURL string `json:"upload_url"`
		Error     string `json:"error"`
	}
	err = json.NewDecoder(io.LimitReader(uploadResp.Body, 1<<20)).Decode(&uploaded)
	uploadResp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("Could not read the AssemblyAI upload response: %w", err)
	}
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 || uploaded.UploadURL == "" {
		if uploaded.Error == "" {
			uploaded.Error = fmt.Sprintf("HTTP %d", uploadResp.StatusCode)
		}
		return nil, fmt.Errorf("AssemblyAI audio upload failed: %s", uploaded.Error)
	}
	speechModel := "universal-2"
	if model == assemblyModelUniversal35Pro || model == assemblyModelUniversal35ProDiarized {
		speechModel = "universal-3-5-pro"
	}
	requestData := map[string]any{"audio_url": uploaded.UploadURL, "speech_models": []string{speechModel}}
	if isAssemblyDiarizationModel(model) {
		requestData["speaker_labels"] = true
	}
	if code := assemblyLanguageCode(language); code != "" {
		requestData["language_code"] = code
	}
	body, _ := json.Marshal(requestData)
	transcriptReq, err := http.NewRequest(http.MethodPost, assemblyAPI, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	transcriptReq.Header.Set("Authorization", key)
	transcriptReq.Header.Set("Content-Type", "application/json")
	transcriptResp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(transcriptReq)
	if err != nil {
		return nil, fmt.Errorf("Could not submit audio to AssemblyAI: %v", err)
	}
	var job struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	err = json.NewDecoder(io.LimitReader(transcriptResp.Body, 1<<20)).Decode(&job)
	transcriptResp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("Could not read the AssemblyAI transcription response: %w", err)
	}
	if transcriptResp.StatusCode < 200 || transcriptResp.StatusCode >= 300 || job.ID == "" {
		if job.Error == "" {
			job.Error = fmt.Sprintf("HTTP %d", transcriptResp.StatusCode)
		}
		return nil, fmt.Errorf("AssemblyAI could not start transcription: %s", job.Error)
	}
	pollClient := &http.Client{Timeout: 30 * time.Second}
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		pollReq, err := http.NewRequest(http.MethodGet, assemblyAPI+"/"+url.PathEscape(job.ID), nil)
		if err != nil {
			return nil, err
		}
		pollReq.Header.Set("Authorization", key)
		pollResp, err := pollClient.Do(pollReq)
		if err != nil {
			return nil, fmt.Errorf("Could not check AssemblyAI transcription status: %v", err)
		}
		var result struct {
			Status     string `json:"status"`
			Text       string `json:"text"`
			Error      string `json:"error"`
			Utterances []struct {
				Start   float64 `json:"start"`
				End     float64 `json:"end"`
				Text    string  `json:"text"`
				Speaker string  `json:"speaker"`
			} `json:"utterances"`
		}
		err = json.NewDecoder(io.LimitReader(pollResp.Body, 4<<20)).Decode(&result)
		pollResp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("Could not read AssemblyAI transcription status: %w", err)
		}
		if pollResp.StatusCode < 200 || pollResp.StatusCode >= 300 {
			return nil, fmt.Errorf("AssemblyAI status check returned HTTP %d", pollResp.StatusCode)
		}
		switch result.Status {
		case "completed":
			response := map[string]any{"text": result.Text}
			if isAssemblyDiarizationModel(model) {
				segments := make([]any, 0, len(result.Utterances))
				for _, utterance := range result.Utterances {
					segments = append(segments, map[string]any{
						"start": utterance.Start / 1000,
						"end": utterance.End / 1000,
						"text": utterance.Text,
						"speaker": utterance.Speaker,
					})
				}
				response["segments"] = segments
			}
			return response, nil
		case "error":
			return nil, fmt.Errorf("AssemblyAI transcription failed: %s", result.Error)
		}
	}
	return nil, errors.New("AssemblyAI transcription timed out after 30 minutes.")
}

func assemblyLanguageCode(language string) string {
	switch language {
	case "en":
		return "en_us"
	case "fr", "es", "de", "it", "pt", "ja", "zh", "ko", "nl":
		return language
	default:
		return ""
	}
}

type transcriptSegment struct {
	Start, End           float64
	Text, Local, Speaker string
}

func diarizedTranscript(chunks []audioChunk, responses []map[string]any) string {
	nextSpeaker := 1
	output := []transcriptSegment{}
	previous := []transcriptSegment{}
	for i := 0; i < len(chunks) && i < len(responses); i++ {
		chunk := chunks[i]
		current := []transcriptSegment{}
		segments, _ := responses[i]["segments"].([]any)
		for _, raw := range segments {
			segment, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			start, ok1 := number(segment["start"])
			end, ok2 := number(segment["end"])
			if !ok1 || !ok2 {
				continue
			}
			start += chunk.Start
			end += chunk.Start
			text, _ := segment["text"].(string)
			text = strings.TrimSpace(text)
			speaker, _ := segment["speaker"].(string)
			if speaker == "" {
				speaker = "Unknown"
			}
			if text != "" {
				current = append(current, transcriptSegment{Start: start, End: end, Text: text, Local: speaker})
			}
		}
		mapping := map[string]string{}
		if i > 0 {
			type pair struct{ old, new string }
			scores := map[pair]float64{}
			seamStart, seamEnd := chunk.KeepStart-chunkOverlap, chunk.KeepStart+chunkOverlap
			for _, old := range previous {
				if old.Speaker == "" {
					continue
				}
				for _, new := range current {
					overlap := minFloat(minFloat(old.End, new.End), seamEnd) - maxFloat(maxFloat(old.Start, new.Start), seamStart)
					if overlap > 0 {
						scores[pair{old.Speaker, new.Local}] += overlap
					}
				}
			}
			pairs := make([]pair, 0, len(scores))
			for p := range scores {
				pairs = append(pairs, p)
			}
			sort.Slice(pairs, func(a, b int) bool { return scores[pairs[a]] > scores[pairs[b]] })
			used := map[string]bool{}
			for _, p := range pairs {
				if scores[p] >= .25 && mapping[p.new] == "" && !used[p.old] {
					mapping[p.new] = p.old
					used[p.old] = true
				}
			}
		}
		for j := range current {
			if mapping[current[j].Local] == "" {
				mapping[current[j].Local] = fmt.Sprintf("Speaker %d", nextSpeaker)
				nextSpeaker++
			}
			current[j].Speaker = mapping[current[j].Local]
		}
		for _, segment := range current {
			mid := (segment.Start + segment.End) / 2
			if mid < chunk.KeepStart || mid >= chunk.KeepEnd {
				continue
			}
			if len(output) > 0 {
				last := output[len(output)-1]
				if last.Speaker == segment.Speaker && strings.EqualFold(last.Text, segment.Text) && abs(last.Start-segment.Start) < 20 {
					continue
				}
			}
			output = append(output, segment)
		}
		previous = current
	}
	sort.SliceStable(output, func(i, j int) bool { return output[i].Start < output[j].Start })
	lines := make([]string, 0, len(output))
	for _, s := range output {
		lines = append(lines, fmt.Sprintf("[%s] %s: %s", formatTimestamp(s.Start), s.Speaker, s.Text))
	}
	return strings.Join(lines, "\n")
}

func mergeOverlap(previous, current string) string {
	left, right := wordRE.FindAllStringIndex(previous, -1), wordRE.FindAllStringIndex(current, -1)
	limit := minInt(80, minInt(len(left), len(right)))
	overlap := 0
	for count := limit; count > 3; count-- {
		same := true
		any := false
		for j := 0; j < count; j++ {
			a := previous[left[len(left)-count+j][0]:left[len(left)-count+j][1]]
			b := current[right[j][0]:right[j][1]]
			a = cleanWordRE.ReplaceAllString(strings.ToLower(a), "")
			b = cleanWordRE.ReplaceAllString(strings.ToLower(b), "")
			if a != "" {
				any = true
			}
			if a != b {
				same = false
				break
			}
		}
		if same && any {
			overlap = count
			break
		}
	}
	if overlap > 0 {
		current = strings.TrimLeft(current[right[overlap-1][1]:], " \t\r\n,.;:!?—–-…")
	}
	if strings.TrimSpace(current) == "" {
		return previous
	}
	return strings.TrimSpace(strings.TrimRight(previous, " \t\r\n") + " " + strings.TrimLeft(current, " \t\r\n"))
}

func formatTimestamp(seconds float64) string {
	total := int(seconds)
	if total < 0 {
		total = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}
func persistAudio(id, source string) error {
	dst := storedAudioPath(id)
	staging := filepath.Join(audioDir, "."+id+".mp3.tmp")
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(staging, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(staging)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(staging)
		return closeErr
	}
	return os.Rename(staging, dst)
}
func storedAudioPath(id string) string { return filepath.Join(audioDir, id+".mp3") }
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
func decodeHeader(s string) string {
	v, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return v
}
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
func nullFloat(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	default:
		return 0, false
	}
}
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
