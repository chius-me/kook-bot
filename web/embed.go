package web

import "embed"

// FS holds the panel templates and static files.
//
//go:embed templates/*.html static/*
var FS embed.FS
