package httpapi

import (
	"net/http"
	"runtime"
	"time"
)

// healthData mirrors HealthStatus in back/services/health.ts. The field names
// and nesting are part of the contract, so they are kept even though the Go
// implementation runs the scheduler in-process instead of a gRPC worker.
type healthData struct {
	Build    buildStatus    `json:"build"`
	Status   string         `json:"status"`
	Services healthServices `json:"services"`
	Metrics  healthMetrics  `json:"metrics"`
}

type buildStatus struct {
	InstanceID    string `json:"instanceId"`
	ParentPid     int    `json:"parentPid"`
	Version       string `json:"version,omitempty"`
	SourceCommit  string `json:"sourceCommit,omitempty"`
	BackendSha256 string `json:"backendSha256,omitempty"`
}

type healthServices struct {
	HTTP bool `json:"http"`
	GRPC bool `json:"grpc"`
}

type healthMetrics struct {
	Uptime int64        `json:"uptime"`
	Memory healthMemory `json:"memory"`
}

type healthMemory struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

func healthHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		data := healthData{
			Build: buildStatus{
				InstanceID:    deps.Build.InstanceID,
				ParentPid:     deps.Build.ParentPid,
				Version:       deps.Build.Version,
				SourceCommit:  deps.Build.SourceCommit,
				BackendSha256: deps.Build.BackendSha256,
			},
			Status: "ok",
			Services: healthServices{
				HTTP: true,
				// The scheduler will live in this process; M4 replaces this
				// placeholder with a real readiness check.
				GRPC: true,
			},
			Metrics: healthMetrics{
				Uptime: int64(time.Since(deps.StartedAt).Seconds()),
				Memory: healthMemory{
					Used:  mem.HeapAlloc,
					Total: mem.HeapSys,
				},
			},
		}

		status := http.StatusOK
		if data.Status != "ok" {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, envelope{Code: status, Data: data})
	}
}
