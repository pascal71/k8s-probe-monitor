package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Build variables - set via ldflags
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

func init() {
	// Log version info at startup
	log.Printf("Version info - Version: %s, Commit: %s, BuildTime: %s", Version, GitCommit, BuildTime)
}

type PodInfo struct {
	PodName      string      `json:"podName"`
	PodIP        string      `json:"podIP"`
	NodeHostname string      `json:"nodeHostname"`
	ContainerAge int64       `json:"containerAge"`
	StartTime    string      `json:"startTime"`
	ProbeStatus  ProbeStatus `json:"probeStatus"`
	StartupDelay int         `json:"startupDelay"`
	StartupReady string      `json:"startupReady"`
}

type ProbeStatus struct {
	Started bool `json:"started"`
	Live    bool `json:"live"`
	Ready   bool `json:"ready"`
}

type PodStatusInfo struct {
	Name         string
	IP           string
	Node         string
	Status       string
	Info         *PodInfo
	Error        string
	LastCheck    time.Time
	ReplicaSetID string
}

type Dashboard struct {
	pods      map[string]*PodStatusInfo
	mu        sync.RWMutex
	clientset *kubernetes.Clientset
}

func NewDashboard() (*Dashboard, error) {
	config, err := getKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	return &Dashboard{
		pods:      make(map[string]*PodStatusInfo),
		clientset: clientset,
	}, nil
}

func getKubeConfig() (*rest.Config, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// Fall back to kubeconfig file (for local development)
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	if envConfig := os.Getenv("KUBECONFIG"); envConfig != "" {
		kubeconfig = envConfig
	}

	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	return config, nil
}

func (d *Dashboard) monitorPods(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.updatePodStatuses(ctx)
		}
	}
}

func (d *Dashboard) updatePodStatuses(ctx context.Context) {
	// Get pods with label app=probe-demo
	pods, err := d.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: "app=probe-demo",
	})
	if err != nil {
		log.Printf("Error listing pods: %v", err)
		return
	}

	currentPods := make(map[string]bool)

	for _, pod := range pods.Items {
		currentPods[pod.Name] = true

		// Extract ReplicaSet ID from pod name (format: name-replicasetid-podid)
		replicaSetID := ""
		parts := strings.Split(pod.Name, "-")
		if len(parts) >= 2 {
			// Get the second-to-last part as replica set ID
			replicaSetID = parts[len(parts)-2]
		}

		podStatus := &PodStatusInfo{
			Name:         pod.Name,
			IP:           pod.Status.PodIP,
			Node:         pod.Spec.NodeName,
			Status:       string(pod.Status.Phase),
			LastCheck:    time.Now(),
			ReplicaSetID: replicaSetID,
		}

		// Only query running pods with an IP
		if pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			info, err := d.getPodInfo(pod.Status.PodIP)
			if err != nil {
				podStatus.Error = err.Error()
			} else {
				podStatus.Info = info
			}
		}

		d.mu.Lock()
		d.pods[pod.Name] = podStatus
		d.mu.Unlock()
	}

	// Remove pods that no longer exist
	d.mu.Lock()
	for name := range d.pods {
		if !currentPods[name] {
			delete(d.pods, name)
		}
	}
	d.mu.Unlock()
}

func (d *Dashboard) getPodInfo(podIP string) (*PodInfo, error) {
	url := fmt.Sprintf("http://%s:8080/api/info", podIP)

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var info PodInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %v", err)
	}

	return &info, nil
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, r *http.Request) {
	tmpl := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Pod Monitor Dashboard</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: #0a0a0a;
            color: #e0e0e0;
            line-height: 1.6;
        }
        
        .container {
            max-width: 1400px;
            margin: 0 auto;
            padding: 20px;
        }
        
        h1 {
            text-align: center;
            color: #ffffff;
            margin-bottom: 20px;
            font-size: 2.5em;
            text-shadow: 0 0 20px rgba(100, 200, 255, 0.5);
        }
        
        .version-info {
            text-align: center;
            color: #fff;
            font-size: 1em;
            margin-bottom: 20px;
            background: rgba(0, 212, 255, 0.2);
            border: 2px solid rgba(0, 212, 255, 0.5);
            border-radius: 25px;
            padding: 10px 30px;
            display: inline-block;
            left: 50%;
            transform: translateX(-50%);
            position: relative;
            box-shadow: 0 4px 20px rgba(0, 212, 255, 0.3);
        }
        
        .version-info span {
            margin: 0 15px;
            color: #fff;
            font-weight: 500;
        }
        
        .controls {
            text-align: center;
            margin-bottom: 30px;
            padding: 20px;
            background: rgba(255, 255, 255, 0.05);
            border-radius: 15px;
            border: 2px solid rgba(0, 212, 255, 0.3);
        }
        
        .refresh-control {
            display: inline-flex;
            align-items: center;
            gap: 20px;
            background: rgba(0, 255, 136, 0.1);
            border: 2px solid rgba(0, 255, 136, 0.5);
            border-radius: 30px;
            padding: 15px 30px;
            box-shadow: 0 6px 25px rgba(0, 255, 136, 0.3);
        }
        
        .refresh-control label {
            color: #00ff88;
            font-size: 1.2em;
            font-weight: 600;
            text-shadow: 0 0 10px rgba(0, 255, 136, 0.5);
        }
        
        .refresh-control input[type="range"] {
            width: 150px;
            height: 8px;
            background: linear-gradient(to right, #444 0%, #666 100%);
            outline: none;
            border-radius: 10px;
            -webkit-appearance: none;
            cursor: pointer;
            box-shadow: inset 0 2px 4px rgba(0, 0, 0, 0.3);
        }
        
        .refresh-control input[type="range"]::-webkit-slider-thumb {
            -webkit-appearance: none;
            width: 24px;
            height: 24px;
            background: radial-gradient(circle, #00ff88 0%, #00d4ff 100%);
            border-radius: 50%;
            cursor: pointer;
            box-shadow: 0 0 20px rgba(0, 255, 136, 0.8);
            transition: all 0.2s ease;
            border: 2px solid #fff;
        }
        
        .refresh-control input[type="range"]::-webkit-slider-thumb:hover {
            transform: scale(1.3);
            box-shadow: 0 0 30px rgba(0, 255, 136, 1);
        }
        
        .refresh-control input[type="range"]::-moz-range-thumb {
            width: 24px;
            height: 24px;
            background: radial-gradient(circle, #00ff88 0%, #00d4ff 100%);
            border-radius: 50%;
            cursor: pointer;
            border: 2px solid #fff;
            box-shadow: 0 0 20px rgba(0, 255, 136, 0.8);
            transition: all 0.2s ease;
        }
        
        .refresh-control input[type="range"]::-moz-range-thumb:hover {
            transform: scale(1.3);
            box-shadow: 0 0 30px rgba(0, 255, 136, 1);
        }
        
        .refresh-control span {
            color: #00ff88;
            font-weight: bold;
            font-size: 1.1em;
            min-width: 35px;
            text-align: center;
        }
        
        .grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(400px, 1fr));
            gap: 20px;
        }
        
        .pod-card {
            background: linear-gradient(135deg, #1a1a2e 0%, #16213e 100%);
            border-radius: 15px;
            padding: 25px;
            box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3), 0 0 0 1px rgba(255, 255, 255, 0.1);
            transition: transform 0.3s ease, box-shadow 0.3s ease;
            position: relative;
            overflow: hidden;
        }
        
        .pod-card:hover {
            transform: translateY(-5px);
            box-shadow: 0 12px 40px rgba(0, 0, 0, 0.4), 0 0 0 1px rgba(255, 255, 255, 0.2);
        }
        
        .pod-card::before {
            content: '';
            position: absolute;
            top: 0;
            left: 0;
            right: 0;
            height: 4px;
            background: linear-gradient(90deg, #00ff88, #00d4ff);
        }
        
        .pod-card.error::before {
            background: linear-gradient(90deg, #ff4444, #ff6666);
        }
        
        .pod-card.not-ready::before {
            background: linear-gradient(90deg, #ff9800, #ffc107);
        }
        
        .pod-name {
            font-size: 1.4em;
            font-weight: bold;
            color: #00d4ff;
            margin-bottom: 15px;
            word-break: break-all;
        }
        
        .replica-set-id {
            font-size: 0.8em;
            color: #888;
            margin-top: -10px;
            margin-bottom: 15px;
        }
        
        .info-grid {
            display: grid;
            gap: 10px;
        }
        
        .info-row {
            display: flex;
            justify-content: space-between;
            padding: 8px 0;
            border-bottom: 1px solid rgba(255, 255, 255, 0.1);
        }
        
        .info-label {
            color: #888;
            font-size: 0.9em;
        }
        
        .info-value {
            color: #fff;
            font-weight: 500;
            text-align: right;
            word-break: break-all;
        }
        
        .probe-status {
            display: flex;
            gap: 15px;
            margin-top: 15px;
            padding-top: 15px;
            border-top: 1px solid rgba(255, 255, 255, 0.1);
        }
        
        .probe-indicator {
            display: flex;
            align-items: center;
            gap: 5px;
            font-size: 0.9em;
            cursor: pointer;
            padding: 5px 10px;
            border-radius: 15px;
            transition: all 0.2s ease;
            user-select: none;
        }
        
        .probe-indicator:hover {
            background: rgba(255, 255, 255, 0.1);
            transform: scale(1.05);
        }
        
        .probe-indicator:active {
            transform: scale(0.95);
        }
        
        .probe-dot {
            width: 12px;
            height: 12px;
            border-radius: 50%;
            background: #444;
            transition: all 0.3s ease;
            box-shadow: inset 0 2px 4px rgba(0, 0, 0, 0.3);
        }
        
        .probe-dot.active {
            background: #00ff88;
            box-shadow: 0 0 15px rgba(0, 255, 136, 0.8), inset 0 -2px 4px rgba(0, 0, 0, 0.2);
        }
        
        .error-message {
            background: rgba(255, 68, 68, 0.1);
            border: 1px solid rgba(255, 68, 68, 0.3);
            border-radius: 8px;
            padding: 10px;
            margin-top: 15px;
            color: #ff6666;
            font-size: 0.9em;
        }
        
        .last-check {
            text-align: center;
            color: #666;
            font-size: 0.8em;
            margin-top: 15px;
        }
        
        .refresh-indicator {
            position: fixed;
            top: 20px;
            right: 20px;
            background: rgba(0, 212, 255, 0.1);
            border: 1px solid rgba(0, 212, 255, 0.3);
            border-radius: 50%;
            width: 40px;
            height: 40px;
            display: flex;
            align-items: center;
            justify-content: center;
            animation: pulse 2s infinite;
        }
        
        @keyframes pulse {
            0% { transform: scale(1); opacity: 1; }
            50% { transform: scale(1.1); opacity: 0.7; }
            100% { transform: scale(1); opacity: 1; }
        }
        
        .no-pods {
            text-align: center;
            color: #666;
            font-size: 1.2em;
            margin-top: 100px;
        }
    </style>
    <script>
        let refreshInterval = 1000; // Default 1 second
        let refreshTimer;
        
        function formatDuration(ms) {
            const seconds = Math.floor(ms / 1000);
            const minutes = Math.floor(seconds / 60);
            const hours = Math.floor(minutes / 60);
            const days = Math.floor(hours / 24);
            
            if (days > 0) return days + 'd ' + (hours % 24) + 'h';
            if (hours > 0) return hours + 'h ' + (minutes % 60) + 'm';
            if (minutes > 0) return minutes + 'm ' + (seconds % 60) + 's';
            return seconds + 's';
        }
        
        function formatTime(dateStr) {
            return new Date(dateStr).toLocaleString();
        }
        
        function updateRefreshInterval(value) {
            refreshInterval = value * 1000;
            document.getElementById('refresh-value').textContent = value + 's';
            
            // Clear existing timer and set new one
            if (refreshTimer) {
                clearInterval(refreshTimer);
            }
            refreshTimer = setInterval(refreshPage, refreshInterval);
            
            // Save preference
            localStorage.setItem('refreshInterval', value);
        }
        
        function refreshPage() {
            location.reload();
        }
        
        async function toggleProbe(podIP, probeType, currentState) {
            const action = currentState ? 'fail' : 'recover';
            const url = ` + "`" + `http://${podIP}:8080/api/probes/${probeType}/${action}` + "`" + `;
            
            try {
                // Make the API call through a proxy endpoint on our server
                const response = await fetch(` + "`" + `/api/proxy` + "`" + `, {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify({
                        url: url,
                        method: 'POST'
                    })
                });
                
                if (response.ok) {
                    // Refresh the page after a short delay to see the change
                    setTimeout(refreshPage, 500);
                } else {
                    console.error('Failed to toggle probe:', await response.text());
                }
            } catch (error) {
                console.error('Error toggling probe:', error);
            }
        }
        
        // Initialize on page load
        window.onload = function() {
            // Restore saved refresh interval or use default of 1 second
            const savedInterval = localStorage.getItem('refreshInterval');
            const defaultInterval = savedInterval || '1';
            const slider = document.getElementById('refresh-slider');
            slider.value = defaultInterval;
            updateRefreshInterval(parseInt(defaultInterval));
        };
    </script>
</head>
<body>
    <div class="container">
        <h1>🚀 Pod Monitor Dashboard</h1>
        <div class="version-info">
            <span>Version: {{.Version}}</span>
            <span>•</span>
            <span>Commit: {{.GitCommit}}</span>
            <span>•</span>
            <span>Built: {{.BuildTime}}</span>
        </div>
        <div class="controls">
            <div class="refresh-control">
                <label for="refresh-slider">Refresh Interval:</label>
                <input type="range" id="refresh-slider" min="1" max="10" value="1" onchange="updateRefreshInterval(this.value)">
                <span id="refresh-value">1s</span>
            </div>
        </div>
        <div class="refresh-indicator">🔄</div>
        
        {{if .Pods}}
        <div class="grid">
            {{range .Pods}}
            <div class="pod-card {{if .Error}}error{{else if not .Info.ProbeStatus.Ready}}not-ready{{end}}">
                <div class="pod-name">{{.Name}}</div>
                <div class="replica-set-id">ReplicaSet: {{.ReplicaSetID}}</div>
                
                <div class="info-grid">
                    <div class="info-row">
                        <span class="info-label">Status</span>
                        <span class="info-value">{{.Status}}</span>
                    </div>
                    <div class="info-row">
                        <span class="info-label">Pod IP</span>
                        <span class="info-value"><a href="http://{{.IP}}:8080" target="_self" style="color: #00d4ff; text-decoration: none; border-bottom: 1px dotted #00d4ff;">{{.IP}}</a></span>
                    </div>
                    <div class="info-row">
                        <span class="info-label">Node</span>
                        <span class="info-value">{{.Node}}</span>
                    </div>
                    
                    {{if .Info}}
                    <div class="info-row">
                        <span class="info-label">Container Age</span>
                        <span class="info-value"><script>document.write(formatDuration({{.Info.ContainerAge}} / 1000000))</script></span>
                    </div>
                    <div class="info-row">
                        <span class="info-label">Start Time</span>
                        <span class="info-value"><script>document.write(formatTime('{{.Info.StartTime}}'))</script></span>
                    </div>
                    <div class="info-row">
                        <span class="info-label">Startup Delay</span>
                        <span class="info-value">{{.Info.StartupDelay}}s</span>
                    </div>
                    {{end}}
                </div>
                
                {{if .Info}}
                <div class="probe-status">
                    <div class="probe-indicator" onclick="toggleProbe('{{.IP}}', 'startup', {{.Info.ProbeStatus.Started}})" title="Click to toggle startup probe">
                        <div class="probe-dot {{if .Info.ProbeStatus.Started}}active{{end}}"></div>
                        <span>Started</span>
                    </div>
                    <div class="probe-indicator" onclick="toggleProbe('{{.IP}}', 'liveness', {{.Info.ProbeStatus.Live}})" title="Click to toggle liveness probe">
                        <div class="probe-dot {{if .Info.ProbeStatus.Live}}active{{end}}"></div>
                        <span>Live</span>
                    </div>
                    <div class="probe-indicator" onclick="toggleProbe('{{.IP}}', 'readiness', {{.Info.ProbeStatus.Ready}})" title="Click to toggle readiness probe">
                        <div class="probe-dot {{if .Info.ProbeStatus.Ready}}active{{end}}"></div>
                        <span>Ready</span>
                    </div>
                </div>
                {{end}}
                
                {{if .Error}}
                <div class="error-message">{{.Error}}</div>
                {{end}}
                
                <div class="last-check">Last check: {{.LastCheck.Format "15:04:05"}}</div>
            </div>
            {{end}}
        </div>
        {{else}}
        <div class="no-pods">No pods found with label app=probe-demo</div>
        {{end}}
    </div>
</body>
</html>`

	t, err := template.New("dashboard").Parse(tmpl)
	if err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}

	d.mu.RLock()
	pods := make([]*PodStatusInfo, 0, len(d.pods))
	for _, pod := range d.pods {
		pods = append(pods, pod)
	}
	d.mu.RUnlock()

	// Sort pods by ReplicaSetID, then by Name
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].ReplicaSetID == pods[j].ReplicaSetID {
			return pods[i].Name < pods[j].Name
		}
		return pods[i].ReplicaSetID < pods[j].ReplicaSetID
	})

	data := struct {
		Pods      []*PodStatusInfo
		Version   string
		GitCommit string
		BuildTime string
	}{
		Pods:      pods,
		Version:   Version,
		GitCommit: GitCommit,
		BuildTime: BuildTime,
	}

	// Debug log
	log.Printf("Rendering template with Version: %s, GitCommit: %s, BuildTime: %s", data.Version, data.GitCommit, data.BuildTime)

	w.Header().Set("Content-Type", "text/html")
	if err := t.Execute(w, data); err != nil {
		http.Error(w, "Template execution error", http.StatusInternalServerError)
	}
}

func (d *Dashboard) handleAPI(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.pods)
}

func (d *Dashboard) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	proxyReq, err := http.NewRequest(req.Method, req.URL, nil)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to call pod API: %v", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func main() {
	log.Printf("Pod Monitor Dashboard %s (commit: %s, built: %s)", Version, GitCommit, BuildTime)

	dashboard, err := NewDashboard()
	if err != nil {
		log.Fatalf("Failed to create dashboard: %v", err)
	}

	ctx := context.Background()

	// Start monitoring pods in the background
	go dashboard.monitorPods(ctx)

	// Give the monitor a moment to collect initial data
	time.Sleep(2 * time.Second)

	// Setup HTTP routes
	http.HandleFunc("/", dashboard.handleIndex)
	http.HandleFunc("/api/pods", dashboard.handleAPI)
	http.HandleFunc("/api/proxy", dashboard.handleProxy)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	log.Printf("Starting dashboard server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
