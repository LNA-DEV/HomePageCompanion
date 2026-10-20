package core

import (
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/core/internal/logger"
	"github.com/gin-gonic/gin"
)

const (
	maxClientLogEntries  = 200
	maxClientLogMessage  = 4096
	maxClientLogBodySize = 1 << 20 // 1 MiB
)

type clientLogEntry struct {
	Level   string `json:"level"`
	Source  string `json:"source"`
	URL     string `json:"url,omitempty"`
	Time    string `json:"time"`
	Message string `json:"message"`
}

type clientLogPayload struct {
	ClientID string           `json:"clientId"`
	Entries  []clientLogEntry `json:"entries"`
}

// ingestClientLogs accepts a batch of browser-side log entries and appends
// them as text lines to data/logs/clients/<clientId>/YYYY-MM-DD.log.
func (c *Core) ingestClientLogs(g *gin.Context) {
	g.Request.Body = http.MaxBytesReader(g.Writer, g.Request.Body, maxClientLogBodySize)

	var payload clientLogPayload
	if err := g.ShouldBindJSON(&payload); err != nil {
		g.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}
	if err := logger.ValidateClientID(payload.ClientID); err != nil {
		g.JSON(http.StatusBadRequest, gin.H{"error": "invalid clientId"})
		return
	}
	if len(payload.Entries) == 0 {
		g.Status(http.StatusNoContent)
		return
	}
	if len(payload.Entries) > maxClientLogEntries {
		payload.Entries = payload.Entries[:maxClientLogEntries]
	}

	writer, err := logger.ClientWriter(payload.ClientID)
	if err != nil {
		g.JSON(http.StatusInternalServerError, gin.H{"error": "log writer unavailable"})
		return
	}

	var buf strings.Builder
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, e := range payload.Entries {
		level := strings.ToLower(strings.TrimSpace(e.Level))
		if level == "" {
			level = "info"
		}
		source := strings.TrimSpace(e.Source)
		if source == "" {
			source = "client"
		}
		msg := e.Message
		if len(msg) > maxClientLogMessage {
			msg = msg[:maxClientLogMessage] + "…"
		}
		// Collapse newlines so each entry stays on a single line.
		msg = strings.ReplaceAll(msg, "\r\n", "\n")
		msg = strings.ReplaceAll(msg, "\n", " ⏎ ")

		buf.WriteString(now)
		buf.WriteString(" [")
		buf.WriteString(level)
		buf.WriteString("] [")
		buf.WriteString(source)
		buf.WriteString("]")
		if e.URL != "" {
			buf.WriteString(" ")
			buf.WriteString(e.URL)
		}
		if e.Time != "" {
			buf.WriteString(" client=")
			buf.WriteString(e.Time)
		}
		buf.WriteString(" ")
		buf.WriteString(msg)
		buf.WriteString("\n")
	}

	if _, err := writer.Write([]byte(buf.String())); err != nil {
		g.JSON(http.StatusInternalServerError, gin.H{"error": "write failed"})
		return
	}
	g.Status(http.StatusNoContent)
}

func reflectTypeOf(v any) reflect.Type { return reflect.TypeOf(v) }
