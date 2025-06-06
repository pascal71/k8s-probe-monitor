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
            margin-bottom: 30px;
            font-size: 2.5em;
            text-shadow: 0 0 20px rgba(100, 200, 255, 0.5);
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
        }
        
        .probe-dot {
            width: 10px;
            height: 10px;
            border-radius: 50%;
            background: #444;
        }
        
        .probe-dot.active {
            background: #00ff88;
            box-shadow: 0 0 10px rgba(0, 255, 136, 0.5);
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
        
        function refreshPage() {
            location.reload();
        }
        
        // Auto-refresh every 5 seconds
        setInterval(refreshPage, 5000);
    </script>
</head>
<body>
    <div class="container">
        <h1>🚀 Pod Monitor Dashboard</h1>
        <div class="refresh-indicator">🔄</div>
        
        {{if .Pods}}
        <div class="grid">
            {{range .Pods}}
            <div class="pod-card {{if .Error}}error{{else if not .Info.ProbeStatus.Ready}}not-ready{{end}}">
                <div class="pod-name">{{.Name}}</div>
                
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
                    <div class="probe-indicator">
                        <div class="probe-dot {{if .Info.ProbeStatus.Started}}active{{end}}"></div>
                        <span>Started</span>
                    </div>
                    <div class="probe-indicator">
                        <div class="probe-dot {{if .Info.ProbeStatus.Live}}active{{end}}"></div>
                        <span>Live</span>
                    </div>
                    <div class="probe-indicator">
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

	data := struct {
		Pods []*PodStatusInfo
	}{
		Pods: pods,
	}

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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	log.Printf("Starting dashboard server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
