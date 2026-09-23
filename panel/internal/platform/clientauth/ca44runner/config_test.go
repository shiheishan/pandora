package ca44runner

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testManifestPin = "1111111111111111111111111111111111111111111111111111111111111111"
	testSignerPin   = "2222222222222222222222222222222222222222222222222222222222222222"
	testArtifactPin = "3333333333333333333333333333333333333333333333333333333333333333"
	testEvidencePin = "4444444444444444444444444444444444444444444444444444444444444444"
)

func validConfig() Config {
	return Config{
		ExpectedManifestSHA256:  testManifestPin,
		ApprovedSignerKeySHA256: testSignerPin,
		ArtifactKeySHA256:       testArtifactPin,
		EvidenceKeySHA256:       testEvidencePin,
		TrustRoot:               "/var/lib/pandora/ca44",
		ManifestPath:            "release/manifest.txt",
		SignerPublicKeyPath:     "trust/signer.pub",
		ContractPath:            "trust/contract.txt",
		ClassifierPath:          "bin/classifier",
		VerifierPath:            "bin/verifier",
		SourcePath:              "input/devices.ndjson",
		ArtifactKeyPath:         "keys/artifact.key",
		EvidenceKeyPath:         "keys/evidence.key",
		StagingRoot:             "state/staging",
		PublishRoot:             "published/releases",
		ChildTimeout:            2 * time.Minute,
		WaitDelay:               25 * time.Millisecond,
	}
}

func TestConfigValidateAcceptedBoundaries(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		config := validConfig()
		config.ChildTimeout = MinChildTimeout
		config.WaitDelay = MinWaitDelay
		if err := config.Validate("linux", architecture); err != nil {
			t.Fatalf("minimum boundary rejected for %s: %v", architecture, err)
		}
		config.ChildTimeout = MaxChildTimeout
		config.WaitDelay = MaxWaitDelay
		if err := config.Validate("linux", architecture); err != nil {
			t.Fatalf("maximum boundary rejected for %s: %v", architecture, err)
		}
	}
}

func TestConfigExactPublicShapeContainsNoKeyMaterial(t *testing.T) {
	want := []string{
		"ExpectedManifestSHA256", "ApprovedSignerKeySHA256", "ArtifactKeySHA256",
		"EvidenceKeySHA256", "TrustRoot", "ManifestPath", "SignerPublicKeyPath",
		"ContractPath", "ClassifierPath", "VerifierPath", "SourcePath",
		"ArtifactKeyPath", "EvidenceKeyPath", "StagingRoot", "PublishRoot",
		"ChildTimeout", "WaitDelay",
	}
	configType := reflect.TypeOf(Config{})
	got := make([]string, configType.NumField())
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		got[i] = field.Name
		if field.Type.Kind() == reflect.Slice || field.Type.Kind() == reflect.Array {
			t.Fatalf("Config must not contain key byte containers: %s %s", field.Name, field.Type)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Config public shape drifted:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestConfigRejectsUnsupportedRuntime(t *testing.T) {
	for name, runtime := range map[string][2]string{
		"windows": {"windows", "amd64"},
		"darwin":  {"darwin", "arm64"},
		"386":     {"linux", "386"},
		"empty":   {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validConfig().Validate(runtime[0], runtime[1]); err == nil {
				t.Fatal("unsupported runtime accepted")
			}
		})
	}
}

func TestConfigRejectsInvalidPinsAndKeyAliases(t *testing.T) {
	tests := map[string]func(*Config){
		"empty manifest pin":     func(c *Config) { c.ExpectedManifestSHA256 = "" },
		"uppercase signer pin":   func(c *Config) { c.ApprovedSignerKeySHA256 = strings.Repeat("A", 64) },
		"nonhex artifact pin":    func(c *Config) { c.ArtifactKeySHA256 = strings.Repeat("g", 64) },
		"short evidence pin":     func(c *Config) { c.EvidenceKeySHA256 = strings.Repeat("4", 63) },
		"same key pins":          func(c *Config) { c.EvidenceKeySHA256 = c.ArtifactKeySHA256 },
		"same key paths":         func(c *Config) { c.EvidenceKeyPath = c.ArtifactKeyPath },
		"generic object alias":   func(c *Config) { c.SourcePath = c.ManifestPath },
		"staging inside publish": func(c *Config) { c.StagingRoot = c.PublishRoot + "/staging" },
		"publish inside staging": func(c *Config) { c.PublishRoot = c.StagingRoot + "/publish" },
		"staging equals protected": func(c *Config) {
			c.StagingRoot = c.ManifestPath
		},
		"staging ancestor of protected": func(c *Config) {
			c.StagingRoot = "release"
		},
		"staging descendant of protected": func(c *Config) {
			c.ManifestPath = "state"
		},
		"publish ancestor of protected": func(c *Config) {
			c.PublishRoot = "trust"
		},
		"publish descendant of protected": func(c *Config) {
			c.SignerPublicKeyPath = "published"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			mutate(&config)
			if err := config.Validate("linux", "amd64"); err == nil {
				t.Fatal("invalid identity or alias accepted")
			}
		})
	}
}

func TestConfigRejectsInvalidTrustRoot(t *testing.T) {
	segment := strings.Repeat("a", maxPathSegmentBytes+1)
	for name, value := range map[string]string{
		"empty":          "",
		"relative":       "var/lib/pandora",
		"filesystem":     "/",
		"unclean parent": "/var/lib/../tmp",
		"double slash":   "/var//lib/pandora",
		"trailing slash": "/var/lib/pandora/",
		"backslash":      "/var/lib\\pandora",
		"colon":          "/var/lib/pandora:old",
		"control":        "/var/lib/pandora\nold",
		"long segment":   "/var/lib/" + segment,
		"invalid utf8":   "/var/lib/" + string([]byte{0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			config.TrustRoot = value
			if err := config.Validate("linux", "amd64"); err == nil {
				t.Fatalf("invalid trust root accepted: %q", value)
			}
		})
	}
}

func TestConfigRejectsInvalidRelativeObjectPaths(t *testing.T) {
	segment := strings.Repeat("a", maxPathSegmentBytes+1)
	longPath := strings.Repeat("a/", maxConfigPathBytes/2) + "a"
	for name, value := range map[string]string{
		"empty":          "",
		"absolute":       "/etc/passwd",
		"dot":            ".",
		"parent":         "..",
		"traversal":      "keys/../secret",
		"double slash":   "keys//secret",
		"trailing slash": "keys/secret/",
		"windows drive":  "C:/keys/secret",
		"backslash":      "keys\\secret",
		"nul":            "keys/secret\x00old",
		"newline":        "keys/secret\nold",
		"long segment":   "keys/" + segment,
		"long path":      longPath,
		"invalid utf8":   "keys/" + string([]byte{0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			config.ManifestPath = value
			if err := config.Validate("linux", "amd64"); err == nil {
				t.Fatalf("invalid relative path accepted: %q", value)
			}
		})
	}
}

func TestConfigRejectsTimeoutsOutsidePolicy(t *testing.T) {
	tests := map[string]func(*Config){
		"zero child":      func(c *Config) { c.ChildTimeout = 0 },
		"negative child":  func(c *Config) { c.ChildTimeout = -time.Second },
		"child below min": func(c *Config) { c.ChildTimeout = MinChildTimeout - time.Nanosecond },
		"child above max": func(c *Config) { c.ChildTimeout = MaxChildTimeout + time.Nanosecond },
		"zero wait":       func(c *Config) { c.WaitDelay = 0 },
		"negative wait":   func(c *Config) { c.WaitDelay = -time.Millisecond },
		"wait below min":  func(c *Config) { c.WaitDelay = MinWaitDelay - time.Nanosecond },
		"wait above max":  func(c *Config) { c.WaitDelay = MaxWaitDelay + time.Nanosecond },
		"wait equals child": func(c *Config) {
			c.ChildTimeout = MinChildTimeout
			c.WaitDelay = MinChildTimeout
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			mutate(&config)
			if err := config.Validate("linux", "amd64"); err == nil {
				t.Fatal("invalid timeout accepted")
			}
		})
	}
}
