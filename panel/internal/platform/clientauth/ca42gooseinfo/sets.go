package ca42gooseinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
)

var moduleVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.+-]+)?$`)

const requiredDefaultGODEBUG = "asynctimerchan=1,containermaxprocs=0,cryptocustomrand=1,decoratemappings=0,gotestjsonbuildtext=1,gotypesalias=0,httpcookiemaxnum=0,httplaxcontentlength=1,httpmuxgo121=1,httpservecontentkeepheaders=1,multipathtcp=0,randseednop=0,rsa1024min=0,tls10server=1,tls3des=1,tlsmlkem=0,tlsrsakex=1,tlssecpmlkem=0,tlssha1=1,tlsunsafeekm=1,updatemaxprocs=0,urlmaxqueryparams=0,urlstrictcolons=0,winreadlinkvolume=0,winsymlink=0,x509keypairleaf=0,x509negativeserial=1,x509rsacrt=0,x509sha256skid=0,x509usepolicies=0"

// DependencySetSHA256 canonicalizes the dependency records extracted by
// debug/buildinfo.Read from the retained Goose FD. Replacements, missing sums,
// local paths and duplicate module paths fail closed.
func DependencySetSHA256(modules []*debug.Module) (string, uint64, error) {
	if len(modules) == 0 || len(modules) > int(MaxDependencies) {
		return "", 0, errors.New("Goose dependency set count invalid")
	}
	copyModules := append([]*debug.Module(nil), modules...)
	sort.Slice(copyModules, func(i, j int) bool {
		if copyModules[i] == nil {
			return true
		}
		if copyModules[j] == nil {
			return false
		}
		return copyModules[i].Path < copyModules[j].Path
	})
	var records strings.Builder
	previous := ""
	for _, module := range copyModules {
		if module == nil || module.Replace != nil || !safeModulePath(module.Path) ||
			!moduleVersion.MatchString(module.Version) || !h1sum.MatchString(module.Sum) || module.Path == previous {
			return "", 0, errors.New("Goose dependency set entry invalid")
		}
		previous = module.Path
		records.WriteString(module.Path)
		records.WriteByte('|')
		records.WriteString(module.Version)
		records.WriteByte('|')
		records.WriteString(module.Sum)
		records.WriteByte('\n')
	}
	digest := domainDigest("PANDORA\x00CA42-GO-DEPS\x00V1\x00", records.String())
	return hex.EncodeToString(digest[:]), uint64(len(copyModules)), nil
}

// BuildSettingSetSHA256 accepts only the frozen privileged Goose setting set.
func BuildSettingSetSHA256(settings []debug.BuildSetting, architecture string) (string, uint64, error) {
	tuningKey, tuningValue := "", ""
	switch architecture {
	case "amd64":
		tuningKey, tuningValue = "GOAMD64", "v1"
	case "arm64":
		tuningKey, tuningValue = "GOARM64", "v8.0"
	default:
		return "", 0, errors.New("Goose build setting architecture invalid")
	}
	expected := map[string]string{
		"-buildmode": "exe", "-compiler": "gc", "-trimpath": "true", "DefaultGODEBUG": requiredDefaultGODEBUG,
		"CGO_ENABLED": "0", "GOARCH": architecture, "GOOS": "linux", tuningKey: tuningValue,
	}
	if len(settings) != len(expected) {
		return "", 0, errors.New("Goose build setting count invalid")
	}
	values := make(map[string]string, len(settings))
	for _, setting := range settings {
		if setting.Key == "" || setting.Value == "" || strings.ContainsAny(setting.Key+setting.Value, "\r\n\x00|") || values[setting.Key] != "" {
			return "", 0, errors.New("Goose build setting entry invalid")
		}
		values[setting.Key] = setting.Value
	}
	for key, value := range expected {
		if values[key] != value {
			return "", 0, errors.New("Goose build setting fixed value invalid")
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var records strings.Builder
	for _, key := range keys {
		records.WriteString(key)
		records.WriteByte('=')
		records.WriteString(values[key])
		records.WriteByte('\n')
	}
	digest := domainDigest("PANDORA\x00CA42-GO-SETTINGS\x00V1\x00", records.String())
	return hex.EncodeToString(digest[:]), uint64(len(values)), nil
}

func safeModulePath(value string) bool {
	return value != "" && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, ".") &&
		!strings.HasPrefix(value, "file:") && !strings.Contains(value, "\\") &&
		!strings.Contains(value, "..") && !strings.ContainsAny(value, "\r\n\x00| ")
}

func domainDigest(domain, records string) [sha256.Size]byte {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(domain))
	_, _ = hasher.Write([]byte(records))
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest
}
