package httpserver

import (
	"encoding/json"
	"net/http"
)

type okBody struct {
	OK   bool `json:"ok"`
	Data any  `json:"data"`
}

type errBody struct {
	OK    bool     `json:"ok"`
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeOK(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, okBody{OK: true, Data: data})
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errBody{OK: false, Error: apiError{Code: code, Message: message}})
}

type kookErrBody struct {
	OK    bool        `json:"ok"`
	Error apiError    `json:"error"`
	Data  kookErrData `json:"data"`
}

type kookErrData struct {
	KookCode    int    `json:"kook_code"`
	KookMessage string `json:"kook_message"`
}

func writeKookErr(w http.ResponseWriter, code int, message string) {
	writeJSON(w, http.StatusOK, kookErrBody{
		OK:    false,
		Error: apiError{Code: "kook_error", Message: message},
		Data:  kookErrData{KookCode: code, KookMessage: message},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}
