package ca42gooseinfo

import (
	"runtime/debug"
	"testing"
)

func TestDependencySetCanonicalOrderAndGuards(t *testing.T) {
	first := &debug.Module{Path: "example.com/b", Version: "v1.2.3", Sum: fixtureModuleSum}
	second := &debug.Module{Path: "example.com/a", Version: "v0.1.0", Sum: "h1:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="}
	digestA, count, err := DependencySetSHA256([]*debug.Module{first, second})
	if err != nil || count != 2 {
		t.Fatalf("canonical dependency set failed: digest=%s count=%d err=%v", digestA, count, err)
	}
	digestB, _, err := DependencySetSHA256([]*debug.Module{second, first})
	if err != nil || digestA != digestB {
		t.Fatal("dependency order changed canonical digest")
	}
	for name, modules := range map[string][]*debug.Module{
		"duplicate":   {first, first},
		"replace":     {first, {Path: "example.com/c", Version: "v1.0.0", Sum: fixtureModuleSum, Replace: second}},
		"local":       {{Path: "../local", Version: "v1.0.0", Sum: fixtureModuleSum}},
		"missing_sum": {{Path: "example.com/c", Version: "v1.0.0"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DependencySetSHA256(modules); err == nil {
				t.Fatal("invalid dependency set accepted")
			}
		})
	}
}

func TestBuildSettingSetCanonicalOrderAndGuards(t *testing.T) {
	settings := validSettings()
	digestA, count, err := BuildSettingSetSHA256(settings, "amd64")
	if err != nil || count != 8 {
		t.Fatalf("canonical setting set failed: digest=%s count=%d err=%v", digestA, count, err)
	}
	for left, right := 0, len(settings)-1; left < right; left, right = left+1, right-1 {
		settings[left], settings[right] = settings[right], settings[left]
	}
	digestB, _, err := BuildSettingSetSHA256(settings, "amd64")
	if err != nil || digestA != digestB {
		t.Fatal("setting order changed canonical digest")
	}
	for name, mutate := range map[string]func([]debug.BuildSetting) []debug.BuildSetting{
		"unknown":   func(value []debug.BuildSetting) []debug.BuildSetting { value[0].Key = "unknown"; return value },
		"duplicate": func(value []debug.BuildSetting) []debug.BuildSetting { value[1].Key = value[0].Key; return value },
		"compiler": func(value []debug.BuildSetting) []debug.BuildSetting {
			setSetting(value, "-compiler", "gccgo")
			return value
		},
		"default_godebug": func(value []debug.BuildSetting) []debug.BuildSetting {
			setSetting(value, "DefaultGODEBUG", requiredDefaultGODEBUG+",attacker=1")
			return value
		},
		"wrong_arch": func(value []debug.BuildSetting) []debug.BuildSetting {
			setSetting(value, "GOARCH", "arm64")
			return value
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := append([]debug.BuildSetting(nil), validSettings()...)
			if _, _, err := BuildSettingSetSHA256(mutate(candidate), "amd64"); err == nil {
				t.Fatal("invalid setting set accepted")
			}
		})
	}
}

func validSettings() []debug.BuildSetting {
	return []debug.BuildSetting{
		{Key: "-buildmode", Value: "exe"}, {Key: "-compiler", Value: "gc"}, {Key: "-trimpath", Value: "true"},
		{Key: "DefaultGODEBUG", Value: requiredDefaultGODEBUG},
		{Key: "CGO_ENABLED", Value: "0"}, {Key: "GOARCH", Value: "amd64"}, {Key: "GOOS", Value: "linux"},
		{Key: "GOAMD64", Value: "v1"},
	}
}

func setSetting(settings []debug.BuildSetting, key, value string) {
	for index := range settings {
		if settings[index].Key == key {
			settings[index].Value = value
			return
		}
	}
}
