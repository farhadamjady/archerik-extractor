// Package registry is the single place providers are registered. Adding a
// framework (Micronaut next) = one line in Default(). Nothing else in the core
// changes — detection picks the winner from this list by Match score.
package registry

import (
	"github.com/farhadamjady/service-discovery/internal/provider"
	"github.com/farhadamjady/service-discovery/internal/provider/spring"
)

// Default returns every registered provider.
func Default() []provider.Provider {
	return []provider.Provider{
		spring.New(),
	}
}
