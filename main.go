package main

import (
	"context"
	"flag"
	"log"
	"net/http"
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	idx, err := loader.LoadIndex(ctx)
	if err != nil {
		log.Fatalf("load proto: %v", err)
	}
	log.Printf("loaded %d enums from packages %v", idx.Len(), idx.Packages())

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.NewRouter(idx, cfg.Secret),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("listening on %s", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func toLoaderSources(in []config.Source) []proto.Source {
	out := make([]proto.Source, len(in))
	for i, s := range in {
		out[i] = proto.Source{URL: s.URL, Path: s.Path, Glob: s.Glob}
	}
	return out
}
