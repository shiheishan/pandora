//go:build linux

package ca44runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

const maxExecutableBytes = 128 << 20

type Result struct {
	ReleaseID        string
	AlreadyPublished bool
}

func Run(ctx context.Context, cfg Config) (result Result, failure *Failure) {
	if ctx == nil {
		return result, fail(FailureUsage, StageBootstrap, errors.New("nil context"))
	}
	if err := cfg.Validate("linux", runtime.GOARCH); err != nil {
		return result, fail(FailureUsage, StageBootstrap, err)
	}
	if unix.Geteuid() != 0 {
		return result, fail(FailureTrust, StageBootstrap, errors.New("root required"))
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return result, fail(FailureIO, StageBootstrap, err)
	}
	stage, err := newPrivateStage(cfg.TrustRoot, cfg.StagingRoot, cfg.PublishRoot)
	if err != nil {
		return result, fail(FailureTrust, StageRecovery, err)
	}
	defer stage.close()
	committed := false
	defer func() {
		if !committed && stage.runName != "" {
			if quarantineErr := stage.quarantine(); quarantineErr != nil {
				failure = mergeRecoveryFailure(failure, quarantineErr)
			}
		}
	}()

	manifestFile, manifestIdentity, err := OpenTrustedRegularAt(stage.trustFD, cfg.ManifestPath,
		cfg.ExpectedManifestSHA256, MaxManifestBytes, 0o400, 0)
	if err != nil {
		return result, fail(FailureTrust, StageManifest, err)
	}
	defer manifestFile.Close()
	manifestBytes, err := readRetainedFile(manifestFile, MaxManifestBytes)
	if err != nil {
		return result, fail(FailureIO, StageManifest, err)
	}
	if err := VerifySameFile(manifestFile, manifestIdentity, 0); err != nil {
		return result, fail(FailureTrust, StageManifest, err)
	}

	signerFile, signerIdentity, err := OpenTrustedRegularAt(stage.trustFD, cfg.SignerPublicKeyPath,
		cfg.ApprovedSignerKeySHA256, ed25519.PublicKeySize, 0o400, 0)
	if err != nil {
		return result, fail(FailureTrust, StageManifest, err)
	}
	defer signerFile.Close()
	signerBytes, err := readRetainedFile(signerFile, ed25519.PublicKeySize)
	if err != nil || len(signerBytes) != ed25519.PublicKeySize {
		return result, fail(FailureTrust, StageManifest, errors.New("approved signer size mismatch"))
	}
	if err := VerifySameFile(signerFile, signerIdentity, 0); err != nil {
		return result, fail(FailureTrust, StageManifest, err)
	}
	manifestPin, err := decodeSHA256(cfg.ExpectedManifestSHA256)
	if err != nil {
		return result, fail(FailureUsage, StageManifest, err)
	}
	signerPin, err := decodeSHA256(cfg.ApprovedSignerKeySHA256)
	if err != nil {
		return result, fail(FailureUsage, StageManifest, err)
	}
	manifest, err := ParseAndVerifyManifest(manifestBytes, ed25519.PublicKey(signerBytes),
		manifestPin, signerPin, runtime.GOARCH)
	wipeBytes(signerBytes)
	if err != nil {
		return result, fail(FailureTrust, StageManifest, err)
	}
	if err := verifyRunningExecutable(manifest.RunnerSHA256); err != nil {
		return result, fail(FailureTrust, StageIdentity, err)
	}

	contract, contractIdentity, err := OpenTrustedRegularAt(stage.trustFD, cfg.ContractPath,
		ContractSHA256, MaxManifestBytes, 0o400, 0)
	if err != nil {
		return result, fail(FailureTrust, StageIdentity, err)
	}
	defer contract.Close()
	if err := VerifySameFile(contract, contractIdentity, 0); err != nil {
		return result, fail(FailureTrust, StageIdentity, err)
	}
	classifier, classifierIdentity, err := OpenTrustedRegularAt(stage.trustFD, cfg.ClassifierPath,
		manifest.ClassifierSHA256, maxExecutableBytes, 0o500, 0)
	if err != nil {
		return result, fail(FailureTrust, StageIdentity, err)
	}
	defer classifier.Close()
	verifier, verifierIdentity, err := OpenTrustedRegularAt(stage.trustFD, cfg.VerifierPath,
		manifest.VerifierSHA256, maxExecutableBytes, 0o500, 0)
	if err != nil {
		return result, fail(FailureTrust, StageIdentity, err)
	}
	defer verifier.Close()

	source, sourceIdentity, err := OpenTrustedRegularAt(stage.trustFD, cfg.SourcePath,
		manifest.SourceSHA256, int64(manifest.SourceLength), 0o400, 0)
	if err != nil {
		return result, fail(FailureTrust, StageInputs, err)
	}
	defer source.Close()
	if err := requireExactSize(source, int64(manifest.SourceLength)); err != nil {
		return result, fail(FailureTrust, StageInputs, err)
	}
	artifactKey, artifactKeyIdentity, err := OpenTrustedRegularAt(stage.trustFD, cfg.ArtifactKeyPath,
		cfg.ArtifactKeySHA256, 32, 0o400, 0)
	if err != nil {
		return result, fail(FailureTrust, StageInputs, err)
	}
	defer artifactKey.Close()
	evidenceKey, evidenceKeyIdentity, err := OpenTrustedRegularAt(stage.trustFD, cfg.EvidenceKeyPath,
		cfg.EvidenceKeySHA256, 32, 0o400, 0)
	if err != nil {
		return result, fail(FailureTrust, StageInputs, err)
	}
	defer evidenceKey.Close()
	if err := requireExactSize(artifactKey, 32); err != nil {
		return result, fail(FailureTrust, StageInputs, err)
	}
	if err := requireExactSize(evidenceKey, 32); err != nil {
		return result, fail(FailureTrust, StageInputs, err)
	}
	artifactKeyBytes, err := readRetainedFile(artifactKey, 32)
	if err != nil {
		return result, fail(FailureIO, StageInputs, err)
	}
	evidenceKeyBytes, err := readRetainedFile(evidenceKey, 32)
	if err != nil {
		wipeBytes(artifactKeyBytes)
		return result, fail(FailureIO, StageInputs, err)
	}
	if err := ValidateSeparatedKeys(artifactKeyBytes, evidenceKeyBytes); err != nil {
		wipeBytes(artifactKeyBytes)
		wipeBytes(evidenceKeyBytes)
		return result, fail(FailureTrust, StageInputs, err)
	}
	wipeBytes(artifactKeyBytes)
	wipeBytes(evidenceKeyBytes)

	expectationsBytes, err := manifest.ExpectationsBytes()
	if err != nil {
		return result, fail(FailureInternal, StageManifest, err)
	}
	detachedBytes, err := manifest.DetachedBytes()
	if err != nil {
		return result, fail(FailureInternal, StageManifest, err)
	}
	expectedReceipt, err := manifest.ExpectedReceiptBytes()
	if err != nil {
		return result, fail(FailureInternal, StageManifest, err)
	}

	candidateDirFD, err := duplicateCloseOnExec(stage.candidateFD)
	if err != nil {
		return result, fail(FailureIO, StageClassifier, err)
	}
	candidateDir := os.NewFile(uintptr(candidateDirFD), "ca44-candidate")
	if candidateDir == nil {
		unix.Close(candidateDirFD)
		return result, fail(FailureInternal, StageClassifier, errors.New("cannot wrap candidate directory"))
	}
	defer candidateDir.Close()
	if err := verifyChildInputs(source, sourceIdentity, artifactKey, artifactKeyIdentity,
		classifier, classifierIdentity); err != nil {
		return result, fail(FailureTrust, StageClassifier, err)
	}
	classifierResult, err := runProtectedChild(ctx, 5, source,
		[]*os.File{artifactKey, candidateDir, classifier}, BuildClassifierArgs(manifest),
		cfg.ChildTimeout, cfg.WaitDelay, MaxEnvelopeBytes)
	if ctx.Err() != nil {
		return result, fail(FailureInterrupted, StageClassifier, ctx.Err())
	}
	if err != nil {
		return result, fail(FailureIO, StageClassifier, err)
	}
	if childFailure := classifyChildResult(StageClassifier, classifierResult); childFailure != nil {
		return result, childFailure
	}
	if err := classifierResult.stdout.MatchExact(detachedBytes); err != nil {
		return result, fail(FailureClassifier, StageClassifier, err)
	}
	if err := requireEmptyCapture(classifierResult.stderr); err != nil {
		return result, fail(FailureClassifier, StageClassifier, err)
	}
	if err := rewindAndVerify(source, sourceIdentity); err != nil {
		return result, fail(FailureTrust, StageClassifier, err)
	}
	if err := rewindAndVerify(artifactKey, artifactKeyIdentity); err != nil {
		return result, fail(FailureTrust, StageClassifier, err)
	}
	if err := VerifySameFile(classifier, classifierIdentity, 0); err != nil {
		return result, fail(FailureTrust, StageClassifier, err)
	}

	artifact, artifactIdentity, err := OpenTrustedRegularAt(stage.candidateFD, artifactName,
		manifest.ArtifactSHA256, int64(manifest.ArtifactLength), 0o600, 0)
	if err != nil {
		return result, fail(FailureTrust, StageClassifier, err)
	}
	defer artifact.Close()
	if err := requireExactSize(artifact, int64(manifest.ArtifactLength)); err != nil {
		return result, fail(FailureTrust, StageClassifier, err)
	}
	detachedCreated, err := createExactFileAt(stage.bundleFD, detachedName, detachedBytes, 0o600)
	if err != nil {
		return result, fail(FailureIO, StageVerifier, err)
	}
	if err := detachedCreated.Close(); err != nil {
		return result, fail(FailureIO, StageVerifier, err)
	}
	detachedFile, detachedIdentity, err := OpenTrustedRegularAt(stage.bundleFD, detachedName,
		digestHex(detachedBytes), int64(len(detachedBytes)), 0o600, 0)
	if err != nil {
		return result, fail(FailureTrust, StageVerifier, err)
	}
	defer detachedFile.Close()
	expectationsCreated, err := createExactFileAt(stage.bundleFD, expectationsName, expectationsBytes, 0o600)
	if err != nil {
		return result, fail(FailureIO, StageVerifier, err)
	}
	if err := expectationsCreated.Close(); err != nil {
		return result, fail(FailureIO, StageVerifier, err)
	}
	expectationsFile, expectationsIdentity, err := OpenTrustedRegularAt(stage.bundleFD, expectationsName,
		digestHex(expectationsBytes), int64(len(expectationsBytes)), 0o600, 0)
	if err != nil {
		return result, fail(FailureTrust, StageVerifier, err)
	}
	defer expectationsFile.Close()
	releaseManifestCreated, err := createExactFileAt(stage.bundleFD, releaseManifestName, manifestBytes, 0o600)
	if err != nil {
		return result, fail(FailureIO, StageVerifier, err)
	}
	if err := releaseManifestCreated.Close(); err != nil {
		return result, fail(FailureIO, StageVerifier, err)
	}
	releaseManifestFile, releaseManifestIdentity, err := OpenTrustedRegularAt(stage.bundleFD,
		releaseManifestName, cfg.ExpectedManifestSHA256, int64(len(manifestBytes)), 0o600, 0)
	if err != nil {
		return result, fail(FailureTrust, StageVerifier, err)
	}
	defer releaseManifestFile.Close()

	if err := verifyChildInputs(source, sourceIdentity, artifact, artifactIdentity,
		detachedFile, detachedIdentity, expectationsFile, expectationsIdentity,
		releaseManifestFile, releaseManifestIdentity,
		artifactKey, artifactKeyIdentity,
		evidenceKey, evidenceKeyIdentity, verifier, verifierIdentity); err != nil {
		return result, fail(FailureTrust, StageVerifier, err)
	}
	verifierExtra := make([]*os.File, 34)
	verifierExtra[27] = source
	verifierExtra[28] = artifact
	verifierExtra[29] = detachedFile
	verifierExtra[30] = expectationsFile
	verifierExtra[31] = artifactKey
	verifierExtra[32] = evidenceKey
	verifierExtra[33] = verifier
	verifierResult, err := runProtectedChild(ctx, 36, nil, verifierExtra,
		BuildVerifierArgs(manifest), cfg.ChildTimeout, cfg.WaitDelay, MaxEnvelopeBytes)
	if ctx.Err() != nil {
		return result, fail(FailureInterrupted, StageVerifier, ctx.Err())
	}
	if err != nil {
		return result, fail(FailureIO, StageVerifier, err)
	}
	if childFailure := classifyChildResult(StageVerifier, verifierResult); childFailure != nil {
		return result, childFailure
	}
	if err := verifierResult.stdout.MatchExact(expectedReceipt); err != nil {
		return result, fail(FailureVerifier, StageVerifier, err)
	}
	if err := requireEmptyCapture(verifierResult.stderr); err != nil {
		return result, fail(FailureVerifier, StageVerifier, err)
	}
	if err := verifyChildInputs(source, sourceIdentity, artifact, artifactIdentity,
		detachedFile, detachedIdentity, expectationsFile, expectationsIdentity,
		releaseManifestFile, releaseManifestIdentity,
		artifactKey, artifactKeyIdentity, evidenceKey, evidenceKeyIdentity,
		verifier, verifierIdentity); err != nil {
		return result, fail(FailureTrust, StageVerifier, err)
	}

	if err := unix.Renameat2(stage.candidateFD, artifactName, stage.bundleFD,
		artifactName, unix.RENAME_NOREPLACE); err != nil {
		return result, fail(FailureIO, StagePublish, err)
	}
	receiptFile, err := createExactFileAt(stage.bundleFD, receiptName, expectedReceipt, 0o600)
	if err != nil {
		return result, fail(FailureIO, StagePublish, err)
	}
	if err := receiptFile.Close(); err != nil {
		return result, fail(FailureIO, StagePublish, err)
	}
	if err := fsyncDirectories(stage.candidateFD, stage.bundleFD, stage.runFD); err != nil {
		return result, fail(FailureIO, StagePublish, err)
	}
	if err := stage.publishBundle(manifest.ReleaseID); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return result, fail(FailureIO, StagePublish, err)
		}
		matchErr := stage.existingBundleMatches(manifest, manifestBytes,
			detachedBytes, expectationsBytes, expectedReceipt)
		if matchErr != nil {
			return result, fail(FailureTrust, StagePublish, matchErr)
		}
		if err := stage.discardVerifiedDuplicate(); err != nil {
			return result, fail(FailureIO, StagePublish, err)
		}
		committed = true
		return Result{ReleaseID: manifest.ReleaseID, AlreadyPublished: true}, nil
	}
	if err := stage.cleanupSuccess(); err != nil {
		return result, fail(FailureIO, StagePublish, err)
	}
	committed = true
	return Result{ReleaseID: manifest.ReleaseID}, nil
}

func fail(kind FailureKind, stage FailureStage, err error) *Failure {
	return &Failure{Kind: kind, Stage: stage, Err: err}
}

func readRetainedFile(file *os.File, max int64) ([]byte, error) {
	if file == nil || max <= 0 {
		return nil, errors.New("invalid retained file")
	}
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() <= 0 || stat.Size() > max {
		return nil, errors.New("retained file size denied")
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, stat.Size()))
	if err != nil || int64(len(data)) != stat.Size() {
		wipeBytes(data)
		return nil, errors.New("retained file read failed")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		wipeBytes(data)
		return nil, err
	}
	return data, nil
}

func requireExactSize(file *os.File, expected int64) error {
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if expected <= 0 || stat.Size() != expected {
		return errors.New("file size mismatch")
	}
	return nil
}

func rewindAndVerify(file *os.File, identity TrustedFileIdentity) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return VerifySameFile(file, identity, 0)
}

func verifyChildInputs(values ...any) error {
	for index := 0; index < len(values); {
		file, ok := values[index].(*os.File)
		if !ok || file == nil {
			return errors.New("invalid child file")
		}
		if index+1 < len(values) {
			if identity, ok := values[index+1].(TrustedFileIdentity); ok {
				if err := rewindAndVerify(file, identity); err != nil {
					return err
				}
				index += 2
				continue
			}
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		index++
	}
	return nil
}

func classifyChildResult(stage FailureStage, result childResult) *Failure {
	if result.timedOut || result.signal != 0 {
		return fail(FailureIO, stage, errors.New("child did not exit normally"))
	}
	if result.exitCode == 0 {
		return nil
	}
	switch result.exitCode {
	case 70:
		return fail(FailureInternal, stage, errors.New("child internal failure"))
	case 74:
		return fail(FailureIO, stage, errors.New("child io failure"))
	default:
		if stage == StageClassifier {
			return fail(FailureClassifier, stage, errors.New("classifier denied"))
		}
		return fail(FailureVerifier, stage, errors.New("verifier denied"))
	}
}

func requireEmptyCapture(capture *BoundedCapture) error {
	data, total, overflow := capture.Snapshot()
	if overflow || total != 0 || len(data) != 0 {
		return errors.New("unexpected child diagnostic output")
	}
	return nil
}

func decodeSHA256(value string) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
		return out, errors.New("invalid sha256 pin")
	}
	copy(out[:], decoded)
	return out, nil
}

func verifyRunningExecutable(expectedSHA string) error {
	fd, err := unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "ca44-self")
	if file == nil {
		unix.Close(fd)
		return errors.New("cannot wrap self executable")
	}
	defer file.Close()
	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != 0 || before.Nlink != 1 ||
		before.Mode&0o7777 != 0o500 || before.Size <= 0 || before.Size > maxExecutableBytes {
		return errors.New("self executable identity denied")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, 0, before.Size)); err != nil {
		return err
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode ||
		before.Uid != after.Uid || before.Gid != after.Gid || before.Nlink != after.Nlink ||
		before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return errors.New("self executable changed while hashing")
	}
	expected, err := decodeSHA256(expectedSHA)
	if err != nil {
		return err
	}
	actual := hash.Sum(nil)
	if subtle.ConstantTimeCompare(actual, expected[:]) != 1 {
		return errors.New("self executable hash mismatch")
	}
	if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
		return errors.New("self executable offset drift")
	}
	return nil
}

func byteEqual(left, right []byte) bool { return bytes.Equal(left, right) }

func formatResult(result Result) string {
	status := "published"
	if result.AlreadyPublished {
		status = "already_published"
	}
	return fmt.Sprintf("root_runner=OK status=%s authorization=NONE\n", status)
}
