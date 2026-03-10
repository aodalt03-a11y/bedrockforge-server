package main

import (
"crypto/rand"
"database/sql"
"encoding/hex"
"encoding/json"
"fmt"
"io"
"log"
"net/http"
"os"
"os/exec"
"path/filepath"
"sync"
"time"

_ "github.com/tursodatabase/libsql-client-go/libsql"
)

type User struct {
Username   string
Password   string
Token      string
ServerIP   string
ServerPort string
}

type ProxyInstance struct {
Cmd     *exec.Cmd
LogFile *os.File
Running bool
}

var (
db      *sql.DB
proxies = map[string]*ProxyInstance{}
mu      sync.Mutex
dataDir = "./data"
)

func randomToken() string {
b := make([]byte, 16)
rand.Read(b)
return hex.EncodeToString(b)
}

func initDB() {
dbURL := os.Getenv("TURSO_URL")
dbToken := os.Getenv("TURSO_TOKEN")
url := dbURL + "?authToken=" + dbToken
log.Printf("Connecting to: %s", dbURL)
var err error
db, err = sql.Open("libsql", url)
if err != nil {
log.Fatal("failed to open db:", err)
}
db.SetMaxOpenConns(1)
_, err = db.Exec(`CREATE TABLE IF NOT EXISTS users (
username TEXT PRIMARY KEY,
password TEXT,
token TEXT,
server_ip TEXT DEFAULT '',
server_port TEXT DEFAULT ''
)`)
if err != nil {
log.Printf("TURSO_URL: %s", os.Getenv("TURSO_URL"))
	log.Fatalf("failed to create table: %v", err)
}
log.Println("Database connected")
}

func authMiddleware(r *http.Request) *User {
token := r.Header.Get("Authorization")
row := db.QueryRow("SELECT username, password, token, server_ip, server_port FROM users WHERE token = ?", token)
u := &User{}
if err := row.Scan(&u.Username, &u.Password, &u.Token, &u.ServerIP, &u.ServerPort); err != nil {
return nil
}
return u
}

func main() {
os.MkdirAll(dataDir, 0755)
initDB()

http.HandleFunc("/api/register", func(w http.ResponseWriter, r *http.Request) {
if r.Method != "POST" { return }
var req struct{ Username, Password string }
json.NewDecoder(r.Body).Decode(&req)
if req.Username == "" || req.Password == "" {
http.Error(w, "missing fields", 400); return
}
token := randomToken()
_, err := db.Exec("INSERT INTO users (username, password, token) VALUES (?, ?, ?)",
req.Username, req.Password, token)
if err != nil {
http.Error(w, "user exists", 400); return
}
os.MkdirAll(filepath.Join(dataDir, req.Username, "schematics"), 0755)
json.NewEncoder(w).Encode(map[string]string{"token": token})
})

http.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
if r.Method != "POST" { return }
var req struct{ Username, Password string }
json.NewDecoder(r.Body).Decode(&req)
row := db.QueryRow("SELECT token FROM users WHERE username = ? AND password = ?", req.Username, req.Password)
var token string
if err := row.Scan(&token); err != nil {
http.Error(w, "invalid credentials", 401); return
}
json.NewEncoder(w).Encode(map[string]string{"token": token})
})

http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
if r.Method == "POST" {
var req struct{ ServerIP, ServerPort string }
json.NewDecoder(r.Body).Decode(&req)
db.Exec("UPDATE users SET server_ip = ?, server_port = ? WHERE username = ?",
req.ServerIP, req.ServerPort, u.Username)
json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
} else {
json.NewEncoder(w).Encode(map[string]string{"server_ip": u.ServerIP, "server_port": u.ServerPort})
}
})

http.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
r.ParseMultipartForm(50 << 20)
file, header, err := r.FormFile("file")
if err != nil { http.Error(w, err.Error(), 400); return }
defer file.Close()
os.MkdirAll(filepath.Join(dataDir, u.Username, "schematics"), 0755)
dst, _ := os.Create(filepath.Join(dataDir, u.Username, "schematics", header.Filename))
defer dst.Close()
io.Copy(dst, file)
json.NewEncoder(w).Encode(map[string]string{"status": "uploaded", "file": header.Filename})
})

http.HandleFunc("/api/schematics", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
entries, _ := os.ReadDir(filepath.Join(dataDir, u.Username, "schematics"))
var files []string
for _, e := range entries {
files = append(files, e.Name())
}
json.NewEncoder(w).Encode(files)
})

http.HandleFunc("/api/proxy/start", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
mu.Lock()
defer mu.Unlock()
if p, exists := proxies[u.Username]; exists && p.Running {
http.Error(w, "proxy already running", 400); return
}
row := db.QueryRow("SELECT server_ip, server_port FROM users WHERE username = ?", u.Username)
var ip, port string
row.Scan(&ip, &port)
if ip == "" { http.Error(w, "no server configured", 400); return }
logPath := filepath.Join(dataDir, u.Username, "proxy.log")
os.MkdirAll(filepath.Join(dataDir, u.Username), 0755)
logFile, _ := os.Create(logPath)
schematicDir := filepath.Join(dataDir, u.Username, "schematics")
cmd := exec.Command("./mcproxy-linux-amd64",
"--server", fmt.Sprintf("%s:%s", ip, port),
"--schematic-dir", schematicDir,
)
cmd.Stdout = logFile
cmd.Stderr = logFile
cmd.Start()
proxies[u.Username] = &ProxyInstance{Cmd: cmd, LogFile: logFile, Running: true}
go func() {
cmd.Wait()
mu.Lock()
if p, ok := proxies[u.Username]; ok { p.Running = false }
mu.Unlock()
}()
json.NewEncoder(w).Encode(map[string]string{"status": "started"})
})

http.HandleFunc("/api/proxy/stop", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
mu.Lock()
defer mu.Unlock()
p, exists := proxies[u.Username]
if !exists || !p.Running { http.Error(w, "not running", 400); return }
p.Cmd.Process.Kill()
p.Running = false
json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
})

http.HandleFunc("/api/proxy/status", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
mu.Lock()
defer mu.Unlock()
p, exists := proxies[u.Username]
running := exists && p.Running
json.NewEncoder(w).Encode(map[string]bool{"running": running})
})

http.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
logPath := filepath.Join(dataDir, u.Username, "proxy.log")
data, err := os.ReadFile(logPath)
if err != nil { json.NewEncoder(w).Encode(map[string]string{"logs": ""}); return }
if len(data) > 5000 { data = data[len(data)-5000:] }
json.NewEncoder(w).Encode(map[string]string{"logs": string(data)})
})

http.HandleFunc("/api/logs/download", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
logPath := filepath.Join(dataDir, u.Username, "proxy.log")
w.Header().Set("Content-Disposition", "attachment; filename=proxy.log")
http.ServeFile(w, r, logPath)
})

http.Handle("/", http.FileServer(http.Dir("./static")))

port := os.Getenv("PORT")
if port == "" { port = "8080" }
log.Printf("Server starting on port %s", port)
_ = time.Now()
http.ListenAndServe(":"+port, nil)
}
