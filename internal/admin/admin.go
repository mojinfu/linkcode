// Package admin provides a simple web dashboard for viewing LinkCode state.
package admin

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"

	"linkcode/internal/botpool"
	"linkcode/internal/session"
)

// Server serves the admin web UI.
type Server struct {
	bindAddr   string
	sessionMgr *session.Manager
	botPool    *botpool.Pool
}

// New creates a new admin server.
func New(bindAddr string, sessMgr *session.Manager, pool *botpool.Pool) *Server {
	return &Server{
		bindAddr:   bindAddr,
		sessionMgr: sessMgr,
		botPool:    pool,
	}
}

// Start begins listening and serving the admin panel.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	return http.ListenAndServe(s.bindAddr, mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	sessions, _ := s.sessionMgr.ListActive()
	bots, _ := s.botPool.List()

	page := pageData{
		Sessions: sessions,
		Bots:     bots,
	}

	tmpl := template.Must(template.New("admin").Parse(adminHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	sessions, _ := s.sessionMgr.ListActive()
	bots, _ := s.botPool.List()

	idle, bound, unavail := 0, 0, 0
	for _, b := range bots {
		switch b.Status {
		case "idle":
			idle++
		case "bound":
			bound++
		case "unavailable":
			unavail++
		}
	}

	resp := map[string]interface{}{
		"session_count": len(sessions),
		"bot_total":     len(bots),
		"bot_idle":      idle,
		"bot_bound":     bound,
		"bot_unavailable": unavail,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type pageData struct {
	Sessions []session.Session
	Bots     []botpool.Bot
}

const adminHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>LinkCode Admin</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; margin: 20px; background: #f5f5f5; }
  h1 { color: #333; }
  .card { background: white; border-radius: 8px; padding: 16px; margin: 12px 0; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
  .badge { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 12px; font-weight: bold; }
  .badge-waked { background: #e8f5e9; color: #2e7d32; }
  .badge-sleeped { background: #fff3e0; color: #e65100; }
  .badge-idle { background: #e3f2fd; color: #1565c0; }
  .badge-bound { background: #fce4ec; color: #c62828; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 8px; border-bottom: 1px solid #eee; }
  th { color: #666; font-size: 13px; }
</style>
</head>
<body>
<h1>LinkCode Admin</h1>

<div class="card">
  <h2>活跃 Session ({{len .Sessions}})</h2>
  <table>
    <tr><th>ID</th><th>名称</th><th>类型</th><th>状态</th><th>最后活跃</th></tr>
    {{range .Sessions}}
    <tr>
      <td>{{.ID}}</td>
      <td>{{.Name}}</td>
      <td>{{.AgentType}}</td>
      <td><span class="badge badge-{{.ProcessStatus}}">{{.ProcessStatus}}</span></td>
      <td>{{.LastActiveAt.Format "15:04:05"}}</td>
    </tr>
    {{end}}
  </table>
</div>

<div class="card">
  <h2>Bot 池 ({{len .Bots}})</h2>
  <table>
    <tr><th>ID</th><th>名称</th><th>平台BotID</th><th>状态</th></tr>
    {{range .Bots}}
    <tr>
      <td>{{.ID}}</td>
      <td>{{.Name}}</td>
      <td>{{.BotID}}</td>
      <td><span class="badge badge-{{.Status}}">{{.Status}}</span></td>
    </tr>
    {{end}}
  </table>
</div>
</body>
</html>`

// Ensure fmt is used (for template).
var _ = fmt.Sprintf
