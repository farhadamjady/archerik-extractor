package graphdiff

import (
	"fmt"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/model"
)

// maxItemsPerSection caps rendered lines per section, so a first scan (empty
// baseline -> everything "added") cannot produce a thousand-line PR comment.
const maxItemsPerSection = 20

// Markdown renders the diff as a GitHub PR comment. An empty diff renders ""
// (the caller posts nothing — the spec's fast path). The format is plain
// GitHub-flavored markdown: works in PR comments, issues, and summaries.
func Markdown(d *Diff) string {
	if d.Empty() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### 🏗 Architecture impact: **%s**\n\n", d.ServiceID)
	fmt.Fprintf(&b, "**%d added · %d removed · %d changed**\n", d.Summary.Added, d.Summary.Removed, d.Summary.Changed)

	renderCategory(&b, "Endpoints", d.Endpoints, endpointLine)
	renderCategory(&b, "Outbound dependencies", d.Outbound, dependencyLine)
	renderCategory(&b, "Kafka — produced topics", d.KafkaProducers, kafkaLine)
	renderCategory(&b, "Kafka — consumed topics", d.KafkaConsumers, kafkaLine)

	b.WriteString("\n<sub>service-discovery · confidence: confirmed = found literally · " +
		"likely = resolved through config · uncertain = not statically resolvable (still real)</sub>\n")
	return b.String()
}

// renderCategory writes one section: added and removed as ± lines, changed as
// nested bullets with the field-level detail.
func renderCategory[T any](b *strings.Builder, title string, c CategoryDiff[T], line func(T) string) {
	if len(c.Added) == 0 && len(c.Removed) == 0 && len(c.Changed) == 0 {
		return
	}
	fmt.Fprintf(b, "\n#### %s\n", title)

	n := 0
	for _, a := range c.Added {
		if capped(b, &n) {
			break
		}
		fmt.Fprintf(b, "- ➕ %s\n", line(a))
	}
	for _, r := range c.Removed {
		if capped(b, &n) {
			break
		}
		fmt.Fprintf(b, "- ➖ %s\n", line(r))
	}
	for _, ch := range c.Changed {
		if capped(b, &n) {
			break
		}
		// identity keys use "|" internally (target|detection, topic|role) —
		// render it as a separator humans read
		fmt.Fprintf(b, "- 🔀 **%s** — %s\n", strings.ReplaceAll(ch.Key, "|", " · "), strings.Join(ch.Changes, ", "))
		for i, fd := range ch.SchemaDiff {
			if i >= 10 {
				fmt.Fprintf(b, "  - …and %d more field changes\n", len(ch.SchemaDiff)-10)
				break
			}
			b.WriteString("  - " + fieldLine(fd) + "\n")
		}
		if before, after := confidencePair(ch.Changes, ch.Before, ch.After); before != "" {
			fmt.Fprintf(b, "  - confidence: %s → %s%s\n", before, after, warnIf(after))
		}
	}
	total := len(c.Added) + len(c.Removed) + len(c.Changed)
	if total > maxItemsPerSection {
		fmt.Fprintf(b, "- …and %d more\n", total-maxItemsPerSection)
	}
}

func capped(b *strings.Builder, n *int) bool {
	*n++
	return *n > maxItemsPerSection
}

// --- line renderers (every line carries detection + confidence) ---

func endpointLine(e model.Endpoint) string {
	return fmt.Sprintf("**%s %s** · %s · %s%s", e.Method, e.Path, e.Detection, e.Confidence, warnIf(string(e.Confidence)))
}

func dependencyLine(dep model.Dependency) string {
	target := dep.TargetName
	if dep.URL != "" && dep.URL != dep.TargetName {
		target = fmt.Sprintf("%s → `%s`", orUnknown(dep.TargetName), dep.URL)
	} else if target == "" {
		target = "(unresolved target)"
	} else {
		target = "**" + target + "**"
	}
	return fmt.Sprintf("%s · %s · %s · %s%s", target, dep.Protocol, dep.Detection, dep.Confidence, warnIf(string(dep.Confidence)))
}

func kafkaLine(k model.KafkaEdge) string {
	schema := ""
	if k.Schema != nil {
		schema = fmt.Sprintf(" · schema `%s`", k.Schema.Type)
	}
	return fmt.Sprintf("**%s**%s · %s · %s%s", orUnknown(k.Topic), schema, k.Detection, k.Confidence, warnIf(string(k.Confidence)))
}

func fieldLine(fd FieldDiff) string {
	switch fd.Change {
	case "added":
		return fmt.Sprintf("`%s`: added (%s)", fd.Field, fd.After)
	case "removed":
		return fmt.Sprintf("`%s`: removed (was %s)", fd.Field, fd.Before)
	default: // type_changed / requiredness_changed / nullability_changed
		what := strings.TrimSuffix(fd.Change, "_changed")
		return fmt.Sprintf("`%s`: %s changed %s → %s", fd.Field, what, fd.Before, fd.After)
	}
}

// confidencePair extracts before/after confidence when "confidence" changed.
func confidencePair[T any](changes []string, before, after T) (string, string) {
	for _, c := range changes {
		if c == "confidence" {
			return confOf(before), confOf(after)
		}
	}
	return "", ""
}

func confOf(v any) string {
	switch t := v.(type) {
	case model.Endpoint:
		return string(t.Confidence)
	case model.Dependency:
		return string(t.Confidence)
	case model.KafkaEdge:
		return string(t.Confidence)
	}
	return ""
}

func warnIf(conf string) string {
	if conf == string(model.Uncertain) {
		return " ⚠️"
	}
	return ""
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
