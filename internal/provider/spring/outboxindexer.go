package spring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/provider"
)

// outboxIndexer finds Debezium outbox EventRouter topic patterns in
// Kafka-Connect connector JSONs (IMPROVEMENTS #28). In the outbox pattern the
// service "produces" by inserting an OutBox row — no KafkaTemplate anywhere —
// and the destination topic only materializes in the connector config
// (`transforms.<t>.route.topic.replacement = ${routedByValue}.events`, routed by
// the row's aggregate-type column). Connector files conventionally live at the
// repo root, next to docker-compose; we scan the top-level JSONs of the service
// root, its parent, and the repo top (no recursion — connector registration
// files do not hide in source trees).
type outboxIndexer struct{}

func (outboxIndexer) Name() string { return "spring.outbox" }

// defaultOutboxRoute is Debezium's EventRouter default when no
// route.topic.replacement is configured.
const defaultOutboxRoute = "outbox.event.${routedByValue}"

func (outboxIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	seen := map[string]bool{}
	for _, dir := range outboxSearchDirs(ic.Root) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			if route, ok := outboxRoutePattern(b); ok && !seen[route] {
				seen[route] = true
				idx.OutboxRoutes = append(idx.OutboxRoutes, route)
			}
		}
	}
	sort.Strings(idx.OutboxRoutes) // deterministic output ordering
	return nil
}

// outboxSearchDirs is the deduplicated {root, parent, repo top} set.
func outboxSearchDirs(root string) []string {
	dirs := []string{root, filepath.Dir(root)}
	if top := repoTop(root); top != "" {
		dirs = append(dirs, top)
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// outboxRoutePattern reports whether a JSON document is a Kafka-Connect
// connector configured with the Debezium outbox EventRouter, and returns its
// topic route pattern. The config map is either the document itself or nested
// under "config" (the REST-registration shape).
func outboxRoutePattern(b []byte) (string, bool) {
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return "", false
	}
	cfg := doc
	if nested, ok := doc["config"].(map[string]any); ok {
		cfg = nested
	}
	isOutbox, route := false, ""
	for k, v := range cfg {
		s, _ := v.(string)
		if strings.Contains(s, "outbox.EventRouter") {
			isOutbox = true
		}
		if strings.HasSuffix(k, "route.topic.replacement") {
			route = s
		}
	}
	if !isOutbox {
		return "", false
	}
	if route == "" {
		route = defaultOutboxRoute
	}
	return route, true
}
