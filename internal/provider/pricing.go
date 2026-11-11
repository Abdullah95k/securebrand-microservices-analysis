package provider

import (
	"math"
	"strings"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
)

// Pricing computes the cost of requests from list prices (USD per million
// tokens), batch discounts included.
type Pricing map[string]config.ModelPrice

// Price returns the price of model; a model missing from the table costs 0
// and is reported by Known.
func (p Pricing) Price(model string) config.ModelPrice {
	if pr, ok := p[model]; ok {
		return pr
	}
	// Dated or prefixed variants ("anthropic.claude-haiku-5-5") use the base
	// entry.
	for name, pr := range p {
		if strings.HasSuffix(model, name) {
			return pr
		}
	}
	return config.ModelPrice{}
}

// Known reports whether model has a price.
func (p Pricing) Known(model string) bool {
	pr := p.Price(model)
	return pr.Input > 0 || pr.Output > 0 || pr.PerMinute > 0 || model == "fake"
}

// Cost is the USD cost of usage on model, in the batch lane or not.
func (p Pricing) Cost(model string, u Usage, batch bool) float64 {
	pr := p.Price(model)
	c := (float64(u.InputTokens)*pr.Input + float64(u.OutputTokens)*pr.Output +
		float64(u.CacheReadTokens)*pr.CacheRead + float64(u.CacheWriteTokens)*cacheWrite(pr)) / 1e6
	if batch && pr.BatchDiscount > 0 {
		c *= pr.BatchDiscount
	}
	return c
}

// cacheWrite defaults to 1.25x the input price (5-minute cache writes).
func cacheWrite(pr config.ModelPrice) float64 {
	if pr.CacheWrite > 0 {
		return pr.CacheWrite
	}
	return pr.Input * 1.25
}

// Sizes the estimate counts in tokens, measured on 11 Nov: the comment
// route's system prompt and schema, 23,237 bytes, were read from the cache
// as 13,238 tokens a request (1.75 bytes a token; the schema as the API
// renders it costs more than its JSON). Item text is mostly Arabic, two
// bytes a letter in UTF-8: counted at 2 bytes a token, on the high side.
const (
	PrefixBytesPerToken = 1.75
	TextBytesPerToken   = 2.0
	ImageTokens         = 900
)

// Est is a request's cost before it is sent, at the two ends of its
// prefix (system prompt and schema, marked for caching): HitUSD with the
// prefix read from the cache, MissUSD with it written to the cache (a
// write is 1.25 times plain input), the most a prefix costs. Item text is
// priced as input and output as expected either way.
type Est struct {
	HitUSD, MissUSD float64
}

// At is the cost with the given share of the prefix read from the cache
// (0 to 1) and the rest written to it.
func (e Est) At(hit float64) float64 {
	hit = math.Min(math.Max(hit, 0), 1)
	return hit*e.HitUSD + (1-hit)*e.MissUSD
}

// Estimate is the cost of a request before it is sent, from the byte
// sizes of its prefix (system prompt and schema) and of its own text, its
// images, and the output expected of it (in tokens). An unpriced model
// estimates at 0.
func (p Pricing) Estimate(model string, prefixBytes, textBytes, images, expectedOut int, batch bool) Est {
	prefix := int(math.Ceil(float64(prefixBytes) / PrefixBytesPerToken))
	u := Usage{
		InputTokens:     int(math.Ceil(float64(textBytes)/TextBytesPerToken)) + images*ImageTokens,
		OutputTokens:    expectedOut,
		CacheReadTokens: prefix,
	}
	e := Est{HitUSD: p.Cost(model, u, batch)}
	u.CacheReadTokens, u.CacheWriteTokens = 0, prefix
	e.MissUSD = p.Cost(model, u, batch)
	return e
}
