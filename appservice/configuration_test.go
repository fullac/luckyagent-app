package appservice

import (
	"errors"
	"testing"

	"github.com/yurika0211/luckyagent-runtime/config"
)

type configurationFixture struct {
	stored, active                               *config.Config
	validationErr, saveErr, applyErr, restoreErr error
	writes, applies                              int
}

func (f *configurationFixture) Get() *config.Config { return config.Clone(f.stored) }
func (f *configurationFixture) Replace(next *config.Config) error {
	f.writes++
	if f.writes == 1 && f.saveErr != nil {
		return f.saveErr
	}
	if f.writes > 1 && f.restoreErr != nil {
		return f.restoreErr
	}
	f.stored = config.Clone(next)
	return nil
}
func (f *configurationFixture) ValidateRuntimeConfig(*config.Config) error { return f.validationErr }
func (f *configurationFixture) ApplyRuntimeConfig(next *config.Config) error {
	f.applies++
	if f.applies == 1 && f.applyErr != nil {
		return f.applyErr
	}
	f.active = config.Clone(next)
	return nil
}

func TestConfigurationPreviewAndCommit(t *testing.T) {
	current := config.DefaultConfig()
	fixture := &configurationFixture{stored: config.Clone(current), active: config.Clone(current)}
	service := NewConfiguration(fixture, fixture)
	submitted := config.Clone(current)
	submitted.Agent.MaxIterations = 17
	change, err := service.Prepare(current, submitted)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.writes != 0 || fixture.applies != 0 || fixture.active.Agent.MaxIterations != current.Agent.MaxIterations {
		t.Fatal("preview changed runtime")
	}
	active, err := service.Commit(change)
	if err != nil || active.Agent.MaxIterations != 17 || fixture.active.Agent.MaxIterations != 17 || fixture.writes != 1 {
		t.Fatalf("commit: %v", err)
	}
}

func TestConfigurationFailureStagesAndRollback(t *testing.T) {
	failed := errors.New("injected failure")
	rollbackFailed := errors.New("rollback write failure")
	for _, stage := range []string{"validation", "save", "apply", "rollback"} {
		t.Run(stage, func(t *testing.T) {
			current := config.DefaultConfig()
			expectedIterations := current.Agent.MaxIterations
			fixture := &configurationFixture{stored: config.Clone(current), active: config.Clone(current)}
			switch stage {
			case "validation":
				fixture.validationErr = failed
			case "save":
				fixture.saveErr = failed
			case "apply":
				fixture.applyErr = failed
			case "rollback":
				fixture.applyErr = failed
				fixture.restoreErr = rollbackFailed
			}
			service := NewConfiguration(fixture, fixture)
			submitted := config.Clone(current)
			submitted.Agent.MaxIterations = 17
			change, err := service.Prepare(current, submitted)
			if stage == "validation" {
				if !errors.Is(err, failed) || ConfigurationErrorStage(err) != "validation" || fixture.writes != 0 {
					t.Fatalf("validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			current.Agent.MaxIterations = 99 // caller mutation must not alter the rollback snapshot
			_, err = service.Commit(change)
			if !errors.Is(err, failed) {
				t.Fatalf("failure lost cause: %v", err)
			}
			if stage == "save" {
				if ConfigurationErrorStage(err) != "save" || fixture.applies != 0 || fixture.stored.Agent.MaxIterations != expectedIterations {
					t.Fatal("failed save exposed runtime")
				}
				return
			}
			if fixture.active.Agent.MaxIterations != expectedIterations || fixture.writes != 2 || fixture.applies != 2 {
				t.Fatal("runtime was not restored")
			}
			if stage == "apply" && (!ConfigurationRestored(err) || fixture.stored.Agent.MaxIterations != expectedIterations) {
				t.Fatal("successful rollback not recorded")
			}
			if stage == "rollback" && (ConfigurationRestored(err) || !errors.Is(err, rollbackFailed)) {
				t.Fatal("rollback failure hidden")
			}
		})
	}
}
