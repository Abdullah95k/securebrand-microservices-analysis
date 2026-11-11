package provider

import (
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

// Estimate is the expected cost of a request before it is sent: input
// tokens estimated from characters (Arabic runs ~2.5-3 characters per
// token) and expected output tokens. systemChars is the system prompt and
// the schema. In real time they are assumed cached after the first request;
// in a batch they are priced as plain input, since a batch's cache hits are
// not to be counted on, and with 1-2 items a request they are most of its
// cost (assumed cached, 11 Nov estimates ran about 6 times low, and the
// day's cap was passed by what was in flight).
func (p Pricing) Estimate(model string, systemChars, userChars, images, expectedOut int, batch bool) float64 {
	u := Usage{
		InputTokens:  userChars/3 + images*900,
		OutputTokens: expectedOut,
	}
	if batch {
		u.InputTokens += systemChars / 3
	} else {
		u.CacheReadTokens = systemChars / 3
	}
	return p.Cost(model, u, batch)
}
