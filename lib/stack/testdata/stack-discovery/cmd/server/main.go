// Package main is a minimal HTTP service used as the stack-discovery
// fixture. Zap-Production logger + chi router + middleware.Logger.
// The /echo endpoint reacts to ?status=NNN to emit a configured status
// code so e2e tests can capture stdout shapes covering 2xx/4xx/5xx.
package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Get("/echo", func(w http.ResponseWriter, req *http.Request) {
		status := 200
		if s := req.URL.Query().Get("status"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				status = n
			}
		}
		w.WriteHeader(status)
		fmt.Fprintln(w, "ok")
	})

	logger.Info("server listening", zap.String("addr", ":8080"))
	if err := http.ListenAndServe(":8080", r); err != nil {
		logger.Error("listen", zap.Error(err))
	}
}
