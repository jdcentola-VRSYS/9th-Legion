package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	vastclient "9th-legion/control/vast"
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

// /api/status response
type StatusResponse struct {
	OK           bool   `json:"ok"`
	Time         string `json:"time"`
	UptimeSec    int64  `json:"uptime_sec"`
	NodeCount    int    `json:"node_count"`
	ProviderMode string `json:"provider_mode"`
}

// ---------- Globals ----------

var (
	mu                sync.Mutex
	registry          = map[string]*NodeRecord{}
	heartbeatInterval = 30 // seconds
	staleAfter        = 2 * time.Duration(heartbeatInterval) * time.Second

	startedAt    = time.Now().UTC()
	providerMode = "standalone"
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

// Simple key check for agent endpoints.
// Uses LEGION_KEY + X-LEGION-KEY header. (Dev mode: no key set = allow.)
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

// ---------- Handlers (core) ----------

// /heartbeat  (legacy)
// /api/status (new)
func heartbeatHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HeartbeatResponse{
		Status:  "ok",
		Time:    time.Now().Format(time.RFC3339),
		Message: "9th Legion Control Node active",
	})
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	mu.Lock()
	nodeCount := len(registry)
	mu.Unlock()

	resp := StatusResponse{
		OK:           true,
		Time:         time.Now().UTC().Format(time.RFC3339),
		UptimeSec:    int64(time.Since(startedAt).Seconds()),
		NodeCount:    nodeCount,
		ProviderMode: providerMode,
	}

	_ = json.NewEncoder(w).Encode(resp)
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
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

// ---------- Vast Offers (read-only adapter) ----------

// GET /api/vast/offers?limit=20
func vastOffersHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("[VAST] /api/vast/offers request received")

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Optional limit param
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	client, err := vastclient.NewFromEnv()
	if err != nil {
		fmt.Println("[VAST] client init error:", err)
		http.Error(w, "vast api not configured (set VAST_API_KEY)", http.StatusBadGateway)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	offers, err := client.SearchOffers(ctx, limit)
	if err != nil {
		fmt.Println("[VAST] search offers error:", err)
		http.Error(w, "failed to fetch offers from vast", http.StatusBadGateway)
		return
	}

	fmt.Printf("[VAST] fetched %d offers from Vast\n", len(offers))

	resp := struct {
		Count  int                `json:"count"`
		Offers []vastclient.Offer `json:"offers"`
	}{
		Count:  len(offers),
		Offers: offers,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		fmt.Println("[VAST] encode response error:", err)
	}
}

// ---------- Background ----------

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

// ---------- main ----------

func main() {
	startedAt = time.Now().UTC()
	if mode := os.Getenv("PROVIDER_MODE"); mode != "" {
		providerMode = mode
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8081"
	}

	mux := http.NewServeMux()

	// Health + status
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/api/status", statusHandler)

	// Legacy endpoints (keep working)
	mux.HandleFunc("/heartbeat", heartbeatHandler)
	mux.HandleFunc("/register", registerHandler)              // POST
	mux.HandleFunc("/nodes", listNodesHandler)                // GET
	mux.HandleFunc("/agent/heartbeat", agentHeartbeatHandler) // POST

	// New standardized API paths for agents
	mux.HandleFunc("/api/agents/register", registerHandler)        // POST
	mux.HandleFunc("/api/agents/heartbeat", agentHeartbeatHandler) // POST

	// Vast read-only marketplace endpoint
	mux.HandleFunc("/api/vast/offers", vastOffersHandler) // GET

	startStaleMonitor()

	fmt.Printf("Legion Control listening on %s (provider_mode=%s)...\n", listenAddr, providerMode)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		fmt.Println("server error:", err)
	}
}
