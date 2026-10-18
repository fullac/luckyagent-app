package appservice

import (
	service "github.com/yurika0211/luckyagent-runtime/appservice"
	"github.com/yurika0211/luckyagent-runtime/provider"
)

type ModelRuntime = service.ModelRuntime
type ModelQuery = service.ModelQuery
type ModelList = service.ModelList
type Models = service.Models
type MutableSettings = service.MutableSettings
type ModelSwitchError = service.ModelSwitchError

func NewModels(runtime ModelRuntime) *Models { return service.NewModels(runtime) }

// LegacyModelRuntime is the remaining host catalog adapter. The public module
// accepts a provider-resolution port and never imports the host catalog.
type LegacyModelRuntime interface {
	Catalog() *provider.ModelCatalog
	SwitchModel(string) error
}

type legacyModelAdapter struct{ LegacyModelRuntime }

func (a legacyModelAdapter) ResolveModelProvider(id string) (string, bool) {
	if catalog := a.Catalog(); catalog != nil {
		provider, err := catalog.ResolveProvider(id)
		return provider, err == nil
	}
	return "", false
}

func SwitchLegacyModel(runtime LegacyModelRuntime, settings MutableSettings, id string) (bool, error) {
	return service.SwitchLegacyModel(legacyModelAdapter{runtime}, settings, id)
}
