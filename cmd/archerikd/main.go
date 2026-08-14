// Command archerikd is a reference implementation of the Archerik API:
// API-key validation (/v1/auth/validate) and result ingestion with
// per-commit architecture diffing (/v1/ingest). See internal/backend.
//
// Usage:
//
//	archerikd --addr :8080 --data ./archerik-data --keys key1,key2
//	ARCHERIKD_KEYS=key1,key2 archerikd
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/backend"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	data := flag.String("data", "./archerik-data", "directory for baseline storage")
	keys := flag.String("keys", "", "comma-separated accepted API keys (or ARCHERIKD_KEYS env)")
	flag.Parse()

	keyList := *keys
	if keyList == "" {
		keyList = os.Getenv("ARCHERIKD_KEYS")
	}
	if strings.TrimSpace(keyList) == "" {
		fmt.Fprintln(os.Stderr, "archerikd: no API keys configured (--keys or ARCHERIKD_KEYS)")
		os.Exit(1)
	}

	store, err := backend.NewStore(*data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archerikd:", err)
		os.Exit(1)
	}
	srv := backend.New(strings.Split(keyList, ","), store)

	log.Printf("archerikd listening on %s (data: %s)", *addr, *data)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
