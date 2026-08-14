// Package registry is the single place providers are registered. Adding a
// framework = one line in Default(). Nothing else in the core changes —
// detection picks the winner from this list by Match score.
package registry

import (
	"github.com/farhadamjady/archerik-extractor/internal/provider"
	"github.com/farhadamjady/archerik-extractor/internal/provider/aspnet"
	"github.com/farhadamjady/archerik-extractor/internal/provider/express"
	"github.com/farhadamjady/archerik-extractor/internal/provider/micronaut"
	"github.com/farhadamjady/archerik-extractor/internal/provider/nestjs"
	"github.com/farhadamjady/archerik-extractor/internal/provider/nethttp"
	"github.com/farhadamjady/archerik-extractor/internal/provider/quarkus"
	"github.com/farhadamjady/archerik-extractor/internal/provider/spring"
	"github.com/farhadamjady/archerik-extractor/internal/provider/springkt"
)

// Default returns every registered provider. Detection picks the single winner
// per repo by Match score (fail-loud on tie), so the order here is irrelevant.
func Default() []provider.Provider {
	return []provider.Provider{
		spring.New(),
		micronaut.New(),
		quarkus.New(),
		springkt.New(),
		nestjs.New(),
		express.New(),
		aspnet.New(),
		nethttp.New(),
	}
}
