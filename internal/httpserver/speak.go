package httpserver

import (
	"errors"
	"net/http"

	"bsdx/internal/speak"
)

// MountSpeak registers the speak page and queue. Routes stay out of server.go.
func MountSpeak(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /speak", speakPage(d))
	mux.HandleFunc("POST /api/speak", postSpeak(d))
	mux.HandleFunc("GET /api/speak/queue", speakQueue(d))
	mux.HandleFunc("POST /api/speak/skip", speakSkip(d))
	mux.HandleFunc("POST /api/speak/stop", speakStop(d))
	mux.HandleFunc("POST /api/voice/leave", voiceLeave(d))
	mux.HandleFunc("GET /api/voice", voiceState(d))
}

func speakPage(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Sessions == nil || !d.Sessions.Valid(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		renderNamed(w, d.Log, d.Templates, "speak", pageData{Title: "代说"})
	}
}

func postSpeak(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, true) {
			return
		}
		if d.Speak == nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "代说还没准备好")
			return
		}
		var body struct {
			Text            string  `json:"text"`
			VoiceID         string  `json:"voice_id"`
			Rate            float64 `json:"rate"`
			Volume          float64 `json:"volume"`
			ClientRequestID string  `json:"client_request_id"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", "请求格式错误")
			return
		}
		job, err := d.Speak.Submit(r.Context(), speak.SpeakRequest{
			Text:            body.Text,
			VoiceID:         body.VoiceID,
			Rate:            body.Rate,
			Volume:          body.Volume,
			Requester:       "webui",
			ClientRequestID: body.ClientRequestID,
		})
		if err != nil {
			writeSpeakErr(w, err)
			return
		}
		writeOK(w, http.StatusAccepted, job)
	}
}

func speakQueue(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, false) {
			return
		}
		if d.Speak == nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "代说还没准备好")
			return
		}
		writeOK(w, http.StatusOK, d.Speak.Snapshot(r.Context()))
	}
}

func speakSkip(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !speakReady(w, r, d) {
			return
		}
		_ = d.Speak.Skip()
		writeOK(w, http.StatusOK, map[string]bool{"skipped": true})
	}
}

func speakStop(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !speakReady(w, r, d) {
			return
		}
		_ = d.Speak.Stop()
		writeOK(w, http.StatusOK, map[string]bool{"stopped": true})
	}
}

func voiceLeave(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !speakReady(w, r, d) {
			return
		}
		if err := d.Speak.Leave(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "离开语音频道失败")
			return
		}
		writeOK(w, http.StatusOK, map[string]bool{"left": true})
	}
}

func voiceState(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, d, false) {
			return
		}
		if d.Speak == nil {
			writeOK(w, http.StatusOK, map[string]any{"state": "idle", "state_label": "空闲", "roster": []any{}})
			return
		}
		snap := d.Speak.Snapshot(r.Context())
		writeOK(w, http.StatusOK, map[string]any{
			"state":       snap.State,
			"state_label": snap.StateLabel,
			"channel_id":  snap.ChannelID,
			"now_playing": snap.Current,
			"queue":       snap.Queue,
			"roster":      snap.Roster,
			"last_error":  snap.LastError,
		})
	}
}

func speakReady(w http.ResponseWriter, r *http.Request, d Deps) bool {
	if !authorized(w, r, d, true) {
		return false
	}
	if d.Speak == nil {
		writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "代说还没准备好")
		return false
	}
	return true
}

func writeSpeakErr(w http.ResponseWriter, err error) {
	var rej *speak.Reject
	if errors.As(err, &rej) {
		writeErr(w, rej.Status, rej.Code, rej.Message)
		return
	}
	writeErr(w, http.StatusServiceUnavailable, "kook_unavailable", "暂时无法代说")
}
