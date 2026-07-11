// Command ekgd is the MVP control-plane server: API-key validation
// (/v1/auth/validate) and result ingestion with per-commit architecture
// diffing (/v1/ingest). See internal/backend for behavior.
//
// Usage:
//
//	ekgd --addr :8080 --data ./ekg-data --keys key1,key2
//	EKGD_KEYS=key1,key2 ekgd
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/backend"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	data := flag.String("data", "./ekg-data", "directory for baseline storage")
	keys := flag.String("keys", "", "comma-separated accepted API keys (or EKGD_KEYS env)")
	flag.Parse()

	keyList := *keys
	if keyList == "" {
		keyList = os.Getenv("EKGD_KEYS")
	}
	if strings.TrimSpace(keyList) == "" {
		fmt.Fprintln(os.Stderr, "ekgd: no API keys configured (--keys or EKGD_KEYS)")
		os.Exit(1)
	}

	store, err := backend.NewStore(*data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ekgd:", err)
		os.Exit(1)
	}
	srv := backend.New(strings.Split(keyList, ","), store)

	log.Printf("ekgd listening on %s (data: %s)", *addr, *data)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
