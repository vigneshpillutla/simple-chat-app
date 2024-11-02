package main

import (
	"fmt"
	"net/http"
)

func main() {
	cs := newChatServer()
	portAddr := ":3000"

	srv := &http.Server{
		Handler: cs,
		Addr:    portAddr,
	}

	fmt.Printf("Staring the server on port %v\n", portAddr)
	err := srv.ListenAndServe()

	if err != nil {
		fmt.Print(err)
	}
}
