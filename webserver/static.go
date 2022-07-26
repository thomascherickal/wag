package webserver

import (
	"log"
	"net/http"
	"path/filepath"
	"wag/webserver/resources"
)

func setMimeType(w http.ResponseWriter, r *http.Request) {
	headers := w.Header()
	ext := filepath.Ext(r.URL.Path)

	switch ext {
	case ".css":
		headers.Set("Content-Type", "text/css")
	case ".png":
		headers.Set("Content-Type", "image/png")
	case ".jpg":
		headers.Set("Content-Type", "image/jpg")
	case ".svg":
		headers.Set("Content-Type", "image/svg")
	}
}

func embeddedStatic(w http.ResponseWriter, r *http.Request) {

	var err error
	var fileContent []byte

	if len(r.URL.Path) > 0 {
		r.URL.Path = r.URL.Path[1:]
	}

	if fileContent, err = resources.Static.ReadFile(r.URL.Path); err != nil {
		log.Println("Error getting static: ", err)
		http.NotFound(w, r)
		return
	}

	setMimeType(w, r)

	_, err = w.Write(fileContent)
	if err != nil {
		log.Println("Error writing content")
		http.Error(w, "Unable to write static resource", 500)
	}
}
