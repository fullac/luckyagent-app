package appservice

import (
	service "github.com/yurika0211/luckyagent-runtime/appservice"
	"github.com/yurika0211/luckyagent-runtime/config"
)

type ConfigurationRuntime = service.ConfigurationRuntime[config.Config]
type ConfigurationStore = service.ConfigurationStore[config.Config]
type Configuration = service.Configuration[config.Config]
type ConfigurationChange = service.ConfigurationChange[config.Config]
type ConfigurationError = service.ConfigurationError
type Setting = service.Setting
type SettingError = service.SettingError

func NewConfiguration(store ConfigurationStore, runtime ConfigurationRuntime) *Configuration {
	return service.NewConfiguration(store, runtime, service.ConfigurationPolicy[config.Config]{
		Clone:     config.Clone,
		Normalize: config.Normalized,
		Classify:  config.ReloadClassification,
	})
}

func ConfigurationErrorStage(err error) string { return service.ConfigurationErrorStage(err) }
func ConfigurationRestored(err error) bool     { return service.ConfigurationRestored(err) }
func SaveSettings(store MutableSettings, changes ...Setting) error {
	return service.SaveSettings(store, changes...)
}
