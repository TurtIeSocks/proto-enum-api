package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"proto-enum-api/internal/api"
	"proto-enum-api/internal/config"
	"proto-enum-api/internal/proto"
)

func main() {
	cfgPath := flag.String("config", "./config.toml", "path to TOML config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.Secret == "" {
		log.Println("warning: no secret configured; the auth middleware will see an empty secret")
	}

	loader := &proto.Loader{
		Sources:  toLoaderSources(cfg.Sources),
		Strict:   cfg.Strict,
		CacheDir: cfg.CacheDir,
	}

	mgr, err := proto.NewManager(loader)
	if err != nil {
		log.Fatalf("load proto: %v", err)
	}
	idx := mgr.Index()
	log.Printf("loaded %d enums from packages %v", idx.Len(), idx.Packages())

	// Background refresh runs until SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mgr.Start(ctx)
	defer mgr.Stop()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.NewRouter(mgr, cfg.Secret),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("listening on %s", cfg.Listen)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown: draining connections")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func toLoaderSources(in []config.Source) []proto.Source {
	out := make([]proto.Source, len(in))
	for i, s := range in {
		out[i] = proto.Source{
			URL:             s.URL,
			Path:            s.Path,
			Glob:            s.Glob,
			RefreshInterval: s.RefreshInterval.Std(),
		}
	}
	return out
}
