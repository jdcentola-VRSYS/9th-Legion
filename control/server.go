package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------- Models ----------
type CPUInfo struct {
	Model string `json:"model"`
	Cores int    `json:"cores"`
}

type GPUInfo struct {
	Name   string `json:"name"`
	VRAMGB int    `json:"vram_gb"`
}

type Capacity struct {
	JobsParallel int `json:"jobs_parallel"`
}

type RegisterRequest struct {
	Hostname     string    `json:"hostname"`
	IP           string    `json:"ip"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch"`
	AgentVersion string    `json:"agent_version"`
	CPU          CPUInfo   `json:"cpu"`
	GPU          []GPUInfo `json:"gpu"`
	RAMGB        int       `json:"ram_gb"`
	UptimeSec    int64     `json:"uptime_sec"`
	PowerW       int       `json:"power_w"`
	Capacity     Capacity  `json:"capacity"`
	Labels       []string  `json:"labels,omitempty"`
}

type NodeRecord struct {
	NodeID       string    `json:"node_id"`
	Hostname     string    `json:"hostname"`
	ReportedIP   string    `json:"reported_ip"`
	PublicIP     string    `json:"public_ip"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch"`
	AgentVersion string    `json:"agent_version"`
	CPU          CPUInfo   `json:"cpu"`
	GPU          []GPUInfo `json:"gpu"`
	RAMGB        int       `json:"ram_gb"`
	UptimeSec    int64     `json:"uptime_sec"`
	PowerW       int       `json:"power_w"`
	Capacity     Capacity  `json:"capacity"`
	Labels       []string  `json:"labels,omitempty"`
	LastSeen     time.Time `json:"last_seen"`
	Status       string    `json:"status"` // online / stale
}

type RegisterResponse struct {
	NodeID               string `json:"node_id"`
	HeartbeatIntervalSec int    `json:"heartbeat_interval_sec"`
	Message              string `json:"message"`
}

type HeartbeatResponse struct {
	Status  string `json:"status"`
	Time    string `json:"time"`
	Message string `json:"message"`
}

// Agent heartbeat payload (keep it small)
type AgentHeartbeat struct {
	NodeID    string `json:"node_id"`
	UptimeSec int64  `json:"uptime_sec,omitempty"`
	PowerW    int    `json:"power_w,omitempty"`
}

// ---------- Globals ----------
var (
	mu                sync.Mutex
	registry          = map[string]*NodeRecord{}
	heartbeatInterval = 30 // seconds
	staleAfter        = 2 * time.Duration(heartbeatInterval) * time.Second
)

// ---------- Helpers ----------
func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("ts%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func getPublicIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func requireKey(w http.ResponseWriter, r *http.Request) bool {
	want := os.Getenv("LEGION_KEY")
	got := r.Header.Get("X-LEGION-KEY")
	if want == "" {
		return true // dev mode
	}
	if got == "" || got != want {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// ---------- Handlers ----------
func heartbeatHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HeartbeatResponse{
		Status:  "ok",
		Time:    time.Now().Format(time.RFC3339),
		Message: "9th Legion Control Node active",
	})
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireKey(w, r) {
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}

	publicIP := getPublicIP(r)

	mu.Lock()
	defer mu.Unlock()

	// Idempotent: match existing by hostname + reported IP
	var node *NodeRecord
	for _, n := range registry {
		if n.Hostname == req.Hostname && n.ReportedIP == req.IP {
			node = n
			break
		}
	}
	if node == nil {
		node = &NodeRecord{NodeID: randomID(8)}
		registry[node.NodeID] = node
	}

	node.Hostname = req.Hostname
	node.ReportedIP = req.IP
	node.PublicIP = publicIP
	node.OS = req.OS
	node.Arch = req.Arch
	node.AgentVersion = req.AgentVersion
	node.CPU = req.CPU
	node.GPU = req.GPU
	node.RAMGB = req.RAMGB
	node.UptimeSec = req.UptimeSec
	node.PowerW = req.PowerW
	node.Capacity = req.Capacity
	node.Labels = req.Labels
	node.LastSeen = time.Now().UTC()
	node.Status = "online"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(RegisterResponse{
		NodeID:               node.NodeID,
		HeartbeatIntervalSec: heartbeatInterval,
		Message:              "registered",
	})
}

func listNodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	mu.Lock()
	defer mu.Unlock()

	out := make([]NodeRecord, 0, len(registry))
	for _, n := range registry {
		out = append(out, *n)
	}
	json.NewEncoder(w).Encode(out)
}

func agentHeartbeatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireKey(w, r) {
		return
	}

	var hb AgentHeartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if hb.NodeID == "" {
		http.Error(w, "node_id required", http.StatusBadRequest)
		return
	}

	mu.Lock()
	defer mu.Unlock()

	node, ok := registry[hb.NodeID]
	if !ok {
		http.Error(w, "unknown node_id", http.StatusNotFound)
		return
	}

	// Optional live updates
	if hb.UptimeSec > 0 {
		node.UptimeSec = hb.UptimeSec
	}
	if hb.PowerW > 0 {
		node.PowerW = hb.PowerW
	}
	node.LastSeen = time.Now().UTC()
	node.Status = "online"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":                 "ok",
		"next_heartbeat_seconds": heartbeatInterval,
		"server_time":            time.Now().Format(time.RFC3339),
	})
}

// background: mark nodes stale if they stop pinging
func startStaleMonitor() {
	ticker := time.NewTicker(15 * time.Second)
	go func() {
		for range ticker.C {
			now := time.Now().UTC()
			mu.Lock()
			for _, n := range registry {
				if now.Sub(n.LastSeen) > staleAfter {
					n.Status = "stale"
				}
			}
			mu.Unlock()
		}
	}()
}

func main() {
	http.HandleFunc("/heartbeat", heartbeatHandler)
	http.HandleFunc("/register", registerHandler)              // POST
	http.HandleFunc("/nodes", listNodesHandler)                // GET
	http.HandleFunc("/agent/heartbeat", agentHeartbeatHandler) // POST

	startStaleMonitor()

	fmt.Println("Legion Control listening on port 8081...")
	http.ListenAndServe(":8081", nil)
}
