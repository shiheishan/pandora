package ca44runner

import (
	"errors"
	"time"
)

var rootRunnerCLIFlags = [...]string{
	"-trust-root",
	"-manifest-path",
	"-expected-manifest-sha256",
	"-signer-public-key-path",
	"-approved-signer-key-sha256",
	"-contract-path",
	"-classifier-path",
	"-verifier-path",
	"-source-path",
	"-artifact-key-path",
	"-artifact-key-sha256",
	"-evidence-key-path",
	"-evidence-key-sha256",
	"-staging-root",
	"-publish-root",
	"-child-timeout",
	"-wait-delay",
}

func ParseCLI(args []string) (Config, error) {
	var cfg Config
	if len(args) != len(rootRunnerCLIFlags)*2 {
		return cfg, errors.New("exact root-runner flags required")
	}
	values := make([]string, len(rootRunnerCLIFlags))
	for index, name := range rootRunnerCLIFlags {
		if args[index*2] != name || args[index*2+1] == "" {
			return cfg, errors.New("invalid ordered root-runner flags")
		}
		values[index] = args[index*2+1]
	}
	childTimeout, err := parseCanonicalDuration(values[15])
	if err != nil {
		return cfg, err
	}
	waitDelay, err := parseCanonicalDuration(values[16])
	if err != nil {
		return cfg, err
	}
	return Config{
		TrustRoot:               values[0],
		ManifestPath:            values[1],
		ExpectedManifestSHA256:  values[2],
		SignerPublicKeyPath:     values[3],
		ApprovedSignerKeySHA256: values[4],
		ContractPath:            values[5],
		ClassifierPath:          values[6],
		VerifierPath:            values[7],
		SourcePath:              values[8],
		ArtifactKeyPath:         values[9],
		ArtifactKeySHA256:       values[10],
		EvidenceKeyPath:         values[11],
		EvidenceKeySHA256:       values[12],
		StagingRoot:             values[13],
		PublishRoot:             values[14],
		ChildTimeout:            childTimeout,
		WaitDelay:               waitDelay,
	}, nil
}

func parseCanonicalDuration(value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 || parsed.String() != value {
		return 0, errors.New("noncanonical duration")
	}
	return parsed, nil
}
