package config

import "testing"

func clearSingboxEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"AEGIS_SINGBOX_GEOSITE_URL_PREFIX", "AEGIS_SINGBOX_GEOIP_URL_PREFIX", "AEGIS_SINGBOX_BLOCK_ADS"} {
		t.Setenv(k, "")
	}
}

func TestSingboxTemplateDefaultsAreEmpty(t *testing.T) {
	clearSingboxEnv(t)
	got, err := loadSingboxTemplate()
	if err != nil || got != (SingboxTemplate{}) {
		t.Fatalf("defaults = %+v err=%v, want empty (renderer defaults apply)", got, err)
	}
}

func TestSingboxTemplateOverridesAndRejects(t *testing.T) {
	clearSingboxEnv(t)
	t.Setenv("AEGIS_SINGBOX_GEOSITE_URL_PREFIX", " https://mirror.example.test/geosite/ ")
	t.Setenv("AEGIS_SINGBOX_GEOIP_URL_PREFIX", "https://mirror.example.test/geoip/")
	t.Setenv("AEGIS_SINGBOX_BLOCK_ADS", "true")
	got, err := loadSingboxTemplate()
	if err != nil || got.GeositeURLPrefix != "https://mirror.example.test/geosite/" ||
		got.GeoIPURLPrefix != "https://mirror.example.test/geoip/" || !got.BlockAds {
		t.Fatalf("overrides = %+v err=%v", got, err)
	}
	for name, bad := range map[string][2]string{
		"plain http":     {"AEGIS_SINGBOX_GEOSITE_URL_PREFIX", "http://mirror.example.test/geosite/"},
		"no slash":       {"AEGIS_SINGBOX_GEOIP_URL_PREFIX", "https://mirror.example.test/geoip"},
		"query":          {"AEGIS_SINGBOX_GEOIP_URL_PREFIX", "https://mirror.example.test/geoip/?x=1"},
		"userinfo":       {"AEGIS_SINGBOX_GEOSITE_URL_PREFIX", "https://u:p@mirror.example.test/g/"},
		"not a bool":     {"AEGIS_SINGBOX_BLOCK_ADS", "maybe"},
		"quote breakout": {"AEGIS_SINGBOX_GEOSITE_URL_PREFIX", `https://mirror.example.test/"x/`},
	} {
		t.Run(name, func(t *testing.T) {
			clearSingboxEnv(t)
			t.Setenv(bad[0], bad[1])
			if _, err := loadSingboxTemplate(); err == nil {
				t.Fatalf("%s=%q accepted", bad[0], bad[1])
			}
		})
	}
}
