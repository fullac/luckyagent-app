package appservice

import (
	"errors"
	"github.com/yurika0211/luckyagent-runtime/provider"
	"reflect"
	"testing"
)

type legacyFixture struct {
	catalog   *provider.ModelCatalog
	switchErr error
}

func (f legacyFixture) Catalog() *provider.ModelCatalog { return f.catalog }
func (f legacyFixture) SwitchModel(string) error        { return f.switchErr }

type settingsFixture struct {
	updates []string
	failKey string
	saveErr error
	saved   bool
}

func (f *settingsFixture) Set(key, value string) error {
	f.updates = append(f.updates, key)
	if key == f.failKey {
		return errors.New("write rejected")
	}
	return nil
}
func (f *settingsFixture) Save() error { f.saved = true; return f.saveErr }

func TestLegacySwitchDistinguishesRuntimeAndPersistenceFailures(t *testing.T) {
	catalog := provider.NewModelCatalog()
	catalog.Register(provider.ModelInfo{ID: "fixture-model", Provider: "openai"})
	for _, test := range []struct {
		name, key, stage    string
		runtimeErr, saveErr error
	}{
		{name: "runtime", runtimeErr: errors.New("switch rejected"), stage: "runtime"},
		{name: "provider", key: "provider", stage: "provider"},
		{name: "model", key: "model", stage: "model"},
		{name: "save", saveErr: errors.New("disk full"), stage: "save"},
		{name: "success"},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := &settingsFixture{failKey: test.key, saveErr: test.saveErr}
			persisted, err := SwitchLegacyModel(legacyFixture{catalog: catalog, switchErr: test.runtimeErr}, settings, "fixture-model")
			if test.stage == "" {
				if err != nil || !persisted || !reflect.DeepEqual(settings.updates, []string{"provider", "model"}) {
					t.Fatalf("success: %v %v %+v", persisted, err, settings)
				}
				return
			}
			var failure *ModelSwitchError
			if !errors.As(err, &failure) || failure.Stage != test.stage || persisted {
				t.Fatalf("failure: %v %v", persisted, err)
			}
			if test.stage == "runtime" && len(settings.updates) != 0 {
				t.Fatal("runtime failure wrote settings")
			}
			if (test.stage == "provider" || test.stage == "model") && settings.saved {
				t.Fatal("partial update saved")
			}
		})
	}
}
