package admin

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"states":      func(values ...string) []string { return values },
		"heights":     func(values ...int) []int { return values },
		"sortlink":    sortLink,
		"sortaria":    sortARIA,
		"playback":    playback,
		"stage":       stageLabel,
		"channelLink": channelLink,
		"gib":         formatGiB,
		"day":         formatDay,
		"when":        formatWhen,
		"height":      formatHeight,
		"age":         formatAge,
		"inc":         func(n int) int { return n + 1 },
	}
}

func formatGiB(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<30), 'f', 2, 64)
}

func formatDay(n int64) string {
	if n == 0 {
		return "Unknown"
	}
	return time.Unix(n, 0).UTC().Format("2006-01-02")
}

func formatWhen(n int64) string {
	if n == 0 {
		return "Never"
	}
	return time.Unix(n, 0).UTC().Format("2006-01-02 15:04 MST")
}

func formatHeight(n int) string {
	if n == 0 {
		return "Unavailable"
	}
	return strconv.Itoa(n) + "p"
}

func formatAge(n int64) string {
	if n == 0 {
		return "Never"
	}
	elapsed := time.Since(time.Unix(n, 0))
	if elapsed < time.Minute {
		return "Just now"
	}
	if elapsed < time.Hour {
		return strconv.Itoa(int(elapsed.Minutes())) + " minutes ago"
	}
	if elapsed < 24*time.Hour {
		return strconv.Itoa(int(elapsed.Hours())) + " hours ago"
	}
	return strconv.Itoa(int(elapsed.Hours()/24)) + " days ago"
}

func (s *Server) render(w http.ResponseWriter, name string, v any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var rendered bytes.Buffer
	if err := s.templates.ExecuteTemplate(&rendered, name, v); err != nil {
		slog.Error("page rendering failed")
		http.Error(w, "page unavailable", http.StatusInternalServerError)
		return
	}
	if _, err := rendered.WriteTo(w); err != nil {
		slog.Error("page response write failed")
	}
}
