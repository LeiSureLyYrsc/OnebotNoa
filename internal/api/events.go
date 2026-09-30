package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// eventFilterFrom reads the stream filter from the query string.
func eventFilterFrom(r *http.Request) hub.EventFilter {
	q := r.URL.Query()
	return hub.EventFilter{
		SelfID:   q.Get("self_id"),
		Bot:      q.Get("bot"),
		Kind:     q.Get("kind"),
		PostType: q.Get("post_type"),
		GroupID:  q.Get("group_id"),
		Query:    q.Get("q"),
	}
}

func (s *Server) handleRecentEvents(w http.ResponseWriter, r *http.Request) {
	if s.opt.Events == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []hub.EventRecord{}})
		return
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			limit = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":      s.opt.Events.Recent(limit, eventFilterFrom(r)),
		"subscribers": s.opt.Events.Subscribers(),
		"dropped":     s.opt.Events.Dropped(),
		"ring_size":   s.opt.Events.Len(),
	})
}

// handleEventStream pushes live records as Server-Sent Events. EventSource sends
// the session cookie, so the usual auth middleware applies.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if s.opt.Events == nil {
		writeError(w, http.StatusServiceUnavailable, "事件流不可用")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前服务器不支持流式响应")
		return
	}

	filter := eventFilterFrom(r)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Resume from the browser's last seen id when it reconnects.
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if after, err := strconv.ParseUint(raw, 10, 64); err == nil {
			for _, rec := range s.opt.Events.Recent(500, filter) {
				if rec.Seq > after {
					writeSSE(w, rec)
				}
			}
			flusher.Flush()
		}
	}

	records, cancel := s.opt.Events.Subscribe(filter, 256)
	defer cancel()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case rec, ok := <-records:
			if !ok {
				return
			}
			writeSSE(w, rec)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, rec hub.EventRecord) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", rec.Seq, rec.Kind, payload)
}
