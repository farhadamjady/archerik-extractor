package spring

import (
	"strings"

	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/lang/java"
)

// kafkaTopicIndexer records Kafka `@Bean NewTopic` methods that build a topic
// via TopicBuilder.name(<expr>). The idiomatic Spring producer injects such a
// bean and sends through a Message whose KafkaHeaders.TOPIC header is
// `topic.name()` — so the destination never appears as a literal at the
// KafkaTemplate.send call site. Indexing the bean's name-argument lets the
// producer detector resolve that topic through the existing config layer.
type kafkaTopicIndexer struct{}

func (kafkaTopicIndexer) Name() string { return "spring.kafka-topics" }

func (kafkaTopicIndexer) Index(ic *provider.IndexContext, idx *provider.Index) error {
	for _, f := range javaFilesOf(ic) {
		f.Root().Walk(func(n java.Node) bool {
			if n.Type() != "method_declaration" || !beanReturnsNewTopic(n) {
				return true
			}
			if arg := topicBuilderNameArg(n); arg.Valid() {
				idx.TopicBeans = append(idx.TopicBeans, provider.TopicBean{
					Name:    n.ChildByFieldName("name").Text(),
					NameArg: arg,
				})
			}
			return true
		})
	}
	return nil
}

// beanReturnsNewTopic reports whether a method is a @Bean returning NewTopic.
func beanReturnsNewTopic(m java.Node) bool {
	mods := childByType(m, "modifiers")
	if !findAnnotation(mods, "Bean").Valid() {
		return false
	}
	return isNewTopicType(m.ChildByFieldName("type").Text())
}

// topicBuilderNameArg finds the TopicBuilder.name(<arg>) call inside a bean
// method and returns its first argument node (the topic name expression).
func topicBuilderNameArg(m java.Node) java.Node {
	var arg java.Node
	m.Walk(func(n java.Node) bool {
		if arg.Valid() {
			return false
		}
		if n.Type() == "method_invocation" &&
			n.ChildByFieldName("name").Text() == "name" &&
			n.ChildByFieldName("object").Text() == "TopicBuilder" {
			if a := n.ChildByFieldName("arguments").NamedChild(0); a.Valid() {
				arg = a
			}
		}
		return true
	})
	return arg
}

// isNewTopicType reports whether a declared type text names Kafka's NewTopic,
// tolerating a package qualifier and stray whitespace.
func isNewTopicType(typeText string) bool {
	s := strings.TrimSpace(typeText)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s == "NewTopic"
}
