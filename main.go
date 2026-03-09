package main

import (
"crypto/rand"
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
)

type User struct {
Username string `json:"username"`
Password string `json:"password"`
Token    string `json:"token"`
ServerIP string `json:"server_ip"`
ServerPort string `json:"server_port"`
}

type ProxyInstance struct {
Cmd     *exec.Cmd
LogFile *os.File
Running bool
}

var (
users   = map[string]*User{}
proxies = map[string]*ProxyInstance{}
mu      sync.Mutex
dataDir = "./data"
)

func randomToken() string {
b := make([]byte, 16)
rand.Read(b)
return hex.EncodeToString(b)
}

func saveUsers() {
data, _ := json.Marshal(users)
os.WriteFile(filepath.Join(dataDir, "users.json"), data, 0644)
}

func loadUsers() {
data, err := os.ReadFile(filepath.Join(dataDir, "users.json"))
if err != nil {
return
}
json.Unmarshal(data, &users)
}

func authMiddleware(r *http.Request) *User {
token := r.Header.Get("Authorization")
for _, u := range users {
if u.Token == token {
return u
}
}
return nil
}

func main() {
os.MkdirAll(dataDir, 0755)
loadUsers()

http.HandleFunc("/api/register", func(w http.ResponseWriter, r *http.Request) {
if r.Method != "POST" { return }
var req struct{ Username, Password string }
json.NewDecoder(r.Body).Decode(&req)
mu.Lock()
defer mu.Unlock()
if _, exists := users[req.Username]; exists {
http.Error(w, "user exists", 400)
return
}
token := randomToken()
users[req.Username] = &User{Username: req.Username, Password: req.Password, Token: token}
os.MkdirAll(filepath.Join(dataDir, req.Username, "schematics"), 0755)
saveUsers()
json.NewEncoder(w).Encode(map[string]string{"token": token})
})

http.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
if r.Method != "POST" { return }
var req struct{ Username, Password string }
json.NewDecoder(r.Body).Decode(&req)
mu.Lock()
defer mu.Unlock()
u, exists := users[req.Username]
if !exists || u.Password != req.Password {
http.Error(w, "invalid credentials", 401)
return
}
json.NewEncoder(w).Encode(map[string]string{"token": u.Token})
})

http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
u := authMiddleware(r)
if u == nil { http.Error(w, "unauthorized", 401); return }
if r.Method == "POST" {
var req struct{ ServerIP, ServerPort string }
json.NewDecoder(r.Body).Decode(&req)
mu.Lock()
u.ServerIP = req.ServerIP
u.ServerPort = req.ServerPort
saveUsers()
mu.Unlock()
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
http.Error(w, "proxy already running", 400)
return
}
if u.ServerIP == "" { http.Error(w, "no server configured", 400); return }
logPath := filepath.Join(dataDir, u.Username, "proxy.log")
logFile, _ := os.Create(logPath)
schematicDir := filepath.Join(dataDir, u.Username, "schematics")
cmd := exec.Command("./mcproxy-linux-amd64",
"--server", fmt.Sprintf("%s:%s", u.ServerIP, u.ServerPort),
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
// last 5000 bytes
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
