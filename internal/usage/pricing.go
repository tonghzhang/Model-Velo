package usage

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"time"

	"model-velo/internal/config"
)

const (
	CostSourceProvider = "provider_reported"
	CostSourceCatalog  = "catalog"
	CostSourceCache    = "cache"
)

type CostSnapshot struct {
	InputNanoUSD   *int64
	OutputNanoUSD  *int64
	TotalNanoUSD   int64
	Currency       string
	Source         string
	PricingVersion string
	Caveat         string
}

type CostResult struct {
	Snapshot *CostSnapshot
	Caveat   string
}

type PricingCatalog struct {
	entries map[string][]priceEntry
}

type priceEntry struct {
	providerID string
	model      string
	version    string
	from       time.Time
	until      time.Time
	rates      tokenRates
}

type tokenRates struct {
	input           int64
	output          int64
	cachedRead      optionalRate
	cachedWrite     optionalRate
	audioInput      optionalRate
	audioOutput     optionalRate
	imageInput      optionalRate
	reasoningOutput optionalRate
}

type optionalRate struct {
	value int64
	set   bool
}

func NewPricingCatalog(configured []config.UsagePrice) (*PricingCatalog, error) {
	catalog := &PricingCatalog{entries: make(map[string][]priceEntry)}
	for index, candidate := range configured {
		entry, err := parsePriceEntry(candidate)
		if err != nil {
			return nil, fmt.Errorf("usage price %d: %w", index, err)
		}
		key := priceKey(entry.providerID, entry.model)
		catalog.entries[key] = append(catalog.entries[key], entry)
	}
	for key, entries := range catalog.entries {
		slices.SortFunc(entries, func(left, right priceEntry) int {
			return left.from.Compare(right.from)
		})
		for index := 1; index < len(entries); index++ {
			if entries[index].from.Before(entries[index-1].until) {
				return nil, fmt.Errorf("usage prices for %q have overlapping effective windows", key)
			}
		}
		catalog.entries[key] = entries
	}
	return catalog, nil
}

func (catalog *PricingCatalog) Quote(event Event) CostResult {
	retryCaveat := ""
	if event.Attempts > 1 || event.Retries > 0 || event.Fallbacks > 0 {
		retryCaveat = "cost_excludes_unreported_failed_attempts"
	}
	if event.Status == StatusCacheHit {
		zero := int64(0)
		return CostResult{Snapshot: &CostSnapshot{
			InputNanoUSD:   &zero,
			OutputNanoUSD:  &zero,
			TotalNanoUSD:   0,
			Currency:       "USD",
			Source:         CostSourceCache,
			PricingVersion: "cache-v1",
		}}
	}
	if event.Usage == nil {
		return CostResult{Caveat: joinCaveats(event.UsageCaveat, "cost_unknown_without_usage", retryCaveat)}
	}
	if reported := event.Usage.ReportedCost; reported != nil {
		return CostResult{Snapshot: &CostSnapshot{
			InputNanoUSD:   cloneInt64(reported.InputNanoUSD),
			OutputNanoUSD:  cloneInt64(reported.OutputNanoUSD),
			TotalNanoUSD:   reported.TotalNanoUSD,
			Currency:       reported.Currency,
			Source:         CostSourceProvider,
			PricingVersion: "provider-reported",
			Caveat:         joinCaveats(event.UsageCaveat, retryCaveat),
		}}
	}

	entry, ok := catalog.lookup(event.ProviderID, event.UpstreamModel, event.RequestedModel, event.StartedAt)
	if !ok {
		return CostResult{Caveat: joinCaveats(event.UsageCaveat, "pricing_not_found", retryCaveat)}
	}
	inputCost, inputCaveat := quoteInput(event.Usage, entry.rates)
	outputCost, outputCaveat := quoteOutput(event.Usage, entry.rates)
	if inputCaveat == "cost_overflow" || outputCaveat == "cost_overflow" {
		return CostResult{Caveat: joinCaveats(event.UsageCaveat, "cost_overflow", retryCaveat)}
	}
	total, ok := safeAdd(inputCost, outputCost)
	if !ok {
		return CostResult{Caveat: joinCaveats(event.UsageCaveat, "cost_overflow", retryCaveat)}
	}
	return CostResult{Snapshot: &CostSnapshot{
		InputNanoUSD:   &inputCost,
		OutputNanoUSD:  &outputCost,
		TotalNanoUSD:   total,
		Currency:       "USD",
		Source:         CostSourceCatalog,
		PricingVersion: entry.version,
		Caveat: joinCaveats(
			event.UsageCaveat,
			inputCaveat,
			outputCaveat,
			retryCaveat,
		),
	}}
}

func (catalog *PricingCatalog) Empty() bool {
	return catalog == nil || len(catalog.entries) == 0
}

func parsePriceEntry(candidate config.UsagePrice) (priceEntry, error) {
	entry := priceEntry{
		providerID: strings.TrimSpace(candidate.ProviderID),
		model:      strings.TrimSpace(candidate.Model),
		version:    strings.TrimSpace(candidate.Version),
		from:       time.Unix(0, 0).UTC(),
		until:      time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
	}
	switch {
	case !validPriceName(entry.providerID, 100):
		return priceEntry{}, errors.New("provider is invalid")
	case !validPriceName(entry.model, 200):
		return priceEntry{}, errors.New("model is invalid")
	case !validPriceName(entry.version, 100):
		return priceEntry{}, errors.New("version is invalid")
	}
	var err error
	if strings.TrimSpace(candidate.EffectiveFrom) != "" {
		entry.from, err = time.Parse(time.RFC3339, strings.TrimSpace(candidate.EffectiveFrom))
		if err != nil {
			return priceEntry{}, errors.New("effective_from must be RFC3339")
		}
		entry.from = entry.from.UTC()
	}
	if strings.TrimSpace(candidate.EffectiveUntil) != "" {
		entry.until, err = time.Parse(time.RFC3339, strings.TrimSpace(candidate.EffectiveUntil))
		if err != nil {
			return priceEntry{}, errors.New("effective_until must be RFC3339")
		}
		entry.until = entry.until.UTC()
	}
	if !entry.until.After(entry.from) {
		return priceEntry{}, errors.New("effective_until must be after effective_from")
	}

	entry.rates.input, err = parseRequiredRate(candidate.InputUSDPerMillion, "input_usd_per_million")
	if err != nil {
		return priceEntry{}, err
	}
	entry.rates.output, err = parseRequiredRate(candidate.OutputUSDPerMillion, "output_usd_per_million")
	if err != nil {
		return priceEntry{}, err
	}
	optional := []struct {
		raw    string
		name   string
		target *optionalRate
	}{
		{candidate.CachedReadUSDPerMillion, "cached_read_usd_per_million", &entry.rates.cachedRead},
		{candidate.CachedWriteUSDPerMillion, "cached_write_usd_per_million", &entry.rates.cachedWrite},
		{candidate.AudioInputUSDPerMillion, "audio_input_usd_per_million", &entry.rates.audioInput},
		{candidate.AudioOutputUSDPerMillion, "audio_output_usd_per_million", &entry.rates.audioOutput},
		{candidate.ImageInputUSDPerMillion, "image_input_usd_per_million", &entry.rates.imageInput},
		{candidate.ReasoningOutputUSDPerMillion, "reasoning_output_usd_per_million", &entry.rates.reasoningOutput},
	}
	for _, field := range optional {
		field.target.value, field.target.set, err = parseOptionalRate(field.raw, field.name)
		if err != nil {
			return priceEntry{}, err
		}
	}
	return entry, nil
}

func parseRequiredRate(raw, name string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	rate, err := parseUSD(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return rate, nil
}

func parseOptionalRate(raw, name string) (int64, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, false, nil
	}
	rate, err := parseUSD(raw)
	if err != nil {
		return 0, false, fmt.Errorf("%s is invalid", name)
	}
	return rate, true, nil
}

func parseUSD(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, "+") {
		return 0, errors.New("invalid USD amount")
	}
	value, ok := new(big.Rat).SetString(raw)
	if !ok {
		return 0, errors.New("invalid USD amount")
	}
	if value.Sign() < 0 {
		return 0, errors.New("invalid USD amount")
	}
	scaled := new(big.Rat).Mul(value, big.NewRat(1_000_000_000, 1))
	rounded := roundedRat(scaled)
	if !rounded.IsInt64() {
		return 0, errors.New("USD amount is too large")
	}
	return rounded.Int64(), nil
}

func roundedRat(value *big.Rat) *big.Int {
	numerator := new(big.Int).Set(value.Num())
	denominator := new(big.Int).Set(value.Denom())
	half := new(big.Int).Quo(denominator, big.NewInt(2))
	numerator.Add(numerator, half)
	return numerator.Quo(numerator, denominator)
}

func validPriceName(value string, maximum int) bool {
	return value == strings.TrimSpace(value) &&
		len(value) > 0 &&
		len(value) <= maximum &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func (catalog *PricingCatalog) lookup(
	providerID string,
	upstreamModel string,
	requestedModel string,
	at time.Time,
) (priceEntry, bool) {
	if catalog == nil {
		return priceEntry{}, false
	}
	providerID = strings.TrimSpace(providerID)
	models := []string{strings.TrimSpace(upstreamModel), strings.TrimSpace(requestedModel), "*"}
	providers := []string{providerID, "*"}
	for _, provider := range providers {
		for _, model := range models {
			if model == "" {
				continue
			}
			entries := catalog.entries[priceKey(provider, model)]
			index := sort.Search(len(entries), func(index int) bool {
				return entries[index].from.After(at)
			}) - 1
			if index >= 0 && at.Before(entries[index].until) {
				return entries[index], true
			}
		}
	}
	return priceEntry{}, false
}

func priceKey(providerID, model string) string {
	return providerID + "\x00" + model
}

func quoteInput(usage *TokenUsage, rates tokenRates) (int64, string) {
	if usage.InputDetails == nil {
		cost, ok := tokenCost(usage.Input, rates.input)
		if !ok {
			return 0, "cost_overflow"
		}
		return cost, ""
	}
	details := usage.InputDetails
	special := details.CachedRead + details.CachedWrite + details.Audio + details.Image
	if special > usage.Input {
		cost, _ := tokenCost(usage.Input, rates.input)
		return cost, "input_token_details_overlap"
	}
	components := []struct {
		tokens int64
		rate   int64
	}{
		{usage.Input - special, rates.input},
		{details.CachedRead, rateOr(rates.cachedRead, rates.input)},
		{details.CachedWrite, rateOr(rates.cachedWrite, rates.input)},
		{details.Audio, rateOr(rates.audioInput, rates.input)},
		{details.Image, rateOr(rates.imageInput, rates.input)},
	}
	return sumTokenCosts(components)
}

func quoteOutput(usage *TokenUsage, rates tokenRates) (int64, string) {
	if usage.OutputDetails == nil {
		cost, ok := tokenCost(usage.Output, rates.output)
		if !ok {
			return 0, "cost_overflow"
		}
		return cost, ""
	}
	details := usage.OutputDetails
	special := details.Audio + details.Reasoning
	if special > usage.Output {
		cost, _ := tokenCost(usage.Output, rates.output)
		return cost, "output_token_details_overlap"
	}
	components := []struct {
		tokens int64
		rate   int64
	}{
		{usage.Output - special, rates.output},
		{details.Audio, rateOr(rates.audioOutput, rates.output)},
		{details.Reasoning, rateOr(rates.reasoningOutput, rates.output)},
	}
	return sumTokenCosts(components)
}

func rateOr(optional optionalRate, fallback int64) int64 {
	if optional.set {
		return optional.value
	}
	return fallback
}

func sumTokenCosts(components []struct {
	tokens int64
	rate   int64
}) (int64, string) {
	var total int64
	for _, component := range components {
		cost, ok := tokenCost(component.tokens, component.rate)
		if !ok {
			return 0, "cost_overflow"
		}
		total, ok = safeAdd(total, cost)
		if !ok {
			return 0, "cost_overflow"
		}
	}
	return total, ""
}

func tokenCost(tokens, nanoUSDPerMillion int64) (int64, bool) {
	if tokens == 0 || nanoUSDPerMillion == 0 {
		return 0, true
	}
	numerator := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(nanoUSDPerMillion))
	numerator.Add(numerator, big.NewInt(500_000))
	numerator.Quo(numerator, big.NewInt(1_000_000))
	if !numerator.IsInt64() {
		return 0, false
	}
	return numerator.Int64(), true
}

func safeAdd(left, right int64) (int64, bool) {
	total := new(big.Int).Add(big.NewInt(left), big.NewInt(right))
	if !total.IsInt64() {
		return 0, false
	}
	return total.Int64(), true
}

func joinCaveats(caveats ...string) string {
	seen := make(map[string]struct{}, len(caveats))
	joined := make([]string, 0, len(caveats))
	for _, caveat := range caveats {
		for _, item := range strings.Split(caveat, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, exists := seen[item]; exists {
				continue
			}
			seen[item] = struct{}{}
			joined = append(joined, item)
		}
	}
	result := strings.Join(joined, ",")
	if len(result) > maximumUsageCaveatLen {
		return result[:maximumUsageCaveatLen]
	}
	return result
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
