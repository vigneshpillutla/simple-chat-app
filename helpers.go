package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
)

func (cs *chatServer) serverError(w http.ResponseWriter, err error) {
	trace := fmt.Sprintf("%s\n%s", err.Error(), debug.Stack())

	cs.errorLog.Output(2, trace)

	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func (cs *chatServer) clientError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func (cs *chatServer) respondWithJSON(w http.ResponseWriter, payload interface{}, status int) {
	data, err := json.Marshal(payload)

	if err != nil {
		cs.serverError(w, err)
		return
	}

	w.WriteHeader(status)
	w.Write(data)
}
