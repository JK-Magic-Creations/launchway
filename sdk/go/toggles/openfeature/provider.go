// Package openfeature is an OpenFeature provider (https://openfeature.dev, an
// open standard for reading feature flags) for Launchway toggles, over the Go
// SDK in the parent directory. It is a separate module so that the SDK itself
// needs nothing outside the standard library.
//
//	client, _ := toggles.New(ctx, toggles.Options{ServerURL: "...", Key: "lwt_srv_..."})
//	openfeature.SetProviderAndWait(lwopenfeature.New(client))
//	of := openfeature.NewClient("app")
//	on, _ := of.BooleanValue(ctx, "checkout-v2", false, openfeature.NewEvaluationContext("user-1", map[string]any{"plan": "pro"}))
//
// The evaluation context's targeting key is the toggle's context key; its
// other attributes are the context's attributes. Evaluation is local.
package openfeature

import (
	"context"
	"math"

	of "github.com/open-feature/go-sdk/openfeature"

	toggles "github.com/jk-magic-creations/launchway/sdk/go/toggles"
)

// Provider reads toggles from a Launchway client.
type Provider struct{ c *toggles.Client }

// New returns the provider of a client. Closing the client is the caller's business.
func New(c *toggles.Client) *Provider { return &Provider{c: c} }

var _ of.FeatureProvider = (*Provider)(nil)

func (p *Provider) Metadata() of.Metadata { return of.Metadata{Name: "launchway"} }
func (p *Provider) Hooks() []of.Hook      { return nil }

func contextOf(flat of.FlattenedContext) toggles.Context {
	c := toggles.Context{}
	for k, v := range flat {
		if k == of.TargetingKey {
			k = "key"
		}
		c[k] = v
	}
	return c
}

// resolve evaluates and maps the reason and the problems onto OpenFeature's.
func (p *Provider) resolve(flag string, flat of.FlattenedContext, want toggles.ValueType) (toggles.Result, of.ProviderResolutionDetail, bool) {
	rs := p.c.Ruleset()
	if rs == nil {
		return toggles.Result{}, of.ProviderResolutionDetail{ResolutionError: of.NewProviderNotReadyResolutionError("no ruleset has been loaded yet"), Reason: of.ErrorReason}, false
	}
	f, ok := rs.Toggles[flag]
	if !ok {
		return toggles.Result{}, of.ProviderResolutionDetail{ResolutionError: of.NewFlagNotFoundResolutionError("no toggle " + flag), Reason: of.ErrorReason}, false
	}
	if f.Type != want {
		return toggles.Result{}, of.ProviderResolutionDetail{ResolutionError: of.NewTypeMismatchResolutionError(flag + " is a " + string(f.Type) + " toggle"), Reason: of.ErrorReason}, false
	}
	r := toggles.Evaluate(rs, flag, contextOf(flat))
	d := of.ProviderResolutionDetail{Reason: of.DefaultReason}
	switch r.Reason {
	case toggles.ReasonRule:
		d.Reason, d.Variant = of.TargetingMatchReason, r.RuleID
	case toggles.ReasonOff:
		d.Reason = of.DisabledReason
	case toggles.ReasonNoBucketKey:
		d.Reason, d.ResolutionError = of.ErrorReason, of.NewTargetingKeyMissingResolutionError("the context has no targeting key to take a percentage over")
		return r, d, false
	case toggles.ReasonError:
		d.Reason, d.ResolutionError = of.ErrorReason, of.NewGeneralResolutionError("the ruleset is malformed")
		return r, d, false
	}
	return r, d, true
}

func (p *Provider) BooleanEvaluation(_ context.Context, flag string, def bool, flat of.FlattenedContext) of.BoolResolutionDetail {
	r, d, ok := p.resolve(flag, flat, toggles.Boolean)
	if v, isBool := r.Value.(bool); ok && isBool {
		return of.BoolResolutionDetail{Value: v, ProviderResolutionDetail: d}
	}
	return of.BoolResolutionDetail{Value: def, ProviderResolutionDetail: d}
}

func (p *Provider) StringEvaluation(_ context.Context, flag string, def string, flat of.FlattenedContext) of.StringResolutionDetail {
	r, d, ok := p.resolve(flag, flat, toggles.String)
	if v, isStr := r.Value.(string); ok && isStr {
		return of.StringResolutionDetail{Value: v, ProviderResolutionDetail: d}
	}
	return of.StringResolutionDetail{Value: def, ProviderResolutionDetail: d}
}

func (p *Provider) FloatEvaluation(_ context.Context, flag string, def float64, flat of.FlattenedContext) of.FloatResolutionDetail {
	r, d, ok := p.resolve(flag, flat, toggles.Number)
	if v, isNum := toggles.Normalize(r.Value).(float64); ok && isNum {
		return of.FloatResolutionDetail{Value: v, ProviderResolutionDetail: d}
	}
	return of.FloatResolutionDetail{Value: def, ProviderResolutionDetail: d}
}

func (p *Provider) IntEvaluation(_ context.Context, flag string, def int64, flat of.FlattenedContext) of.IntResolutionDetail {
	r, d, ok := p.resolve(flag, flat, toggles.Number)
	if v, isNum := toggles.Normalize(r.Value).(float64); ok && isNum {
		if v != math.Trunc(v) {
			d.ResolutionError, d.Reason = of.NewTypeMismatchResolutionError(flag+" serves a number with a fraction, not a whole number"), of.ErrorReason
			return of.IntResolutionDetail{Value: def, ProviderResolutionDetail: d}
		}
		return of.IntResolutionDetail{Value: int64(v), ProviderResolutionDetail: d}
	}
	return of.IntResolutionDetail{Value: def, ProviderResolutionDetail: d}
}

func (p *Provider) ObjectEvaluation(_ context.Context, flag string, def any, flat of.FlattenedContext) of.InterfaceResolutionDetail {
	r, d, ok := p.resolve(flag, flat, toggles.JSON)
	if ok {
		return of.InterfaceResolutionDetail{Value: r.Value, ProviderResolutionDetail: d}
	}
	return of.InterfaceResolutionDetail{Value: def, ProviderResolutionDetail: d}
}
