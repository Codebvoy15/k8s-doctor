package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

// DashboardEngine wraps the diag engine for the web server
type DashboardEngine struct {
	engine  *diag.Engine
	ctx     context.Context
	verbose bool
}

func NewDashboardEngine(ctx context.Context, namespace string, verbose bool) (*DashboardEngine, error) {
	engine, err := diag.NewEngine(ctx, namespace, verbose)
	if err != nil {
		return nil, err
	}
	return &DashboardEngine{engine: engine, ctx: ctx, verbose: verbose}, nil
}

// Server is the HTTP server for the dashboard
type Server struct {
	engine      *DashboardEngine
	refreshSecs int
	mux         *http.ServeMux
	cache       *dataCache
}

type dataCache struct {
	mu        sync.RWMutex
	snapshot  interface{}
	findings  interface{}
	diffs     interface{}
	events    interface{}
	audit     interface{}
	updatedAt time.Time
}

func NewServer(engine *DashboardEngine, refreshSecs int) *Server {
	s := &Server{
		engine:      engine,
		refreshSecs: refreshSecs,
		mux:         http.NewServeMux(),
		cache:       &dataCache{},
	}
	s.registerRoutes()
	go s.backgroundRefresh()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers for local dev
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) registerRoutes() {
	// Dashboard HTML
	s.mux.HandleFunc("/", s.handleDashboard)

	// API endpoints
	s.mux.HandleFunc("/api/snapshot", s.handleSnapshot)
	s.mux.HandleFunc("/api/diagnose", s.handleDiagnose)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/api/diff", s.handleDiff)
	s.mux.HandleFunc("/api/audit", s.handleAudit)
	s.mux.HandleFunc("/api/predict", s.handlePredict)
	s.mux.HandleFunc("/api/top", s.handleTop)
	s.mux.HandleFunc("/api/inventory", s.handleInventory)
	s.mux.HandleFunc("/api/all", s.handleAll)
}

func (s *Server) backgroundRefresh() {
	for {
		s.refreshCache()
		time.Sleep(time.Duration(s.refreshSecs) * time.Second)
	}
}

func (s *Server) refreshCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	eng, _ := diag.NewEngine(ctx, "", false)
	if eng == nil {
		return
	}

	s.cache.mu.Lock()
	defer s.cache.mu.Unlock()

	// Snapshot
	if snap, err := eng.ClusterSnapshot(); err == nil {
		s.cache.snapshot = snap
	}

	// Pod health + triage findings
	var allFindings []diag.Finding
	if f, err := eng.PodHealth(); err == nil {
		allFindings = append(allFindings, f...)
	}
	if f, err := eng.PendingPods(); err == nil {
		allFindings = append(allFindings, f...)
	}
	if f, err := eng.RecentWarningEvents(time.Hour); err == nil {
		allFindings = append(allFindings, f...)
	}
	s.cache.findings = allFindings

	// Diffs
	if diffs, err := eng.LiveDeepDiff(time.Hour); err == nil {
		s.cache.diffs = diffs
	}

	// Events
	if events, err := eng.EventTimeline(time.Hour, "", false); err == nil {
		s.cache.events = events
	}

	// Audit
	if audit, err := eng.AuditLog(time.Hour, "", ""); err == nil {
		s.cache.audit = audit
	}

	s.cache.updatedAt = time.Now()
}

// ── API handlers ──────────────────────────────────────────────────────────────

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}
	snap, err := eng.ClusterSnapshot()
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonOK(w, snap)
}

func (s *Server) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}

	window := time.Hour
	if q := r.URL.Query().Get("window"); q != "" {
		if d, err := time.ParseDuration(q); err == nil {
			window = d
		}
	}

	podFindings, _ := eng.PodHealth()
	pendingFindings, _ := eng.PendingPods()
	eventFindings, _ := eng.RecentWarningEvents(window)
	diffs, _ := eng.LiveDeepDiff(window)
	audit, _ := eng.AuditLog(window, "", "")

	var activeFaults []diag.Finding
	for _, f := range podFindings {
		if f.Score > 0 {
			activeFaults = append(activeFaults, f)
		}
	}
	for _, f := range pendingFindings {
		if f.Score > 0 {
			activeFaults = append(activeFaults, f)
		}
	}
	for _, f := range eventFindings {
		if f.Score > 40 {
			activeFaults = append(activeFaults, f)
		}
	}

	// Sort by score
	for i := 1; i < len(activeFaults); i++ {
		for j := i; j > 0 && activeFaults[j].Score > activeFaults[j-1].Score; j-- {
			activeFaults[j], activeFaults[j-1] = activeFaults[j-1], activeFaults[j]
		}
	}

	type DiagnoseResponse struct {
		ActiveFaults []diag.Finding       `json:"active_faults"`
		RecentDiffs  []diag.DeepDiffEntry `json:"recent_diffs"`
		AuditEntries []diag.AuditEntry    `json:"audit_entries"`
		RootCause    RootCauseResult      `json:"root_cause"`
		GeneratedAt  time.Time            `json:"generated_at"`
	}

	rc := computeRootCause(activeFaults, diffs, audit)

	limit := func(entries []diag.AuditEntry, n int) []diag.AuditEntry {
		if len(entries) > n {
			return entries[:n]
		}
		return entries
	}
	limitDiffs := func(entries []diag.DeepDiffEntry, n int) []diag.DeepDiffEntry {
		if len(entries) > n {
			return entries[:n]
		}
		return entries
	}

	jsonOK(w, DiagnoseResponse{
		ActiveFaults: activeFaults,
		RecentDiffs:  limitDiffs(diffs, 10),
		AuditEntries: limit(audit, 20),
		RootCause:    rc,
		GeneratedAt:  time.Now(),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}
	window := time.Hour
	if q := r.URL.Query().Get("window"); q != "" {
		if d, err := time.ParseDuration(q); err == nil {
			window = d
		}
	}
	warningOnly := r.URL.Query().Get("warning") == "true"
	events, err := eng.EventTimeline(window, "", warningOnly)
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonOK(w, events)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}
	window := time.Hour
	if q := r.URL.Query().Get("window"); q != "" {
		if d, err := time.ParseDuration(q); err == nil {
			window = d
		}
	}
	diffs, err := eng.LiveDeepDiff(window)
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonOK(w, diffs)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}
	window := time.Hour
	if q := r.URL.Query().Get("window"); q != "" {
		if d, err := time.ParseDuration(q); err == nil {
			window = d
		}
	}
	entries, err := eng.AuditLog(window, "", "")
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonOK(w, entries)
}

func (s *Server) handlePredict(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}
	findings, err := eng.PredictRisks()
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonOK(w, findings)
}

func (s *Server) handleTop(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}
	result, err := eng.TopConsumers("memory", 20)
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonOK(w, result)
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}
	ns := r.URL.Query().Get("ns")
	if ns == "*" {
		ns = ""
	}
	opts := diag.InventoryOptions{
		Namespace:     ns,
		AllNamespaces: ns == "",
		IncludeNoisy:  r.URL.Query().Get("includeNoisy") == "1",
		IncludeEvents: r.URL.Query().Get("includeEvents") == "1",
	}
	report, err := eng.ScanNamespace(opts)
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonOK(w, report)
}

func (s *Server) handleAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	eng, err := diag.NewEngine(ctx, "", false)
	if err != nil {
		jsonError(w, err)
		return
	}

	type AllData struct {
		Snapshot    interface{} `json:"snapshot"`
		PodHealth   interface{} `json:"pod_health"`
		Events      interface{} `json:"events"`
		Diffs       interface{} `json:"diffs"`
		Predict     interface{} `json:"predict"`
		GeneratedAt time.Time   `json:"generated_at"`
	}

	snap, _ := eng.ClusterSnapshot()
	pods, _ := eng.PodHealth()
	events, _ := eng.EventTimeline(time.Hour, "", false)
	diffs, _ := eng.LiveDeepDiff(time.Hour)
	predict, _ := eng.PredictRisks()

	jsonOK(w, AllData{
		Snapshot:    snap,
		PodHealth:   pods,
		Events:      events,
		Diffs:       diffs,
		Predict:     predict,
		GeneratedAt: time.Now(),
	})
}

// ── Root cause helper ─────────────────────────────────────────────────────────

type RootCauseResult struct {
	Conclusion string `json:"conclusion"`
	Evidence   string `json:"evidence"`
	ChangedBy  string `json:"changed_by"`
	ChangedAt  string `json:"changed_at"`
	Remedy     string `json:"remedy"`
	Confidence int    `json:"confidence"`
}

func computeRootCause(faults []diag.Finding, diffs []diag.DeepDiffEntry, audit []diag.AuditEntry) RootCauseResult {
	if len(faults) == 0 {
		return RootCauseResult{
			Conclusion: "No active pod faults detected.",
			Confidence: 100,
		}
	}
	top := faults[0]
	for _, d := range diffs {
		if d.CorrelatedFault != "" || containsPrefix(top.Object, d.Name) {
			changedAt := ""
			if !d.Timestamp.IsZero() {
				changedAt = d.Timestamp.Format("2006-01-02 15:04:05")
			}
			remedy := d.Mitigation
			if remedy == "" {
				remedy = "kubectl rollout undo deployment/" + d.Name + " -n " + d.Namespace
			}
			return RootCauseResult{
				Conclusion: top.Title + " on " + top.Namespace + "/" + top.Object + " — likely caused by recent " + d.Kind + " change",
				Evidence:   d.Kind + " field '" + d.Field + "' changed: " + truncate64(d.OldValue) + " → " + truncate64(d.NewValue),
				ChangedBy:  d.ChangedBy,
				ChangedAt:  changedAt,
				Remedy:     remedy,
				Confidence: 85,
			}
		}
	}
	for _, a := range audit {
		if containsPrefix(top.Object, a.Name) {
			remedy := a.Mitigation
			if remedy == "" {
				remedy = "kubectl rollout undo deployment/" + a.Name + " -n " + a.Namespace
			}
			return RootCauseResult{
				Conclusion: top.Title + " on " + top.Namespace + "/" + top.Object + " — " + a.Kind + " change detected around same time",
				Evidence:   a.Kind + " '" + a.Name + "' was " + a.Action,
				ChangedBy:  a.FieldManager,
				ChangedAt:  a.Timestamp.Format("2006-01-02 15:04:05"),
				Remedy:     remedy,
				Confidence: 65,
			}
		}
	}
	return RootCauseResult{
		Conclusion: top.Title + " on " + top.Namespace + "/" + top.Object + " — no correlated changes found",
		Evidence:   top.Detail,
		Remedy:     top.Remedy,
		Confidence: 40,
	}
}

func containsPrefix(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if len(a) >= len(b) {
		return a[:len(b)] == b
	}
	return b[:len(a)] == a
}

func truncate64(s string) string {
	if len(s) > 64 {
		return s[:61] + "..."
	}
	return s
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(dashboardHTML))
}
